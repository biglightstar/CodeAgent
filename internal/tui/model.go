package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"codeagent/internal/approval"
	"codeagent/internal/core"
	"codeagent/internal/shell"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	globalMDRenderer *glamour.TermRenderer
	globalMDWidth    int
)

// markdownRender 把助手回复中的 Markdown 文本渲染成带 ANSI 样式的终端输出。
// 按终端宽度缓存渲染器，仅在宽度变化时重建，避免每次按键都重新构建。
func markdownRender(content string, width int) string {
	if globalMDRenderer == nil || globalMDWidth != width {
		r, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(width))
		if err != nil {
			return content
		}
		globalMDRenderer = r
		globalMDWidth = width
	}
	out, err := globalMDRenderer.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimRight(out, "\n")
}

type Model struct {
	agent         *core.Agent
	approvals     *approval.Manager
	ctx           context.Context
	input         []rune
	cursor        int
	messages      []string
	pending       *approval.Request
	busy          bool
	lastError     string
	processEvents <-chan shell.ProcessEvent
	width         int
	height        int
	scroll        int // 距离消息底部的行数；0 表示跟随最新消息
	spinnerFrame  int
}

type approvalMsg approval.Request
type agentResultMsg struct {
	text string
	err  error
}
type processEventMsg shell.ProcessEvent

type resolveMsg struct {
	ID       string
	Decision approval.Decision
	Manager  *approval.Manager
}

type spinnerTickMsg struct{}

func spinnerTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

func New(agent *core.Agent, approvals *approval.Manager, ctx context.Context, processEvents ...<-chan shell.ProcessEvent) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	m := Model{agent: agent, approvals: approvals, ctx: ctx, width: 80, height: 24}
	if len(processEvents) > 0 {
		m.processEvents = processEvents[0]
	}
	return m
}

func (m Model) Init() tea.Cmd {
	commands := []tea.Cmd{waitApproval(m.approvals.Requests())}
	if m.processEvents != nil {
		commands = append(commands, waitProcessEvent(m.processEvents))
	}
	return tea.Batch(commands...)
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.width < 40 {
			m.width = 40
		}
		if m.height < 10 {
			m.height = 10
		}
		return m, nil
	case approvalMsg:
		req := approval.Request(msg)
		m.pending = &req
		return m, waitApproval(m.approvals.Requests())
	case resolveMsg:
		if err := msg.Manager.Resolve(msg.ID, msg.Decision); err != nil {
			m.lastError = "审批处理失败：" + err.Error()
		} else if msg.Decision == approval.DecisionApproved {
			m.messages = append(m.messages, "系统：已批准本次操作")
		} else {
			m.messages = append(m.messages, "系统：已拒绝本次操作")
		}
		m.pending = nil
		return m, nil
	case spinnerTickMsg:
		if !m.busy {
			return m, nil
		}
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		return m, spinnerTick()
	case processEventMsg:
		event := shell.ProcessEvent(msg)
		if event.Type == shell.ProcessEventStarted {
			m.messages = append(m.messages, "系统：已启动一项后台任务")
		} else {
			line := "系统：后台任务" + processStatusLabel(event.Status)
			if event.ExitCode != 0 {
				line += fmt.Sprintf("（退出码 %d）", event.ExitCode)
			}
			if event.Error != "" {
				line += "：" + event.Error
			}
			m.messages = append(m.messages, line)
		}
		return m, waitProcessEvent(m.processEvents)
	case agentResultMsg:
		m.busy = false
		if msg.err != nil {
			m.lastError = msg.err.Error()
			m.messages = append(m.messages, "助手：执行遇到问题："+msg.err.Error())
		} else {
			m.lastError = ""
			m.messages = append(m.messages, "助手："+msg.text)
		}
		return m, nil
	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonWheelUp && msg.Action == tea.MouseActionPress {
			m.scroll += 3
			m.clampScroll()
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown && msg.Action == tea.MouseActionPress {
			m.scroll -= 3
			m.clampScroll()
			return m, nil
		}
		if m.pending != nil && msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			if msg.Y == m.approvalButtonY() {
				if decision, ok := approvalDecisionAt(msg.X); ok {
					return m, m.resolvePending(decision)
				}
			}
		}
		return m, nil
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		if m.pending != nil {
			switch msg.Type {
			case tea.KeyRunes:
				v := strings.ToLower(string(msg.Runes))
				if v == "y" {
					return m, m.resolvePending(approval.DecisionApproved)
				}
				if v == "n" {
					return m, m.resolvePending(approval.DecisionRejected)
				}
			case tea.KeyEnter:
				text := strings.TrimSpace(string(m.input))
				m.input = nil
				m.cursor = 0
				if isApproval(text) {
					return m, m.resolvePending(approval.DecisionApproved)
				}
				if isRejection(text) {
					return m, m.resolvePending(approval.DecisionRejected)
				}
				m.lastError = "当前有待审批操作，请按 Y 批准或 N 拒绝。"
			}
			return m, nil
		}
		switch msg.Type {
		case tea.KeyEnter:
			text := strings.TrimSpace(string(m.input))
			m.input = nil
			m.cursor = 0
			if text == "" || m.busy {
				return m, nil
			}
			if text == "/quit" || text == "/exit" {
				return m, tea.Quit
			}
			m.messages = append(m.messages, "你："+text)
			m.scroll = 0
			m.busy = true
			m.spinnerFrame = 0
			return m, tea.Batch(runAgent(m.ctx, m.agent, text), spinnerTick())
		case tea.KeyBackspace:
			if m.cursor > 0 {
				m.input = append(m.input[:m.cursor-1], m.input[m.cursor:]...)
				m.cursor--
			}
		case tea.KeyDelete:
			if m.cursor < len(m.input) {
				m.input = append(m.input[:m.cursor], m.input[m.cursor+1:]...)
			}
		case tea.KeyLeft:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyRight:
			if m.cursor < len(m.input) {
				m.cursor++
			}
		case tea.KeyHome:
			m.cursor = 0
		case tea.KeyEnd:
			m.cursor = len(m.input)
		case tea.KeyUp:
			m.scroll += 3
			m.clampScroll()
		case tea.KeyDown:
			m.scroll -= 3
			m.clampScroll()
		case tea.KeyRunes:
			runes := msg.Runes
			m.input = append(m.input[:m.cursor], append(runes, m.input[m.cursor:]...)...)
			m.cursor += len(runes)
		case tea.KeySpace:
			m.input = append(m.input[:m.cursor], append([]rune{' '}, m.input[m.cursor:]...)...)
			m.cursor++
		}
	}
	return m, nil
}

