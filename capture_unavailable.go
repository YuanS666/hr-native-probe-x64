//go:build linux || (windows && !amd64)

package main

import "fmt"

// captureInput 拒绝未支持平台上的真实屏幕采集，Linux Relay 仍不依赖采集设备。
func captureInput() ([]string, string, error) {
	return nil, "", fmt.Errorf("屏幕采集仅支持 macOS 和 Windows x64")
}
