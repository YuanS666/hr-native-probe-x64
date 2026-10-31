//go:build linux || (darwin && !cgo) || (windows && !amd64)

package main

import "fmt"

type nativeMouse struct{}

// mousePermissions 为无 macOS 原生 API 的 Relay 构建返回明确说明，不申请或模拟权限。
func mousePermissions() string {
	return "此构建没有原生鼠标实现；需要 macOS+cgo 或 Windows x64，其他构建仅支持 Relay"
}

// newNativeMouse 拒绝在非 macOS 原生构建上初始化鼠标，Relay 本身不执行控制事件。
func newNativeMouse(device string) (*nativeMouse, error) {
	return nil, fmt.Errorf("鼠标控制需要 macOS+cgo 或 Windows x64")
}

// handle 拒绝不受支持平台上的操作，不提供任何替代输入注入。
func (m *nativeMouse) handle(msg mouseMessage) error {
	return fmt.Errorf("此构建不能执行鼠标事件")
}

// release 为 Relay 编译提供空清理方法；此构建从未按下系统鼠标按钮。
func (m *nativeMouse) release() {}
