//go:build windows && amd64

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

var winKernel = syscall.NewLazyDLL("kernel32.dll")
var winUser = windowsSystemDLL("user32.dll")
var winShcore = windowsSystemDLL("shcore.dll")
var winSend = winUser.NewProc("SendInput")
var winMetrics = winUser.NewProc("GetSystemMetrics")
var winDPIOnce sync.Once
var winDPIError error
var winDPIMode string

// 两个方向的数组长度断言让任何 x64 ABI 尺寸变化直接导致编译失败。
var _ [40 - unsafe.Sizeof(windowsInput{})]byte
var _ [unsafe.Sizeof(windowsInput{}) - 40]byte
var _ [32 - unsafe.Sizeof(windowsMouseInput{})]byte
var _ [unsafe.Sizeof(windowsMouseInput{}) - 32]byte

// windowsSystemDLL 从系统目录定位原生库，避免从工作目录加载同名 DLL；参数是系统 DLL 名称。
func windowsSystemDLL(name string) *syscall.LazyDLL {
	var path [260]uint16
	n, _, err := winKernel.NewProc("GetSystemDirectoryW").Call(uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)))
	if n == 0 || n >= uintptr(len(path)) {
		panic(fmt.Sprintf("GetSystemDirectoryW 失败: %v", err))
	}
	return syscall.NewLazyDLL(filepath.Join(syscall.UTF16ToString(path[:]), name))
}

// ensureWindowsDPI 在任何显示 API 前设置进程的逐显示器 DPI 感知，旧版 Windows 10 回退 PMv1，拒绝虚拟化坐标。
func ensureWindowsDPI() error {
	// 进程 DPI 只设置一次；后续会话及 Go 工作线程继承相同默认坐标语义。
	winDPIOnce.Do(func() {
		modern := winUser.NewProc("SetProcessDpiAwarenessContext")
		if modern.Find() == nil {
			// -4 是系统定义的 PER_MONITOR_AWARE_V2 伪句柄，不是内存地址。
			ok, _, _ := modern.Call(^uintptr(3))
			if ok != 0 {
				winDPIMode = "PerMonitorV2"
			}
		}
		if winDPIMode == "" {
			winShcore.NewProc("SetProcessDpiAwareness").Call(2)
		}
		var awareness int32
		hr, _, _ := winShcore.NewProc("GetProcessDpiAwareness").Call(0, uintptr(unsafe.Pointer(&awareness)))
		if hr != 0 || awareness != 2 {
			winDPIError = fmt.Errorf("无法启用逐显示器 DPI 感知，HRESULT=0x%x awareness=%d", hr, awareness)
			return
		}
		if winDPIMode == "" {
			winDPIMode = "PerMonitor"
		}
	})
	return winDPIError
}

// windowsMetric 保留有符号返回值，正确读取位于主屏左侧或上方的负虚拟桌面原点。
func windowsMetric(index int) int {
	r, _, _ := winMetrics.Call(uintptr(index))
	return int(int32(r))
}

// windowsObjectName 读取窗口站或桌面名称，仅申请查询所需的信息，不修改目标桌面。
func windowsObjectName(handle uintptr) (string, error) {
	var name [256]uint16
	var needed uint32
	ok, _, err := winUser.NewProc("GetUserObjectInformationW").Call(handle, 2, uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Sizeof(name)), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		return "", fmt.Errorf("查询桌面名称失败: %v", err)
	}
	return syscall.UTF16ToString(name[:]), nil
}

// windowsDesktopReady 仅允许当前物理控制台的 WinSta0/Default 桌面，拒绝服务、RDP 及锁屏/UAC 安全桌面。
func windowsDesktopReady() error {
	if err := ensureWindowsDPI(); err != nil {
		return err
	}
	var session uint32
	ok, _, err := winKernel.NewProc("ProcessIdToSessionId").Call(uintptr(os.Getpid()), uintptr(unsafe.Pointer(&session)))
	console, _, _ := winKernel.NewProc("WTSGetActiveConsoleSessionId").Call()
	if ok == 0 || session == 0 || uint32(console) != session {
		return fmt.Errorf("需要已登录的本机控制台会话，当前 session=%d console=%d: %v", session, uint32(console), err)
	}
	station, _, _ := winUser.NewProc("GetProcessWindowStation").Call()
	name, err := windowsObjectName(station)
	if err != nil || name != "WinSta0" {
		return fmt.Errorf("需要交互窗口站 WinSta0，当前 %q: %v", name, err)
	}
	desktop, _, err := winUser.NewProc("OpenInputDesktop").Call(0, 0, 1)
	if desktop == 0 {
		return fmt.Errorf("无法查询输入桌面，锁屏或 UAC 期间不执行输入: %v", err)
	}
	defer winUser.NewProc("CloseDesktop").Call(desktop)
	name, err = windowsObjectName(desktop)
	if err != nil || name != "Default" {
		return fmt.Errorf("仅支持 Default 普通桌面，当前 %q: %v", name, err)
	}
	return nil
}

type windowsDisplay struct {
	Primary, Virtual desktopRect
	Monitors         int
	DPI              uint32
}

