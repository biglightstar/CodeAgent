// =======================工具注册中心，统一管理agent的工具=========================
package tools

import (
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/openai/openai-go/v3/responses"
)

// 工具仓库
type Registry struct {
	mu    sync.RWMutex
	items map[string]*ToolDef // 工具列表
}

// 创建一个工具仓库
func NewRegistry() *Registry {
	return &Registry{items: make(map[string]*ToolDef)}
}

// 注册一个工具
func (r *Registry) Register(tool *ToolDef) error {
	// 检查工具定义
	if tool == nil || tool.Name == "" || tool.Fn == nil {
		return errors.New("工具定义不完整")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// 工具名是否冲突
	if _, exists := r.items[tool.Name]; exists {
		return errors.New("工具名称冲突: " + tool.Name)
	}
	// 注册工具到工具列表里面
	r.items[tool.Name] = tool
	return nil
}

// 根据工具名称获取对应工具
func (r *Registry) Get(name string) (*ToolDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.items[name]
	return tool, ok
}

// 获取工具列表
func (r *Registry) List() []*ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// 获取所有的工具名称
	names := make([]string, 0, len(r.items))
	for name := range r.items {
		names = append(names, name)
	}
	// 工具名称排序
	sort.Strings(names)
	result := make([]*ToolDef, 0, len(names))
	// 根据工具名称依次获取工具
	for _, name := range names {
		result = append(result, r.items[name])
	}
	// 返回工具列表
	return result
}

// 将工具转化为openai认识的格式
func (r *Registry) BuildOpenAITools() []responses.ToolUnionParam {
	// 获取工具列表
	items := r.List()
	result := make([]responses.ToolUnionParam, 0, len(items))
	// 遍历工具列表，转换格式
	for _, item := range items {
		result = append(result, item.ToParam())
	}
	return result
}

// 根据工具名称删除对应工具
func (r *Registry) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 如果工具不存在
	if _, ok := r.items[name]; !ok {
		return false
	}
	// 工具存在返回工具
	delete(r.items, name)
	return true
}

// 删除名称以指定前缀开头的工具
// 比如，如果断开mcpserver，就应该删除对应提供的mcp工具
func (r *Registry) RemovePrefix(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for name := range r.items {
		if strings.HasPrefix(name, prefix) {
			delete(r.items, name)
			removed++
		}
	}
	return removed
}
