package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

type mouseController interface {
	handle(mouseMessage) error
	release()
}

// openAgentMouse 按现有开关和系统权限创建控制器，不可用时仍允许远程观看视频。
func openAgentMouse() (mouseController, error) {
	if !*mouseEnabled {
		return nil, fmt.Errorf("未启用鼠标；Agent 需要使用 -mouse 启动")
	}
	m, err := newNativeMouse(*device)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// sendAgentFrame 为原视频或配置加上随机会话外层，Relay 去除外层后浏览器仍收到原格式。
func sendAgentFrame(w *websocket, sid string, op byte, b []byte) error {
	session, err := hex.DecodeString(sid)
	if err != nil || len(session) != 16 {
		return fmt.Errorf("视频会话无效")
	}
	packet := make([]byte, 17+len(b))
	copy(packet, session)
	packet[16] = op
	copy(packet[17:], b)
	return w.write(2, packet)
}

// agentConnection 在一个认证连接内依次处理观看会话；注入编码和鼠标工厂以便无屏幕环境测试清理行为。
func agentConnection(parent context.Context, w *websocket, encode func(context.Context, func(byte, []byte) error) error, openMouse func() (mouseController, error)) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer w.Close()
	go w.keepAlive(ctx)
	type incoming struct {
		op  byte
		b   []byte
		err error
	}
	input := make(chan incoming, 1)
	// 唯一读取协程将连接消息交给会话循环；取消时不会遗留阻塞的发送协程。
	go func() {
		for {
			op, b, err := w.read()
			select {
			case input <- incoming{op, b, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var session string
	var mouse mouseController
	var stopEncode context.CancelFunc
	var encoded chan error
	// 先释放鼠标再等待编码退出，断开浏览器不会因视频写入阻塞而拖延按键清理。
	stopSession := func() {
		if mouse != nil {
			mouse.release()
			mouse = nil
		}
		if stopEncode != nil {
			stopEncode()
			stopEncode = nil
		}
		if encoded != nil {
			<-encoded
			encoded = nil
		}
		session = ""
	}
	defer stopSession()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-encoded:
			encoded = nil
			sid := session
			stopSession()
			failure, _ := json.Marshal(map[string]string{"error": fmt.Sprint("Agent 编码终止: ", err)})
			sendAgentFrame(w, sid, 1, failure)
			return fmt.Errorf("编码结束: %v", err)
		case message := <-input:
			if message.err != nil {
				return message.err
			}
			cmd, err := decodeRelayCommand(message.b)
			if message.op != 1 || err != nil || cmd.Device != *deviceID {
				return fmt.Errorf("Relay 指令格式或设备无效")
			}
			switch cmd.Type {
			case "start":
				stopSession()
				session = cmd.Session
				mouse, err = openMouse()
				status := "原生鼠标控制可用，可勾选鼠标控制"
				if err != nil {
					status = err.Error()
					mouse = nil
				}
				state, _ := json.Marshal(map[string]any{"type": "mouse-status", "enabled": mouse != nil, "message": status})
				if err := sendAgentFrame(w, session, 1, state); err != nil {
					return err
				}
				videoCtx, stop := context.WithCancel(ctx)
				stopEncode = stop
				done := make(chan error, 1)
				encoded = done
				sid := session
				// 每个新会话都启动新的编码器，第一帧自然产生带参数集的 IDR，不复用旧参考帧。
				go func() {
					// 编码结果通过同一 WSS 发送，取消后的旧会话帧不再进入发送队列。
					err := encode(videoCtx, func(op byte, b []byte) error {
						if videoCtx.Err() != nil {
							return videoCtx.Err()
						}
						return sendAgentFrame(w, sid, op, b)
					})
					done <- err
				}()
			case "stop":
				if cmd.Session != session {
					return fmt.Errorf("停止指令会话不匹配")
				}
				stopSession()
			case "control":
				if session == "" || cmd.Session != session {
					return fmt.Errorf("控制指令会话不匹配")
				}
				msg, err := parseMouse(cmd.Mouse)
				if err != nil {
					return err
				}
				if mouse == nil {
					if msg.Action == "heartbeat" || msg.Action == "release" {
						continue
					}
					return fmt.Errorf("未获准执行鼠标控制")
				}
				if err := mouse.handle(msg); err != nil {
					return err
				}
			}
		}
	}
}

// runAgent 校验证书与环境密钥后主动连接 Relay，断线使用 1–30 秒指数退避，退出时停止重试。
func runAgent(ctx context.Context) error {
	key := os.Getenv("RELAY_AGENT_KEY")
	u, err := url.Parse(*relayAddress)
	if err != nil || u.Scheme != "wss" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/agent" {
		return fmt.Errorf("需要 -relay wss://域名[:端口]/agent")
	}
	if !validDevice(*deviceID) || len(key) < 32 {
		return fmt.Errorf("需要设备 ID 和至少 32 字节的 RELAY_AGENT_KEY")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			return err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("CA 文件不包含有效证书")
		}
		config.RootCAs = pool
	}
	headers := http.Header{"Authorization": []string{"Bearer " + key}, "X-Device-Id": []string{*deviceID}}
	delay := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		w, err := dialWS(ctx, *relayAddress, headers, config)
		if err == nil {
			w.limit = 1024
			log.Print("Agent: 已通过 WSS 连接 Relay，等待浏览器")
			err = agentConnection(ctx, w, encodeVideo, openAgentMouse)
		}
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > 30*time.Second {
			delay = time.Second
		}
		log.Printf("Agent: 连接结束（%v），%s 后重连", err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
	return nil
}
