// ===============MCP客户端管理===================
// 管理多个MCP server，负责连接 断开 重连，并把mcpserver提供的工具注册到工具仓库里面
package mcpclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"codeagent/internal/tools"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go/v3"
)

// 正则表达式
var (
	invalidName   = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	validServerID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,24}$`)
)

// MCP配置文件结构
// 一个配置文件可以配置多个mcpserver
type ConfigFile struct {
	Servers []ServerConfig `json:"servers"`
}

// 单个MCPserver配置
type ServerConfig struct {
	ID        string            `json:"id"`        // 唯一标识
	Enabled   bool              `json:"enabled"`   // 是否启用
	Transport string            `json:"transport"` // 通信传输方式
	Command   string            `json:"command"`   // 启动命令
	Args      []string          `json:"args"`      // 启动参数
	Env       map[string]string `json:"env"`       // 环境变量
	WorkDir   string            `json:"work_dir"`  // 启动时的工作目录
}

// MCPserver的当前配置和连接状态
type ServerInfo struct {
	ID        string `json:"id"`
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"` // 是否建立了连接
	Transport string `json:"transport"`
	Command   string `json:"command"`
}

// 已经连接的
type connectedServer struct {
	config  ServerConfig       // server的配置信息
	session *mcp.ClientSession // MCPclient和server之间建立的会话
}

// 图片分析接口
type ImageAnalyzer interface {
	Analyze(ctx context.Context, imageData []byte, mimeType, question string) (string, error)
}

// MCP管理器
type Manager struct {
	opMu     sync.Mutex                  // 普通互斥锁，串行化连接、断开和重连，避免工具注册状态竞争
	mu       sync.RWMutex                // 读写锁 保护内部共享数据
	servers  map[string]*connectedServer // serverid：已经连接的mcpserver
	configs  map[string]ServerConfig     // 已经配置的server
	registry *tools.Registry             // mcp工具仓库
	vision   ImageAnalyzer               // 图片分析器
}

// 创建一个MCP管理器
func NewManager() *Manager {
	return &Manager{
		servers: make(map[string]*connectedServer),
		configs: make(map[string]ServerConfig),
	}
}

// 设置图片分析器
func (m *Manager) SetImageAnalyzer(analyzer ImageAnalyzer) {
	m.mu.Lock()
	m.vision = analyzer
	m.mu.Unlock()
}

// 加载MCP配置文件
func LoadConfig(path string) (ConfigFile, error) {
	// 读取mcp配置文件
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ConfigFile{}, nil
	}
	if err != nil {
		return ConfigFile{}, err
	}
	// 反序列化成配置切片
	var cfg ConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ConfigFile{}, err
	}
	return cfg, nil
}

// SaveConfig 以原子方式持久化 MCP 配置
// 在 Windows 上，文件会继承父目录的
// 访问控制列表；在支持 Unix 文件权限的平台上文件会以仅所有者可访问的严格权限创建
func SaveConfig(path string, cfg ConfigFile) error {
	// 检查路径参数
	if strings.TrimSpace(path) == "" {
		return errors.New("MCP 配置路径不能为空")
	}
	// 序列化，美化版
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 获取文件父目录，如果不存在就创建
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// 创建临时文件
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcp-config-*.tmp")
	if err != nil {
		return err
	}
	// 保存临时文件名称
	tmpName := tmp.Name()
	// 函数返回前尝试删除临时文件
	defer os.Remove(tmpName)
	// 给Linux 设置文件权限
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	// 把数据写入临时文件
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// 关闭临时文件
	if err := tmp.Close(); err != nil {
		return err
	}
	// 把临时文件重命名来替换最终配置文件
	return os.Rename(tmpName, path)
}

// 连接已经启用的mcpserver
// ConnectAll 为每个已启用的 Server 创建独立的 stdio 客户端会话
func (m *Manager) ConnectAll(ctx context.Context, configs []ServerConfig, registry *tools.Registry) error {
	// 检查工具注册表是否初始化
	if registry == nil {
		return errors.New("工具注册表未初始化")
	}
	m.mu.Lock()
	// 保存工具注册表
	// 后面注册工具需要用到它
	m.registry = registry
	// 遍历所有的mcpserver配置
	for _, cfg := range configs {
		if _, exists := m.configs[cfg.ID]; exists {
			m.mu.Unlock()
			return fmt.Errorf("MCP Server ID 重复: %s", cfg.ID)
		}
		// 把配置副本保存到配置表中
		m.configs[cfg.ID] = cloneConfig(cfg)
	}
	m.mu.Unlock()
	// 再遍历所有mcpserver配置
	var connectErrors []error
	for _, cfg := range configs {
		// 如果当前server没有启用，就跳过，不建立连接
		if !cfg.Enabled {
			continue
		}
		// 如果启用，就尝试建立连接
		if err := m.ConnectServer(ctx, cfg); err != nil {
			// 保存此次连接失败
			connectErrors = append(connectErrors, err)
		}
	}
	// 将所有连接错误合并后返回
	return errors.Join(connectErrors...)
}

