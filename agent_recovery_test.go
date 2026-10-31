package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type captureHarness struct {
	server   *websocket
	messages chan []byte
	closed   chan struct{}
	done     chan error
	mouse    *observedMouse
	session  string
}

// newCaptureHarness 用内存WebSocket运行生产Agent状态机，保留会话外层并观察鼠标释放，不模拟真实DXGI成功。
func newCaptureHarness(t *testing.T, encode func(context.Context, func(byte, []byte) error) error) *captureHarness {
	t.Helper()
	oldID, oldEncoder := *deviceID, *encoder
	*deviceID = "capture-test"
	*encoder = "mf"
	a, b := net.Pipe()
	server := &websocket{Conn: a, r: bufio.NewReader(a), limit: maxRelayMessage}
	client := &websocket{Conn: b, r: bufio.NewReader(b), client: true, limit: 1024}
	ctx, cancel := context.WithCancel(context.Background())
	h := &captureHarness{server: server, messages: make(chan []byte, 64), closed: make(chan struct{}), done: make(chan error, 1), mouse: &observedMouse{events: make(chan string, 16)}, session: strings.Repeat("a", 32)}
	// 退出时取消计时器和唯一编码任务，再恢复进程全局测试配置。
	t.Cleanup(func() { cancel(); server.Close(); <-h.done; *deviceID = oldID; *encoder = oldEncoder })
	// 鼠标工厂返回可观察的逻辑替身，不投递真实系统事件。
	open := func() (mouseController, error) { return h.mouse, nil }
	// 单个生产状态机使用内存连接，退出结果交给清理函数回收。
	go func() { h.done <- agentConnection(ctx, client, encode, open) }()
	// 收集带认证会话外层的视频和状态，真实读取路径同时处理ping/pong。
	go func() {
		defer close(h.closed)
		for {
			_, b, err := server.read()
			if err != nil {
				return
			}
			h.messages <- b
		}
	}()
	h.command(t, "start", "")
	return h
}

// command 在当前会话发送开始、停止或鼠标指令，检验恢复过程不要求新会话或重新认证。
func (h *captureHarness) command(t *testing.T, kind, mouse string) {
	t.Helper()
	b, _ := json.Marshal(relayCommand{Type: kind, Device: "capture-test", Session: h.session, Mouse: json.RawMessage(mouse)})
	if err := h.server.write(1, b); err != nil {
		t.Fatal(err)
	}
}

// state 等待目标采集状态，并检查所有消息仍绑定原会话，超时说明发生断开或错误恢复。
func (h *captureHarness) state(t *testing.T, want string) {
	t.Helper()
	for {
		select {
		case b := <-h.messages:
			if len(b) < 18 || string(b[:16]) != strings.Repeat("\xaa", 16) {
				t.Fatalf("会话外层无效: %x", b)
			}
			if b[16] != 1 {
				continue
			}
			var state struct{ Type, State string }
			if json.Unmarshal(b[17:], &state) != nil {
				t.Fatal("JSON损坏")
			}
			if state.Type == "capture-status" && state.State == want {
				return
			}
		case <-time.After(90 * time.Second):
			t.Fatalf("未收到状态 %s", want)
		}
	}
}

