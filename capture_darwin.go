package main

import (
	"fmt"
	"strconv"
)

// captureInput 返回原有 macOS 采集参数及缩放滤镜，不改变 AVFoundation 或 VideoToolbox 行为。
func captureInput() ([]string, string, error) {
	return []string{"-f", "avfoundation", "-framerate", strconv.Itoa(*fps), "-i", *device + ":none"}, fmt.Sprintf("scale=%d:%d,format=yuv420p", *width, *height), nil
}