// ConnectServer 启动并初始化一个 MCP Server，然后注册它提供的工具
// 并发操作的连接控制
func (m *Manager) ConnectServer(ctx context.Context, cfg ServerConfig) error {
	// 并发控制
	m.opMu.Lock()
	defer m.opMu.Unlock()
	// 调用具体业务逻辑函数
	return m.connectServer(ctx, cfg)
}

// 一个mcpserver具体的连接 初始化 工具注册
func (m *Manager) connectServer(ctx context.Context, cfg ServerConfig) error {
	if ctx == nil {
		return errors.New("MCP 上下文不能为空")
	}
	// 根据前面设置的serverid正则表达式
	// 检查传入的serverid是否合法
	// 检查启动命令是否为空
	if !validServerID.MatchString(cfg.ID) || strings.TrimSpace(cfg.Command) == "" {
		return fmt.Errorf("MCP Server 配置无效: id=%q,command 不能为空", cfg.ID)
	}
	// 检查传输方式 并规范化
	transport := strings.ToLower(strings.TrimSpace(cfg.Transport))
	// 默认使用stdio通信方式
	if transport == "" {
		transport = "stdio"
	}
	// 暂不支持其他的通信方式
	if transport != "stdio" {
		return fmt.Errorf("MCP Server %q 的 transport %q 尚未实现；当前支持 stdio", cfg.ID, transport)
	}

	m.mu.RLock()
	// 根据id获检查该mcpserver是否已经连接
	_, connected := m.servers[cfg.ID]
	// 获取工具列表
	registry := m.registry
	m.mu.RUnlock()
	// 当前mcpserver已经连接，不重复连接
	if connected {
		return fmt.Errorf("MCP Server %q 已连接", cfg.ID)
	}
	// 工具注册表没有初始化
	if registry == nil {
		return errors.New("MCP 工具注册表未初始化")
	}
	// 启动命令和参数 cmd对象
	cmd := exec.Command(cfg.Command, cfg.Args...)
	// cmd执行目录
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	// 当前Go环境变量
	// mcpserver配置中 额外指定的环境变量
	// 两个环境变量合并得到cmd环境变量
	if len(cfg.Env) > 0 {
		cmd.Env = mergeEnvironment(os.Environ(), cfg.Env)
	}
	// 创建mcp客户端
	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "code-agent",
			Version: "0.2.0",
		},
		nil,
	)
	// 客户端服务端建立连接 得到mcp会话对象
	session, err := client.Connect(
		ctx,
		&mcp.CommandTransport{Command: cmd},
		nil,
	)
	// 建立连接失败
	if err != nil {
		return fmt.Errorf("连接 MCP Server %s 失败: %w", cfg.ID, err)
	}
	// 把该mcpserver的工具注册到工具仓库里面
	if err := m.registerServerTools(ctx, cfg.ID, session, registry); err != nil {
		// 注册工具失败
		// 关闭会话
		_ = session.Close()
		// 删除已经注册一半的此次server工具
		registry.RemovePrefix("mcp__" + cfg.ID + "__")
		return fmt.Errorf("注册 MCP Server %s 的工具失败: %w", cfg.ID, err)
	}
	// 再次检查该mcpserver是否连接
	m.mu.Lock()
	if _, exists := m.servers[cfg.ID]; exists {
		// 如果断开连接
		m.mu.Unlock()
		// 关闭mcp会话
		_ = session.Close()
		// 删除注册的工具
		registry.RemovePrefix("mcp__" + cfg.ID + "__")
		return fmt.Errorf("MCP Server %q 在连接期间已被其他操作连接", cfg.ID)
	}
	// 仍然连接
	// 打卡启用状态
	cfg.Enabled = true
	// 更新已经成功连接的server
	m.servers[cfg.ID] = &connectedServer{
		config:  cloneConfig(cfg),
		session: session,
	}
	// 更新已经配置的mcpserver
	m.configs[cfg.ID] = cloneConfig(cfg)
	m.mu.Unlock()
	return nil
}

