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
*/
import "C"

import (
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
	if msg.Action == "release" {
		m.release()
		return nil
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

// release 为会话中仍按下的左右键发送释放事件；即使权限被撤回也尽力清理，避免残留拖动状态。
func (m *nativeMouse) release() {
	for button, pressed := range m.pressed {
		if !pressed {
			continue
		}
		kind := C.CGEventType(C.kCGEventLeftMouseUp)
		if button == 1 {
			kind = C.kCGEventRightMouseUp
		}
		m.post(kind, button, m.clicks[button], m.x, m.y, 0)
		m.pressed[button] = false
	}
}