func (m Model) View() string {
	blue := lipgloss.Color("#2563EB")
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7CC7FF")).Render("Code Agent · 交互终端")
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	var b strings.Builder
	b.WriteString(title + "\n")
	b.WriteString(muted.Render(strings.Repeat("─", maxInt(20, m.width-2))) + "\n")

	available := m.height - 7
	if m.pending != nil {
		available -= 6
	}
	if available < 3 {
		available = 3
	}
	lines := m.renderedMessages()
	end := len(lines) - m.scroll
	if end < 0 {
		end = 0
	}
	start := end - available
	if start < 0 {
		start = 0
	}
	if len(lines) == 0 {
		shown := []string{"欢迎使用 Code Agent。描述你希望完成的开发任务。"}
		for i := len(shown); i < available; i++ {
			b.WriteString("\n")
		}
		for _, line := range shown {
			b.WriteString(line + "\n")
		}
	} else {
		shown := lines[start:end]
		for i := len(shown); i < available; i++ {
			b.WriteString("\n")
		}
		for _, line := range shown {
			b.WriteString(line + "\n")
		}
	}
	if m.scroll > 0 {
		b.WriteString(muted.Render("↑ 正在查看历史消息 · 滚轮向下回到底部") + "\n")
	}
	if m.busy {
		spinner := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#E6B450")).Render(spinner+" 正在处理任务…") + "\n")
	}
	if m.pending != nil {
		b.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E6B450")).Render("需要你的确认") + "\n")
		b.WriteString("操作：" + approvalActionLabel(m.pending.ToolName, m.pending.Description) + "\n")
		if target := approvalTarget(m.pending.Arguments); target != "" {
			b.WriteString("目标：" + target + "\n")
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#8BD5A6")).Bold(true).Render("[ Y 批准 ]") + "    " + lipgloss.NewStyle().Foreground(lipgloss.Color("#F28B82")).Bold(true).Render("[ N 拒绝 ]") + "\n")
	}
	if m.lastError != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#F28B82")).Render(m.lastError) + "\n")
	}

	prompt := lipgloss.NewStyle().Foreground(lipgloss.Color("#7CC7FF")).Bold(true).Render("> ")
	input := string(m.input)
	if m.cursor >= 0 && m.cursor <= len(m.input) {
		before, after := string(m.input[:m.cursor]), string(m.input[m.cursor:])
		cursor := " "
		if m.cursor < len(m.input) {
			cursor = string(m.input[m.cursor])
		}
		cursor = lipgloss.NewStyle().Reverse(true).Render(cursor)
		input = before + cursor + after
	}
	inputStyle := lipgloss.NewStyle().Width(maxInt(10, m.width-4)).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(blue)
	b.WriteString("\n" + inputStyle.Render(prompt+input) + "\n")
	b.WriteString(muted.Render("Enter 发送 · ↑/↓ 或鼠标滚轮查看历史 · Ctrl+C 退出 · 审批时按 Y/N"))
	return b.String()
}

