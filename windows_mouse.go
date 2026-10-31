package main

import (
	"fmt"
	"math"
	"strconv"
)

const (
	winMove        = 0x0001
	winLeftDown    = 0x0002
	winLeftUp      = 0x0004
	winRightDown   = 0x0008
	winRightUp     = 0x0010
	winWheel       = 0x0800
	winVirtualDesk = 0x4000
	winAbsolute    = 0x8000
)

// 显式保留 x64 对齐空位；MOUSEINPUT 为 32 字节，INPUT 的联合体从第 8 字节开始。
type windowsMouseInput struct {
	DX, DY            int32
	Data, Flags, Time uint32
	Padding           uint32
	ExtraInfo         uint64
}
type windowsInput struct {
	Kind, Padding uint32
	Mouse         windowsMouseInput
}
type desktopRect struct{ Left, Top, Width, Height int }
type windowsVideoLayout struct {
	Primary, Virtual, Content desktopRect
	Width, Height             int
}

// fitWindowsVideo 用物理主屏尺寸计算偶数缩放与黑边区域，保证编码端和输入映射共用相同几何定义。
func fitWindowsVideo(primary, virtual desktopRect, width, height int) (windowsVideoLayout, error) {
	l := windowsVideoLayout{Primary: primary, Virtual: virtual, Width: width, Height: height}
	if primary.Width < 2 || primary.Height < 2 || virtual.Width < 2 || virtual.Height < 2 || width < 2 || height < 2 || width%2 != 0 || height%2 != 0 {
		return l, fmt.Errorf("桌面或视频尺寸无效")
	}
	if primary.Left < virtual.Left || primary.Top < virtual.Top || primary.Left+primary.Width > virtual.Left+virtual.Width || primary.Top+primary.Height > virtual.Top+virtual.Height {
		return l, fmt.Errorf("主显示器不在虚拟桌面内")
	}
	w, h := width, height
	if int64(primary.Width)*int64(height) > int64(primary.Height)*int64(width) {
		h = int(int64(primary.Height) * int64(width) / int64(primary.Width))
	} else {
		w = int(int64(primary.Width) * int64(height) / int64(primary.Height))
	}
	w = max(2, w&^1)
	h = max(2, h&^1)
	// yuv420p 的色度采样要求偶数坐标，显式对齐，避免 FFmpeg 自动移动一像素。
	l.Content = desktopRect{Left: ((width - w) / 2) &^ 1, Top: ((height - h) / 2) &^ 1, Width: w, Height: h}
	return l, nil
}

// windowsCaptureArgs 返回主屏物理区域的 gdigrab 参数和等比例黑边滤镜，不依赖浏览器增加第二套解码逻辑。
func windowsCaptureArgs(l windowsVideoLayout, fps int) ([]string, string) {
	p, c := l.Primary, l.Content
	args := []string{"-f", "gdigrab", "-framerate", strconv.Itoa(fps), "-draw_mouse", "1", "-offset_x", strconv.Itoa(p.Left), "-offset_y", strconv.Itoa(p.Top), "-video_size", fmt.Sprintf("%dx%d", p.Width, p.Height), "-i", "desktop"}
	filter := fmt.Sprintf("scale=%d:%d,format=yuv420p,pad=%d:%d:%d:%d,setsar=1", c.Width, c.Height, l.Width, l.Height, c.Left, c.Top)
	return args, filter
}

