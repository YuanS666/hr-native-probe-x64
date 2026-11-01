package main

import (
	"fmt"
	"log"
	"runtime"
	"sync"
)

const displayContinuous uint32 = 0x80000000
const displayRequired uint32 = 0x00000002

// keepCaptureDisplay 在专用系统线程申请临时亮屏，返回可重复调用的同步释放函数；request负责原生调用及错误报告。
func keepCaptureDisplay(request func(uint32) error) (func(), error) {
	ready := make(chan error, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	// 执行状态属于线程，申请和撤销必须位于同一系统线程，不能随Go调度迁移。
	go func() {
		runtime.LockOSThread()
		defer close(done)
		// 不解锁线程：goroutine退出时由Go销毁专用线程，撤销失败也不会把状态带给其他任务。
		if err := request(displayContinuous | displayRequired); err != nil {
			ready <- err
			return
		}
		ready <- nil
		<-stop
		if err := request(displayContinuous); err != nil {
			log.Printf("撤销显示电源请求失败，专用线程将退出: %v", err)
		}
	}()
	if err := <-ready; err != nil {
		<-done
		return nil, fmt.Errorf("申请采集亮屏失败: %w", err)
	}
	var once sync.Once
	// 同步等待线程释放，保证采集退出后不遗留本次显示请求。
	release := func() { once.Do(func() { close(stop) }); <-done }
	return release, nil
}
