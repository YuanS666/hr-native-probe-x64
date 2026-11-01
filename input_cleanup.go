package main

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// inputCleanup 保留唯一未释放控制器，断开网络后仍承担清理责任；不积累旧会话列表。
type inputCleanup struct {
	mu      sync.Mutex
	pending mouseController
}

var nativeCleanup inputCleanup

type guardedInput struct {
	mouseController
	cleanup *inputCleanup
}

// openNativeInput 在旧按键清理成功前拒绝建立新控制器，防止新会话继承残留拖动。
func openNativeInput(device string) (mouseController, error) {
	nativeCleanup.mu.Lock()
	defer nativeCleanup.mu.Unlock()
	if nativeCleanup.pending != nil {
		return nil, fmt.Errorf("旧会话按键尚未释放；请恢复普通桌面/输入权限，清理成功后自动允许控制")
	}
	m, err := newNativeMouse(device)
	if err != nil {
		return nil, err
	}
	return &guardedInput{mouseController: m, cleanup: &nativeCleanup}, nil
}

// handle 串行执行会话输入，旧会话的补释放完成前不接受新的按下或移动。
func (g *guardedInput) handle(msg mouseMessage) error {
	g.cleanup.mu.Lock()
	defer g.cleanup.mu.Unlock()
	if g.cleanup.pending != nil {
		return fmt.Errorf("旧按键正在清理，暂不接受控制")
	}
	if msg.Type == "keyboard" && !*keyboardEnabled {
		return fmt.Errorf("服务端未启用键盘控制")
	}
	if msg.Type == "mouse" && !*mouseEnabled && msg.Action != "release" && msg.Action != "heartbeat" {
		return fmt.Errorf("服务端未启用鼠标控制")
	}
	return g.mouseController.handle(msg)
}

// release 立即尝试释放，失败时将原控制器移交唯一后台清理任务，不丢失其按下状态。
func (g *guardedInput) release() error {
	c := g.cleanup
	c.mu.Lock()
	defer c.mu.Unlock()
	err := g.mouseController.release()
	if err == nil {
		return nil
	}
	if c.pending == nil {
		c.pending = g.mouseController
		log.Printf("输入清理暂停：%v；保留原状态，普通桌面恢复后补释放", err)
		// 后台仅重试释放本会话已按下的按键，不执行新输入，也不依赖浏览器或WSS存在。
		go c.retry()
	}
	return err
}

// retry 每秒尝试归还待释放按键，成功后才撤销控制门禁；原生实现负责拒绝安全桌面。
func (c *inputCleanup) retry() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.Lock()
		if c.pending.release() == nil {
			c.pending = nil
			c.mu.Unlock()
			log.Print("旧会话释放事件已提交，清理记录已归还，允许重新建立控制器")
			return
		}
		c.mu.Unlock()
	}
}
