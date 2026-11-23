// ===================agent工作区====================
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// 工作区管理器
type Manager struct {
	mu     sync.RWMutex
	roots  []string // 用户授权的所有工作区目录
	active string   // 当前正在使用的工作区,可以切换当前工作区
}

// 创建一个工作区管理器
func NewManager(roots []string) (*Manager, error) {
	// 检查是否有授权工作区
	if len(roots) == 0 {
		return nil, errors.New("至少需要配置一个授权工作区")
	}
	var canonical []string
	// 处理工作区路径
	for _, root := range roots {
		// 处理路径
		path, err := canonicalDir(root)
		if err != nil {
			return nil, fmt.Errorf("工作区 %q 无效: %w", root, err)
		}
		// 保存规范化路径
		canonical = append(canonical, path)
	}
	return &Manager{
		roots:  canonical,
		active: canonical[0], // 默认使用第一个为当前工作区
	}, nil
}

// 获取所有授权目录
func (m *Manager) Roots() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// 把授权目录复制过来，返回结果
	return append([]string(nil), m.roots...)
}

// 获取当前工作目录
func (m *Manager) Active() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

// 切换工作区，只能切换到已经授权的目录
func (m *Manager) Switch(root string) error {
	// 路径规范化
	path, err := canonicalDir(root)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// 遍历所有授权目录
	for _, allowed := range m.roots {
		// 判断路径是否相同
		if samePath(path, allowed) {
			// 找到匹配目录后，更改当前工作区
			m.active = allowed
			return nil
		}
	}
	return errors.New("目标工作区不在用户配置的授权列表中")
}

// 解析并验证工作目录
func (m *Manager) Resolve(workDir string) (string, error) {
	m.mu.RLock()
	// 获取当前工作目录
	active := m.active
	// 获取所有已授权的工作目录
	roots := append([]string(nil), m.roots...)
	m.mu.RUnlock()
	// 候选路径
	candidate := workDir
	// 确定候选路径
	// 用户没有传入工作目录，就用当前激活的工作区
	if candidate == "" {
		candidate = active
	} else if !filepath.IsAbs(candidate) {
		// 如果用户传入的是相对路径，就拼接到当前工作区下面
		candidate = filepath.Join(active, candidate)
	}
	// 规范化候选路径
	path, err := canonicalDir(candidate)
	if err != nil {
		return "", err
	}
	// 检查路径是否在授权范围内
	for _, root := range roots {
		if withinRoot(root, path) {
			return path, nil
		}
	}
	return "", errors.New("工作目录超出所有已授权工作区")
}

// 规范化路径
func canonicalDir(path string) (string, error) {
	// 转换成绝对路径
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// 检查路径是否存在
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	// 是否是目录
	if !info.IsDir() {
		return "", errors.New("路径不是目录")
	}
	// 解析符号链接
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	// 清除路径格式
	return filepath.Clean(real), nil
}

// 判断目标路径是否在授权范围内
func withinRoot(root, path string) bool {
	// 清除格式
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	// 如果和已授权目录相同，直接返回true
	if samePath(root, path) {
		return true
	}
	// 判断path是否是root的子目录
	// filepath.Separator是当前操作系统的路径分隔符
	// 先清除末尾出现的路径分隔符
	// 再加上路径分隔符
	prefix := strings.TrimRight(root, string(filepath.Separator)) + string(filepath.Separator)
	// 如果path是root的子目录，path一定比root长
	if len(path) < len(prefix) {
		return false
	}
	// 判断path是否是root的子目录
	// windows
	if runtime.GOOS == "windows" {
		return strings.EqualFold(path[:len(prefix)], prefix)
	}
	// Linux
	return path[:len(prefix)] == prefix
}

// 判断两个路径是否相同
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	// Windows对大小写不区分
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	// Linux
	return a == b
}
