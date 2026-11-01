package main

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

type failingRelease struct {
	blocked        bool
	pressed        bool
	calls, handled int
}

// handle 仅记录新输入是否执行，验证清理中的门禁不会把消息投递给原生API。
func (m *failingRelease) handle(mouseMessage) error { m.handled++; return nil }

// release 模拟桌面拒绝释放，恢复前保留按下状态，恢复后只执行成功的一次释放。
func (m *failingRelease) release() error {
	m.calls++
	if m.blocked {
		return errors.New("desktop denied")
	}
	m.pressed = false
	return nil
}

// TestInputCleanupSurvivesDisconnect 验证原控制器被会话丢弃后仍保留责任，恢复桌面才补释放并解除门禁。
func TestInputCleanupSurvivesDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cleanup := &inputCleanup{}
		old := &failingRelease{blocked: true, pressed: true}
		// 断言失败也恢复测试桌面，防止故障模拟留下永不结束的清理协程。
		t.Cleanup(func() {
			cleanup.mu.Lock()
			old.blocked = false
			cleanup.mu.Unlock()
			time.Sleep(time.Second)
			synctest.Wait()
		})
		lease := &guardedInput{mouseController: old, cleanup: cleanup}
		if lease.release() == nil {
			t.Fatal("未报告释放失败")
		}
		lease = nil
		next := &failingRelease{}
		controller := &guardedInput{mouseController: next, cleanup: cleanup}
		if controller.handle(mouseMessage{Action: "down"}) == nil || next.handled != 0 {
			t.Fatal("清理中执行了新按下")
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		cleanup.mu.Lock()
		pressed, pending, callsBefore := old.pressed, cleanup.pending, old.calls
		old.blocked = false
		cleanup.mu.Unlock()
		if !pressed || pending != old || callsBefore != 4 {
			t.Fatal("释放责任丢失或重试失控")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if old.pressed || cleanup.pending != nil || controller.handle(mouseMessage{Action: "down"}) != nil {
			t.Fatal("恢复后未补释放或未解除门禁")
		}
		calls := old.calls
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if old.calls != calls {
			t.Fatal("成功后仍继续释放")
		}
	})
}

// TestInputCleanupKeyboard 保留失败的修饰键UP跨越会话结束，部分成功不能提前允许新控制。
func TestInputCleanupKeyboard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		denied := true
		cleanup := &inputCleanup{}
		// 故障断言失败时同样解除模拟拒绝，保证测试清理有限结束。
		t.Cleanup(func() {
			cleanup.mu.Lock()
			denied = false
			cleanup.mu.Unlock()
			time.Sleep(time.Second)
			synctest.Wait()
		})
		original := &observedKeys{state: keyboardState{
			// send 只拒绝Control的UP，模拟一个键已释放但修饰键仍待清理。
			send: func(code string, down bool, _ uint64) error {
				if !down && code == "ControlLeft" && denied {
					return errors.New("secure desktop")
				}
				return nil
			},
		}}
		for _, body := range []string{`{"type":"keyboard","action":"down","code":"ControlLeft"}`, `{"type":"keyboard","action":"down","code":"KeyA"}`} {
			if err := original.handle(keyMessage(t, body)); err != nil {
				t.Fatal(err)
			}
		}
		lease := &guardedInput{mouseController: original, cleanup: cleanup}
		if lease.release() == nil || len(original.state.held) != 1 || original.state.held[0] != "ControlLeft" {
			t.Fatal("部分释放后丢失修饰键")
		}
		lease = nil
		time.Sleep(2 * time.Second)
		synctest.Wait()
		cleanup.mu.Lock()
		pending := cleanup.pending
		denied = false
		cleanup.mu.Unlock()
		if pending != original {
			t.Fatal("丢失旧键盘所有权")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if len(original.state.held) != 0 || cleanup.pending != nil {
			t.Fatal("解锁后未补发修饰键UP")
		}
	})
}
