package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxRelayMessage = (16 << 20) + 64

type websocket struct {
	net.Conn
	r      *bufio.Reader
	client bool
	limit  int
	mu     sync.Mutex
}

// Close 直接关闭底层连接，避免 TLS 优雅关闭等待对端读取而拖延鼠标释放或阻塞下一会话。
func (w *websocket) Close() error {
	if secure, ok := w.Conn.(*tls.Conn); ok {
		return secure.NetConn().Close()
	}
	return w.Conn.Close()
}

// readWSFrame 读取一个完整帧，检查掩码方向、长度与操作码；不协商压缩或分片，超限时不分配消息内存。
func readWSFrame(r io.Reader, masked bool, limit int) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	op := h[0] & 15
	if h[0]&0xf0 != 0x80 || (h[1]&128 != 0) != masked || (op != 1 && op != 2 && op != 8 && op != 9 && op != 10) {
		return 0, nil, fmt.Errorf("WebSocket 帧格式无效")
	}
	n := uint64(h[1] & 127)
	if n == 126 {
		var size [2]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(size[:]))
		if n < 126 {
			return 0, nil, fmt.Errorf("长度编码无效")
		}
	} else if n == 127 {
		var size [8]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(size[:])
		if n <= 65535 {
			return 0, nil, fmt.Errorf("长度编码无效")
		}
	}
	if n > uint64(limit) || (op >= 8 && n > 125) {
		return 0, nil, fmt.Errorf("WebSocket 消息超限")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	b := make([]byte, int(n))
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range b {
			b[i] ^= mask[i%4]
		}
	}
	if op == 1 && !utf8.Valid(b) {
		return 0, nil, fmt.Errorf("文本不是 UTF-8")
	}
	if op == 8 && (len(b) == 1 || (len(b) > 2 && !utf8.Valid(b[2:]))) {
		return 0, nil, fmt.Errorf("关闭帧无效")
	}
	return op, b, nil
}

// sendWSFrame 按客户端或服务端方向发送一帧，客户端使用随机掩码，写入最多等待三秒。
func sendWSFrame(c net.Conn, op byte, b []byte, masked bool) error {
	if len(b) > maxRelayMessage {
		return fmt.Errorf("发送消息超限")
	}
	c.SetWriteDeadline(time.Now().Add(3 * time.Second))
	h := []byte{128 | op, byte(len(b))}
	if len(b) >= 126 && len(b) <= 65535 {
		h = []byte{128 | op, 126, 0, 0}
		binary.BigEndian.PutUint16(h[2:], uint16(len(b)))
	}
	if len(b) > 65535 {
		h = make([]byte, 10)
		h[0] = 128 | op
		h[1] = 127
		binary.BigEndian.PutUint64(h[2:], uint64(len(b)))
	}
	if masked {
		h[1] |= 128
		var mask [4]byte
		if _, err := rand.Read(mask[:]); err != nil {
			return err
		}
		h = append(h, mask[:]...)
		data := make([]byte, len(b))
		for i := range b {
			data[i] = b[i] ^ mask[i%4]
		}
		b = data
	}
	parts := net.Buffers{h, b}
	_, err := parts.WriteTo(c)
	return err
}

// write 将整个消息串行发送，防止心跳、控制和视频的字节交错。
func (w *websocket) write(op byte, b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return sendWSFrame(w.Conn, op, b, w.client)
}

// read 读取业务消息并处理 ping/pong，十秒无完整消息则返回超时，关闭帧返回 EOF。
func (w *websocket) read() (byte, []byte, error) {
	for {
		w.SetReadDeadline(time.Now().Add(10 * time.Second))
		op, b, err := readWSFrame(w.r, !w.client, w.limit)
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case 8:
			return 0, nil, io.EOF
		case 9:
			if err := w.write(10, b); err != nil {
				return 0, nil, err
			}
		case 10:
		default:
			return op, b, nil
		}
	}
}

// keepAlive 每三秒发送 ping，退出或失败时关闭连接，让读取方立即进行会话清理。
func (w *websocket) keepAlive(ctx context.Context) {
	defer w.Close()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.write(9, nil) != nil {
				return
			}
		}
	}
}

// wsAccept 根据客户端随机 key 生成 RFC 6455 握手响应，不承担身份认证。
func wsAccept(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

// upgradeWS 在调用方完成身份和来源检查后升级连接，limit 为该角色接收的消息上限。
func upgradeWS(w http.ResponseWriter, r *http.Request, limit int) (*websocket, error) {
	key, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	if r.Method != "GET" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !hasUpgrade(r.Header.Get("Connection")) || r.Header.Get("Sec-WebSocket-Version") != "13" || err != nil || len(key) != 16 {
		http.Error(w, "WebSocket 握手无效", 400)
		return nil, fmt.Errorf("握手无效")
	}
	c, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return nil, err
	}
	c.SetWriteDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", wsAccept(r.Header.Get("Sec-WebSocket-Key")))
	if err := rw.Flush(); err != nil {
		c.Close()
		return nil, err
	}
	return &websocket{Conn: c, r: rw.Reader, limit: limit}, nil
}

// hasUpgrade 检查 Connection 的独立 token，避免将其他包含 upgrade 字符的值当作升级请求。
func hasUpgrade(value string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

// clientHandshake 在已建立的 TLS 连接上发送认证握手，校验服务端响应后返回带掩码的客户端连接。
func clientHandshake(c net.Conn, u *url.URL, headers http.Header) (*websocket, error) {
	c.SetDeadline(time.Now().Add(10 * time.Second))
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	req := &http.Request{Method: "GET", URL: u, Host: u.Host, Header: headers.Clone()}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	if err := req.Write(c); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(io.LimitReader(c, 8192), 8192)
	response, err := http.ReadResponse(r, req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 101 {
		return nil, fmt.Errorf("Relay 握手 HTTP %d", response.StatusCode)
	}
	if !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") || !hasUpgrade(response.Header.Get("Connection")) || response.Header.Get("Sec-WebSocket-Accept") != wsAccept(key) || response.Header.Get("Sec-WebSocket-Extensions") != "" {
		return nil, fmt.Errorf("Relay 握手校验失败")
	}
	// 握手头最多读取 8 KiB；保留提前读到的帧字节，后续再按消息上限读取原连接。
	prefix := make([]byte, r.Buffered())
	if _, err := io.ReadFull(r, prefix); err != nil {
		return nil, err
	}
	r = bufio.NewReader(io.MultiReader(bytes.NewReader(prefix), c))
	c.SetDeadline(time.Time{})
	return &websocket{Conn: c, r: r, client: true, limit: maxRelayMessage}, nil
}

// dialWS 主动连接验证证书的 WSS 服务，不允许明文连接、跳过证书校验或 URL 中携带密钥。
func dialWS(ctx context.Context, address string, headers http.Header, config *tls.Config) (*websocket, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "wss" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/agent" {
		return nil, fmt.Errorf("Relay 地址必须为 wss://主机[:端口]/agent")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: config}
	c, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, err
	}
	// 退出信号也中断 TLS 后的 HTTP 握手，不必等待整个握手超时。
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	w, err := clientHandshake(c, u, headers)
	if err != nil {
		c.Close()
	}
	return w, err
}
