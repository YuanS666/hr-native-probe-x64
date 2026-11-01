package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// testRelay 创建仅用于测试的单设备配置，示例密钥不会进入任何页面或生产配置。
func testRelay() *relayServer {
	return &relayServer{device: "mac-test", origin: "https://localhost:8443", agentKey: strings.Repeat("a", 64), viewerKey: strings.Repeat("v", 64)}
}

// testRequest 构造具备 TLS 和固定来源的请求，供无端口的 HTTP 认证与授权测试使用。
func testRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "https://localhost:8443"+path, strings.NewReader(body))
	r.TLS = &tls.ConnectionState{HandshakeComplete: true}
	r.Header.Set("Origin", "https://localhost:8443")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// loginCookie 通过实际登录处理函数获取 Cookie，并检查浏览器不能读取或跨站发送该 Cookie。
func loginCookie(t *testing.T, s *relayServer) *http.Cookie {
	t.Helper()
	r := testRequest("POST", "/login", url.Values{"device": {s.device}, "key": {s.viewerKey}}.Encode())
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("登录失败: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("缺少登录 Cookie")
	}
	c := cookies[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || strings.Contains(c.Value, s.viewerKey) {
		t.Fatal("Cookie 属性或内容无效")
	}
	return c
}

// TestRelayAuthorization 验证角色密钥分离、设备授权、来源、TLS、Cookie 签名和到期检查。
func TestRelayAuthorization(t *testing.T) {
	s := testRelay()
	cookie := loginCookie(t, s)
	cases := []struct {
		name   string
		req    *http.Request
		status int
	}{
		{"无 Cookie", testRequest("GET", "/ws", ""), 401},
		{"错误设备", testRequest("POST", "/login", "device=other&key="+s.viewerKey), 401},
		{"角色密钥混用", testRequest("POST", "/login", "device=mac-test&key="+s.agentKey), 401},
		{"空 Agent 认证", testRequest("GET", "/agent", ""), 401},
	}
	r := testRequest("GET", "/ws", "")
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://evil.example")
	cases = append(cases, struct {
		name   string
		req    *http.Request
		status int
	}{"跨站 WebSocket", r, 403})
	r = testRequest("GET", "/ws", "")
	r.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value + "x"})
	cases = append(cases, struct {
		name   string
		req    *http.Request
		status int
	}{"篡改 Cookie", r, 401})
	r = testRequest("GET", "/", "")
	r.TLS = nil
	cases = append(cases, struct {
		name   string
		req    *http.Request
		status int
	}{"明文 HTTP", r, 403})
	r = testRequest("GET", "/", "")
	r.Host = "evil.example"
	cases = append(cases, struct {
		name   string
		req    *http.Request
		status int
	}{"错误域名", r, 403})
	r = testRequest("POST", "/login", "device=mac-test&key="+s.viewerKey)
	r.Header.Set("Origin", "https://evil.example")
	cases = append(cases, struct {
		name   string
		req    *http.Request
		status int
	}{"跨站登录", r, 403})
	for _, c := range cases {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, c.req)
		if w.Code != c.status {
			t.Errorf("%s: %d != %d", c.name, w.Code, c.status)
		}
	}
	r = testRequest("GET", "/", "")
	r.AddCookie(cookie)
	if _, ok := s.viewerExpiry(r); !ok {
		t.Fatal("有效 Cookie 被拒绝")
	}
	other := testRelay()
	other.device = "other"
	if _, ok := other.viewerExpiry(r); ok {
		t.Fatal("跨设备 Cookie 被接受")
	}
	payload := strings.Split(cookie.Value, ".")[0]
	data, _ := base64.RawURLEncoding.DecodeString(payload)
	fields := strings.Split(string(data), "|")
	fields[1] = "1"
	payload = base64.RawURLEncoding.EncodeToString([]byte(strings.Join(fields, "|")))
	r = testRequest("GET", "/", "")
	r.AddCookie(&http.Cookie{Name: cookie.Name, Value: payload + "." + s.cookieSignature(payload)})
	if _, ok := s.viewerExpiry(r); ok {
		t.Fatal("过期 Cookie 被接受")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, testRequest("GET", "/", ""))
	if strings.Contains(w.Body.String(), s.viewerKey) || strings.Contains(w.Body.String(), s.agentKey) {
		t.Fatal("前端泄漏配置密钥")
	}
}

