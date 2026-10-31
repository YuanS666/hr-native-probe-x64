package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMouseValidation 检查有效操作与恶意输入，确保非法类型、越界坐标和大消息不会进入原生 API。
func TestMouseValidation(t *testing.T) {
	valid := []string{
		`{"type":"mouse","action":"move","x":0,"y":1}`,
		`{"type":"mouse","action":"down","x":0.5,"y":0.5,"button":0,"clicks":2}`,
		`{"type":"mouse","action":"up","x":0.5,"y":0.5,"button":1}`,
		`{"type":"mouse","action":"wheel","x":0.5,"y":0.5,"delta":-600}`,
		`{"type":"mouse","action":"release"}`,
		`{"type":"mouse","action":"heartbeat"}`,
	}
	for _, input := range valid {
		if _, err := parseMouse([]byte(input)); err != nil {
			t.Errorf("有效消息被拒绝 %s: %v", input, err)
		}
	}
	invalid := []string{
		`null`, `{}`, `{"type":"keyboard","action":"release"}`,
		`{"type":"mouse","action":"click"}`,
		`{"type":"mouse","action":"move","x":1.01,"y":0}`,
		`{"type":"mouse","action":"move","x":-0.01,"y":0}`,
		`{"type":"mouse","action":"move","x":null,"y":0}`,
		`{"type":"mouse","action":"move","x":"0","y":0}`,
		`{"type":"mouse","action":"move","x":1e999,"y":0}`,
		`{"type":"mouse","action":"move","x":0,"y":0,"extra":true}`,
		`{"type":"mouse","action":"down","x":0,"y":0,"button":2,"clicks":1}`,
		`{"type":"mouse","action":"down","x":0,"y":0,"button":0}`,
		`{"type":"mouse","action":"down","x":0,"y":0,"button":0,"clicks":3}`,
		`{"type":"mouse","action":"wheel","x":0,"y":0,"delta":601}`,
		`{"type":"mouse","action":"wheel","x":0,"y":0,"delta":0.5}`,
		`{"type":"mouse","action":"release","button":0}`,
		`{"type":"mouse","action":"release"} {}`,
		strings.Repeat(" ", 513), string([]byte{0xff}),
	}
	for _, input := range invalid {
		if _, err := parseMouse([]byte(input)); err == nil {
			t.Errorf("非法消息被接受: %s", input)
		}
	}
}

type hijackRecorder struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

// Hijack 将 HTTP 握手交给进程内连接，使测试覆盖真实会话而无需监听端口。
func (w *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

// TestVideoControlSession 验证真实握手后视频与 pong 共用连接，且未启用鼠标时控制指令会终止会话。
func TestVideoControlSession(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("需要 FFmpeg")
	}
	oldEncoder, oldPattern, oldMouse := *encoder, *testPattern, *mouseEnabled
	*encoder = "libx264"
	*testPattern = true
	*mouseEnabled = false
	// 恢复参数，防止测试会话影响其余视频测试。
	defer func() { *encoder = oldEncoder; *testPattern = oldPattern; *mouseEnabled = oldMouse }()
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	client.SetDeadline(time.Now().Add(8 * time.Second))
	req := httptest.NewRequest("GET", "http://127.0.0.1:8080/ws", nil)
	for key, value := range map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==", "Origin": "http://127.0.0.1:8080"} {
		req.Header.Set(key, value)
	}
	done := make(chan struct{})
	// 执行原有 HTTP 处理函数，保持与正常浏览器连接相同的握手和会话清理路径。
	go func() { stream(&hijackRecorder{httptest.NewRecorder(), server}, req); close(done) }()
	r := bufio.NewReader(client)
	resp, err := http.ReadResponse(r, req)
	if err != nil || resp.StatusCode != 101 {
		t.Fatal("握手失败", resp, err)
	}
	pingDone := make(chan error, 1)
	// 同时发送 ping，确认上行控制帧不会再像旧版本一样直接终止视频。
	go func() { _, err := client.Write(maskedFrame(9, []byte("check"))); pingDone <- err }()
	video, pong := false, false
	for !video || !pong {
		var head [2]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			t.Fatal(err)
		}
		n := uint64(head[1] & 127)
		if n == 126 {
			var size [2]byte
			if _, err := io.ReadFull(r, size[:]); err != nil {
				t.Fatal(err)
			}
			n = uint64(binary.BigEndian.Uint16(size[:]))
		} else if n == 127 {
			var size [8]byte
			if _, err := io.ReadFull(r, size[:]); err != nil {
				t.Fatal(err)
			}
			n = binary.BigEndian.Uint64(size[:])
		}
		if n > 16<<20 {
			t.Fatal("服务端帧过大")
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			t.Fatal(err)
		}
		if head[0]&15 == 2 {
			video = true
		}
		if head[0]&15 == 10 {
			pong = string(body) == "check"
		}
	}
	if err := <-pingDone; err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(maskedFrame(1, []byte(`{"type":"mouse","action":"release"}`))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("拒绝未授权控制后会话未清理")
	}
}

// maskedFrame 构造真实浏览器格式的掩码帧，供协议测试覆盖短消息和扩展长度消息。
func maskedFrame(opcode byte, body []byte) []byte {
	head := []byte{0x80 | opcode, 0x80 | byte(len(body))}
	if len(body) >= 126 {
		head = []byte{0x80 | opcode, 0xfe, 0, 0}
		binary.BigEndian.PutUint16(head[2:], uint16(len(body)))
	}
	mask := []byte{1, 2, 3, 4}
	head = append(head, mask...)
	for i, b := range body {
		head = append(head, b^mask[i%4])
	}
	return head
}

// TestClientWS 验证掩码解析和连续消息边界，并拒绝分片、超长、未掩码及未知操作码。
func TestClientWS(t *testing.T) {
	message := []byte(`{"type":"mouse","action":"release"}`)
	combined := append(maskedFrame(1, message), maskedFrame(9, []byte("ping"))...)
	r := bytes.NewReader(combined)
	for _, expected := range []struct {
		opcode byte
		body   []byte
	}{{1, message}, {9, []byte("ping")}} {
		op, body, err := readClientWS(r)
		if err != nil || op != expected.opcode || !bytes.Equal(body, expected.body) {
			t.Fatalf("op=%d body=%q err=%v", op, body, err)
		}
	}
	large := []byte(strings.Repeat("a", 512))
	_, body, err := readClientWS(bytes.NewReader(maskedFrame(1, large)))
	if err != nil || !bytes.Equal(body, large) {
		t.Fatal("512 字节合法消息读取失败", err)
	}
	invalid := [][]byte{
		{0x81, 0}, {0x01, 0x80}, {0xc1, 0x80}, {0x82, 0x80},
		{0x81, 0xff}, {0x81, 0xfe, 2, 1}, {0x89, 0xfe, 0, 126},
		{0x81, 0xfe, 0, 1}, maskedFrame(1, []byte{0xff}),
	}
	for _, frame := range invalid {
		if _, _, err := readClientWS(bytes.NewReader(frame)); err == nil {
			t.Errorf("非法帧被接受: %x", frame)
		}
	}
}
