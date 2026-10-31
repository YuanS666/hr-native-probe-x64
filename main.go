package main

import (
	"context"
	"crypto/sha1"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed index.html
var html []byte
var width = flag.Int("width", 1920, "输出宽度（偶数）")
var height = flag.Int("height", 1080, "输出高度（偶数）")
var fps = flag.Int("fps", 20, "帧率")
var bitrate = flag.Int("bitrate", 2000000, "目标码率 bit/s")
var defaultDevice, defaultEncoder = captureDefaults()
var device = flag.String("device", defaultDevice, "macOS 屏幕名称/编号；Windows 使用 desktop（主显示器）")
var encoder = flag.String("encoder", defaultEncoder, "macOS 默认 h264_videotoolbox；Windows 默认 mf（系统软件H264），libx264需外部FFmpeg")
var testPattern = flag.Bool("test-pattern", false, "使用测试画面而非真实屏幕")
var port = flag.Int("port", 8080, "本地端口")
var active = make(chan struct{}, 1)
var mouseEnabled = flag.Bool("mouse", false, "启用本机原生鼠标控制；macOS 需要辅助功能授权，Windows 受 UIPI 限制")
var checkPermissions = flag.Bool("check-permissions", false, "查询当前平台的录屏/输入运行条件后退出")
var appContext = context.Background()
var sessions sync.WaitGroup
var writeMutex sync.Mutex
var mode = flag.String("mode", "local", "local、agent 或 relay")
var relayAddress = flag.String("relay", os.Getenv("RELAY_URL"), "Agent 主动连接的 wss://主机/agent")
var deviceID = flag.String("device-id", os.Getenv("RELAY_DEVICE_ID"), "Relay 与 Agent 共用的设备 ID")
var publicURL = flag.String("public-url", os.Getenv("RELAY_PUBLIC_URL"), "浏览器访问的 https://域名[:端口]")
var relayListen = flag.String("listen", "127.0.0.1:8443", "Relay 的 TLS 监听地址")
var tlsCert = flag.String("tls-cert", os.Getenv("RELAY_TLS_CERT"), "Relay TLS 证书路径")
var tlsKey = flag.String("tls-key", os.Getenv("RELAY_TLS_KEY"), "Relay TLS 私钥路径")
var caFile = flag.String("ca-file", "", "Agent 额外信任的本地测试 CA，默认使用系统信任库")

// main 校验配置并选择本地、主动出站 Agent 或 TLS Relay 模式，本地服务仍只监听回环地址。
func main() {
	flag.Parse()
	if *checkPermissions {
		fmt.Println(mousePermissions())
		return
	}
	if *mode != "local" && *mode != "agent" && *mode != "relay" {
		log.Fatal("-mode 只能是 local、agent、relay")
	}
	if *mode == "relay" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runRelay(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	if runtime.GOOS != "darwin" && !(runtime.GOOS == "windows" && runtime.GOARCH == "amd64") {
		log.Fatal("此平台仅支持 -mode relay；采集和鼠标 Agent 需要 macOS 或 Windows x64")
	}
	if *mouseEnabled && *testPattern {
		log.Fatal("测试画面不能控制真实桌面，请移除 -test-pattern")
	}
	if *width <= 0 || *height <= 0 || *width%2 != 0 || *height%2 != 0 || *fps < 1 || *fps > 120 || *bitrate < 1 {
		log.Fatal("宽高必须为正偶数，FPS 为 1–120，码率必须为正")
	}
	if *encoder != "h264_videotoolbox" && *encoder != "libx264" && *encoder != "mf" {
		log.Fatal("仅支持 H.264 编码器")
	}
	if runtime.GOOS == "windows" && *encoder != "libx264" && *encoder != "mf" {
		log.Fatal("Windows 仅支持 mf 或显式 libx264")
	}
	if *encoder == "mf" && runtime.GOOS != "windows" {
		log.Fatal("mf 编码仅支持 Windows")
	}
	if *mode == "agent" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runAgent(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	http.HandleFunc("/ws", stream)
	// 页面无需外部依赖；根路径之外的请求直接返回 404。
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html)
	})
	addr := "127.0.0.1:" + strconv.Itoa(*port)
	log.Printf("打开 http://%s；encoder=%s test-pattern=%v", addr, *encoder, *testPattern)
	var stop context.CancelFunc
	appContext, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: addr, ReadHeaderTimeout: 5 * time.Second}
	stopped := make(chan struct{})
	// 收到退出信号时停止接收连接，活动会话通过上下文取消后释放鼠标按键。
	go func() { <-appContext.Done(); server.Shutdown(context.Background()); close(stopped) }()
	err := server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	<-stopped
	sessions.Wait()
}

// writeWS 向浏览器发送一个未掩码的服务端消息；设置写超时以终止阻塞的本地客户端。
func writeWS(c net.Conn, opcode byte, b []byte) error {
	writeMutex.Lock()
	defer writeMutex.Unlock()
	return sendWSFrame(c, opcode, b, false)
}