// TestNativeAgentRecovery 验证采集失败释放拖动、旧任务退出后恢复、同会话继续控制及非法设备仍拒绝。
func TestNativeAgentRecovery(t *testing.T) {
	// 使用Go标准库虚拟时钟执行真实退避策略，不缩短生产等待或跳过实际状态机。
	synctest.Test(t, func(t *testing.T) {
		fail := make(chan struct{})
		calls := 0
		active := 0
		// 每次模拟新进程从PTS零和新IDR开始，第二次持续工作到会话取消。
		encode := func(ctx context.Context, send func(byte, []byte) error) error {
			calls++
			active++
			if active != 1 {
				panic("采集重叠")
			}
			// 返回前标记模拟资源已回收，检验不会同时启动两个采集器。
			defer func() { active-- }()
			if err := send(1, []byte(`{"codec":"avc1.42c028","width":1920,"height":1080}`)); err != nil {
				return err
			}
			if err := send(2, nativeTestPacket(0, true)); err != nil {
				return err
			}
			if calls == 1 {
				select {
				case <-fail:
					return &nativeCaptureError{cause: errors.New("DXGI access lost"), retry: true}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			<-ctx.Done()
			return ctx.Err()
		}
		h := newCaptureHarness(t, encode)
		h.state(t, "running")
		synctest.Wait()
		h.command(t, "control", `{"type":"mouse","action":"down","x":0.5,"y":0.5,"button":0,"clicks":1}`)
		waitMouse(t, h.mouse, "down")
		close(fail)
		h.state(t, "paused")
		waitMouse(t, h.mouse, "release")
		h.command(t, "control", `{"type":"mouse","action":"down","x":0.5,"y":0.5,"button":0,"clicks":1}`)
		h.state(t, "running")
		synctest.Wait()
		if calls != 2 || active != 1 {
			t.Fatalf("恢复调用=%d active=%d", calls, active)
		}
		select {
		case x := <-h.mouse.events:
			t.Fatalf("暂停期间执行了输入: %s", x)
		default:
		}
		h.command(t, "control", `{"type":"mouse","action":"down","x":0.25,"y":0.75,"button":0,"clicks":1}`)
		waitMouse(t, h.mouse, "down")
	})
}

// TestNativeAgentRecoveryBudget 验证持续访问拒绝只完整初始化七次，随后保留连接且不再自动启动。
func TestNativeAgentRecoveryBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		// 持续返回已分类的访问拒绝，不把它伪装成网络失败。
		encode := func(context.Context, func(byte, []byte) error) error {
			calls++
			return &nativeCaptureError{cause: errors.New("E_ACCESSDENIED"), retry: true}
		}
		h := newCaptureHarness(t, encode)
		h.state(t, "stopped")
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if calls != 7 {
			t.Fatalf("初次加六次恢复调用=%d", calls)
		}
		h.command(t, "control", `{"type":"mouse","action":"heartbeat"}`)
		h.command(t, "stop", "")
		synctest.Wait()
		if calls != 7 {
			t.Fatal("停止后仍重启")
		}
	})
}

// TestNativeAgentPermanentFailure 验证永久错误不重试、不断开观看会话，且允许显式重新开始。
func TestNativeAgentPermanentFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		// 模拟缺少系统编码器等永久失败，只在人工开始时尝试。
		encode := func(context.Context, func(byte, []byte) error) error {
			calls++
			return &nativeCaptureError{cause: errors.New("MFT unavailable")}
		}
		h := newCaptureHarness(t, encode)
		h.state(t, "stopped")
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls != 1 {
			t.Fatal("永久错误自动重启")
		}
		h.command(t, "start", "")
		h.state(t, "stopped")
		synctest.Wait()
		if calls != 2 {
			t.Fatal("人工重新开始失败")
		}
	})
}

// TestNativeAgentStopDuringRecovery 验证浏览器停止会取消待执行恢复，不遗留后台采集任务。
func TestNativeAgentStopDuringRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		// 模拟尚未成功采集便失去桌面，等待真实退避定时器。
		encode := func(context.Context, func(byte, []byte) error) error {
			calls++
			return &nativeCaptureError{cause: errors.New("access denied"), retry: true}
		}
		h := newCaptureHarness(t, encode)
		h.state(t, "paused")
		h.command(t, "stop", "")
		synctest.Wait()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("停止后仍启动采集: %d", calls)
		}
	})
}

// TestNativeAgentPausedAuthorization 验证采集暂停时仍拒绝伪造设备、会话和越界坐标，不能因恢复弱化校验。
func TestNativeAgentPausedAuthorization(t *testing.T) {
	for _, invalid := range []string{"device", "session", "coordinates"} {
		// 每种输入独立运行生产状态机，确保前一个断连不会掩盖后一个校验。
		t.Run(invalid, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// 永久采集失败保留连接，专门检验暂停状态下的认证分支。
				encode := func(context.Context, func(byte, []byte) error) error {
					return &nativeCaptureError{cause: errors.New("paused")}
				}
				h := newCaptureHarness(t, encode)
				h.state(t, "stopped")
				cmd := relayCommand{Type: "control", Device: "capture-test", Session: h.session, Mouse: json.RawMessage(`{"type":"mouse","action":"move","x":0.5,"y":0.5}`)}
				if invalid == "device" {
					cmd.Device = "other-device"
				}
				if invalid == "session" {
					cmd.Session = strings.Repeat("b", 32)
				}
				if invalid == "coordinates" {
					cmd.Mouse = json.RawMessage(`{"type":"mouse","action":"move","x":1.1,"y":0.5}`)
				}
				b, _ := json.Marshal(cmd)
				if err := h.server.write(1, b); err != nil {
					t.Fatal(err)
				}
				select {
				case <-h.closed:
				case <-time.After(time.Second):
					t.Fatal("暂停时未拒绝非法控制")
				}
			})
		})
	}
}
