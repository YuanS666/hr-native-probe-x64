package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// limitedLogin 用真实HTTP处理入口提交指定来源和密钥，返回状态及响应头供断言。
func limitedLogin(s *relayServer, peer, key string) *httptest.ResponseRecorder {
	r := testRequest("POST", "/login", url.Values{"device": {s.device}, "key": {key}}.Encode())
	r.RemoteAddr = peer
	r.Header.Set("X-Forwarded-For", key)
	r.Header.Set("Forwarded", key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// TestRelayLoginLimitHTTP 验证失败上限、端口和转发头不可绕过、正确密钥也遵守冷却时间。
func TestRelayLoginLimitHTTP(t *testing.T) {
	s := testRelay()
	for i := 0; i < loginSourceFailures; i++ {
		if w := limitedLogin(s, "192.0.2.1:1234", "wrong"); w.Code != 401 {
			t.Fatalf("failure %d: %d", i, w.Code)
		}
	}
	w := limitedLogin(s, "[::ffff:192.0.2.1]:5555", s.viewerKey)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), s.viewerKey) {
		t.Fatalf("invalid block: %d %v", w.Code, w.Header())
	}
	if w := limitedLogin(s, "192.0.2.2:1234", s.viewerKey); w.Code != 303 {
		t.Fatalf("other source: %d", w.Code)
	}
	if w := limitedLogin(s, "invalid", s.viewerKey); w.Code != 400 {
		t.Fatalf("invalid peer: %d", w.Code)
	}
	s = testRelay()
	for i := 0; i < loginSourceFailures; i++ {
		limitedLogin(s, "[2001:db8:1::1]:1234", "wrong")
	}
	if w := limitedLogin(s, "[2001:db8:1::99]:1234", s.viewerKey); w.Code != 429 {
		t.Fatalf("IPv6 rotation: %d", w.Code)
	}
	if w := limitedLogin(s, "[2001:db8:2::1]:1234", s.viewerKey); w.Code != 303 {
		t.Fatalf("IPv6 other prefix: %d", w.Code)
	}
}

// TestRelayLoginLimitConcurrent 验证并发请求只能执行五次失败凭据比较，不能越过原子预算。
func TestRelayLoginLimitConcurrent(t *testing.T) {
	var l loginLimiter
	now := time.Now()
	var compared atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		// 并发模拟相同来源的攻击请求，比较回调仅记录执行次数。
		go func() { defer wg.Done(); l.verify("source", now, func() bool { compared.Add(1); return false }) }()
	}
	wg.Wait()
	if compared.Load() != loginSourceFailures {
		t.Fatalf("comparisons=%d", compared.Load())
	}
	if wait := l.blocked("source", now.Add(30*time.Second)); wait != 30*time.Second {
		t.Fatalf("wait=%v", wait)
	}
	if wait := l.blocked("source", now.Add(time.Minute)); wait != 0 {
		t.Fatalf("expired=%v", wait)
	}
}

// TestRelayLoginLimitBounds 验证全局预算、来源容量、成功请求不分配记录和到期回收。
func TestRelayLoginLimitBounds(t *testing.T) {
	now := time.Now()
	var l loginLimiter
	for i := 0; i < loginSourceCapacity; i++ {
		l.verify(string(rune(i)), now, func() bool { return false })
	}
	if _, wait := l.verify("new", now, func() bool { t.Fatal("capacity bypass"); return true }); wait != time.Minute {
		t.Fatalf("capacity wait=%v", wait)
	}
	if len(l.sources) != loginSourceCapacity {
		t.Fatal("unbounded records")
	}
	l.verify("new", now.Add(time.Minute), func() bool { return false })
	if len(l.sources) != 1 || l.global.count != 1 {
		t.Fatal("expired records not reclaimed")
	}
	l = loginLimiter{}
	for i := 0; i < 20; i++ {
		for j := 0; j < 5; j++ {
			l.verify(string(rune(i)), now, func() bool { return false })
		}
	}
	if l.blocked("fresh", now) != time.Minute {
		t.Fatal("global limit missing")
	}
	l = loginLimiter{}
	for i := 0; i < 1000; i++ {
		ok, wait := l.verify(string(rune(i)), now, func() bool { return true })
		if !ok || wait != 0 {
			t.Fatal("valid login blocked")
		}
	}
	if len(l.sources) != 0 || l.global.count != 0 {
		t.Fatal("success consumes budget")
	}
}

// TestRelayLoginLimitExistingSession 验证限频不影响现有Cookie会话和Agent独立认证与重连。
func TestRelayLoginLimitExistingSession(t *testing.T) {
	s := testRelay()
	cookie := loginCookie(t, s)
	now := time.Now()
	for i := 0; i < 20; i++ {
		for j := 0; j < 5; j++ {
			s.logins.verify(string(rune(i)), now, func() bool { return false })
		}
	}
	r := testRequest("GET", "/session", "")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("session=%d", w.Code)
	}
	serverTLS, clientTLS := relayTLS(t)
	headers := http.Header{"Authorization": {"Bearer " + s.agentKey}, "X-Device-Id": {s.device}}
	for i := 0; i < 2; i++ {
		agent, done := connectPipeRelay(t, s, "/agent", headers, serverTLS, clientTLS)
		agent.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("agent disconnect not cleaned")
		}
	}
}

// TestRelayLoginLimitPolicy 验证非法来源不消耗预算，成功登录不清空失败记录，等待秒数向上取整。
func TestRelayLoginLimitPolicy(t *testing.T) {
	s := testRelay()
	for i := 0; i < 10; i++ {
		r := testRequest("POST", "/login", "")
		r.Header.Set("Origin", "https://other.invalid")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("origin=%d", w.Code)
		}
	}
	if len(s.logins.sources) != 0 {
		t.Fatal("invalid origin consumes budget")
	}
	for i := 0; i < 4; i++ {
		limitedLogin(s, "192.0.2.9:1234", "wrong")
	}
	for i := 0; i < 10; i++ {
		if w := limitedLogin(s, "192.0.2.9:1234", s.viewerKey); w.Code != 303 {
			t.Fatal("valid login blocked early")
		}
	}
	if w := limitedLogin(s, "192.0.2.9:1234", "wrong"); w.Code != 401 {
		t.Fatal("fifth failure")
	}
	if w := limitedLogin(s, "192.0.2.9:1234", s.viewerKey); w.Code != 429 {
		t.Fatal("success reset failures")
	}
	w := httptest.NewRecorder()
	loginRetry(w, time.Second+time.Nanosecond)
	if w.Header().Get("Retry-After") != "2" {
		t.Fatal("retry rounding")
	}
}
