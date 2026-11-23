package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"codeagent/internal/tools"

	"github.com/openai/openai-go/v3"
)

// RegisterManagementTools 向 Agent 提供运行时管理 MCP Server 生命周期的工具。
// 任何启动或停止外部进程的操作都必须经过人工审批。
func RegisterManagementTools(registry *tools.Registry, manager *Manager, configPath string) error {
	if registry == nil || manager == nil {
		return errors.New("MCP 管理工具依赖未初始化")
	}

	if err := registry.Register(&tools.ToolDef{
		Name:        "list_mcp_servers",
		Description: "列出 MCP Server 配置及连接状态。",
		Params:      openai.FunctionParameters{"type": "object", "properties": map[string]any{}},
		Fn: func(context.Context, map[string]any) (string, error) {
			data, err := json.MarshalIndent(manager.ListServers(), "", "  ")
			return string(data), err
		},
	}); err != nil {
		return err
	}

	if err := registry.Register(&tools.ToolDef{
		Name:        "connect_mcp_server",
		Description: "动态配置并连接一个 stdio MCP Server。启动外部进程前必须经过用户审批。",
		Params: openai.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"id":       map[string]any{"type": "string", "description": "唯一 Server ID，仅字母、数字、下划线和连字符"},
				"command":  map[string]any{"type": "string", "description": "要启动的程序路径或命令名"},
				"args":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"env":      map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
				"work_dir": map[string]any{"type": "string"},
			},
			"required": []string{"id", "command"},
		},
		RequiresApproval: true,
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			cfg, err := configFromArgs(args)
			if err != nil {
				return "", err
			}
			cfg.Enabled = false
			if err := manager.UpsertConfig(cfg); err != nil {
				return "", err
			}
			if err := SaveConfig(configPath, manager.ConfigSnapshot()); err != nil {
				return "", err
			}
			if err := manager.ConnectServer(ctx, cfg); err != nil {
				return "", err
			}
			if err := SaveConfig(configPath, manager.ConfigSnapshot()); err != nil {
				_ = manager.DisconnectServer(cfg.ID)
				return "", err
			}
			return fmt.Sprintf("MCP Server %q 已连接，工具已动态注册。", cfg.ID), nil
		},
	}); err != nil {
		return err
	}

	if err := registry.Register(&tools.ToolDef{
		Name:        "disconnect_mcp_server",
		Description: "断开指定 MCP Server，移除它注册的工具，并将配置标记为禁用。",
		Params: openai.FunctionParameters{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"required":   []string{"id"},
		},
		RequiresApproval: true,
		Fn: func(_ context.Context, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			if strings.TrimSpace(id) == "" {
				return "", errors.New("id 不能为空")
			}
			if err := manager.DisconnectServer(id); err != nil {
				return "", err
			}
			if err := SaveConfig(configPath, manager.ConfigSnapshot()); err != nil {
				return "", err
			}
			return "MCP Server 已断开，相关工具已从 Registry 移除：" + id, nil
		},
	}); err != nil {
		return err
	}

	return registry.Register(&tools.ToolDef{
		Name:        "reconnect_mcp_server",
		Description: "按已保存的配置重启并重新发现指定 MCP Server 的工具。",
		Params: openai.FunctionParameters{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"required":   []string{"id"},
		},
		RequiresApproval: true,
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			if strings.TrimSpace(id) == "" {
				return "", errors.New("id 不能为空")
			}
			if err := manager.ReconnectServer(ctx, id); err != nil {
				return "", err
			}
			if err := SaveConfig(configPath, manager.ConfigSnapshot()); err != nil {
				return "", err
			}
			return "MCP Server 已重新连接，工具已刷新：" + id, nil
		},
	})
}

func configFromArgs(args map[string]any) (ServerConfig, error) {
	id, _ := args["id"].(string)
	command, _ := args["command"].(string)
	if strings.TrimSpace(id) == "" || strings.TrimSpace(command) == "" {
		return ServerConfig{}, errors.New("id 和 command 均不能为空")
	}
	cfg := ServerConfig{ID: id, Enabled: true, Transport: "stdio", Command: command}
	if value, ok := args["work_dir"].(string); ok {
		cfg.WorkDir = value
	}
	if values, ok := args["args"].([]any); ok {
		for _, value := range values {
			item, ok := value.(string)
			if !ok {
				return ServerConfig{}, errors.New("args 必须全部是字符串")
			}
			cfg.Args = append(cfg.Args, item)
		}
	}
	if values, ok := args["env"].(map[string]any); ok {
		cfg.Env = make(map[string]string, len(values))
		for key, value := range values {
			text, ok := value.(string)
			if !ok {
				return ServerConfig{}, fmt.Errorf("env[%s] 必须是字符串", key)
			}
			cfg.Env[key] = text
		}
	}
	return cfg, nil
}
