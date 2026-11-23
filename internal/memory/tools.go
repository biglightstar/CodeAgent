// =================注册工具：追加长期记忆===================
package memory

import (
	"context"
	"errors"
	"strings"

	"github.com/openai/openai-go/v3"

	"codeagent/internal/tools"
)

// 将用户要求的规则追加到长期记忆里面
func (s *Store) RegisterTools(registry *tools.Registry) error {
	return registry.Register(&tools.ToolDef{
		Name:        "save_long_term_memory",
		Description: "将用户明确要求长期记住的非敏感项目约定或工作偏好保存到本地记忆文件。保存前需要用户批准。",
		Params: openai.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "需要长期保存的项目约定或工作偏好",
				},
			},
			"required": []string{"content"},
		},
		RequiresApproval: true,
		Fn: func(_ context.Context, args map[string]any) (string, error) {
			content, _ := args["content"].(string)
			if strings.TrimSpace(content) == "" {
				return "", errors.New("content 不能为空")
			}
			// 调用追加长期记忆的函数
			if err := s.AppendLongTerm(content); err != nil {
				return "", err
			}
			return "已将这条信息保存到本地长期记忆文件", nil
		},
	})
}
