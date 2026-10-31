//go:build windows && amd64

package main

import (
	"fmt"
	"log"
)

// captureInput 获取主屏物理尺寸并固定 gdigrab 区域，避免 FFmpeg 自行推断 DPI 虚拟化后的全桌面范围。
func captureInput() ([]string, string, error) {
	if *device != "desktop" {
		return nil, "", fmt.Errorf("Windows 首版仅采集主显示器，使用 -device desktop")
	}
	if err := windowsDesktopReady(); err != nil {
		return nil, "", err
	}
	d, err := windowsDisplayInfo()
	if err != nil {
		return nil, "", err
	}
	l, err := fitWindowsVideo(d.Primary, d.Virtual, *width, *height)
	if err != nil {
		return nil, "", err
	}
	log.Printf("Windows gdigrab：primary=%+v virtual=%+v monitors=%d DPI=%d，视频内容区=%+v", d.Primary, d.Virtual, d.Monitors, d.DPI, l.Content)
	input, filter := windowsCaptureArgs(l, *fps)
	return input, filter, nil
}
