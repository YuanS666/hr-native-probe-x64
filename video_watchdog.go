package main

import (
	"context"
	"fmt"
	"time"
)

const firstVideoTimeout = 15 * time.Second
const idleVideoTimeout = 8 * time.Second

type videoWatchdog struct {
	progress chan struct{}
	done     chan struct{}
	result   chan error
}

// newVideoWatchdog 监测完整编码帧而非像素变化；超时或取消时由abort终止进程并关闭读取管道。
func newVideoWatchdog(ctx context.Context, abort func()) *videoWatchdog {
	w := &videoWatchdog{progress: make(chan struct{}, 1), done: make(chan struct{}), result: make(chan error, 1)}
	// 唯一监测协程保留至多一个进度通知，正常结束立即回收计时器，不积累逐帧状态。
	go func() {
		timer := time.NewTimer(firstVideoTimeout)
		var failure error
		// 每条退出路径恰好归还一个结果，让调用方确定监测协程已经退出。
		defer func() { timer.Stop(); w.result <- failure }()
		received := false
		for {
			select {
			case <-w.done:
				return
			case <-ctx.Done():
				abort()
				return
			case <-w.progress:
				received = true
				timer.Reset(idleVideoTimeout)
			case <-timer.C:
				if received {
					failure = fmt.Errorf("连续%s没有完整编码帧", idleVideoTimeout)
				} else {
					failure = fmt.Errorf("启动%s仍未收到完整首帧", firstVideoTimeout)
				}
				abort()
				return
			}
		}
	}()
	return w
}

// frame 通知一个完整有效访问单元已到达，重复画面仍算进展；不以日志、配置或零散字节续期。
func (w *videoWatchdog) frame() {
	select {
	case w.progress <- struct{}{}:
	default:
	}
}

// finish 在读取结束后停止监测并取回结果；每个采集进程只调用一次，保留真实进程退出原因。
func (w *videoWatchdog) finish() error { close(w.done); return <-w.result }
