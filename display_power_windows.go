//go:build windows && amd64

package main

import (
	"fmt"
	"log"
)

// captureDisplayRequest 仅在当前交互普通桌面为Windows采集请求亮屏，不更改电源计划、解锁或唤醒系统睡眠。
func captureDisplayRequest() (func(), error) {
	if err := windowsDesktopReady(); err != nil {
		return nil, &nativeCaptureError{cause: err, retry: true}
	}
	release, err := keepCaptureDisplay(func(flags uint32) error {
		result, _, callErr := winKernel.NewProc("SetThreadExecutionState").Call(uintptr(flags))
		if result == 0 {
			return fmt.Errorf("SetThreadExecutionState flags=0x%x: %v", flags, callErr)
		}
		if flags == displayContinuous {
			log.Print("Windows采集显示请求已释放：恢复系统原有熄屏策略")
		}
		return nil
	})
	if err != nil {
		return nil, &nativeCaptureError{cause: err}
	}
	log.Print("Windows采集显示请求已启用：请求亮屏并阻止空闲熄屏，采集结束自动释放；不解锁桌面")
	return release, nil
}
