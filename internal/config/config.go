// ======================默认配置参数=====================
package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	APIKey           string
	BaseURL          string
	Model            string
	WorkspaceRoots   []string
	DataDir          string
	SkillsDir        string
	MCPConfig        string
	SummaryThreshold int
}

// 默认配置函数
func Load() (Config, error) {
	cfg := Config{
		APIKey:           strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		BaseURL:          strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
		Model:            strings.TrimSpace(os.Getenv("OPENAI_MODEL")),
		DataDir:          envOr("DATA_DIR", "./data"),
		SkillsDir:        envOr("SKILLS_DIR", "./skills"),
		MCPConfig:        envOr("MCP_CONFIG", "./configs/mcp_servers.json"),
		SummaryThreshold: 40,
	}
	if cfg.APIKey == "" {
		return Config{}, errors.New("OPENAI_API_KEY 未设置")
	}
	if cfg.Model == "" {
		cfg.Model = "deepseek-flash"
	}
	if raw := strings.TrimSpace(os.Getenv("WORKSPACE_ROOT")); raw != "" {
		for _, item := range strings.Split(raw, string(os.PathListSeparator)) {
			if v := strings.TrimSpace(item); v != "" {
				cfg.WorkspaceRoots = append(cfg.WorkspaceRoots, v)
			}
		}
	}
	if len(cfg.WorkspaceRoots) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return Config{}, err
		}
		cfg.WorkspaceRoots = []string{cwd}
	}
	if raw := strings.TrimSpace(os.Getenv("SUMMARY_THRESHOLD")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 10 {
			return Config{}, errors.New("SUMMARY_THRESHOLD 必须是大于等于 10 的整数")
		}
		cfg.SummaryThreshold = parsed
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
