//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework ApplicationServices
#include <ApplicationServices/ApplicationServices.h>

// postMouse 将 kind/button/clicks 表示的事件投递到全局 x/y 坐标，delta 为像素滚动；返回值只表示事件创建成功。
static int postMouse(int kind, int button, int clicks, double x, double y, int delta) {
    CGEventRef event;
    if (kind == 0) {
        event = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitPixel, 1, delta);
        if (event) CGEventSetLocation(event, CGPointMake(x, y));
    } else {
        event = CGEventCreateMouseEvent(NULL, kind, CGPointMake(x, y), button);
        if (event) CGEventSetIntegerValueField(event, kCGMouseEventClickState, clicks);
    }
    if (!event) return 0;
    // 系统没有返回事件执行确认，真实效果必须在目标窗口中验证。
    CGEventPost(kCGHIDEventTap, event);
    CFRelease(event);
    return 1;
}
// postKey 创建带明确修饰键标志的物理键盘事件，返回值仅代表事件创建及投递调用完成。
static int postKey(int key, int down, uint64_t flags) {
    CGEventRef event=CGEventCreateKeyboardEvent(NULL,(CGKeyCode)key,down!=0);
    if(!event) return 0;
    CGEventSetFlags(event,(CGEventFlags)flags);
    CGEventPost(kCGHIDEventTap,event);
    CFRelease(event);
    return 1;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type nativeMouse struct {
	display C.CGDirectDisplayID
	bounds  C.CGRect
	x, y    float64
	pressed [2]bool
	clicks  [2]int
	keys    keyboardState
}

// mousePermissions 只查询系统录屏和辅助功能授权状态，不弹出提示或申请权限。
func mousePermissions() string {
	return fmt.Sprintf("录屏授权=%v，辅助功能授权=%v", bool(C.CGPreflightScreenCaptureAccess()), C.AXIsProcessTrusted() != 0)
}

// newNativeMouse 将 Capture screen N 对应到 FFmpeg 使用的活动显示器顺序；不明确的设备名拒绝控制。
func newNativeMouse(device string) (*nativeMouse, error) {
	if C.AXIsProcessTrusted() == 0 {
		return nil, fmt.Errorf("未获得辅助功能权限；授权后重启程序")
	}
	if !strings.HasPrefix(device, "Capture screen ") {
		return nil, fmt.Errorf("鼠标控制需要 -device 'Capture screen N'，不接受有歧义的设备编号")
	}
	index, err := strconv.Atoi(strings.TrimPrefix(device, "Capture screen "))
	if err != nil || index < 0 || index >= 32 {
		return nil, fmt.Errorf("屏幕序号无效")
	}
	var displays [32]C.CGDirectDisplayID
	var count C.uint32_t
	if C.CGGetActiveDisplayList(32, &displays[0], &count) != C.kCGErrorSuccess || index >= int(count) {
		return nil, fmt.Errorf("无法确定采集屏幕对应的系统显示器")
	}
	bounds := C.CGDisplayBounds(displays[index])
	if bounds.size.width <= 0 || bounds.size.height <= 0 {
		return nil, fmt.Errorf("显示器坐标范围无效")
	}
	return &nativeMouse{display: displays[index], bounds: bounds}, nil
}

// post 将归一化位置映射到显示器逻辑坐标并投递事件；不使用编码分辨率或 Retina 物理像素。
func (m *nativeMouse) post(kind C.CGEventType, button, clicks int, x, y float64, delta int) error {
	b := m.bounds
	// 右下端点需落在屏幕内部，避免边缘点击进入相邻显示器。
	px := float64(b.origin.x) + math.Min(x*float64(b.size.width), float64(b.size.width)-1)
	py := float64(b.origin.y) + math.Min(y*float64(b.size.height), float64(b.size.height)-1)
	if C.postMouse(C.int(kind), C.int(button), C.int(clicks), C.double(px), C.double(py), C.int(delta)) == 0 {
		return fmt.Errorf("创建原生鼠标事件失败")
	}
	return nil
}

// handle 执行已校验的鼠标指令，记录按键状态用于拖动、双击和异常断开后的释放。
func (m *nativeMouse) handle(msg mouseMessage) error {
	if msg.Type == "keyboard" {
		m.keys.send = m.sendKey
		if msg.Action == "release" {
			return m.keys.release()
		}
	}
	if msg.Action == "release" {
		return m.releaseMouse()
	}
	if msg.Action == "heartbeat" {
		return nil
	}
	if C.AXIsProcessTrusted() == 0 {
		m.release()
		return fmt.Errorf("辅助功能权限不可用，鼠标控制已停止")
	}
	if C.CGDisplayIsActive(m.display) == 0 || C.CGDisplayBounds(m.display) != m.bounds {
		m.release()
		return fmt.Errorf("显示器布局已变化，请重新连接")
	}
	if msg.Type == "keyboard" {
		return m.keys.apply(msg)
	}
	m.x, m.y = *msg.X, *msg.Y
	button := 0
	if msg.Button != nil {
		button = *msg.Button
	}
	kind, clicks, delta := C.CGEventType(C.kCGEventMouseMoved), 0, 0
	switch msg.Action {
	case "move":
		if m.pressed[0] {
			kind = C.kCGEventLeftMouseDragged
		} else if m.pressed[1] {
			kind = C.kCGEventRightMouseDragged
			button = 1
		}
	case "down":
		if m.pressed[button] {
			return fmt.Errorf("重复按下鼠标按键")
		}
		m.clicks[button] = *msg.Clicks
		clicks = m.clicks[button]
		kind = C.kCGEventLeftMouseDown
		if button == 1 {
			kind = C.kCGEventRightMouseDown
		}
	case "up":
		if !m.pressed[button] {
			return nil
		}
		clicks = m.clicks[button]
		kind = C.kCGEventLeftMouseUp
		if button == 1 {
			kind = C.kCGEventRightMouseUp
		}
	case "wheel":
		kind = 0
		delta = -*msg.Delta
	}
	if err := m.post(kind, button, clicks, m.x, m.y, delta); err != nil {
		return err
	}
	if msg.Action == "down" {
		m.pressed[button] = true
	}
	if msg.Action == "up" {
		m.pressed[button] = false
	}
	return nil
}

// release 归还本会话鼠标和键盘，只有成功才清除状态；权限不可用时返回错误供后台补释放。
func (m *nativeMouse) release() error { return errors.Join(m.releaseMouse(), m.keys.release()) }

// releaseMouse 只归还鼠标按钮，关闭鼠标开关不会悄悄清空独立键盘状态；失败保留按钮记录。
func (m *nativeMouse) releaseMouse() error {
	var first error
	for button, pressed := range m.pressed {
		if !pressed {
			continue
		}
		if C.AXIsProcessTrusted() == 0 {
			return fmt.Errorf("辅助功能权限不可用，保留待释放按键")
		}
		kind := C.CGEventType(C.kCGEventLeftMouseUp)
		if button == 1 {
			kind = C.kCGEventRightMouseUp
		}
		if err := m.post(kind, button, m.clicks[button], m.x, m.y, 0); err != nil {
			if first == nil {
				first = err
			}
		} else {
			m.pressed[button] = false
		}
	}
	return first
}

// sendKey 按macOS物理键位投递Quartz事件；权限撤回时保留键盘状态给P0清理任务。
func (m *nativeMouse) sendKey(code string, down bool, flags uint64) error {
	if C.AXIsProcessTrusted() == 0 {
		return fmt.Errorf("辅助功能权限不可用，保留键盘待释放状态")
	}
	spec, ok := keyCodes[code]
	if !ok {
		return fmt.Errorf("不支持的键盘按键")
	}
	// Quartz用NumericPad标志区分数字区；NumLock物理位置在Mac对应Clear，不是Windows式锁定。
	if strings.HasPrefix(code, "Numpad") || code == "NumLock" {
		flags |= uint64(C.kCGEventFlagMaskNumericPad)
	}
	pressed := 0
	if down {
		pressed = 1
	}
	if C.postKey(C.int(spec.mac), C.int(pressed), C.uint64_t(flags)) == 0 {
		return fmt.Errorf("创建键盘事件失败")
	}
	return nil
}
