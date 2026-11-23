package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/openai/openai-go/v3/responses"
)

// 历史记录
type HistoryEntry struct {
	Role      string    `json:"role"`       // 角色(用户/大模型/工具调用/工具结果···)
	Content   string    `json:"content"`    // 内容
	CreatedAt time.Time `json:"created_at"` // 产生时间
}

// 给模型提供的上下文
type Session struct {
	ID       string                       // 会话的唯一标识
	Messages responses.ResponseInputParam // 上下文
	Len      int                          // 当前messages中的消息数量
	Summary  string                       //历史对话的摘要
	History  []HistoryEntry               // 对话历史记录
	mu       sync.RWMutex
}

// 要持久化的session内容
type sessionDisk struct {
	ID       string                       `json:"id"`
	Messages responses.ResponseInputParam `json:"messages"`
	Len      int                          `json:"len"`
	Summary  string                       `json:"summary,omitempty"`
	History  []HistoryEntry               `json:"history,omitempty"`
}

// 创建会话
func NewSession(id, systemPrompt string) *Session {
	// 如果创建session没有传入id，就使用当前时间生成一个id
	if id == "" {
		id = time.Now().Format("20060102-150405.000000")
	}
	// 创建会话
	s := &Session{ID: id}
	// 如果是第一次创建会话，有系统提示词
	// 就加入到会话的msgs里面
	if systemPrompt != "" {
		s.Messages = append(s.Messages,
			responses.ResponseInputItemParamOfMessage(
				systemPrompt,
				responses.EasyInputMessageRoleSystem,
			),
		)
	}
	return s
}

// 根据文件路径，从磁盘中拿出历史的session
func LoadSession(path string) (*Session, error) {
	// 读取指定路径的文件
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// 将历史session数据反序列化
	var disk sessionDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, err
	}
	// 返回session
	return &Session{
		ID:       disk.ID,
		Messages: disk.Messages,
		Len:      disk.Len,
		Summary:  disk.Summary,
		History:  disk.History,
	}, nil
}

// 获取当前session的模型上下文
func (s *Session) GetMessages() responses.ResponseInputParam {
	s.mu.RLock()
	defer s.mu.RUnlock()
	copyMessages := make(responses.ResponseInputParam, len(s.Messages))
	copy(copyMessages, s.Messages)
	return copyMessages
}

// 给模型添加上下文
func (s *Session) AddMessage(item ...responses.ResponseInputItemUnionParam) {
	// 锁
	s.mu.Lock()
	defer s.mu.Unlock()
	// 同时添加多条msg
	s.Messages = append(s.Messages, item...)
	s.Len += len(item)
}

// 添加系统角色信息
func (s *Session) AddSystemNote(text string) {
	s.AddMessage(responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleSystem))
}

// 添加一条对话历史
func (s *Session) AddHistory(role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.History = append(s.History, HistoryEntry{
		Role:      role,
		Content:   content,
		CreatedAt: time.Now(),
	})
}

// 将session中的历史记录转化为普通文本
func (s *Session) HistoryText() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out string
	// 遍历s.History切片
	for _, item := range s.History {
		// 按照"[角色]内容"的格式拼接历史记录
		out += "[" + item.Role + "] " + item.Content + "\n"
	}
	return out
}

// 将数据写入磁盘文件，达到持久化目的
func (s *Session) Save(path string) error {
	s.mu.RLock()
	// 把session中的除了锁这个字段，其他都保存在磁盘中
	disk := sessionDisk{
		ID:       s.ID,
		Messages: s.Messages,
		Len:      s.Len,
		Summary:  s.Summary,
		History:  append([]HistoryEntry(nil), s.History...),
	}
	s.mu.RUnlock()
	// 把session数据转换成json
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	// 如果目录不存在就创建
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// 写入磁盘
	return os.WriteFile(path, data, 0o600)
}

// 把原来的上下文替换成系统提示词和历史摘要
func (s *Session) Compact(systemPrompt, summary string) {
	// 锁
	s.mu.Lock()
	defer s.mu.Unlock()
	// 更新摘要
	s.Summary = summary
	// 更新上下文：变成系统提示词+摘要
	s.Messages = responses.ResponseInputParam{
		responses.ResponseInputItemParamOfMessage(systemPrompt, responses.EasyInputMessageRoleSystem),
		responses.ResponseInputItemParamOfMessage("之前对话的摘要：\n"+summary, responses.EasyInputMessageRoleSystem),
	}
	// 更新消息数量
	s.Len = len(s.Messages)
	// 清空历史记录
	s.History = nil
}
