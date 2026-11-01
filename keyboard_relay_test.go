package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRelaySessionAuthorization 检查重连预检只承认既有Cookie，不续期、不泄露密钥、不接受明文或错误域名。
func TestRelaySessionAuthorization(t *testing.T) {
	relay := testRelay()
	cookie := loginCookie(t, relay)
	for _, test := range []struct {
		name, method string
		cookie       bool
		want         int
	}{{"missing", "GET", false, 401}, {"authorized", "GET", true, 204}, {"method", "POST", true, 405}} {
		request := testRequest(test.method, "/session", "")
		if test.cookie {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		relay.ServeHTTP(response, request)
		if response.Code != test.want || response.Header().Get("Set-Cookie") != "" {
			t.Fatalf("%s status=%d renewed=%q", test.name, response.Code, response.Header().Get("Set-Cookie"))
		}
	}
	request := testRequest("GET", "/session", "")
	request.AddCookie(cookie)
	request.TLS = nil
	response := httptest.NewRecorder()
	relay.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("明文允许查询授权")
	}
}

type observedKeys struct{ state keyboardState }

// handle 将实际经过Relay/Agent解析的键盘事件交给纯状态机，不执行系统注入。
func (m *observedKeys) handle(msg mouseMessage) error {
	if msg.Type == "keyboard" {
		return m.state.apply(msg)
	}
	return nil
}

// release 模拟平台键盘释放，事件仍通过真实状态机逐个归还。
func (m *observedKeys) release() error { return m.state.release() }

// TestKeyboardRelayRouting 使用真实TLS与认证WebSocket验证键盘路由、会话校验及浏览器断开后的修饰键释放。
func TestKeyboardRelayRouting(t *testing.T) {
	oldID, oldEncoder, oldKeyboard := *deviceID, *encoder, *keyboardEnabled
	*deviceID = "mac-test"
	*encoder = "mf"
	*keyboardEnabled = true
	// 最后恢复全局配置，连接清理先于此操作执行。
	defer func() { *deviceID = oldID; *encoder = oldEncoder; *keyboardEnabled = oldKeyboard }()
	relay := testRelay()
	serverTLS, clientTLS := relayTLS(t)
	cookie := loginCookie(t, relay)
	agent, _ := connectPipeRelay(t, relay, "/agent", http.Header{"Authorization": {"Bearer " + relay.agentKey}, "X-Device-Id": {relay.device}}, serverTLS, clientTLS)
	events := make(chan string, 16)
	input := &observedKeys{state: keyboardState{
		// send 记录控制键的真实状态机输出，区分DOWN和UP。
		send: func(code string, down bool, _ uint64) error {
			action := " up"
			if down {
				action = " down"
			}
			events <- code + action
			return nil
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// encode 只提供协议测试用首帧，不冒充真实Windows或H264解码验收。
	encode := func(ctx context.Context, send func(byte, []byte) error) error {
		if err := send(1, []byte(`{"codec":"avc1.42c028","width":1920,"height":1080}`)); err != nil {
			return err
		}
		if err := send(2, nativeTestPacket(0, true)); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
	// open 返回可观察的键盘状态，不需要系统GUI权限。
	open := func() (mouseController, error) { return input, nil }
	// 运行生产Agent，不监听端口，TLS经net.Pipe真实握手。
	go func() { done <- agentConnection(ctx, agent, encode, open) }()
	// 测试失败同样终止唯一编码任务，避免留有后台协程。
	defer func() {
		cancel()
		agent.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Agent未清理")
		}
	}()
	viewer, _ := connectPipeRelay(t, relay, "/ws", http.Header{"Origin": {relay.origin}, "Cookie": {cookie.Name + "=" + cookie.Value}}, serverTLS, clientTLS)
	defer viewer.Close()
	sid := viewerSession(t, viewer)
	// 首帧后还要等待生产Agent确认输入权限，不能提前发送而掩盖首帧门控。
	for {
		op, b, err := viewer.read()
		if err != nil {
			t.Fatal(err)
		}
		if op != 1 {
			continue
		}
		var status struct {
			Type     string
			Keyboard bool
		}
		json.Unmarshal(b, &status)
		if status.Type == "mouse-status" && status.Keyboard {
			break
		}
	}
	for _, code := range []string{"ControlLeft", "KeyA", "NumpadEnter", "NumpadDivide"} {
		body, _ := json.Marshal(map[string]string{"type": "keyboard", "action": "down", "code": code})
		command, _ := json.Marshal(relayCommand{Type: "control", Device: relay.device, Session: sid, Mouse: body})
		if err := viewer.write(1, command); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-events:
			if got != code+" down" {
				t.Fatal(got)
			}
		case <-time.After(time.Second):
			t.Fatal("键盘没有经TLS转发")
		}
	}
	viewer.Close()
	for _, want := range []string{"NumpadDivide up", "NumpadEnter up", "KeyA up", "ControlLeft up"} {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("释放=%s expected=%s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("浏览器断开后键盘未释放")
		}
	}
}