// TestLoginOriginPolicy 确保登录页允许同源表单携带真实 Origin，同时继续拒绝 null、缺失和跨站来源。
func TestLoginOriginPolicy(t *testing.T) {
	s := testRelay()
	page := httptest.NewRecorder()
	s.ServeHTTP(page, testRequest("GET", "/", ""))
	if page.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("登录页来源策略会影响表单的 Origin")
	}
	body := url.Values{"device": {s.device}, "key": {s.viewerKey}}.Encode()
	for _, origin := range []string{s.origin, "null", "", "https://evil.example"} {
		r := testRequest("POST", "/login", body)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		want := 403
		if origin == s.origin {
			want = 303
		}
		if w.Code != want {
			t.Errorf("Origin=%q: HTTP %d，预期 %d", origin, w.Code, want)
		}
	}
}

// relayTLS 生成内存测试证书和对应信任池，测试完整 TLS 校验而不跳过证书认证。
func relayTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	server := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	client := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "localhost", RootCAs: pool}
	return server, client
}

type tlsPipeRecorder struct {
	*httptest.ResponseRecorder
	conn   net.Conn
	reader *bufio.Reader
}

// Hijack 保留已读取 HTTP 请求后的缓冲数据，测试真实 TLS 上的 WebSocket 升级过程。
func (w *tlsPipeRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(w.reader, bufio.NewWriter(w.conn)), nil
}

// connectPipeRelay 使用真实 TLS、HTTP 和 WebSocket 握手连接处理函数，不调用系统监听端口。
func connectPipeRelay(t *testing.T, s *relayServer, path string, headers http.Header, serverTLS, clientTLS *tls.Config) (*websocket, <-chan struct{}) {
	t.Helper()
	server, client := net.Pipe()
	secureServer := tls.Server(server, serverTLS)
	secureClient := tls.Client(client, clientTLS)
	secureServer.SetDeadline(time.Now().Add(8 * time.Second))
	done := make(chan struct{})
	// TLS 握手后从字节流读取真实请求，只将实际 TLS 状态交给 Relay 进行来源检查。
	go func() {
		defer close(done)
		defer secureServer.Close()
		reader := bufio.NewReader(secureServer)
		req, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		state := secureServer.ConnectionState()
		req.TLS = &state
		s.ServeHTTP(&tlsPipeRecorder{httptest.NewRecorder(), secureServer, reader}, req)
	}()
	u, _ := url.Parse(s.origin + path)
	w, err := clientHandshake(secureClient, u, headers)
	if err != nil {
		secureClient.Close()
		t.Fatal(err)
	}
	// 测试结束时关闭字节流并等待处理函数退出，避免遗留视频或心跳协程。
	t.Cleanup(func() {
		w.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Relay 会话未清理")
		}
	})
	return w, done
}

type observedMouse struct {
	events  chan string
	pressed bool
}

// handle 记录控制器实际收到的动作并模拟按键状态，不向系统投递任何鼠标事件。
func (m *observedMouse) handle(msg mouseMessage) error {
	if msg.Action == "down" {
		m.pressed = true
	}
	if msg.Action == "up" {
		m.pressed = false
	}
	m.events <- msg.Action
	return nil
}

// release 只有存在模拟按下状态时才记录释放，验证断开通知真的触发 Agent 清理。
func (m *observedMouse) release() error {
	if m.pressed {
		m.pressed = false
		m.events <- "release"
	}
	return nil
}

