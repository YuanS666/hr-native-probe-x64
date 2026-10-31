package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// TestWindowsInputABI 验证 x64 INPUT 联合体、MOUSEINPUT 的尺寸及关键偏移，不依赖真实 Windows GUI。
func TestWindowsInputABI(t *testing.T) {
	var input windowsInput
	if unsafe.Sizeof(input) != 40 || unsafe.Offsetof(input.Mouse) != 8 || unsafe.Sizeof(input.Mouse) != 32 || unsafe.Offsetof(input.Mouse.ExtraInfo) != 24 {
		t.Fatalf("INPUT=%d unionOffset=%d MOUSEINPUT=%d extraOffset=%d", unsafe.Sizeof(input), unsafe.Offsetof(input.Mouse), unsafe.Sizeof(input.Mouse), unsafe.Offsetof(input.Mouse.ExtraInfo))
	}
}

// TestWindowsNinePoints 按物理像素独立反算九点，覆盖 DPI 场景、宽高比、负虚拟原点及绝对坐标量化误差。
func TestWindowsNinePoints(t *testing.T) {
	maxError := 0.0
	for _, dpi := range []int{96, 120, 144} {
		for _, size := range [][2]int{{1920, 1080}, {2560, 1440}, {3840, 2160}, {1280, 1024}, {1080, 1920}, {1366, 768}} {
			primary := desktopRect{Width: size[0], Height: size[1]}
			// 模拟左侧和上方存在副屏；DPI 缩放不能再次乘进物理像素坐标。
			virtual := desktopRect{Left: -1920, Top: -1080, Width: size[0] + 1920, Height: size[1] + 1080}
			layout, err := fitWindowsVideo(primary, virtual, 1920, 1080)
			if err != nil {
				t.Fatal(err)
			}
			if layout.Content.Left%2 != 0 || layout.Content.Top%2 != 0 || layout.Content.Width%2 != 0 || layout.Content.Height%2 != 0 {
				t.Fatal("色度几何没有偶数对齐")
			}
			for _, py := range []int{0, size[1] / 2, size[1] - 1} {
				for _, px := range []int{0, size[0] / 2, size[0] - 1} {
					// 以原始像素中心在画面内的位置生成输入，不使用被测 point 方法反推期望值。
					x := (float64(layout.Content.Left) + (float64(px)+0.5)*float64(layout.Content.Width)/float64(primary.Width)) / 1920
					y := (float64(layout.Content.Top) + (float64(py)+0.5)*float64(layout.Content.Height)/float64(primary.Height)) / 1080
					event, inside := layout.point(x, y, false)
					if !inside {
						t.Fatal("九点落入黑边")
					}
					// 按绝对输入的 16 位桶模型估计落点；真实 SendInput 结果另行做 GUI 验收。
					actualX := virtual.Left + int(int64(event.Mouse.DX)*int64(virtual.Width)/65536)
					actualY := virtual.Top + int(int64(event.Mouse.DY)*int64(virtual.Height)/65536)
					errorPX := math.Max(math.Abs(float64(actualX-px)), math.Abs(float64(actualY-py)))
					maxError = math.Max(maxError, errorPX)
					if errorPX > 5 {
						t.Fatalf("DPI=%d source=%v point=(%d,%d) actual=(%d,%d)", dpi, size, px, py, actualX, actualY)
					}
				}
			}
		}
	}
	t.Logf("162 组纯逻辑定位：估计最大单轴误差 %.0f 物理像素；未验证 Windows GUI", maxError)
}

// TestWindowsBlackBars 验证编码黑边不能点击、拖动能钳制到屏幕边缘，越界或 NaN 输入始终拒绝。
func TestWindowsBlackBars(t *testing.T) {
	l, err := fitWindowsVideo(desktopRect{Width: 1280, Height: 1024}, desktopRect{Width: 1280, Height: 1024}, 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if l.Content != (desktopRect{Left: 284, Width: 1350, Height: 1080}) {
		t.Fatalf("内容区=%+v", l.Content)
	}
	if _, ok := l.point(0, 0.5, false); ok {
		t.Fatal("左黑边被接受")
	}
	p, ok := l.point(0, 0.5, true)
	if !ok || p.Mouse.DX != 0 {
		t.Fatal("拖动不能钳制到左边缘")
	}
	for _, x := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		if _, ok := l.point(x, 0.5, true); ok {
			t.Fatal("非法坐标被接受")
		}
	}
	if _, err := fitWindowsVideo(desktopRect{Width: 1920, Height: 1080}, desktopRect{Width: 100, Height: 100}, 1920, 1080); err == nil {
		t.Fatal("主屏越出虚拟桌面被接受")
	}
	input, filter := windowsCaptureArgs(l, 20)
	if !strings.Contains(strings.Join(input, " "), "-video_size 1280x1024 -i desktop") || filter != "scale=1350:1080,format=yuv420p,pad=1920:1080:284:0,setsar=1" {
		t.Fatalf("input=%v filter=%s", input, filter)
	}
}

