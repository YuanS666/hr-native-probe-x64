package main

import (
	"errors"
	"sync"
	"testing"
)

// TestNativeDisplayPower 验证仅请求显示器、同一工作协程撤销及并发释放幂等，不调用系统电源API。
func TestNativeDisplayPower(t *testing.T) {
	var calls []uint32
	release, err := keepCaptureDisplay(func(flags uint32) error { calls = append(calls, flags); return nil })
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); release() }()
	}
	wg.Wait()
	if len(calls) != 2 || calls[0] != 0x80000002 || calls[1] != 0x80000000 {
		t.Fatalf("calls=%x", calls)
	}
}

// TestNativeDisplayPowerFailure 验证申请失败时不返回可用释放句柄，撤销失败也能退出专用线程。
func TestNativeDisplayPowerFailure(t *testing.T) {
	failure := errors.New("denied")
	release, err := keepCaptureDisplay(func(uint32) error { return failure })
	if release != nil || !errors.Is(err, failure) {
		t.Fatalf("release=%v err=%v", release != nil, err)
	}
	calls := 0
	release, err = keepCaptureDisplay(func(uint32) error {
		calls++
		if calls == 2 {
			return failure
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}