// waitMouse 在有限时间内检查控制指令或释放通知，防止网络清理失败让测试永久阻塞。
func waitMouse(t *testing.T, m *observedMouse, want string) {
	t.Helper()
	select {
	case got := <-m.events:
		if got != want {
			t.Fatalf("鼠标动作 %q != %q", got, want)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("没有收到鼠标动作 %s", want)
	}
}

// viewerSession 读取 Relay 会话、配置和首个真实 H.264 关键帧，检查 SPS/PPS 与 IDR 同时存在。
func viewerSession(t *testing.T, w *websocket) string {
	t.Helper()
	var sid string
	for {
		op, b, err := w.read()
		if err != nil {
			t.Fatal(err)
		}
		if op == 1 {
			var message struct{ Type, Session, Error string }
			if json.Unmarshal(b, &message) != nil {
				t.Fatal("消息不是 JSON")
			}
			if message.Type == "relay-session" {
				sid = message.Session
			}
			if message.Error != "" {
				t.Fatal(message.Error)
			}
		} else if op == 2 {
			if len(b) < 18 || b[0] != 1 || sid == "" {
				t.Fatal("首帧不是新会话的关键帧")
			}
			var sps, pps []byte
			key, codec := frameInfo(b[17:], &sps, &pps)
			if !key || codec == "" || len(pps) == 0 {
				t.Fatal("首个 IDR 没有 SPS/PPS")
			}
			return sid
		}
	}
}

// TestRelayVideoAndControl 验证加密管道中的真实 FFmpeg 转发、鼠标路由、单控制者、新会话 IDR 与断开释放。
func TestRelayVideoAndControl(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("需要 FFmpeg")
	}
	oldID, oldEncoder, oldPattern := *deviceID, *encoder, *testPattern
	*deviceID = "mac-test"
	*encoder = "libx264"
	*testPattern = true
	// 最后恢复共享配置，所有异步会话先由 cleanup 关闭并等待退出。
	t.Cleanup(func() { *deviceID = oldID; *encoder = oldEncoder; *testPattern = oldPattern })
	s := testRelay()
	serverTLS, clientTLS := relayTLS(t)
	cookie := loginCookie(t, s)
	a, _ := connectPipeRelay(t, s, "/agent", http.Header{"Authorization": {"Bearer " + s.agentKey}, "X-Device-Id": {s.device}}, serverTLS, clientTLS)
	ctx, cancel := context.WithCancel(context.Background())
	mouse := &observedMouse{events: make(chan string, 32)}
	agentDone := make(chan error, 1)
	// 使用真实编码器和模拟鼠标，隔离沙箱不允许的系统输入注入。
	go func() {
		agentDone <- agentConnection(ctx, a, encodeVideo, func() (mouseController, error) { return mouse, nil })
		close(agentDone)
	}()
	// 退出前等待 Agent 释放控制器并结束 FFmpeg。
	t.Cleanup(func() {
		cancel()
		a.Close()
		select {
		case <-agentDone:
		case <-time.After(5 * time.Second):
			t.Error("Agent 未退出")
		}
	})
	headers := http.Header{"Origin": {s.origin}, "Cookie": {cookie.Name + "=" + cookie.Value}}
	v, viewerDone := connectPipeRelay(t, s, "/ws", headers, serverTLS, clientTLS)
	sid := viewerSession(t, v)
	r := testRequest("GET", "/ws", "")
	r.AddCookie(cookie)
	dup := httptest.NewRecorder()
	s.ServeHTTP(dup, r)
	if dup.Code != 409 {
		t.Fatalf("第二个控制者未被拒绝: %d", dup.Code)
	}
	body := json.RawMessage(`{"type":"mouse","action":"down","x":0.5,"y":0.5,"button":0,"clicks":1}`)
	control, _ := json.Marshal(relayCommand{Type: "control", Device: s.device, Session: sid, Mouse: body})
	if err := v.write(1, control); err != nil {
		t.Fatal(err)
	}
	waitMouse(t, mouse, "down")
	v.Close()
	waitMouse(t, mouse, "release")
	select {
	case <-viewerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("浏览器未解绑")
	}
	v2, viewer2Done := connectPipeRelay(t, s, "/ws", headers, serverTLS, clientTLS)
	sid2 := viewerSession(t, v2)
	if sid2 == sid {
		t.Fatal("新浏览器复用了旧会话")
	}
	control, _ = json.Marshal(relayCommand{Type: "control", Device: s.device, Session: sid2, Mouse: body})
	if err := v2.write(1, control); err != nil {
		t.Fatal(err)
	}
	waitMouse(t, mouse, "down")
	// 重放前一会话的控制帧必须断开当前连接并释放按键，不能投递给模拟鼠标。
	control, _ = json.Marshal(relayCommand{Type: "control", Device: s.device, Session: sid, Mouse: body})
	if err := v2.write(1, control); err != nil {
		t.Fatal(err)
	}
	waitMouse(t, mouse, "release")
	select {
	case <-viewer2Done:
	case <-time.After(5 * time.Second):
		t.Fatal("拒绝旧会话后未解绑")
	}
	v3, viewer3Done := connectPipeRelay(t, s, "/ws", headers, serverTLS, clientTLS)
	sid3 := viewerSession(t, v3)
	control, _ = json.Marshal(relayCommand{Type: "control", Device: s.device, Session: sid3, Mouse: body})
	if err := v3.write(1, control); err != nil {
		t.Fatal(err)
	}
	waitMouse(t, mouse, "down")
	// 不再读取视频，写入超时应断开整个会话并释放按键，而不是继续丢弃预测帧。
	waitMouse(t, mouse, "release")
	select {
	case <-viewer3Done:
	case <-time.After(5 * time.Second):
		t.Fatal("慢浏览器未解绑")
	}
	t.Log("通过真实 TLS、认证 WebSocket、FFmpeg IDR 转发、慢客户端断开和模拟鼠标释放；未使用浏览器或原生鼠标")
}