// DisconnectServer 关闭指定会话，并移除该 Server 注册的全部工具
func (m *Manager) DisconnectServer(serverID string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.disconnectServer(serverID)
}

func (m *Manager) disconnectServer(serverID string) error {
	m.mu.Lock()
	// 根据id找到连接信息
	server, ok := m.servers[serverID]
	// m.servers中删除对应连接
	if ok {
		delete(m.servers, serverID)
	}
	// 更新配置状态
	if cfg, exists := m.configs[serverID]; exists {
		// 未启用
		cfg.Enabled = false
		m.configs[serverID] = cfg
	}
	// 获取工具仓库
	registry := m.registry
	m.mu.Unlock()
	// 该server没有连接
	if !ok {
		return fmt.Errorf("MCP Server %q 当前未连接", serverID)
	}
	// 删除对应连接的mcp工具
	if registry != nil {
		registry.RemovePrefix("mcp__" + serverID + "__")
	}
	// 关闭mcp会话
	return server.session.Close()
}

// ReconnectServer 根据 Server ID 重启此前已经配置的 MCP Server。
func (m *Manager) ReconnectServer(ctx context.Context, serverID string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.RLock()
	cfg, ok := m.configs[serverID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("MCP Server %q 没有配置", serverID)
	}
	if _, connected := m.connected(serverID); connected {
		if err := m.disconnectServer(serverID); err != nil {
			return err
		}
	}
	cfg.Enabled = true
	return m.connectServer(ctx, cfg)
}

func (m *Manager) UpsertConfig(cfg ServerConfig) error {
	if !validServerID.MatchString(cfg.ID) || strings.TrimSpace(cfg.Command) == "" {
		return errors.New("MCP Server 配置无效")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.configs[cfg.ID]; exists {
		return fmt.Errorf("MCP Server ID %q 已经配置；请使用 reconnect_mcp_server，或选择新的 ID", cfg.ID)
	}
	m.configs[cfg.ID] = cloneConfig(cfg)
	return nil
}

func (m *Manager) ConfigSnapshot() ConfigFile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := ConfigFile{Servers: make([]ServerConfig, 0, len(m.configs))}
	for _, cfg := range m.configs {
		result.Servers = append(result.Servers, cloneConfig(cfg))
	}
	sort.Slice(result.Servers, func(i, j int) bool { return result.Servers[i].ID < result.Servers[j].ID })
	return result
}

func (m *Manager) ListServers() []ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ServerInfo, 0, len(m.configs))
	for id, cfg := range m.configs {
		_, connected := m.servers[id]
		result = append(result, ServerInfo{ID: id, Enabled: cfg.Enabled, Connected: connected, Transport: cfg.Transport, Command: cfg.Command})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (m *Manager) connected(id string) (*connectedServer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	server, ok := m.servers[id]
	return server, ok
}

func (m *Manager) registerServerTools(ctx context.Context, serverID string, session *mcp.ClientSession, registry *tools.Registry) error {
	cursor := ""
	for {
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return err
		}
		for _, remote := range result.Tools {
			if remote == nil {
				continue
			}
			name := namespacedToolName(serverID, remote.Name)
			if name == "mcp__"+serverID+"__" || len(name) > 64 {
				return fmt.Errorf("MCP 工具名称无效或超过 64 个字符: server=%s tool=%q", serverID, remote.Name)
			}
			params := openai.FunctionParameters{"type": "object", "properties": map[string]any{}}
			if remote.InputSchema != nil {
				data, err := json.Marshal(remote.InputSchema)
				if err != nil {
					return fmt.Errorf("序列化工具 %s schema: %w", remote.Name, err)
				}
				if err := json.Unmarshal(data, &params); err != nil {
					return fmt.Errorf("解析工具 %s schema: %w", remote.Name, err)
				}
			}

			remoteName := remote.Name
			remoteSession := session
			description := remote.Description
			if description == "" {
				description = "MCP Server " + serverID + " 提供的工具"
			}
			// MCP 工具默认要求审批。只有 Server 明确声明只读时才自动执行；
			// 未提供只读标记的工具继续采用保守策略。
			requiresApproval := true
			if remote.Annotations != nil && remote.Annotations.ReadOnlyHint {
				requiresApproval = false
			}
			definition := &tools.ToolDef{
				Name: name, Description: "[MCP: " + serverID + "] " + description,
				Params: params, RequiresApproval: requiresApproval,
				Fn: func(callCtx context.Context, args map[string]any) (string, error) {
					callResult, err := remoteSession.CallTool(callCtx, &mcp.CallToolParams{Name: remoteName, Arguments: args})
					if err != nil {
						return "", err
					}
					encoded, err := json.Marshal(callResult)
					if err != nil {
						return "", err
					}
					resultText := string(redactImagePayloads(encoded))
					m.mu.RLock()
					analyzer := m.vision
					m.mu.RUnlock()
					if analyzer != nil {
						analyses, imageErr := analyzeMCPImages(callCtx, analyzer, encoded)
						if imageErr != nil {
							resultText += "\n\n视觉分析失败：" + imageErr.Error()
						} else if len(analyses) > 0 {
							resultText += "\n\n截图视觉分析（图片已作为 input_image 发送给视觉模型）：\n" + strings.Join(analyses, "\n\n")
						}
					} else if strings.Contains(resultText, "image omitted") {
						resultText += "\n视觉模型未配置，未执行图片分析。"
					}
					return resultText, nil
				},
			}
			if err := registry.Register(definition); err != nil {
				return err
			}
		}
		if result.NextCursor == "" {
			return nil
		}
		cursor = result.NextCursor
	}
}

