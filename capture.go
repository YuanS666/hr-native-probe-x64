package main

import "runtime"

// captureDefaults 返回当前平台的默认采集设备和编码器，保留 macOS 原有默认值。
func captureDefaults() (string, string) {
	if runtime.GOOS == "windows" {
		return "desktop", "mf"
	}
	return "Capture screen 0", "h264_videotoolbox"
}
