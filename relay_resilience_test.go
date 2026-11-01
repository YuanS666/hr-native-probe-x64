package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// drainRelayCommands 消费真实TLS Agent通道并记录命令，测试结束关闭并等待读取协程回收。
func drainRelayCommands(t *testing.T, agent *websocket) <-chan relayCommand {
	t.Helper()
	commands := make(chan relayCommand, 32)
	done := make(chan struct{})
	// read自动回复协议ping，命令只作为状态观测，不伪造视频或执行系统输入。
	go func() {
		defer close(done)
		defer close(commands)
		for {
			_, b, err := agent.read()
			if err != nil {
				return
			}
			var cmd relayCommand
			if json.Unmarshal(b, &cmd) == nil {
				commands <- cmd
			}
		}
	}()
	// 先关闭测试Agent，防止读取协程遗留到下一用例。
	t.Cleanup(func() { agent.Close(); <-done })
	return commands
}

// TestRelaySlowViewer 验证慢TLS浏览器有界解绑；停止指令失败必须关闭Agent，通过断连触发输入释放。
func TestRelaySlowViewer(t *testing.T) {
	s := testRelay()
	cookie := loginCookie(t, s)
	serverTLS, clientTLS := relayTLS(t)
	agent, agentDone := connectPipeRelay(t, s, "/agent", http.Header{"Authorization": {"Bearer " + s.agentKey}, "X-Device-Id": {s.device}}, serverTLS, clientTLS)
	commands := drainRelayCommands(t, agent)
	viewer, done := connectPipeRelay(t, s, "/ws", http.Header{"Origin": {s.origin}, "Cookie": {cookie.Name + "=" + cookie.Value}}, serverTLS, clientTLS)
	_, welcome, err := viewer.read()
	if err != nil {
		t.Fatal(err)
	}
	var info struct{ Session string }
	json.Unmarshal(welcome, &info)
	if cmd := <-commands; cmd.Type != "start" {
		t.Fatal(cmd.Type)
	}
	packet := make([]byte, 64*1024)
	packet[0] = 1
	if err := sendAgentFrame(agent, info.Session, 2, packet); err != nil {
		t.Fatal(err)
	}
	// 不再读取viewer，真实net.Pipe/TLS写入必然反压；时间来自生产3秒截止而非替换阈值。
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("slow viewer not detached within video-write + stop-write deadlines")
	}
	cmd, delivered := <-commands
	if delivered && cmd.Type != "stop" {
		t.Fatal("unexpected cleanup command", cmd.Type)
	}
	if !delivered {
		select {
		case <-agentDone:
		case <-time.After(time.Second):
			t.Fatal("closed Agent handler did not release ownership")
		}
	}
	s.mu.Lock()
	valid := s.viewer == nil && (delivered || s.agent == nil)
	s.mu.Unlock()
	if !valid {
		t.Fatal("neither stop delivery nor Agent disconnect completed")
	}
	t.Logf("slow client cleanup: stop delivered=%v; otherwise Agent closed as fail-safe", delivered)
}

// TestRelayHalfOpenAgent 验证不读不回复的旧Agent有界清理，期间拒绝重复连接，清理后允许重连。
func TestRelayHalfOpenAgent(t *testing.T) {
	s := testRelay()
	serverTLS, clientTLS := relayTLS(t)
	_, done := connectPipeRelay(t, s, "/agent", http.Header{"Authorization": {"Bearer " + s.agentKey}, "X-Device-Id": {s.device}}, serverTLS, clientTLS)
	r := testRequest("GET", "/agent", "")
	r.Header.Del("Origin")
	r.Header.Set("Authorization", "Bearer "+s.agentKey)
	r.Header.Set("X-Device-Id", s.device)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("duplicate status=%d", w.Code)
	}
	select {
	case <-done:
	case <-time.After(11 * time.Second):
		t.Fatal("half-open agent retained")
	}
	agent, _ := connectPipeRelay(t, s, "/agent", http.Header{"Authorization": {"Bearer " + s.agentKey}, "X-Device-Id": {s.device}}, serverTLS, clientTLS)
	agent.Close()
}

// TestRelayActiveAuthorization 验证Cookie过期不硬切活跃连接，新连接仍拒绝过期Cookie且无保活时关闭旧会话。
func TestRelayActiveAuthorization(t *testing.T) {
	s := testRelay()
	cookie := loginCookie(t, s)
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(cookie.Value, ".")[0])
	fields := strings.Split(string(raw), "|")
	fields[1] = strconv.FormatInt(time.Now().Add(3*time.Second).Unix(), 10)
	payload := base64.RawURLEncoding.EncodeToString([]byte(strings.Join(fields, "|")))
	cookie.Value = payload + "." + s.cookieSignature(payload)
	serverTLS, clientTLS := relayTLS(t)
	agent, _ := connectPipeRelay(t, s, "/agent", http.Header{"Authorization": {"Bearer " + s.agentKey}, "X-Device-Id": {s.device}}, serverTLS, clientTLS)
	drainRelayCommands(t, agent)
	viewer, done := connectPipeRelay(t, s, "/ws", http.Header{"Origin": {s.origin}, "Cookie": {cookie.Name + "=" + cookie.Value}}, serverTLS, clientTLS)
	_, welcome, err := viewer.read()
	if err != nil {
		t.Fatal(err)
	}
	var info struct{ Session string }
	json.Unmarshal(welcome, &info)
	readerDone := make(chan struct{})
	// 持续处理协议ping；应用保活由下方循环单独发送，验证两层超时含义不同。
	go func() {
		defer close(readerDone)
		for {
			if _, _, err := viewer.read(); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { viewer.Close(); <-readerDone })
	b, _ := json.Marshal(relayCommand{Type: "control", Device: s.device, Session: info.Session, Mouse: json.RawMessage(`{"type":"mouse","action":"heartbeat"}`)})
	for i := 0; i < 8; i++ {
		if err := viewer.write(1, b); err != nil {
			t.Fatalf("active session cut: %v", err)
		}
		time.Sleep(time.Second)
	}
	for _, path := range []string{"/session", "/ws"} {
		r := testRequest("GET", path, "")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("expired cookie path=%s status=%d", path, w.Code)
		}
	}
	select {
	case <-done:
		t.Fatal("active session closed at cookie expiry")
	default:
	}
	// 停止应用保活但继续回复协议ping，仍必须由5秒输入看门狗清理，不扩大孤立会话存活期。
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("idle viewer retained")
	}
}
