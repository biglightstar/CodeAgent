package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrRejected = errors.New("用户拒绝了本次操作")
	ErrExpired  = errors.New("审批请求已过期")
	ErrMissing  = errors.New("审批请求不存在或已处理")
)

type Decision string

const (
	DecisionApproved Decision = "approved"
	DecisionRejected Decision = "rejected"
)

type Request struct {
	ID          string    `json:"id"`
	ToolName    string    `json:"tool_name"`
	Description string    `json:"description"`
	Arguments   string    `json:"arguments"`
	Workspace   string    `json:"workspace"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type pending struct {
	request Request
	result  chan Decision
}

type Manager struct {
	mu       sync.Mutex
	pending  map[string]*pending
	requests chan Request
	seq      uint64
	ttl      time.Duration
}

func NewManager(buffer int, ttl time.Duration) *Manager {
	if buffer < 1 {
		buffer = 16
	}
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &Manager{
		pending:  make(map[string]*pending),
		requests: make(chan Request, buffer),
		ttl:      ttl,
	}
}

func (m *Manager) Requests() <-chan Request { return m.requests }

// Ask 会阻塞当前工具调用，直到 TUI 对这条不可变的审批请求作出决定。
func (m *Manager) Ask(ctx context.Context, req Request) error {
	if ctx == nil {
		return errors.New("审批上下文不能为空")
	}
	if req.ToolName == "" {
		return errors.New("审批请求缺少工具名称")
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	if req.ExpiresAt.IsZero() {
		req.ExpiresAt = req.CreatedAt.Add(m.ttl)
	}
	if req.Fingerprint == "" {
		req.Fingerprint = fingerprint(req.ToolName, req.Arguments, req.Workspace)
	}

	m.mu.Lock()
	m.seq++
	req.ID = fmt.Sprintf("approval-%06d", m.seq)
	p := &pending{request: req, result: make(chan Decision, 1)}
	m.pending[req.ID] = p
	m.mu.Unlock()

	select {
	case m.requests <- req:
	case <-ctx.Done():
		m.remove(req.ID)
		return ctx.Err()
	}

	timer := time.NewTimer(time.Until(req.ExpiresAt))
	defer timer.Stop()
	select {
	case decision := <-p.result:
		if time.Now().After(req.ExpiresAt) {
			return ErrExpired
		}
		if decision != DecisionApproved {
			return ErrRejected
		}
		// 原始工具名称、参数和工作区信息保留在服务端。
		// 界面只能根据请求 ID 提交审批结果，不能替换原始参数。
		if req.Fingerprint != fingerprint(req.ToolName, req.Arguments, req.Workspace) {
			return errors.New("审批请求指纹校验失败")
		}
		return nil
	case <-timer.C:
		m.remove(req.ID)
		return ErrExpired
	case <-ctx.Done():
		m.remove(req.ID)
		return ctx.Err()
	}
}

func (m *Manager) Resolve(id string, decision Decision) error {
	if decision != DecisionApproved && decision != DecisionRejected {
		return errors.New("无效的审批决定")
	}
	m.mu.Lock()
	p, ok := m.pending[id]
	if !ok {
		m.mu.Unlock()
		return ErrMissing
	}
	if time.Now().After(p.request.ExpiresAt) {
		delete(m.pending, id)
		m.mu.Unlock()
		return ErrExpired
	}
	delete(m.pending, id)
	m.mu.Unlock()
	p.result <- decision
	return nil
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.pending, id)
	m.mu.Unlock()
}

func PreviewArguments(raw string, max int) string {
	if max < 80 {
		max = 80
	}
	var pretty any
	if json.Unmarshal([]byte(raw), &pretty) == nil {
		if data, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			raw = string(data)
		}
	}
	if len(raw) > max {
		return raw[:max] + "…（已截断）"
	}
	return raw
}

func fingerprint(toolName, args, workspace string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{toolName, args, workspace}, "\n")))
	return hex.EncodeToString(sum[:])
}
