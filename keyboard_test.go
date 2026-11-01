package main

import (
	"errors"
	"reflect"
	"testing"
	"unsafe"
)

// keyMessage 为纯逻辑测试建立真实解析后的键盘消息，不直接构造绕过校验的按键。
func keyMessage(t *testing.T, body string) mouseMessage {
	t.Helper()
	msg, err := parseInput([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// TestKeyboardValidation 验证键盘字段、动作、白名单和鼠标协议隔离，不放宽旧鼠标校验。
func TestKeyboardValidation(t *testing.T) {
	for _, b := range []string{`{"type":"keyboard","action":"down","code":"KeyA","repeat":false}`, `{"type":"keyboard","action":"up","code":"ControlRight"}`, `{"type":"keyboard","action":"release"}`} {
		keyMessage(t, b)
		if _, err := parseMouse([]byte(b)); err == nil {
			t.Fatal("旧鼠标解析接受键盘")
		}
	}
	for _, b := range []string{`{"type":"keyboard","action":"text","code":"KeyA"}`, `{"type":"keyboard","action":"down","code":"Unidentified"}`, `{"type":"keyboard","action":"down","code":"CapsLock"}`, `{"type":"keyboard","action":"up","code":"KeyA","repeat":false}`, `{"type":"keyboard","action":"release","code":"KeyA"}`, `{"type":"keyboard","action":"down","code":"KeyA","x":0.2}`,
		`{"type":"keyboard","action":"down","code":"KeyA","x":null}`, `{"type":"keyboard","action":"down","code":"KeyA","text":"evil"}`, `{"type":"mouse","action":"heartbeat","code":"KeyA"}`,
		`{"type":"mouse","action":"heartbeat","code":null}`,
		`{"type":"mouse","action":"heartbeat","repeat":null}`,
		`{"type":"mouse","action":"heartbeat","Code":null}`, `{"type":"keyboard","action":"down","code":null}`, `{"type":"keyboard","action":"down","code":"KeyA","repeat":"true"}`} {
		if _, err := parseInput([]byte(b)); err == nil {
			t.Fatalf("接受非法消息: %s", b)
		}
	}
}

// TestKeyboardOrderAndCleanup 验证Ctrl+A和重复顺序、修饰键标志、失败UP保留状态且成功后停止清理。
func TestKeyboardOrderAndCleanup(t *testing.T) {
	var events []string
	var flags []uint64
	deny := false
	state := keyboardState{
		// send 记录跨平台逻辑要求，不调用系统API；被拒绝的释放不得移除按下记录。
		send: func(code string, down bool, mask uint64) error {
			if !down && deny {
				return errors.New("desktop denied")
			}
			kind := " up"
			if down {
				kind = " down"
			}
			events = append(events, code+kind)
			flags = append(flags, mask)
			return nil
		},
	}
	for _, b := range []string{`{"type":"keyboard","action":"down","code":"ControlLeft"}`, `{"type":"keyboard","action":"down","code":"KeyA"}`, `{"type":"keyboard","action":"down","code":"KeyA","repeat":true}`} {
		if err := state.apply(keyMessage(t, b)); err != nil {
			t.Fatal(err)
		}
	}
	if len(state.held) != 2 || flags[1] != (1<<18) {
		t.Fatal("重复导致状态增长或Ctrl标志缺失")
	}
	deny = true
	if state.release() == nil || len(state.held) != 2 {
		t.Fatal("失败释放丢失责任")
	}
	deny = false
	if state.release() != nil || len(state.held) != 0 {
		t.Fatal("补释放失败")
	}
	want := []string{"ControlLeft down", "KeyA down", "KeyA down", "KeyA up", "ControlLeft up"}
	if !reflect.DeepEqual(events, want) || flags[len(flags)-1] != 0 {
		t.Fatalf("事件=%v flags=%v", events, flags)
	}
	if state.apply(keyMessage(t, `{"type":"keyboard","action":"down","code":"KeyA","repeat":true}`)) == nil {
		t.Fatal("允许没有DOWN的重复")
	}
}

// TestKeyboardInputABI 检查Windows键盘INPUT布局、扩展扫描码和UP标志，防止跨平台编译掩盖ABI错误。
func TestKeyboardInputABI(t *testing.T) {
	var v windowsKeyboardInput
	if unsafe.Sizeof(v) != 40 || unsafe.Offsetof(v.Scan) != 10 || unsafe.Offsetof(v.Flags) != 12 || unsafe.Offsetof(v.ExtraInfo) != 24 {
		t.Fatalf("键盘INPUT布局错误: %d", unsafe.Sizeof(v))
	}
	for _, test := range []struct {
		code  string
		scan  uint16
		flags uint32
	}{{"KeyA", 0x1e, 8}, {"ControlRight", 0x1d, 9}, {"ArrowLeft", 0x4b, 9}, {"MetaLeft", 0x5b, 9}} {
		event, err := windowsKeyEvent(test.code, true)
		if err != nil || event.Kind != 1 || event.VK != 0 || event.Scan != test.scan || event.Flags != test.flags {
			t.Fatalf("键盘INPUT=%+v err=%v", event, err)
		}
		up, _ := windowsKeyEvent(test.code, false)
		if up.Flags != test.flags|2 {
			t.Fatal("缺少KEYUP")
		}
	}
}

// TestKeyboardServerOptIn 验证已认证浏览器也不能绕过服务端键盘开关，不调用任何平台API。
func TestKeyboardServerOptIn(t *testing.T) {
	old := *keyboardEnabled
	*keyboardEnabled = false
	// 测试结束恢复全局启动选项。
	defer func() { *keyboardEnabled = old }()
	backend := &failingRelease{}
	controller := &guardedInput{mouseController: backend, cleanup: &inputCleanup{}}
	if controller.handle(keyMessage(t, `{"type":"keyboard","action":"down","code":"KeyA"}`)) == nil || backend.handled != 0 {
		t.Fatal("服务端关闭键盘仍执行了输入")
	}
	*keyboardEnabled = true
	if controller.handle(keyMessage(t, `{"type":"keyboard","action":"down","code":"KeyA"}`)) != nil || backend.handled != 1 {
		t.Fatal("明确启用后输入未执行")
	}
}

// TestKeyboardNumpad 验证数字区扫描码及E0位，保证它不会错误变成独立导航区或普通Enter。
func TestKeyboardNumpad(t *testing.T) {
	cases := []struct {
		code     string
		scan     uint16
		extended bool
	}{
		{"Numpad0", 0x52, false}, {"Numpad1", 0x4f, false}, {"Numpad2", 0x50, false}, {"Numpad3", 0x51, false},
		{"Numpad4", 0x4b, false}, {"Numpad5", 0x4c, false}, {"Numpad6", 0x4d, false}, {"Numpad7", 0x47, false},
		{"Numpad8", 0x48, false}, {"Numpad9", 0x49, false}, {"NumpadDecimal", 0x53, false}, {"NumpadEnter", 0x1c, true},
		{"NumpadAdd", 0x4e, false}, {"NumpadSubtract", 0x4a, false}, {"NumpadMultiply", 0x37, false}, {"NumpadDivide", 0x35, true},
		{"NumpadEqual", 0x59, false}, {"NumLock", 0x45, true},
	}
	for _, test := range cases {
		keyMessage(t, `{"type":"keyboard","action":"down","code":"`+test.code+`"}`)
		down, err := windowsKeyEvent(test.code, true)
		want := uint32(8)
		if test.extended {
			want |= 1
		}
		if err != nil || down.Scan != test.scan || down.Flags != want || down.VK != 0 {
			t.Fatalf("%s INPUT=%+v err=%v", test.code, down, err)
		}
		up, _ := windowsKeyEvent(test.code, false)
		if up.Flags != want|2 {
			t.Fatalf("%s UP不正确", test.code)
		}
	}
	keypad, _ := windowsKeyEvent("Numpad1", true)
	end, _ := windowsKeyEvent("End", true)
	if keypad.Scan != end.Scan || keypad.Flags == end.Flags {
		t.Fatal("小键盘1与独立End没有区分")
	}
}

// TestKeyboardNumLockOnce 验证长按NumLock只提交一次DOWN，释放或清理也不额外切换锁定状态。
func TestKeyboardNumLockOnce(t *testing.T) {
	var operations []bool
	state := keyboardState{
		// send 只记录原生调用意图，不读取或改变本机NumLock状态。
		send: func(code string, down bool, _ uint64) error { operations = append(operations, down); return nil },
	}
	if err := state.apply(keyMessage(t, `{"type":"keyboard","action":"down","code":"NumLock"}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := state.apply(keyMessage(t, `{"type":"keyboard","action":"down","code":"NumLock","repeat":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	if len(operations) != 1 || len(state.held) != 1 {
		t.Fatal("NumLock重复切换或状态增长")
	}
	if err := state.release(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(operations, []bool{true, false}) {
		t.Fatalf("锁定键事件=%v", operations)
	}
}