func (m Model) renderedMessages() []string {
	var lines []string
	maxWidth := m.width - 8
	if maxWidth < 24 {
		maxWidth = 24
	}
	for _, message := range m.messages {
		var block string
		if strings.HasPrefix(message, "你：") {
			content := strings.TrimPrefix(message, "你：")
			style := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#2563EB")).Padding(0, 1).MaxWidth(maxWidth)
			block = style.Render(content)
		} else if strings.HasPrefix(message, "助手：") {
			content := strings.TrimPrefix(message, "助手：")
			rendered := markdownRender(content, maxWidth)
			block = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render("助手") + "\n" + rendered
		} else {
			block = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(message)
		}
		lines = append(lines, strings.Split(block, "\n")...)
	}
	return lines
}

func (m *Model) clampScroll() {
	maxScroll := len(m.renderedMessages()) - 1
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
}

func approvalActionLabel(toolName, description string) string {
	switch toolName {
	case "exec_shell", "start_shell_task":
		return "在工作区执行命令"
	case "cancel_shell_task":
		return "取消后台任务"
	case "switch_workspace":
		return "切换工作区"
	case "save_long_term_memory":
		return "保存长期记忆"
	case "connect_mcp_server":
		return "连接外部工具服务"
	case "disconnect_mcp_server":
		return "断开外部工具服务"
	case "reconnect_mcp_server":
		return "重新连接外部工具服务"
	default:
		if strings.TrimSpace(description) != "" {
			return description
		}
		return "执行一项需要确认的操作"
	}
}

func approvalTarget(raw string) string {
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "workdir", "command", "content"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			value = strings.TrimSpace(value)
			if key == "command" && utf8.RuneCountInString(value) > 90 {
				value = string([]rune(value)[:90]) + "…"
			}
			if key == "content" {
				return "将写入内容（详情已隐藏）"
			}
			return value
		}
	}
	return ""
}

func (m Model) approvalButtonY() int {
	lines := strings.Split(m.View(), "\n")
	for i, line := range lines {
		if strings.Contains(line, "Y 批准") && strings.Contains(line, "N 拒绝") {
			return i
		}
	}
	return -1
}

func approvalDecisionAt(x int) (approval.Decision, bool) {
	if x >= 0 && x < 12 {
		return approval.DecisionApproved, true
	}
	if x >= 16 && x < 28 {
		return approval.DecisionRejected, true
	}
	return "", false
}

func (m Model) resolvePending(decision approval.Decision) tea.Cmd {
	id := m.pending.ID
	return func() tea.Msg { return resolveMsg{ID: id, Decision: decision, Manager: m.approvals} }
}

func waitApproval(ch <-chan approval.Request) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return approvalMsg(req)
	}
}
func waitProcessEvent(ch <-chan shell.ProcessEvent) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return nil
		}
		return processEventMsg(event)
	}
}
func runAgent(ctx context.Context, agent *core.Agent, text string) tea.Cmd {
	return func() tea.Msg { result, err := agent.RunTurn(ctx, text); return agentResultMsg{text: result, err: err} }
}
func isApproval(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return lower == "approve" || lower == "yes" || lower == "y" || lower == "通过" || lower == "批准" || lower == "同意"
}
func isRejection(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return lower == "reject" || lower == "no" || lower == "n" || lower == "拒绝" || lower == "不同意"
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func Run(model Model) error {
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := program.Run()
	return err
}

func processStatusLabel(status shell.ProcessStatus) string {
	switch status {
	case shell.ProcessRunning:
		return "运行中"
	case shell.ProcessSucceeded:
		return "已成功"
	case shell.ProcessFailed:
		return "执行失败"
	case shell.ProcessTimedOut:
		return "已超时"
	case shell.ProcessCancelled:
		return "已取消"
	default:
		return "状态未知"
	}
}
