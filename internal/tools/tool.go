// ============工具定义和调用===================
package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

// ToolDef
type ToolDef struct {
	Name             string
	Description      string
	Params           openai.FunctionParameters
	RequiresApproval bool // 该工具是否需要用户审批
	Fn               func(ctx context.Context, args map[string]any) (string, error)
}

// 将toolDef转变成openai认识的工具参数
func (t *ToolDef) ToParam() responses.ToolUnionParam {
	return responses.ToolUnionParam{
		OfFunction: &responses.FunctionToolParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  t.Params,
		},
	}
}

// 根据大模型提供的json参数 调用对应工具
func (t *ToolDef) Call(ctx context.Context, argsJSON string) (string, error) {
	if t == nil || t.Fn == nil {
		return "", errors.New("工具未初始化")
	}
	// 将json参数反序列化为map
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	if args == nil {
		args = make(map[string]any)
	}
	// 执行，返回结果
	return t.Fn(ctx, args)
}