// TestAgentDisconnectReleases 在已有按下状态下切断 Relay 字节流，验证 Agent 在返回前释放按键。
func TestAgentDisconnectReleases(t *testing.T) {
	oldID, oldEncoder := *deviceID, *encoder
	*deviceID = "mac-test"
	// 此用例模拟无首帧的旧FFmpeg生命周期；MF首帧门控由TestNativeAgent系列独立验证。
	*encoder = "libx264"
	// 恢复仅供本测试使用的设备配置。
	defer func() { *deviceID = oldID; *encoder = oldEncoder }()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	server := &websocket{Conn: a, r: bufio.NewReader(a), limit: maxRelayMessage}
	client := &websocket{Conn: b, r: bufio.NewReader(b), client: true, limit: 1024}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mouse := &observedMouse{events: make(chan string, 8)}
	finished := make(chan error, 1)
	// 模拟只等待取消的编码器，隔离网络断开清理与实际屏幕采集。
	encode := func(ctx context.Context, send func(byte, []byte) error) error { <-ctx.Done(); return ctx.Err() }
	// 工厂返回模拟控制器，不需要系统辅助功能权限。
	open := func() (mouseController, error) { return mouse, nil }
	// 执行与真实 Agent 相同的会话状态机。
	go func() { finished <- agentConnection(ctx, client, encode, open) }()
	// 持续读取 Agent 状态和心跳，避免进程内无缓冲连接产生人工阻塞。
	go func() {
		for {
			if _, _, err := server.read(); err != nil {
				return
			}
		}
	}()
	sid := strings.Repeat("0", 32)
	start, _ := json.Marshal(relayCommand{Type: "start", Device: "mac-test", Session: sid})
	if err := server.write(1, start); err != nil {
		t.Fatal(err)
	}
	control, _ := json.Marshal(relayCommand{Type: "control", Device: "mac-test", Session: sid, Mouse: json.RawMessage(`{"type":"mouse","action":"down","x":0.5,"y":0.5,"button":0,"clicks":1}`)})
	if err := server.write(1, control); err != nil {
		t.Fatal(err)
	}
	waitMouse(t, mouse, "down")
	server.Close()
	waitMouse(t, mouse, "release")
	select {
	case <-finished:
	case <-time.After(4 * time.Second):
		t.Fatal("Agent 断开后没有退出")
	}
}

