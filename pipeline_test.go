package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os/exec"
	"testing"
	"time"
)

// TestPipeline 用真实 FFmpeg 和进程内连接验证 25 帧消息、参数配置和严格递增的媒体时间戳，不占用端口。
func TestPipeline(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("需要 FFmpeg")
	}
	oldEncoder, oldPattern := *encoder, *testPattern
	*encoder = "libx264"
	*testPattern = true
	// 测试结束恢复命令参数，避免影响后续测试。
	defer func() { *encoder = oldEncoder; *testPattern = oldPattern }()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	done := make(chan error, 1)
	// 启动真实编码器，由测试读取端检查服务端发出的 WebSocket 帧。
	go func() { done <- encode(ctx, server) }()
	client.SetReadDeadline(time.Now().Add(7 * time.Second))
	var previous uint64
	count, configs := 0, 0
	for count < 25 {
		h := make([]byte, 2)
		if _, err := io.ReadFull(client, h); err != nil {
			t.Fatal(err)
		}
		n := uint64(h[1] & 127)
		if n == 127 {
			b := make([]byte, 8)
			if _, err := io.ReadFull(client, b); err != nil {
				t.Fatal(err)
			}
			n = binary.BigEndian.Uint64(b)
		} else if n == 126 {
			b := make([]byte, 2)
			if _, err := io.ReadFull(client, b); err != nil {
				t.Fatal(err)
			}
			n = uint64(binary.BigEndian.Uint16(b))
		}
		if n > 16<<20 {
			t.Fatal("消息过大")
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(client, b); err != nil {
			t.Fatal(err)
		}
		if h[0]&15 == 1 {
			configs++
			continue
		}
		if len(b) <= 17 {
			t.Fatal("视频包为空")
		}
		pts := binary.BigEndian.Uint64(b[1:9])
		if pts != uint64(count)*50000 {
			t.Fatalf("PTS=%d previous=%d", pts, previous)
		}
		if count == 0 && (b[0] != 1 || configs == 0) {
			t.Fatal("首帧缺少 IDR 或配置")
		}
		var sps, pps []byte
		key, codec := frameInfo(b[17:], &sps, &pps)
		if key && (codec == "" || len(pps) == 0) {
			t.Fatal("关键帧缺参数集")
		}
		previous = pts
		count++
	}
	cancel()
	client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("FFmpeg 未及时退出")
	}
	t.Logf("验证 %d 帧，%d 次关键帧配置", count, configs)
}
