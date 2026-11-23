// ========================注册执行shell命令的工具==============================
package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"

	"codeagent/internal/tools"
	"codeagent/internal/workspace"
)

// 注册shell工具到register
func RegisterTools(registry *tools.Registry, manager *ProcessManager, workspaces *workspace.Manager, enabled bool) error {
	// 检查工具注册中心或工作区管理器是否为空
	if registry == nil || workspaces == nil {
		return errors.New("Shell 工具依赖未初始化")
	}
	// 注册第一个工具：exec_shell 执行命令并等待完成 需要批准
	// 需要注册：名字 描述 参数 是否需要批准 执行器
	if err := registry.Register(&tools.ToolDef{
		Name:        "exec_shell",
		Description: "在当前已授权工作区中执行一条 Windows PowerShell 命令，返回退出码、标准输出、标准错误和执行状态。每次执行前都需要用户批准。",
		Params: openai.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"command":         map[string]any{"type": "string", "description": "需要执行的 PowerShell 命令"},
				"workdir":         map[string]any{"type": "string", "description": "可选，工作区内的绝对路径或相对路径；默认当前工作区根目录"},
				"timeout_seconds": map[string]any{"type": "integer", "description": "可选，超时秒数，默认 30"},
			},
			"required": []string{"command"},
		},
		RequiresApproval: true, // 该工具需要经过用户批准
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			// 检查shell是否启用 以及进程管理器是否初始化
			// 未启用时禁止执行命令
			// 这两个字段是此注册函数的传入参数
			if !enabled || manager == nil {
				return "", errors.New("Shell 执行当前已禁用：尚未配置 Windows OS Sandbox。不要通过此开发模式执行不可信命令。")
			}
			// 获取用户要执行的命令
			// 如果参数不存在或者类型不匹配，返回空串
			command, _ := args["command"].(string)
			// 去除字符串两端的空白字符，并检查命令是否为空
			if strings.TrimSpace(command) == "" {
				return "", errors.New("command 不能为空")
			}
			// 获取用户指定的工作目录
			workdir, _ := args["workdir"].(string)
			// 解析并校验工作目录(使用workspace包)
			resolved, err := workspaces.Resolve(workdir)
			if err != nil {
				return "", err
			}
			// 解析超时时间，默认使用30秒
			timeout := parseTimeout(args["timeout_seconds"], 30)
			// 将命令、工作目录、超时时间封装成processrequest进程请求
			// 交给manager启动一个shell任务
			task, err := manager.Start(ctx, ProcessRequest{
				Command: command,
				WorkDir: resolved,
				Timeout: timeout,
			})
			if err != nil {
				return "", err
			}
			// 等待这个任务完成
			// 拿到这个结果finished，processInfo，返回命令执行的结果信息
			finished, err := manager.WaitTask(ctx, task.ID)
			if err != nil {
				return "", err
			}
			// 将执行结果结构体序列化成json 返回给大模型
			data, err := json.Marshal(finished)
			return string(data), err
		},
	});
	// 注册工具失败，返回错误
	err != nil {
		return err
	}
	// 注册第二个工具：start_shell_task 启动后台命令 需要批准
	if err := registry.Register(&tools.ToolDef{
		Name:        "start_shell_task",
		Description: "在当前授权工作区启动后台 PowerShell 任务，立即返回 task_id。每次启动前都需要用户批准。",
		Params: openai.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"command":         map[string]any{"type": "string"},
				"workdir":         map[string]any{"type": "string"},
				"timeout_seconds": map[string]any{"type": "integer"},
			},
			"required": []string{"command"},
		},
		RequiresApproval: true,
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			// 判断shell命令是否启动、进程管理是否初始化
			if !enabled || manager == nil {
				return "", errors.New("Shell 执行当前已禁用：尚未配置 Windows OS Sandbox")
			}
			// 获取命令
			command, _ := args["command"].(string)
			if strings.TrimSpace(command) == "" {
				return "", errors.New("command 不能为空")
			}
			// 获取工作目录
			workdir, _ := args["workdir"].(string)
			// 解析工作目录
			resolved, err := workspaces.Resolve(workdir)
			if err != nil {
				return "", err
			}
			// 调用processManager的start方法，启动一个后台任务
			task, err := manager.Start(ctx, ProcessRequest{
				Command: command,
				WorkDir: resolved,
				Timeout: parseTimeout(args["timeout_seconds"], 0),
			})
			if err != nil {
				return "", err
			}
			// 启动成功,返回任务启动信息
			return fmt.Sprintf("后台任务已启动:task_id=%s,status=%s", task.ID, task.Status), nil
		},
	}); err != nil {
		return err
	}
	// 注册第三个工具：get_shell_task 查询后台任务 不需要批准
	if err := registry.Register(&tools.ToolDef{
		Name:        "get_shell_task",
		Description: "根据 task_id 查询后台 Shell 任务状态和输出。",
		Params: openai.FunctionParameters{
			"type":       "object",
			"properties": map[string]any{"task_id": map[string]any{"type": "string"}},
			"required":   []string{"task_id"},
		},
		RequiresApproval: false,
		Fn: func(_ context.Context, args map[string]any) (string, error) {
			if manager == nil {
				return "", errors.New("进程管理器未初始化")
			}
			// 获取任务id
			id, _ := args["task_id"].(string)
			// 获取该任务的详细信息info结构体
			info, err := manager.GetTask(id)
			if err != nil {
				return "", err
			}
			// info序列化并返回结果
			data, err := json.Marshal(info)
			return string(data), err
		},
	}); err != nil {
		return err
	}
	// 注册第四个工具：cancel_shell_task 取消后台任务 需要批准
	return registry.Register(&tools.ToolDef{
		Name:        "cancel_shell_task",
		Description: "取消指定的后台 Shell 任务。每次取消前都需要用户批准。",
		Params: openai.FunctionParameters{
			"type":       "object",
			"properties": map[string]any{"task_id": map[string]any{"type": "string"}},
			"required":   []string{"task_id"},
		},
		RequiresApproval: true,
		Fn: func(_ context.Context, args map[string]any) (string, error) {
			if manager == nil {
				return "", errors.New("进程管理器未初始化")
			}
			// 获取任务id
			id, _ := args["task_id"].(string)
			// 取消该任务
			if err := manager.CancelTask(id); err != nil {
				return "", err
			}
			return "已向任务发送取消请求：" + id, nil
		},
	})
}

// 把大模型传入的超时时间参数，转换成Go的时间间隔
func parseTimeout(value any, fallback int) time.Duration {
	// 默认时间fallback
	seconds := fallback
	switch n := value.(type) {
	case float64:
		seconds = int(n)
	case int:
		seconds = n
	case int64:
		seconds = int(n)
	}
	if seconds < 0 {
		seconds = fallback
	}
	if seconds == 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
