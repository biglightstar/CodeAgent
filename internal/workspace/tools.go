// ===============注册工作区管理相关工具=================
package workspace

import (
	"context"
	"encoding/json"
	"errors"

	"codeagent/internal/tools"

	"github.com/openai/openai-go/v3"
)

func (m *Manager) RegisterTools(registry *tools.Registry) error {
	// 注册第一个工具：查看当前授权的工作区以及激活的工作区
	if err := registry.Register(&tools.ToolDef{
		Name:        "list_workspaces",
		Description: "列出用户配置并授权给 Agent 的项目工作区，以及当前激活的工作区。",
		Params: openai.FunctionParameters{
			"type":       "object",
			"properties": map[string]any{},
		},
		Fn: func(context.Context, map[string]any) (string, error) {
			// 获取所有授权工作区和当前激活的工作区
			payload := map[string]any{
				"active":        m.Active(),
				"allowed_roots": m.Roots(),
			}
			// 序列化并返回
			data, err := json.Marshal(payload)
			return string(data), err
		},
	});
	// 如果第一个工具注册错误，就直接返回错误
	err != nil {
		return err
	}
	// 注册第二个工具：切换工作区
	return registry.Register(&tools.ToolDef{
		Name:        "switch_workspace",
		Description: "将当前会话切换到一个已经由用户配置授权的项目工作区。不能切换到授权列表之外的路径",
		Params: openai.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "必须是已授权工作区的完整路径",
				},
			},
			"required": []string{"path"},
		},
		RequiresApproval: true, // 该工具调用需要用户审批
		Fn: func(_ context.Context, args map[string]any) (string, error) {
			// 获取路径参数
			path, ok := args["path"].(string)
			if !ok || path == "" {
				return "", errors.New("path 必须是非空字符串")
			}
			// 切换工作区
			if err := m.Switch(path); err != nil {
				return "", err
			}
			return "当前工作区已切换为：" + m.Active(), nil
		},
	})
}
