package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// nativeTestPacket 构造仅用于协议测试的AUD/参数集/切片和既有视频头，不把合成包当作可解码视频证据。
func nativeTestPacket(pts uint64, key bool) []byte {
	b := make([]byte, 17)
	binary.BigEndian.PutUint64(b[1:9], pts)
	b = append(b, 0, 0, 0, 1, 9, 0xf0)
	if key {
		b[0] = 1
		b = append(b, 0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x28, 0, 0, 0, 1, 0x68, 0xce, 0x80, 0, 0, 0, 1, 0x65, 0xb8, 0x80)
	} else {
		b = append(b, 0, 0, 0, 1, 0x41, 0xe0)
	}
	return b
}

// nativeTestRecord 添加原生管道长度，便于测试短读、损坏包及跨语言协议约定。
func nativeTestRecord(packet []byte) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(len(packet)))
	return append(b, packet...)
}

// TestNativeProtocol 验证逐字节读取仍按完整AU转发，保持媒体PTS间隔和原17字节头，并在IDR前发配置。
func TestNativeProtocol(t *testing.T) {
	var input []byte
	for _, pts := range []uint64{0, 50000, 150000, 1000000} {
		input = append(input, nativeTestRecord(nativeTestPacket(pts, pts == 0 || pts == 1000000))...)
	}
	var ops []byte
	var packets [][]byte
	// send 记录实际封包结果，不建立网络连接或调用系统编码器。
	send := func(op byte, b []byte) error {
		ops = append(ops, op)
		packets = append(packets, bytes.Clone(b))
		return nil
	}
	err := forwardNativeVideo(iotest.OneByteReader(bytes.NewReader(input)), send)
	if err != io.EOF || !bytes.Equal(ops, []byte{1, 2, 2, 2, 1, 2}) {
		t.Fatalf("逐字节转发: ops=%v err=%v", ops, err)
	}
	var config map[string]any
	if json.Unmarshal(packets[0], &config) != nil || config["codec"] != "avc1.42c028" {
		t.Fatalf("配置不兼容: %s", packets[0])
	}
	for i, pts := range []uint64{0, 50000, 150000} {
		p := packets[i+1]
		want := nativeTestPacket(pts, i == 0)
		if !bytes.Equal(p[:9], want[:9]) || !bytes.Equal(p[17:], want[17:]) || binary.BigEndian.Uint64(p[9:17]) == 0 {
			t.Fatalf("原视频格式/PTS未保留，frame=%d", i)
		}
	}
	if !bytes.Equal(packets[0], packets[4]) || binary.BigEndian.Uint64(packets[5][1:9]) != 1000000 {
		t.Fatal("后续IDR没有重新发送配置或时间戳丢失")
	}
}

// TestNativeInvalidPackets 验证消息大小、完整性、IDR、AUD、参数集和时间戳异常都不能进入发送回调。
func TestNativeInvalidPackets(t *testing.T) {
	badFlag := nativeTestPacket(0, true)
	badFlag[0] = 2
	badReady := nativeTestPacket(0, true)
	badReady[9] = 1
	badPTS := nativeTestPacket(1<<53, true)
	badAUD := nativeTestPacket(0, true)
	badAUD[21] = 0x06
	missingPPS := nativeTestPacket(0, true)
	missingPPS = append(missingPPS[:31], missingPPS[38:]...)
	twoAU := nativeTestPacket(0, true)
	twoAU = append(twoAU, nativeTestPacket(50000, false)[17:]...)
	cases := [][]byte{
		{0xff, 0xff, 0xff, 0xff}, {0, 0, 0, 17}, {0, 0, 0}, {0, 0, 0, 30, 1},
		nativeTestRecord(badFlag), nativeTestRecord(badReady), nativeTestRecord(badPTS),
		nativeTestRecord(badAUD), nativeTestRecord(missingPPS), nativeTestRecord(twoAU),
		nativeTestRecord(nativeTestPacket(0, false)),
	}
	for i, data := range cases {
		called := false
		// send 发现非法包被放行就使测试失败，不弱化输入检查。
		send := func(byte, []byte) error { called = true; return nil }
		err := forwardNativeVideo(bytes.NewReader(data), send)
		if err == nil || err == io.EOF || called {
			t.Fatalf("非法包 %d 未拒绝: called=%v err=%v", i, called, err)
		}
	}
	data := append(nativeTestRecord(nativeTestPacket(50000, true)), nativeTestRecord(nativeTestPacket(50000, false))...)
	sent := 0
	// send 只统计已通过检查的配置及首帧，重复PTS不能增加计数。
	err := forwardNativeVideo(bytes.NewReader(data), func(byte, []byte) error { sent++; return nil })
	if err == nil || err == io.EOF || sent != 2 {
		t.Fatalf("重复PTS未拒绝: sent=%d err=%v", sent, err)
	}
}

// TestNativeProcessHelper 作为测试子进程输出协议包或停住，用真实进程验证回收；普通测试调用时不执行任何模拟编码。
func TestNativeProcessHelper(t *testing.T) {
	mode := os.Getenv("SCREEN_NATIVE_TEST_HELPER")
	if mode == "" {
		return
	}
	if mode == "bad" {
		os.Stdout.Write([]byte{255, 255, 255, 255})
	} else {
		os.Stdout.Write(nativeTestRecord(nativeTestPacket(0, true)))
	}
	if mode == "exit" {
		os.Exit(9)
	}
	for {
		time.Sleep(time.Hour)
	}
}

// TestNativeProcessCancellation 在已收到首帧后取消真实子进程，验证停止不会遗留持续编码进程。
func TestNativeProcessCancellation(t *testing.T) {
	t.Setenv("SCREEN_NATIVE_TEST_HELPER", "hold")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	// send 只在完整视频包到达时通知父测试，以免在子进程启动前提前取消。
	send := func(op byte, b []byte) error {
		if op == 2 {
			ready <- struct{}{}
		}
		return nil
	}
	// run 启动生产进程管理路径，测试子进程没有真实屏幕依赖。
	go func() { done <- runNativeVideo(ctx, self, []string{"-test.run=^TestNativeProcessHelper$"}, send) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("子进程未交付首帧")
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("取消后编码子进程没有回收")
	}
}

// TestNativeProcessFailures 验证编码进程退出、畸形输出和发送错误都会结束唯一子进程，不循环重启。
func TestNativeProcessFailures(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exit", "bad", "hold"} {
		t.Setenv("SCREEN_NATIVE_TEST_HELPER", mode)
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		failure := errors.New("slow/closed viewer")
		// send 在hold场景模拟真实WebSocket写失败，其余场景观察子进程自身失败。
		send := func(byte, []byte) error {
			if mode == "hold" {
				return failure
			}
			return nil
		}
		err := runNativeVideo(ctx, self, []string{"-test.run=^TestNativeProcessHelper$"}, send)
		stop()
		if err == nil || errors.Is(err, context.DeadlineExceeded) || (mode == "hold" && !errors.Is(err, failure)) || (mode == "exit" && !strings.Contains(err.Error(), "意外结束")) {
			t.Fatalf("进程失败处理 mode=%s err=%v", mode, err)
		}
	}
}