// TestAgentEncoderFailure 验证编码器异常后释放按键并退出连接，不在同一会话里无限重启编码。
func TestAgentEncoderFailure(t *testing.T) {
	oldID, oldEncoder := *deviceID, *encoder
	*deviceID = "mac-test"
	// 此用例模拟无首帧的旧FFmpeg生命周期；MF首帧门控由TestNativeAgent系列独立验证。
	*encoder = "libx264"
	// 测试结束恢复设备配置，避免影响其他认证用例。
	defer func() { *deviceID = oldID; *encoder = oldEncoder }()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	server := &websocket{Conn: a, r: bufio.NewReader(a), limit: maxRelayMessage}
	client := &websocket{Conn: b, r: bufio.NewReader(b), client: true, limit: 1024}
	mouse := &observedMouse{events: make(chan string, 8)}
	fail := make(chan struct{})
	finished := make(chan error, 1)
	calls := 0
	// 模拟已开始工作后退出的 FFmpeg，退出信号只在按下事件到达后发送。
	encode := func(ctx context.Context, send func(byte, []byte) error) error {
		calls++
		select {
		case <-fail:
			return errors.New("FFmpeg test exit")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// 鼠标工厂返回可观察的状态替身，不投递原生事件。
	open := func() (mouseController, error) { return mouse, nil }
	// 运行生产会话状态机，结束通道也同步编码调用次数的读取。
	go func() { finished <- agentConnection(context.Background(), client, encode, open) }()
	// 排空状态消息和编码错误，避免 net.Pipe 无缓冲写入影响退出测试。
	go func() {
		for {
			if _, _, err := server.read(); err != nil {
				return
			}
		}
	}()
	sid := strings.Repeat("0", 32)
	start, _ := json.Marshal(relayCommand{Type: "start", Device: "mac-test", Session: sid})
	if err := server.write(1, start); err != nil {
		t.Fatal(err)
	}
	control, _ := json.Marshal(relayCommand{Type: "control", Device: "mac-test", Session: sid, Mouse: json.RawMessage(`{"type":"mouse","action":"down","x":0.5,"y":0.5,"button":0,"clicks":1}`)})
	if err := server.write(1, control); err != nil {
		t.Fatal(err)
	}
	waitMouse(t, mouse, "down")
	close(fail)
	waitMouse(t, mouse, "release")
	select {
	case err := <-finished:
		if err == nil || !strings.Contains(err.Error(), "FFmpeg test exit") || calls != 1 {
			t.Fatalf("编码异常处理: calls=%d err=%v", calls, err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("编码异常后 Agent 未退出连接")
	}
}

// TestWSSRequired 验证明文 URL 在拨号前被拒绝，未信任测试证书时真实 TLS 握手必须失败。
func TestWSSRequired(t *testing.T) {
	if _, err := dialWS(context.Background(), "ws://localhost:8443/agent", nil, nil); err == nil {
		t.Fatal("允许明文 WS")
	}
	serverTLS, _ := relayTLS(t)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	a.SetDeadline(time.Now().Add(2 * time.Second))
	b.SetDeadline(time.Now().Add(2 * time.Second))
	server := tls.Server(a, serverTLS)
	client := tls.Client(b, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "localhost", RootCAs: x509.NewCertPool()})
	done := make(chan error, 1)
	// 服务端实际执行握手，客户端的空信任池必须拒绝此证书。
	go func() { done <- server.Handshake() }()
	err := client.Handshake()
	var authority x509.UnknownAuthorityError
	if !errors.As(err, &authority) {
		t.Fatalf("预期证书不受信任，得到 %v", err)
	}
	a.Close()
	b.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TLS 握手没有结束")
	}
}

// TestRelayFrameLimits 验证大视频消息、掩码方向及输入上限，拒绝无限长度的分配请求。
func TestRelayFrameLimits(t *testing.T) {
	for _, size := range []int{1, 126, 65536} {
		a, b := net.Pipe()
		done := make(chan error, 1)
		body := bytes.Repeat([]byte{7}, size)
		// 编码端按客户端方向发送，读取端必须得到完整且未改变的数据。
		go func() { done <- sendWSFrame(a, 2, body, true); a.Close() }()
		op, got, err := readWSFrame(b, true, maxRelayMessage)
		b.Close()
		if err != nil || op != 2 || !bytes.Equal(got, body) {
			t.Fatal("消息长度/掩码失败", size, err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range [][]byte{{0x82, 127, 127, 255, 255, 255, 255, 255, 255, 255}, {0x81, 0x80, 0, 0, 0, 0}} {
		if _, _, err := readWSFrame(bytes.NewReader(frame), false, 1024); err == nil {
			t.Fatal("非法长度或掩码被接受")
		}
	}
	for _, body := range []string{
		`{"type":"control","device":"mac-test","session":"bad","mouse":{"type":"mouse","action":"release"}}`,
		`{"type":"start","device":"mac-test","session":"00000000000000000000000000000000","mouse":{}}`,
		`{"type":"control","device":"mac-test","session":"00000000000000000000000000000000","mouse":{"type":"mouse","action":"move","x":2,"y":0}}`,
	} {
		if _, err := decodeRelayCommand([]byte(body)); err == nil {
			t.Fatal("非法会话消息被接受")
		}
	}
}
