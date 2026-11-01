package main

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

// FuzzReadWSFrame 用有界原始字节检查掩码、长度、控制帧及UTF8解析，不访问网络或分配超限负载。
func FuzzReadWSFrame(f *testing.F) {
	for _, b := range [][]byte{{0x82, 0}, {0x81, 2, 'o', 'k'}, {0x89, 0}, {0x82, 0x80, 1, 2, 3, 4}, {0x82, 127, 0x80, 0, 0, 0, 0, 0, 0, 0}, {0x88, 1, 0}, {0x81, 1, 0xff}} {
		f.Add(b, false, uint16(1024))
		f.Add(b, true, uint16(1024))
	}
	// 接受的帧必须满足生产解析器公开契约，错误输入允许返回错误但不得panic或突破长度上限。
	f.Fuzz(func(t *testing.T, raw []byte, masked bool, limit uint16) {
		if len(raw) > 8192 {
			return
		}
		bound := int(limit % 4097)
		op, b, err := readWSFrame(bytes.NewReader(raw), masked, bound)
		if err != nil {
			return
		}
		if len(b) > bound || (op >= 8 && len(b) > 125) || (op == 1 && !utf8.Valid(b)) {
			t.Fatalf("accepted invalid frame op=%d size=%d limit=%d", op, len(b), bound)
		}
		if op != 1 && op != 2 && op != 8 && op != 9 && op != 10 {
			t.Fatalf("opcode=%d", op)
		}
	})
}