// windowsDisplayInfo 读取 DPI 感知下的物理屏幕范围及主屏有效 DPI，DPI 只用于报告和布局变更检测。
func windowsDisplayInfo() (windowsDisplay, error) {
	var d windowsDisplay
	if err := ensureWindowsDPI(); err != nil {
		return d, err
	}
	d.Primary = desktopRect{Width: windowsMetric(0), Height: windowsMetric(1)}
	d.Virtual = desktopRect{Left: windowsMetric(76), Top: windowsMetric(77), Width: windowsMetric(78), Height: windowsMetric(79)}
	d.Monitors = windowsMetric(80)
	monitor, _, _ := winUser.NewProc("MonitorFromPoint").Call(0, 1)
	var dpiY uint32
	hr, _, _ := winShcore.NewProc("GetDpiForMonitor").Call(monitor, 0, uintptr(unsafe.Pointer(&d.DPI)), uintptr(unsafe.Pointer(&dpiY)))
	if hr != 0 || d.DPI == 0 || d.Primary.Width < 2 || d.Primary.Height < 2 || d.Virtual.Width < 2 || d.Virtual.Height < 2 || d.Monitors < 1 {
		return d, fmt.Errorf("无法获取有效物理屏幕信息，DPI HRESULT=0x%x", hr)
	}
	return d, nil
}

// mousePermissions 只查询普通桌面、DPI 和显示器信息；SendInput 能否控制具体窗口必须实际操作验证。
func mousePermissions() string {
	if err := windowsDesktopReady(); err != nil {
		return err.Error()
	}
	d, err := windowsDisplayInfo()
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("Windows x64：%s；主屏物理区域=%+v；虚拟桌面=%+v；显示器=%d；主屏 DPI=%d（%.0f%%）；仅 Default 控制台，SendInput/UIPI 效果待实际操作确认", winDPIMode, d.Primary, d.Virtual, d.Monitors, d.DPI, float64(d.DPI)*100/96)
}

// sendWindowsInputs 调用 x64 SendInput，完整检查插入数量；零错误码也不能排除 UIPI，不自动提权。
func sendWindowsInputs(events []windowsInput) (uint32, error) {
	if len(events) == 0 {
		return 0, nil
	}
	n, _, last := winSend.Call(uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), unsafe.Sizeof(windowsInput{}))
	runtime.KeepAlive(events)
	if int(n) != len(events) {
		return uint32(n), fmt.Errorf("SendInput 插入 %d/%d，Windows error=%v；UIPI 拦截不能由错误码单独判定", n, len(events), last)
	}
	return uint32(n), nil
}

type nativeMouse struct {
	display windowsDisplay
	layout  windowsVideoLayout
	state   windowsMouseState
	keys    keyboardState
}

// newNativeMouse 为主显示器创建输入映射，复用已有鼠标协议且不注入任何初始化事件。
func newNativeMouse(device string) (*nativeMouse, error) {
	if device != "desktop" {
		return nil, fmt.Errorf("Windows 首版仅支持 -device desktop（主显示器）")
	}
	if err := windowsDesktopReady(); err != nil {
		return nil, err
	}
	d, err := windowsDisplayInfo()
	if err != nil {
		return nil, err
	}
	l, err := fitWindowsVideo(d.Primary, d.Virtual, *width, *height)
	if err != nil {
		return nil, err
	}
	return &nativeMouse{display: d, layout: l, state: windowsMouseState{send: sendWindowsInputs}}, nil
}

// handle 执行已校验的鼠标或键盘事件，布局变化或API失败时返回错误并保留清理责任。
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
	if err := windowsDesktopReady(); err != nil {
		return err
	}
	d, err := windowsDisplayInfo()
	if err != nil {
		return err
	}
	if d != m.display {
		return fmt.Errorf("Windows 显示布局或 DPI 已变化，请重新连接")
	}
	if msg.Action == "heartbeat" {
		return nil
	}
	if msg.Type == "keyboard" {
		return m.keys.apply(msg)
	}
	clamp := msg.Action == "move" && (m.state.pressed[0] || m.state.pressed[1])
	position, inside := m.layout.point(*msg.X, *msg.Y, clamp)
	var point *windowsInput
	if inside {
		point = &position
	}
	if err := m.state.apply(msg, point); err != nil {
		m.release()
		return err
	}
	return nil
}

// release 仅在普通桌面释放本会话鼠标与键盘，失败返回错误并保留状态，不向安全桌面投递。
func (m *nativeMouse) release() error {
	return errors.Join(m.releaseMouse(), m.keys.release())
}

// releaseMouse 只归还鼠标按钮，不取消独立键盘开关正在持有的修饰键；失败保留按钮责任。
func (m *nativeMouse) releaseMouse() error {
	if !m.state.pressed[0] && !m.state.pressed[1] {
		return nil
	}
	if err := windowsDesktopReady(); err != nil {
		return err
	}
	return m.state.release()
}

// sendKey 仅在普通控制台桌面注入一个40字节键盘INPUT，检查实际数量且不绕过UIPI。
func (m *nativeMouse) sendKey(code string, down bool, _ uint64) error {
	if err := windowsDesktopReady(); err != nil {
		return err
	}
	event, err := windowsKeyEvent(code, down)
	if err != nil {
		return err
	}
	n, _, last := winSend.Call(1, uintptr(unsafe.Pointer(&event)), unsafe.Sizeof(event))
	runtime.KeepAlive(&event)
	if n != 1 {
		return fmt.Errorf("键盘SendInput 插入 %d/1，Windows error=%v；可能受UIPI限制", n, last)
	}
	return nil
}

// 键盘INPUT和鼠标INPUT必须使用相同的40字节ABI尺寸。
var _ [40 - unsafe.Sizeof(windowsKeyboardInput{})]byte
var _ [unsafe.Sizeof(windowsKeyboardInput{}) - 40]byte
