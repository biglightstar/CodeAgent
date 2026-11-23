// =================记忆存储管理==============
// data/memory/long_term.md
// data/sessions/session01.json session01.summary.md
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 记忆存储管理器
type Store struct {
	root string // 数据存储的根目录，长期记忆和会话数据都保存在这里
	mu   sync.Mutex
}

// 创建记忆存储管理器
func NewStore(root string) (*Store, error) {
	// 检查数据目录是否为空
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("记忆数据目录不能为空")
	}
	// 根据提供的root目录，分别给记忆和session创建一个目录
	for _, dir := range []string{filepath.Join(root, "memory"), filepath.Join(root, "sessions")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	// 返回记忆存储器
	return &Store{root: root}, nil
}

// 返回长期(long_term)记忆文件的完整路径
// 长期记忆保存在long_term.md里面
func (s *Store) LongTermPath() string {
	return filepath.Join(s.root, "memory", "long_term.md")
}

// 根据sessionID返回 对应会话 json文件的路径
func (s *Store) SessionPath(sessionID string) string {
	return filepath.Join(s.root, "sessions", safeName(sessionID)+".json")
}

// 根据sessionID返回 对应摘要 json文件的路径
func (s *Store) SummaryPath(sessionID string) string {
	return filepath.Join(s.root, "sessions", safeName(sessionID)+".summary.md")
}

// 向长记忆文件追加一条记忆
func (s *Store) AppendLongTerm(note string) error {
	// 检查追加的记忆内容是否为空
	note = strings.TrimSpace(note)
	if note == "" {
		return errors.New("记忆内容不能为空")
	}
	// 锁
	s.mu.Lock()
	defer s.mu.Unlock()
	// 获取长期记忆文件所在的目录，并确保文件存在
	if err := os.MkdirAll(filepath.Dir(s.LongTermPath()), 0o700); err != nil {
		return err
	}
	// 打开长记忆文件
	// 如果文件不存在就创建 追加 只写
	f, err := os.OpenFile(s.LongTermPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// 将格式化后的内容写入文件
	// 两个井号表示二级标题
	_, err = fmt.Fprintf(f, "\n## %s\n%s\n", time.Now().Format(time.RFC3339), note)
	return err
}

// 从磁盘中读取长记忆
func (s *Store) LoadLongTerm() (string, error) {
	// 获取长记忆文件路径，读取内容
	data, err := os.ReadFile(s.LongTermPath())
	// 如果文件不存在，说明当前还没有长期记忆
	// 长期记忆内容返回空
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	// 以字符串形式返回
	return string(data), err
}

// 将指定session的数据保存到磁盘
func (s *Store) SaveSession(sessionID string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 根据sessionID获取对应会话路径
	path := s.SessionPath(sessionID)
	// 确保目标文件路径存在
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// 覆盖写 更新数据
	return os.WriteFile(path, data, 0o600)
}

// 将指定session的摘要保存到磁盘
func (s *Store) SaveSummary(sessionID, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 获取路径
	path := s.SummaryPath(sessionID)
	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// 覆盖写
	return os.WriteFile(path, []byte(summary), 0o600)
}

// 根据sessionID读取对应的摘要文件
func (s *Store) LoadSummary(sessionID string) (string, error) {
	// 获取路径，读取内容
	data, err := os.ReadFile(s.SummaryPath(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// 将sessionID转换成适合拼接到文件路径中的名称
func safeName(value string) string {
	// 路径最后一个组成部分
	value = filepath.Base(value)
	// 处理所有的连续的两个点
	value = strings.ReplaceAll(value, "..", "")
	// 处理后如果是空串
	// 返回default作为默认文件名
	if value == "" || value == "." || value == string(filepath.Separator) {
		return "default"
	}
	return value
}
