//go:build darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFFmpegWatchdogLifecycle 用临时同名测试进程验证Mac管理路径，不调用录屏设备、不修改系统FFmpeg。
func TestFFmpegWatchdogLifecycle(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	// 单引号转义可执行路径，不能让路径中的美元符号或反引号成为shell命令。
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(self, "'", "'\\''") + "' -test.run='^TestNativeProcessHelper$'\n"
	if err := os.WriteFile(filepath.Join(directory, "ffmpeg"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldEncoder, oldPattern, oldBinary := *encoder, *testPattern, *ffmpegBinary
	*encoder = "libx264"
	*testPattern = true
	// 每个测试结束恢复旧配置，不更改用户FFmpeg安装或普通采集参数。
	defer func() { *encoder = oldEncoder; *testPattern = oldPattern; *ffmpegBinary = oldBinary }()
	for _, mode := range []string{"silent", "closed"} {
		// 使用生产阈值，不替换时间常量；silent测试首帧保护，closed测试EOF后Wait保护。
		t.Run(mode, func(t *testing.T) {
			t.Setenv("SCREEN_NATIVE_TEST_HELPER", mode)
			// closed分支只用显式绝对路径，空PATH模拟launchd，确保不会悄悄依赖终端环境。
			if mode == "closed" {
				*ffmpegBinary = filepath.Join(directory, "ffmpeg")
				t.Setenv("PATH", "")
			}
			ctx, cancel := context.WithTimeout(context.Background(), firstVideoTimeout+10*time.Second)
			defer cancel()
			began := time.Now()
			// send 不引入网络等待，失败只能来自进程/监测器。
			err := encodeVideo(ctx, func(byte, []byte) error { return nil })
			if err == nil || ctx.Err() != nil {
				t.Fatalf("未及时回收模拟FFmpeg: err=%v parent=%v", err, ctx.Err())
			}
			if mode == "silent" && (!strings.Contains(err.Error(), "首帧") || time.Since(began) < firstVideoTimeout) {
				t.Fatalf("首帧超时原因错误: %v", err)
			}
			if mode == "closed" && (strings.Contains(err.Error(), "超时") || time.Since(began) > 8*time.Second) {
				t.Fatalf("EOF之后Wait未及时回收或原因错误: %v", err)
			}
		})
	}
}
