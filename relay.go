package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type relayCommand struct {
	Type    string          `json:"type"`
	Device  string          `json:"device"`
	Session string          `json:"session"`
	Mouse   json.RawMessage `json:"mouse,omitempty"`
}

type relayServer struct {
	device, origin, agentKey, viewerKey string
	mu                                  sync.Mutex
	agent, viewer                       *websocket
	session                             string
}

const loginPage = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>远程屏幕认证</title><h1>连接 Mac</h1><form method="post" action="/login"><p><label>设备 ID <input name="device" required maxlength="64" autocomplete="username"></label></p><p><label>浏览器密钥 <input type="password" name="key" required autocomplete="current-password"></label></p><button>认证</button></form><p>输入 Relay 配置的设备 ID 和浏览器密钥。密钥不会放入 URL 或前端源码。</p></html>`

// validDevice 限制设备 ID 为 1–64 个英文字母、数字、横线或下划线，便于严格绑定单设备授权。
func validDevice(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// sameSecret 对固定长度摘要做常量时间比较，避免直接比较认证密钥。
func sameSecret(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// randomSession 生成 128 位随机会话标识，确保旧浏览器指令不能进入新会话。
func randomSession() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

// decodeRelayCommand 严格解析设备和会话外层，鼠标字段仍由原有 parseMouse 进一步验证。
func decodeRelayCommand(b []byte) (relayCommand, error) {
	var cmd relayCommand
	if len(b) > 1024 {
		return cmd, fmt.Errorf("控制消息超限")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&cmd); err != nil {
		return cmd, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return cmd, fmt.Errorf("消息尾部无效")
	}
	sid, err := hex.DecodeString(cmd.Session)
	if !validDevice(cmd.Device) || err != nil || len(sid) != 16 || len(cmd.Session) != 32 {
		return cmd, fmt.Errorf("设备或会话格式无效")
	}
	if cmd.Type == "control" {
		if _, err := parseMouse(cmd.Mouse); err != nil {
			return cmd, err
		}
	} else if (cmd.Type != "start" && cmd.Type != "stop") || len(cmd.Mouse) != 0 {
		return cmd, fmt.Errorf("未知会话指令")
	}
	return cmd, nil
}

// cookieSignature 为设备、到期时间和随机数签名，浏览器只得到 HttpOnly Cookie 而非配置密钥。
func (s *relayServer) cookieSignature(payload string) string {
	mac := hmac.New(sha256.New, []byte(s.viewerKey))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// viewerExpiry 校验登录 Cookie 的签名、目标设备和有效期，返回该浏览器授权的到期时间。
func (s *relayServer) viewerExpiry(r *http.Request) (time.Time, bool) {
	c, err := r.Cookie("__Host-screen")
	if err != nil {
		return time.Time{}, false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 || !sameSecret(parts[1], s.cookieSignature(parts[0])) {
		return time.Time{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return time.Time{}, false
	}
	fields := strings.Split(string(data), "|")
	if len(fields) != 3 || fields[0] != s.device || len(fields[2]) != 32 {
		return time.Time{}, false
	}
	expires, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || expires <= time.Now().Unix() || expires > time.Now().Add(31*time.Minute).Unix() {
		return time.Time{}, false
	}
	return time.Unix(expires, 0), true
}

// login 验证单设备浏览器密钥后签发 30 分钟的安全 Cookie，表单只接受同源 HTTPS POST。
func (s *relayServer) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.Header.Get("Origin") != s.origin {
		http.Error(w, "来源无效", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if r.ParseForm() != nil || r.PostForm.Get("device") != s.device || !sameSecret(r.PostForm.Get("key"), s.viewerKey) {
		http.Error(w, "设备或密钥错误", 401)
		return
	}
	nonce, err := randomSession()
	if err != nil {
		http.Error(w, "无法生成授权", 500)
		return
	}
	expires := time.Now().Add(30 * time.Minute)
	payload := base64.RawURLEncoding.EncodeToString([]byte(s.device + "|" + strconv.FormatInt(expires.Unix(), 10) + "|" + nonce))
	http.SetCookie(w, &http.Cookie{Name: "__Host-screen", Value: payload + "." + s.cookieSignature(payload), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 1800})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ServeHTTP 只接受配置域名上的直接 TLS 请求，页面、Agent 和浏览器各自走独立认证路径。
func (s *relayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u, _ := url.Parse(s.origin)
	if r.TLS == nil || r.Host != u.Host {
		http.Error(w, "需要配置域名的 HTTPS", 403)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// no-referrer 会让普通表单 POST 的 Origin 变为 null；保留同源来源以便严格验证登录请求。
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' wss://"+u.Host+"; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; form-action 'self'")
	switch r.URL.Path {
	case "/login":
		s.login(w, r)
	case "/agent":
		if r.Header.Get("Origin") != "" || r.Header.Get("X-Device-ID") != s.device || !sameSecret(r.Header.Get("Authorization"), "Bearer "+s.agentKey) {
			http.Error(w, "Agent 认证失败", 401)
			return
		}
		s.serveAgent(w, r)
	case "/ws":
		expires, ok := s.viewerExpiry(r)
		if !ok {
			http.Error(w, "请刷新页面重新认证", 401)
			return
		}
		if r.Header.Get("Origin") != s.origin {
			http.Error(w, "来源无效", 403)
			return
		}
		s.serveViewer(w, r, expires)
	case "/":
		if r.Method != "GET" {
			http.Error(w, "仅支持 GET", 405)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, ok := s.viewerExpiry(r); !ok {
			io.WriteString(w, loginPage)
			return
		}
		w.Write(html)
	default:
		http.NotFound(w, r)
	}
}

// serveAgent 登记唯一 Agent 并按会话转发原始视频消息，慢浏览器写入超时后终止整个会话而不丢预测帧。
func (s *relayServer) serveAgent(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.agent != nil {
		s.mu.Unlock()
		http.Error(w, "Agent 已连接", 409)
		return
	}
	a, err := upgradeWS(w, r, maxRelayMessage)
	if err != nil {
		s.mu.Unlock()
		return
	}
	s.agent = a
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer a.Close()
	go a.keepAlive(ctx)
	// 只清理当前 Agent 所属的浏览器，避免旧连接退出时影响之后的新连接。
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.agent == a {
			s.agent = nil
			if s.viewer != nil {
				s.viewer.Close()
			}
			s.viewer = nil
			s.session = ""
		}
	}()
	log.Print("Relay: Agent 已认证连接")
	for {
		op, b, err := a.read()
		if err != nil {
			return
		}
		if op != 2 || len(b) < 18 || (b[16] != 1 && b[16] != 2) {
			return
		}
		session := hex.EncodeToString(b[:16])
		kind, payload := b[16], b[17:]
		if kind == 1 && (len(payload) > 4096 || !json.Valid(payload)) {
			return
		}
		if kind == 2 && (len(payload) <= 17 || payload[0] > 1) {
			return
		}
		s.mu.Lock()
		viewer, current := s.viewer, s.session
		s.mu.Unlock()
		if viewer == nil || session != current {
			continue
		}
		if viewer.write(kind, payload) != nil {
			s.detachViewer(viewer)
		}
	}
}

// sendCommand 在已认证的 Agent 通道写入设备与会话绑定的指令，不允许调用方伪造来源身份。
func (s *relayServer) sendCommand(kind, session string, mouse json.RawMessage) error {
	data, err := json.Marshal(relayCommand{Type: kind, Device: s.device, Session: session, Mouse: mouse})
	if err != nil {
		return err
	}
	return s.agent.write(1, data)
}

// serveViewer 只接纳一个有设备授权的浏览器，每次连接生成新会话并要求 Agent 从新编码器的 IDR 开始。
func (s *relayServer) serveViewer(w http.ResponseWriter, r *http.Request, expires time.Time) {
	s.mu.Lock()
	if s.agent == nil || s.viewer != nil {
		s.mu.Unlock()
		http.Error(w, "Agent 离线或设备已被控制", 409)
		return
	}
	sid, err := randomSession()
	if err != nil {
		s.mu.Unlock()
		http.Error(w, "无法建立会话", 500)
		return
	}
	v, err := upgradeWS(w, r, 1024)
	if err != nil {
		s.mu.Unlock()
		return
	}
	s.viewer = v
	s.session = sid
	welcome, _ := json.Marshal(map[string]string{"type": "relay-session", "device": s.device, "session": sid})
	err = v.write(1, welcome)
	if err == nil {
		err = s.sendCommand("start", sid, nil)
	}
	s.mu.Unlock()
	defer v.Close()
	defer s.detachViewer(v)
	if err != nil {
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), expires)
	defer cancel()
	go v.keepAlive(ctx)
	// 应用层保活消失时也释放控制，不能仅依赖浏览器自动回复的协议 pong。
	watchdog := time.AfterFunc(5*time.Second, func() { v.Close() })
	defer watchdog.Stop()
	for {
		op, b, err := v.read()
		if err != nil {
			return
		}
		cmd, err := decodeRelayCommand(b)
		if op != 1 || err != nil || cmd.Type != "control" || cmd.Device != s.device || cmd.Session != sid {
			return
		}
		watchdog.Reset(5 * time.Second)
		s.mu.Lock()
		if s.viewer != v || s.agent == nil {
			s.mu.Unlock()
			return
		}
		err = s.sendCommand("control", sid, cmd.Mouse)
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// detachViewer 原子解绑浏览器并通知 Agent 停止编码、释放鼠标，通知失败时关闭 Agent 连接触发本地清理。
func (s *relayServer) detachViewer(v *websocket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.viewer != v {
		return
	}
	v.Close()
	if s.agent != nil && s.sendCommand("stop", s.session, nil) != nil {
		s.agent.Close()
	}
	s.viewer = nil
	s.session = ""
}

// closeConnections 在 Relay 退出时关闭两个已升级连接，Agent 会在断开检测后释放所有按键。
func (s *relayServer) closeConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.viewer != nil {
		s.viewer.Close()
	}
	if s.agent != nil {
		s.agent.Close()
	}
}

type cappedListener struct {
	net.Listener
	slots chan struct{}
}
type cappedConn struct {
	net.Conn
	once  sync.Once
	slots chan struct{}
}

// Accept 将包括 TLS 握手在内的同时连接数限制为 16，超额连接直接关闭而不创建处理协程。
func (l *cappedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &cappedConn{Conn: c, slots: l.slots}, nil
		default:
			c.Close()
		}
	}
}

// Close 关闭连接并且只归还一次连接配额，允许 HTTP 与 WebSocket 的多个清理路径重复调用。
func (c *cappedConn) Close() error {
	err := c.Conn.Close()
	// 只在第一次关闭时释放监听器配额。
	c.once.Do(func() { <-c.slots })
	return err
}

// runRelay 校验单设备配置并启动直接 HTTPS/WSS 服务，拒绝缺少证书或弱认证配置的启动。
func runRelay(ctx context.Context) error {
	agentKey, viewerKey := os.Getenv("RELAY_AGENT_KEY"), os.Getenv("RELAY_VIEWER_KEY")
	u, err := url.Parse(*publicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("-public-url 必须是 https://域名[:端口]")
	}
	if !validDevice(*deviceID) || len(agentKey) < 32 || len(viewerKey) < 32 || sameSecret(agentKey, viewerKey) {
		return fmt.Errorf("需要合法设备 ID 及至少 32 字节且不同的 RELAY_AGENT_KEY / RELAY_VIEWER_KEY")
	}
	if *tlsCert == "" || *tlsKey == "" {
		return fmt.Errorf("Relay 必须配置 -tls-cert 和 -tls-key，不提供明文服务")
	}
	pair, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
	if err != nil {
		return fmt.Errorf("加载 TLS 证书: %w", err)
	}
	s := &relayServer{device: *deviceID, origin: "https://" + u.Host, agentKey: agentKey, viewerKey: viewerKey}
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}}
	l, err := net.Listen("tcp", *relayListen)
	if err != nil {
		return err
	}
	defer l.Close()
	done := make(chan struct{})
	// 取消时先关闭升级连接，再等待普通 HTTP 请求结束，避免退出留下控制会话。
	go func() { <-ctx.Done(); s.closeConnections(); server.Close(); close(done) }()
	log.Printf("Relay: %s，监听 %s，单设备 %s", s.origin, *relayListen, s.device)
	err = server.ServeTLS(&cappedListener{Listener: l, slots: make(chan struct{}, 16)}, "", "")
	if ctx.Err() != nil {
		<-done
		return nil
	}
	return err
}
