package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestNativeWatchdogPartialBytes 验证长度头或零散字节不能续期，真实ReadFull阻塞由超时回调解除。
func TestNativeWatchdogPartialBytes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer writer.Close()
		defer reader.Close()
		var aborted atomic.Int64
		// abort 模拟生产路径主动关闭读取管道，必须唤醒正在等待完整帧的读取。
		watch := newVideoWatchdog(context.Background(), func() { aborted.Add(1); reader.Close() })
		done := make(chan error, 1)
		// 运行生产完整AU解析器，不把任意Read字节视为编码进展。
		go func() { done <- forwardNativeVideo(reader, func(byte, []byte) error { watch.frame(); return nil }) }()
		if _, err := writer.Write([]byte{0, 0, 0, 30, 1}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(firstVideoTimeout - time.Second)
		synctest.Wait()
		if aborted.Load() != 0 {
			t.Fatal("首帧超时提前触发")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if aborted.Load() != 1 || (<-done) == nil {
			t.Fatal("超时未解除管道读取")
		}
		if err := watch.finish(); err == nil || !strings.Contains(err.Error(), "首帧") {
			t.Fatalf("超时原因=%v", err)
		}
	})
}

// TestNativeWatchdogProgress 验证静止画面产生的完整帧仍续期，停止输出八秒才报错，三秒发送等待不误判。
func TestNativeWatchdogProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		// abort 只记录监测器是否触发，不需要真实屏幕。
		watch := newVideoWatchdog(context.Background(), func() { calls.Add(1) })
		for i := 0; i < 30; i++ {
			watch.frame()
			synctest.Wait()
			time.Sleep(time.Second)
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("正常静止帧或三秒反压被误判")
		}
		watch.frame()
		synctest.Wait()
		time.Sleep(idleVideoTimeout - time.Second)
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("输出超时提前触发")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if calls.Load() != 1 || watch.finish() == nil {
			t.Fatal("连续无帧未触发")
		}
	})
}

// TestNativeWatchdogStop 验证正常结束或用户取消不会被报告为编码超时，监测协程和定时器均结束。
func TestNativeWatchdogStop(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		// 每个场景使用独立虚拟时钟，执行生产超时值而不缩短安全阈值。
		t.Run(map[bool]string{false: "finish", true: "cancel"}[cancelled], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var calls atomic.Int64
				// abort 记录取消是否主动解除了读取，仅取消场景允许调用。
				watch := newVideoWatchdog(ctx, func() { calls.Add(1) })
				if cancelled {
					cancel()
					synctest.Wait()
				}
				if err := watch.finish(); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Minute)
				synctest.Wait()
				expected := int64(0)
				if cancelled {
					expected = 1
				}
				if calls.Load() != expected {
					t.Fatalf("正常结束后仍触发: %d", calls.Load())
				}
			})
		})
	}
}

// TestNativeProcessWatchdog 使用真实子进程及生产15/8秒阈值验证无输出和半途挂起会回收，不伪造Windows GUI故障。
func TestNativeProcessWatchdog(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		mode, reason string
		minimum      time.Duration
	}{{"silent", "首帧", firstVideoTimeout}, {"hold", "连续", idleVideoTimeout}} {
		// 不输出及仅输出一次的测试子进程，不访问任何系统桌面。
		t.Run(test.mode, func(t *testing.T) {
			t.Setenv("SCREEN_NATIVE_TEST_HELPER", test.mode)
			ctx, cancel := context.WithTimeout(context.Background(), firstVideoTimeout+10*time.Second)
			defer cancel()
			began := time.Now()
			// send 模拟正常接收，防止把网络超时当作编码故障。
			err := runNativeVideo(ctx, self, []string{"-test.run=^TestNativeProcessHelper$"}, func(byte, []byte) error { return nil })
			var failure *nativeCaptureError
			if !errors.As(err, &failure) || !failure.retry || !strings.Contains(err.Error(), test.reason) || ctx.Err() != nil {
				t.Fatalf("进程超时分类: %v parent=%v", err, ctx.Err())
			}
			if time.Since(began) < test.minimum {
				t.Fatal("未执行生产阈值")
			}
		})
	}
}
