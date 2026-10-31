package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

// readFrames 从 Annex B 输入按 AUD 划分完整访问单元；emit 接收含起始码的单帧，返回错误即停止读取。
func readFrames(r io.Reader, emit func([]byte) error) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 65536), 16<<20)
	s.Split(splitNAL)
	var frame []byte
	for s.Scan() {
		nal := s.Bytes()
		if len(nal) == 0 {
			continue
		}
		if nal[0]&31 == 9 && len(frame) > 0 {
			if err := emit(frame); err != nil {
				return err
			}
			frame = nil
		}
		frame = append(frame, 0, 0, 0, 1)
		frame = append(frame, nal...)
	}
	if s.Err() != nil {
		return s.Err()
	}
	if len(frame) > 0 {
		return emit(frame)
	}
	return nil
}

// startCode 返回数据中首个三字节或四字节 Annex B 起始码的位置和长度，未找到时位置为 -1。
func startCode(b []byte) (int, int) {
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 {
			if b[i+2] == 1 {
				return i, 3
			}
			if i+3 < len(b) && b[i+2] == 0 && b[i+3] == 1 {
				return i, 4
			}
		}
	}
	return -1, 0
}

// splitNAL 作为 Scanner 的切分器去除起始码，保留跨读取边界的 NAL，EOF 时交付最后一个 NAL。
func splitNAL(b []byte, eof bool) (int, []byte, error) {
	i, n := startCode(b)
	if i < 0 {
		if eof && len(bytes.Trim(b, "\x00")) > 0 {
			return 0, nil, fmt.Errorf("缺少 Annex B 起始码")
		}
		return 0, nil, nil
	}
	j, _ := startCode(b[i+n:])
	if j >= 0 {
		return i + n + j, b[i+n : i+n+j], nil
	}
	if eof {
		return len(b), b[i+n:], nil
	}
	return 0, nil, nil
}

// frameInfo 检查完整访问单元，返回 IDR 标志和 SPS 派生的 WebCodecs codec 字符串；调用方缓存参数集。
func frameInfo(frame []byte, sps, pps *[]byte) (bool, string) {
	key, codec := false, ""
	s := bufio.NewScanner(bytes.NewReader(frame))
	s.Buffer(make([]byte, 65536), 16<<20)
	s.Split(splitNAL)
	for s.Scan() {
		n := s.Bytes()
		if len(n) == 0 {
			continue
		}
		switch n[0] & 31 {
		case 5:
			key = true
		case 7:
			*sps = append((*sps)[:0], n...)
		case 8:
			*pps = append((*pps)[:0], n...)
		}
	}
	if len(*sps) >= 4 {
		codec = fmt.Sprintf("avc1.%02x%02x%02x", (*sps)[1], (*sps)[2], (*sps)[3])
	}
	return key, codec
}
