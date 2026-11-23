package shell

import "os"

// ShellAdapter接口： 将 Agent 使用的任务管理接口与沙箱启动器解耦
type ShellAdapter interface {
	BuildCommand(command string) (string, []string)
}

// PowerShellAdapter 以专用 sandbox-runner 模式启动当前 Code Agent 可执行文件。
// Runner 在 Windows AppContainer 中启动 PowerShell，并将其加入 Job Object。
type PowerShellAdapter struct {
	Executable string
}

// 创建Windows的shell适配器
func NewPowerShellAdapter() *PowerShellAdapter {
	executable, _ := os.Executable()
	return &PowerShellAdapter{Executable: executable}
}

// 返回启动程序和参数、命令
func (a *PowerShellAdapter) BuildCommand(command string) (string, []string) {
	// 获取程序路径
	executable := a.Executable
	// 程序路径为空，则重新获取
	if executable == "" {
		executable, _ = os.Executable()
	}
	// 返回程序路径和启动程序的参数以及命令
	return executable, []string{"--sandbox-runner", "--", command}
}
