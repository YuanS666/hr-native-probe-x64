package main

import (
	"bytes"
	"testing"
)

// TestFrames 验证混合起始码、参数集、多切片及 EOF 尾帧，确保每次交付的是一帧而非一个 NAL。
func TestFrames(t *testing.T) {
	input := []byte{0, 0, 1, 9, 240, 0, 0, 0, 1, 103, 66, 0, 40, 0, 0, 1, 104, 1, 0, 0, 1, 101, 1, 0, 0, 1, 101, 2, 0, 0, 1, 9, 240, 0, 0, 1, 65, 3}
	var frames [][]byte
	// 保存回调中的帧副本，便于检查尾帧与 IDR 标志。
	err := readFrames(bytes.NewReader(input), func(b []byte) error {
		frames = append(frames, bytes.Clone(b))
		return nil
	})
	if err != nil || len(frames) != 2 {
		t.Fatalf("frames=%d err=%v", len(frames), err)
	}
	var sps, pps []byte
	key, codec := frameInfo(frames[0], &sps, &pps)
	if !key || codec != "avc1.420028" || len(pps) != 2 {
		t.Fatalf("key=%v codec=%s pps=%x", key, codec, pps)
	}
	key, _ = frameInfo(frames[1], &sps, &pps)
	if key {
		t.Fatal("P 帧被标记为关键帧")
	}
}
