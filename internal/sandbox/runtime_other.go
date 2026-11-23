//go:build !windows

// ===============windows以外平台的占位实现================
// 如果操作系统不是windows，不能使用runtime_windows.go这套沙箱实现
// 需要本文件来实现，目前没有写具体是实现，只是占位
package sandbox

import (
	"errors"
	"fmt"
)

// 定义windowsRuntime类型
// 在非Windows平台，这个类型只是一个占位类型
type WindowsRuntime struct{}

// 创建Windows沙箱运行时
func NewWindowsRuntime() (*WindowsRuntime, error) {
	// 非windows平台不支持Windowsappcontainter
	// 返回nil，错误
	return nil, errors.New("Windows AppContainer 沙箱仅支持 Windows")
}

// 判断平台的沙箱是否可用
func Available() bool {
	// 非Windows平台始终返回false
	return false
}

// 沙箱runner的入口函数
func RunnerMain(args []string) int {
	// 告诉用户当前平台不支持沙箱runner
	fmt.Println("sandbox runner 仅支持 Windows")
	// 表示runner执行失败
	return 2
}