func redactImagePayloads(encoded []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return []byte("MCP 工具返回了无法解析的结果")
	}
	content, ok := payload["content"].([]any)
	if !ok {
		return encoded
	}
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok || block["type"] != "image" {
			continue
		}
		if _, exists := block["data"]; exists {
			block["data"] = "[image omitted]"
		}
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return []byte("MCP 图片结果已隐藏，无法序列化其余字段")
	}
	return result
}

func analyzeMCPImages(ctx context.Context, analyzer ImageAnalyzer, encoded []byte) ([]string, error) {
	var payload struct {
		Content []struct {
			Type        string `json:"type"`
			Data        string `json:"data"`
			MIMEType    string `json:"mimeType"`
			MIMETypeAlt string `json:"mime_type"`
		} `json:"content"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}
	var analyses []string
	for index, item := range payload.Content {
		if item.Type != "image" || item.Data == "" {
			continue
		}
		mimeType := item.MIMEType
		if mimeType == "" {
			mimeType = item.MIMETypeAlt
		}
		if mimeType == "" {
			mimeType = "image/png"
		}
		imageData, err := base64.StdEncoding.DecodeString(item.Data)
		if err != nil {
			return analyses, fmt.Errorf("解码 MCP 图片 %d 失败: %w", index+1, err)
		}
		analysis, err := analyzer.Analyze(ctx, imageData, mimeType, "请检查这张浏览器截图中的页面视觉效果，指出布局、间距、对齐、字体、颜色、响应式和明显交互状态问题，并给出可执行的修改建议。")
		if err != nil {
			return analyses, fmt.Errorf("分析 MCP 图片 %d 失败: %w", index+1, err)
		}
		analyses = append(analyses, analysis)
	}
	return analyses, nil
}

func (m *Manager) Close() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	servers := make([]*connectedServer, 0, len(m.servers))
	for _, server := range m.servers {
		servers = append(servers, server)
	}
	m.servers = make(map[string]*connectedServer)
	registry := m.registry
	m.mu.Unlock()

	if registry != nil {
		registry.RemovePrefix("mcp__")
	}
	var firstErr error
	for _, server := range servers {
		if err := server.session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func cloneConfig(cfg ServerConfig) ServerConfig {
	cfg.Args = append([]string(nil), cfg.Args...)
	if cfg.Env != nil {
		env := make(map[string]string, len(cfg.Env))
		for key, value := range cfg.Env {
			env[key] = value
		}
		cfg.Env = env
	}
	return cfg
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	keys := make(map[string]string, len(base)+len(overrides))
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		normalized := strings.ToUpper(key)
		keys[normalized], values[normalized] = key, value
	}
	for key, value := range overrides {
		if key == "" || strings.Contains(key, "=") {
			continue
		}
		normalized := strings.ToUpper(key)
		keys[normalized], values[normalized] = key, value
	}
	result := make([]string, 0, len(values))
	for normalized, value := range values {
		result = append(result, keys[normalized]+"="+value)
	}
	return result
}

func namespacedToolName(serverID, toolName string) string {
	serverPart := strings.Trim(invalidName.ReplaceAllString(serverID, "_"), "_")
	toolPart := strings.Trim(invalidName.ReplaceAllString(toolName, "_"), "_")
	return "mcp__" + serverPart + "__" + toolPart
}
