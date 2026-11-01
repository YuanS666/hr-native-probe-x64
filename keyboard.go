package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

type keySpec struct{ scan, mac uint16 }

// 按物理位置映射Windows Set-1扫描码及macOS SDK Events.h虚拟键码，不承诺不同布局产生相同字符。
var keyCodes = map[string]keySpec{
	"KeyA": {0x1e, 0x00}, "KeyB": {0x30, 0x0b}, "KeyC": {0x2e, 0x08}, "KeyD": {0x20, 0x02},
	"KeyE": {0x12, 0x0e}, "KeyF": {0x21, 0x03}, "KeyG": {0x22, 0x05}, "KeyH": {0x23, 0x04},
	"KeyI": {0x17, 0x22}, "KeyJ": {0x24, 0x26}, "KeyK": {0x25, 0x28}, "KeyL": {0x26, 0x25},
	"KeyM": {0x32, 0x2e}, "KeyN": {0x31, 0x2d}, "KeyO": {0x18, 0x1f}, "KeyP": {0x19, 0x23},
	"KeyQ": {0x10, 0x0c}, "KeyR": {0x13, 0x0f}, "KeyS": {0x1f, 0x01}, "KeyT": {0x14, 0x11},
	"KeyU": {0x16, 0x20}, "KeyV": {0x2f, 0x09}, "KeyW": {0x11, 0x0d}, "KeyX": {0x2d, 0x07},
	"KeyY": {0x15, 0x10}, "KeyZ": {0x2c, 0x06}, "Digit0": {0x0b, 0x1d}, "Digit1": {0x02, 0x12},
	"Digit2": {0x03, 0x13}, "Digit3": {0x04, 0x14}, "Digit4": {0x05, 0x15}, "Digit5": {0x06, 0x17},
	"Digit6": {0x07, 0x16}, "Digit7": {0x08, 0x1a}, "Digit8": {0x09, 0x1c}, "Digit9": {0x0a, 0x19},
	"Minus": {0x0c, 0x1b}, "Equal": {0x0d, 0x18}, "BracketLeft": {0x1a, 0x21}, "BracketRight": {0x1b, 0x1e},
	"Backslash": {0x2b, 0x2a}, "Semicolon": {0x27, 0x29}, "Quote": {0x28, 0x27}, "Backquote": {0x29, 0x32},
	"Comma": {0x33, 0x2b}, "Period": {0x34, 0x2f}, "Slash": {0x35, 0x2c}, "Space": {0x39, 0x31},
	"Enter": {0x1c, 0x24}, "Tab": {0x0f, 0x30}, "Backspace": {0x0e, 0x33}, "Escape": {0x01, 0x35},
	"Delete": {0xe053, 0x75}, "Home": {0xe047, 0x73}, "End": {0xe04f, 0x77}, "PageUp": {0xe049, 0x74},
	"PageDown": {0xe051, 0x79}, "ArrowLeft": {0xe04b, 0x7b}, "ArrowRight": {0xe04d, 0x7c}, "ArrowUp": {0xe048, 0x7e},
	"ArrowDown": {0xe050, 0x7d}, "ShiftLeft": {0x2a, 0x38}, "ShiftRight": {0x36, 0x3c}, "ControlLeft": {0x1d, 0x3b},
	"ControlRight": {0xe01d, 0x3e}, "AltLeft": {0x38, 0x3a}, "AltRight": {0xe038, 0x3d}, "MetaLeft": {0xe05b, 0x37},
	"MetaRight": {0xe05c, 0x36}, "F1": {0x3b, 0x7a}, "F2": {0x3c, 0x78}, "F3": {0x3d, 0x63},
	"F4": {0x3e, 0x76}, "F5": {0x3f, 0x60}, "F6": {0x40, 0x61}, "F7": {0x41, 0x62},
	"F8": {0x42, 0x64}, "F9": {0x43, 0x65}, "F10": {0x44, 0x6d}, "F11": {0x57, 0x67},
	"F12": {0x58, 0x6f},
	// 小键盘必须保留独立扫描码；Enter/除号带E0，数字与导航区同码但不带E0。
	"Numpad0": {0x52, 0x52}, "Numpad1": {0x4f, 0x53}, "Numpad2": {0x50, 0x54}, "Numpad3": {0x51, 0x55},
	"Numpad4": {0x4b, 0x56}, "Numpad5": {0x4c, 0x57}, "Numpad6": {0x4d, 0x58}, "Numpad7": {0x47, 0x59},
	"Numpad8": {0x48, 0x5b}, "Numpad9": {0x49, 0x5c}, "NumpadDecimal": {0x53, 0x41}, "NumpadEnter": {0xe01c, 0x4c},
	"NumpadAdd": {0x4e, 0x45}, "NumpadSubtract": {0x4a, 0x4e}, "NumpadMultiply": {0x37, 0x43}, "NumpadDivide": {0xe035, 0x4b},
	"NumpadEqual": {0x59, 0x51}, "NumLock": {0xe045, 0x47},
}