// testMouseMessage 通过真实协议解析器创建测试指令，保持测试与生产使用相同的输入约束。
func testMouseMessage(t *testing.T, action string, button int) mouseMessage {
	t.Helper()
	b := map[string]any{"type": "mouse", "action": action}
	if action != "release" && action != "heartbeat" {
		b["x"] = 0.5
		b["y"] = 0.5
	}
	if action == "down" || action == "up" {
		b["button"] = button
	}
	if action == "down" {
		b["clicks"] = 2
	}
	if action == "wheel" {
		b["delta"] = 100
	}
	data, _ := json.Marshal(b)
	msg, err := parseMouse(data)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// TestWindowsMouseOrder 验证拖动不重发 DOWN、双击恰好两组按放、滚轮方向和黑边释放。
func TestWindowsMouseOrder(t *testing.T) {
	var flags []uint32
	var wheel int32
	// 模拟成功的原生写入，记录事件顺序而不移动本机鼠标。
	s := windowsMouseState{send: func(events []windowsInput) (uint32, error) {
		for _, event := range events {
			flags = append(flags, event.Mouse.Flags)
			if event.Mouse.Flags == winWheel {
				wheel = int32(event.Mouse.Data)
			}
		}
		return uint32(len(events)), nil
	}}
	position := windowsInput{Mouse: windowsMouseInput{Flags: winMove | winAbsolute | winVirtualDesk}}
	for _, action := range []string{"down", "move", "up", "down", "up", "wheel"} {
		if err := s.apply(testMouseMessage(t, action, 0), &position); err != nil {
			t.Fatal(err)
		}
	}
	move := uint32(winMove | winAbsolute | winVirtualDesk)
	want := []uint32{move, winLeftDown, move, move, winLeftUp, move, winLeftDown, move, winLeftUp, move, winWheel}
	if !reflect.DeepEqual(flags, want) || wheel != -120 || s.pressed[0] {
		t.Fatalf("flags=%v wheel=%d pressed=%v", flags, wheel, s.pressed)
	}
	if err := s.apply(testMouseMessage(t, "down", 1), nil); err != nil || s.pressed[1] {
		t.Fatal("黑边点击改变状态")
	}
	s.apply(testMouseMessage(t, "down", 1), &position)
	s.apply(testMouseMessage(t, "up", 1), nil)
	if s.pressed[1] || flags[len(flags)-1] != winRightUp {
		t.Fatal("黑边释放失效")
	}
}

// TestWindowsPartialInput 验证部分插入和 API 失败会保留清理责任，左右键均能在退出时释放且不无限重试。
func TestWindowsPartialInput(t *testing.T) {
	calls := 0
	// 第一次只插入位置事件，后续释放成功，覆盖下发按键时的部分失败路径。
	s := windowsMouseState{send: func(events []windowsInput) (uint32, error) {
		calls++
		if calls == 1 {
			return 1, nil
		}
		return uint32(len(events)), nil
	}}
	position := windowsInput{}
	if err := s.apply(testMouseMessage(t, "down", 0), &position); err == nil {
		t.Fatal("部分成功没有报错")
	}
	if !s.pressed[0] {
		t.Fatal("丢失了清理责任")
	}
	if err := s.release(); err != nil || s.pressed[0] || calls != 2 {
		t.Fatal("部分成功未完成清理")
	}
	// 始终失败的 API 必须返回错误并保留责任，而不是伪造释放成功。
	s.send = func(events []windowsInput) (uint32, error) { return 0, errors.New("SendInput blocked") }
	s.pressed = [2]bool{true, true}
	if err := s.release(); err == nil || !s.pressed[0] || !s.pressed[1] {
		t.Fatal("释放失败被掩盖")
	}
	t.Log(fmt.Sprintf("部分输入清理调用=%d；失败按键未伪装成已释放", calls))
}