// point 将视频归一化坐标先扣除编码黑边，再映射到 SendInput 的虚拟桌面绝对坐标；clamp 只用于拖出画面的移动。
func (l windowsVideoLayout) point(x, y float64, clamp bool) (windowsInput, bool) {
	if !(x >= 0 && x <= 1 && y >= 0 && y <= 1) {
		return windowsInput{}, false
	}
	c := l.Content
	u := (x*float64(l.Width) - float64(c.Left)) / float64(c.Width)
	v := (y*float64(l.Height) - float64(c.Top)) / float64(c.Height)
	if !clamp && (u < 0 || u > 1 || v < 0 || v > 1) {
		return windowsInput{}, false
	}
	u = math.Max(0, math.Min(1, u))
	v = math.Max(0, math.Min(1, v))
	px := l.Primary.Left + min(int(u*float64(l.Primary.Width)), l.Primary.Width-1)
	py := l.Primary.Top + min(int(v*float64(l.Primary.Height)), l.Primary.Height-1)
	ax := int32(math.Round(float64(px-l.Virtual.Left) * 65535 / float64(l.Virtual.Width-1)))
	ay := int32(math.Round(float64(py-l.Virtual.Top) * 65535 / float64(l.Virtual.Height-1)))
	return windowsInput{Mouse: windowsMouseInput{DX: ax, DY: ay, Flags: winMove | winAbsolute | winVirtualDesk}}, true
}

type windowsMouseState struct {
	pressed        [2]bool
	wheelRemainder int
	send           func([]windowsInput) (uint32, error)
}

// write 检查实际插入数量，不能把 SendInput 的部分成功或零返回值当成操作成功。
func (s *windowsMouseState) write(events []windowsInput) error {
	n, err := s.send(events)
	if err != nil {
		return err
	}
	if int(n) != len(events) {
		return fmt.Errorf("SendInput 仅插入 %d/%d 条事件", n, len(events))
	}
	return nil
}

// apply 按原协议执行鼠标状态变化，nil 位置表示黑边；释放始终可执行，双击保留原有两次 down/up 顺序。
func (s *windowsMouseState) apply(msg mouseMessage, position *windowsInput) error {
	if msg.Action == "heartbeat" {
		return nil
	}
	if msg.Action == "release" {
		return s.release()
	}
	button := 0
	if msg.Button != nil {
		button = *msg.Button
	}
	if button < 0 || button > 1 {
		return fmt.Errorf("鼠标键无效")
	}
	var events []windowsInput
	switch msg.Action {
	case "move":
		if position == nil {
			return nil
		}
		return s.write([]windowsInput{*position})
	case "down":
		if position == nil {
			return nil
		}
		if s.pressed[button] {
			return fmt.Errorf("重复按下鼠标按键")
		}
		flags := uint32(winLeftDown)
		if button == 1 {
			flags = winRightDown
		}
		events = []windowsInput{*position, {Mouse: windowsMouseInput{Flags: flags}}}
		// 先记录清理责任；部分插入或失败后也必须尝试发送对应 UP。
		s.pressed[button] = true
	case "up":
		if !s.pressed[button] {
			return nil
		}
		if position != nil {
			events = append(events, *position)
		}
		flags := uint32(winLeftUp)
		if button == 1 {
			flags = winRightUp
		}
		events = append(events, windowsInput{Mouse: windowsMouseInput{Flags: flags}})
	case "wheel":
		if position == nil {
			return nil
		}
		// 浏览器传像素量，Windows 传 120 单位一格；保留余数，避免细滚动被整数舍弃。
		s.wheelRemainder -= *msg.Delta * 120
		delta := s.wheelRemainder / 100
		s.wheelRemainder %= 100
		if delta == 0 {
			return nil
		}
		events = []windowsInput{*position, {Mouse: windowsMouseInput{Flags: winWheel, Data: uint32(int32(delta))}}}
	default:
		return fmt.Errorf("鼠标动作无效")
	}
	if err := s.write(events); err != nil {
		return err
	}
	if msg.Action == "up" {
		s.pressed[button] = false
	}
	return nil
}

// release 只释放本会话负责的按钮，失败时保留状态供退出路径再次清理，不形成无限重试。
func (s *windowsMouseState) release() error {
	var first error
	for button, pressed := range s.pressed {
		if !pressed {
			continue
		}
		flags := uint32(winLeftUp)
		if button == 1 {
			flags = winRightUp
		}
		if err := s.write([]windowsInput{{Mouse: windowsMouseInput{Flags: flags}}}); err != nil {
			if first == nil {
				first = err
			}
		} else {
			s.pressed[button] = false
		}
	}
	return first
}