// parseInput 保留现有鼠标协议，额外接受严格白名单的keyboard类型；未知类型仍直接拒绝。
func parseInput(b []byte) (mouseMessage, error) {
	var header struct {
		Type string `json:"type"`
	}
	if len(b) > 512 || !utf8.Valid(b) || json.Unmarshal(b, &header) != nil {
		return mouseMessage{}, fmt.Errorf("输入消息格式或长度无效")
	}
	if header.Type != "keyboard" {
		return parseMouse(b)
	}
	var key struct {
		Type   string  `json:"type"`
		Action string  `json:"action"`
		Code   *string `json:"code"`
		Repeat *bool   `json:"repeat,omitempty"`
	}
	var msg mouseMessage
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&key); err != nil {
		return msg, err
	}
	msg = mouseMessage{Type: key.Type, Action: key.Action, Code: key.Code, Repeat: key.Repeat}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return msg, fmt.Errorf("键盘消息不能附带鼠标字段或尾部数据")
	}
	if msg.Action == "release" {
		if msg.Code != nil || msg.Repeat != nil {
			return msg, fmt.Errorf("键盘释放不能附带按键")
		}
		return msg, nil
	}
	if (msg.Action != "down" && msg.Action != "up") || msg.Code == nil {
		return msg, fmt.Errorf("键盘动作或按键缺失")
	}
	if _, ok := keyCodes[*msg.Code]; !ok {
		return msg, fmt.Errorf("不支持的键盘按键")
	}
	if msg.Action == "up" && msg.Repeat != nil {
		return msg, fmt.Errorf("键盘释放不能指定重复")
	}
	return msg, nil
}

type keyboardState struct {
	held []string
	send func(string, bool, uint64) error
}

// flags 根据仍按下的左右修饰键生成Quartz标志，释放当前键时提前扣除其标志而不丢失失败清理责任。
func (s *keyboardState) flags(releasing string) uint64 {
	var flags uint64
	for _, code := range s.held {
		if code == releasing {
			continue
		}
		switch {
		case strings.HasPrefix(code, "Shift"):
			flags |= 1 << 17
		case strings.HasPrefix(code, "Control"):
			flags |= 1 << 18
		case strings.HasPrefix(code, "Alt"):
			flags |= 1 << 19
		case strings.HasPrefix(code, "Meta"):
			flags |= 1 << 20
		}
	}
	return flags
}

// apply 按顺序投递物理按键，失败保留清理责任；只允许已按下键的重复，不生成任意文本。
func (s *keyboardState) apply(msg mouseMessage) error {
	if msg.Action == "release" {
		return s.release()
	}
	code := *msg.Code
	index := -1
	for i, key := range s.held {
		if key == code {
			index = i
			break
		}
	}
	if msg.Action == "down" {
		repeat := msg.Repeat != nil && *msg.Repeat
		if repeat && index < 0 {
			return fmt.Errorf("未按下的键不能重复")
		}
		// NumLock是切换键，按住时的重复DOWN不能反复改变目标机状态。
		if repeat && code == "NumLock" {
			return nil
		}
		if !repeat && index >= 0 {
			return fmt.Errorf("重复的键盘按下")
		}
		if index < 0 {
			s.held = append(s.held, code)
		}
		return s.send(code, true, s.flags(""))
	}
	if index < 0 {
		return nil
	}
	if err := s.send(code, false, s.flags(code)); err != nil {
		return err
	}
	s.held = append(s.held[:index], s.held[index+1:]...)
	return nil
}

// release 逆序补发本会话持有键的UP，成功才删除记录，失败交给进程级清理任务继续承担。
func (s *keyboardState) release() error {
	var first error
	for i := len(s.held) - 1; i >= 0; i-- {
		code := s.held[i]
		if err := s.send(code, false, s.flags(code)); err != nil {
			if first == nil {
				first = err
			}
		} else {
			s.held = append(s.held[:i], s.held[i+1:]...)
		}
	}
	return first
}

// Windows键盘联合体显式补齐到INPUT的40字节，避免把KEYBDINPUT的24字节误传给SendInput。
type windowsKeyboardInput struct {
	Kind, Padding          uint32
	VK, Scan               uint16
	Flags, Time, Alignment uint32
	ExtraInfo              uint64
	Tail                   uint64
}

// windowsKeyEvent 将白名单物理按键转换为Set-1扫描码和扩展/释放标志，不依赖本机字母虚拟键布局。
func windowsKeyEvent(code string, down bool) (windowsKeyboardInput, error) {
	spec, ok := keyCodes[code]
	if !ok {
		return windowsKeyboardInput{}, fmt.Errorf("不支持的键盘按键")
	}
	flags := uint32(0x0008)
	if spec.scan>>8 == 0xe0 {
		flags |= 1
	}
	if !down {
		flags |= 2
	}
	return windowsKeyboardInput{Kind: 1, Scan: spec.scan & 0xff, Flags: flags}, nil
}
