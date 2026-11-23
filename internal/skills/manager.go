// ========================================================
package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openai/openai-go/v3"

	"codeagent/internal/tools"
)

type Skill struct {
	Name        string
	Description string
	Path        string
	Body        string
}

type Manager struct {
	root   string
	skills map[string]Skill
}

func NewManager(root string) *Manager {
	return &Manager{root: root, skills: make(map[string]Skill)}
}

func (m *Manager) Scan() error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	found := make(map[string]Skill)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(m.root, name, "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		skill, err := parseSkill(path, string(data))
		if err != nil {
			return fmt.Errorf("解析 Skill %s 失败: %w", name, err)
		}
		if skill.Name != name {
			return fmt.Errorf("Skill 目录名 %q 必须与 frontmatter name %q 一致", name, skill.Name)
		}
		found[skill.Name] = skill
	}
	m.skills = found
	return nil
}

func (m *Manager) Catalog() string {
	names := make([]string, 0, len(m.skills))
	for name := range m.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("可用 Skills（需要时调用 load_skill 加载完整说明）：\n")
	for _, name := range names {
		s := m.skills[name]
		fmt.Fprintf(&b, "- %s：%s\n", s.Name, s.Description)
	}
	return b.String()
}

func (m *Manager) Load(name string) (Skill, error) {
	if strings.Contains(name, "..") || strings.ContainsAny(name, `/\\`) {
		return Skill{}, errors.New("非法 Skill 名称")
	}
	skill, ok := m.skills[name]
	if !ok {
		return Skill{}, fmt.Errorf("未找到 Skill: %s", name)
	}
	return skill, nil
}

func (m *Manager) RegisterTool(registry *tools.Registry) error {
	return registry.Register(&tools.ToolDef{
		Name:        "load_skill",
		Description: "按名称加载一个 Skill 的完整工作流程和专业规范。只有在当前任务确实需要时才加载。",
		Params: openai.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Skill 名称，例如 frontend-design"},
			},
			"required": []string{"name"},
		},
		RequiresApproval: false,
		Fn: func(_ context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			skill, err := m.Load(name)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("已加载 Skill：%s\n\n%s", skill.Name, skill.Body), nil
		},
	})
}

func parseSkill(path, content string) (Skill, error) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return Skill{}, errors.New("SKILL.md 必须以 YAML frontmatter 开始")
	}
	meta := make(map[string]string)
	bodyStart := -1
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			bodyStart = i + 1
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			meta[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), "\"'")
		}
	}
	if bodyStart < 0 {
		return Skill{}, errors.New("frontmatter 缺少结束标记 ---")
	}
	name := meta["name"]
	description := meta["description"]
	if name == "" || description == "" {
		return Skill{}, errors.New("frontmatter 必须包含 name 和 description")
	}
	if len(name) > 64 || strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return Skill{}, errors.New("Skill name 不符合命名约束")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return Skill{}, errors.New("Skill name 只能包含小写字母、数字和连字符")
		}
	}
	return Skill{Name: name, Description: description, Path: path, Body: strings.TrimSpace(strings.Join(lines[bodyStart:], "\n"))}, nil
}