// stream 完成同源WebSocket握手，为唯一观看者启动平台编码，断开连接即停止采集。
func stream(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Version") != "13" || !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		http.Error(w, "需要 WebSocket", 400)
		return
	}
	if r.Host != "127.0.0.1:"+strconv.Itoa(*port) || r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "仅允许本地同源页面", 403)
		return
	}
	key, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	if err != nil || len(key) != 16 {
		http.Error(w, "握手 key 无效", 400)
		return
	}
	select {
	case active <- struct{}{}:
	default:
		http.Error(w, "仅支持一个观看者", 409)
		return
	}
	// 会话退出时释放单观看者占用。
	defer func() { <-active }()
	sessions.Add(1)
	defer sessions.Done()
	c, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer c.Close()
	hash := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(hash[:]))
	if rw.Flush() != nil {
		return
	}
	ctx, cancel := context.WithCancel(appContext)
	defer cancel()
	var mouse *nativeMouse
	status := "未启用鼠标；使用 -mouse 启动后可启用"
	if *mouseEnabled {
		mouse, err = newNativeMouse(*device)
		if err != nil {
			status = err.Error()
			log.Printf("鼠标控制不可用: %v", err)
		} else {
			status = "原生鼠标控制可用，可勾选鼠标控制"
		}
	}
	state, _ := json.Marshal(map[string]any{"type": "mouse-status", "enabled": mouse != nil, "message": status})
	if writeWS(c, 1, state) != nil {
		return
	}
	readerDone := make(chan struct{})
	// 取消会话时关闭连接，唤醒读取并及时释放鼠标按键。
	go func() { <-ctx.Done(); c.Close() }()
	// 接收鼠标和协议控制帧；异常或客户端关闭后取消编码。
	go func() {
		defer close(readerDone)
		if err := readControls(c, rw, mouse); err != nil && ctx.Err() == nil {
			log.Printf("控制连接结束: %v", err)
		}
		cancel()
	}()
	// 编码结束也必须等待读取协程完成按键清理，再结束整个会话。
	defer func() { cancel(); c.Close(); <-readerDone }()
	err = encode(ctx, c)
	if err != nil && ctx.Err() == nil {
		log.Printf("编码/传输异常: %v", err)
		msg, _ := json.Marshal(map[string]string{"error": err.Error()})
		writeWS(c, 1, msg)
	}
	writeWS(c, 8, []byte{3, 232})
}

// encode 按平台编码后发送原有视频消息，本地和Relay共用完整访问单元封装。
func encode(ctx context.Context, c net.Conn) error {
	// 本地模式维持原帧格式，远程模式使用相同编码逻辑加会话外层。
	return encodeVideo(ctx, func(op byte, data []byte) error { return writeWS(c, op, data) })
}

// encodeVideo 将完整配置和视频消息交给 send，复用本地及 Relay 模式的同一 H.264 编码流程。
func encodeVideo(ctx context.Context, send func(byte, []byte) error) error {
	if *encoder == "mf" {
		return encodeMFVideo(ctx, send)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin"}
	filter := fmt.Sprintf("scale=%d:%d,format=yuv420p", *width, *height)
	if *testPattern {
		args = append(args, "-re", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", *width, *height, *fps))
	} else {
		input, platformFilter, err := captureInput()
		if err != nil {
			return err
		}
		args = append(args, input...)
		filter = platformFilter
	}
	args = append(args, "-an", "-vf", filter, "-r", strconv.Itoa(*fps), "-fps_mode", "cfr", "-c:v", *encoder)
	if *encoder == "h264_videotoolbox" {
		args = append(args, "-allow_sw", "0", "-realtime", "1")
	} else {
		args = append(args, "-preset", "ultrafast", "-tune", "zerolatency")
	}
	args = append(args, "-b:v", strconv.Itoa(*bitrate), "-bf", "0", "-g", strconv.Itoa(*fps), "-bsf:v", "h264_metadata=aud=insert", "-flush_packets", "1", "-f", "h264", "pipe:1")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	var sps, pps []byte
	var sequence, frames, bytesSent int64
	last := time.Now()
	// 每个回调处理一个访问单元；关键帧补齐缓存参数集，让解码器重置后能从下一个 IDR 恢复。
	err = readFrames(stdout, func(frame []byte) error {
		ready := time.Now().UnixMicro()
		key, codec := frameInfo(frame, &sps, &pps)
		if codec == "" || len(pps) == 0 {
			return fmt.Errorf("首帧缺少 SPS/PPS")
		}
		if key {
			config, _ := json.Marshal(map[string]any{"codec": codec, "width": *width, "height": *height, "fps": *fps})
			if err := send(1, config); err != nil {
				return err
			}
			prefix := append([]byte{0, 0, 0, 1}, sps...)
			prefix = append(prefix, 0, 0, 0, 1)
			prefix = append(prefix, pps...)
			// AUD 必须保持在访问单元首位，参数集放在 AUD 后、图像切片前。
			boundary, _ := startCode(frame[4:])
			if boundary < 0 {
				return fmt.Errorf("访问单元缺少图像数据")
			}
			boundary += 4
			joined := append([]byte{}, frame[:boundary]...)
			joined = append(joined, prefix...)
			frame = append(joined, frame[boundary:]...)
		}
		// 二进制头：key:u8、媒体 PTS:u64、Go 收到完整帧的 Unix 微秒:u64，均为大端。
		packet := make([]byte, 17+len(frame))
		if key {
			packet[0] = 1
		}
		binary.BigEndian.PutUint64(packet[1:9], uint64(sequence*1000000/int64(*fps)))
		binary.BigEndian.PutUint64(packet[9:17], uint64(ready))
		copy(packet[17:], frame)
		if err := send(2, packet); err != nil {
			return err
		}
		sequence++
		frames++
		bytesSent += int64(len(frame))
		if elapsed := time.Since(last).Seconds(); elapsed >= 1 {
			log.Printf("输出 %.1f FPS / H.264 %.3f Mbps / 帧 %d", float64(frames)/elapsed, float64(bytesSent)*8/elapsed/1e6, sequence)
			last = time.Now()
			frames = 0
			bytesSent = 0
		}
		return nil
	})
	if err != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if err != nil {
		return err
	}
	if waitErr != nil {
		return fmt.Errorf("FFmpeg: %w（详细原因见终端）", waitErr)
	}
	if sequence == 0 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
