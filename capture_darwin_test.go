package main

import (
	"reflect"
	"testing"
)

// TestMacCaptureContract 检查系统拆分后仍生成原有 Mac 默认采集参数与滤镜，不替代真实录屏回归。
func TestMacCaptureContract(t *testing.T) {
	args, filter, err := captureInput()
	if err != nil || !reflect.DeepEqual(args, []string{"-f", "avfoundation", "-framerate", "20", "-i", "Capture screen 0:none"}) || filter != "scale=1920:1080,format=yuv420p" {
		t.Fatalf("Mac 采集契约变化: %v %q %v", args, filter, err)
	}
}
