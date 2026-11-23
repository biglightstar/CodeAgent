// ================agent结构体和方法==================
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"codeagent/internal/approval"
	"codeagent/internal/memory"
	"codeagent/internal/tools"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// 一轮对话中最大循环次数
const defaultMaxLoop = 20

// agent配置
type AgentConfig struct {
	APIKey           string // 模型api
	BaseURL          string // 模型服务接口地址，需要兼容openai api服务
	Model            string // 使用的模型名称
	MaxLoop          int    // 最大循环次数
	SummaryThreshold int    // 会话历史达到多少条后尝试生成摘要
}

// agent结构体
type Agent struct {
	client    openai.Client // openai客户端，负责向模型发送请求
	model     string
	maxLoop   int
	registry  *tools.Registry   // 工具箱，保存agent可以使用的工具
	approvals *approval.Manager // 审批管理器
	memory    *memory.Store     // 保存会话和摘要
	session   *Session          // 当前会话，保存模型上下文和对话历史
	system    string            // 系统提示词
	threshold int               // 触发自动摘要的历史记录数量阈值
	mu        sync.Mutex        // 互斥锁，保证用一个agent的runturn不会并发执行
}

// 创建一个agent
// 传入参数：agent配置，工具箱，审批管理器，持久化存储，当前会话，系统提示词
func NewAgent(cfg AgentConfig, registry *tools.Registry, approvals *approval.Manager, store *memory.Store, session *Session, systemPrompt string) (*Agent, error) {
	// 检查
	if cfg.APIKey == "" {
		return nil, errors.New("API Key 不能为空")
	}
	if cfg.Model == "" {
		return nil, errors.New("模型名称不能为空")
	}
	if registry == nil || approvals == nil || store == nil || session == nil {
		return nil, errors.New("Agent 依赖未初始化")
	}
	// 创建配置切片
	// 放入apikey和模型请求地址
	options := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
	}
	if strings.TrimSpace(cfg.BaseURL) != "" {
		options = append(options, option.WithBaseURL(cfg.BaseURL))
	}
	// 根据配置切片，创建openai客户端
	client := openai.NewClient(options...)
	// 最大循环次数
	if cfg.MaxLoop <= 0 {
		cfg.MaxLoop = defaultMaxLoop
	}
	// 摘要阈值
	if cfg.SummaryThreshold <= 0 {
		cfg.SummaryThreshold = 40
	}
	// 返回刚配置好的agent
	return &Agent{
		client:    client,
		model:     cfg.Model,
		maxLoop:   cfg.MaxLoop,
		registry:  registry,
		approvals: approvals,
		memory:    store,
		session:   session,
		system:    systemPrompt,
		threshold: cfg.SummaryThreshold,
	}, nil
}

// agentRun
func (a *Agent) RunTurn(ctx context.Context, userText string) (string, error) {
	// 防止同一个agent同时执行多个runturn，避免多个对话轮次同时修改同一个session
	a.mu.Lock()
	defer a.mu.Unlock()
	// 去除用户输入的首位空格和换行
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return "", errors.New("用户消息不能为空")
	}
	// 添加用户消息到session里面
	a.session.AddMessage(
		responses.ResponseInputItemParamOfMessage(
			userText,
			responses.EasyInputMessageRoleUser,
		),
	)
	// 将用户消息添加到历史记录中
	// 方便持久化和摘要
	a.session.AddHistory("user", userText)
	// 保存当前的session
	_ = a.persistSession()
	// 用于保存模型最终返回的文本
	var finalText string
	// agent开始循环思考：感知 决策 执行
	for loop := 0; loop < a.maxLoop; loop++ {
		// 向模型发送请求
		resp, err := a.client.Responses.New(ctx, responses.ResponseNewParams{
			Input: responses.ResponseNewParamsInputUnion{
				OfInputItemList: a.session.GetMessages(),
			},
			Tools:     a.registry.BuildOpenAITools(), // 拿到openai格式的工具
			Model:     a.model,
			Reasoning: shared.ReasoningParam{Effort: shared.ReasoningEffortNone},
		})
		// 模型请求失败
		if err != nil {
			return "", fmt.Errorf("请求模型失败: %w", err)
		}
		// 初始默认没有调用工具
		hasToolCall := false
		// 遍历模型拿到的结果
		for _, item := range resp.Output {
			// 非模型调用
			if item.Type != "function_call" {
				continue
			}
			// 是模型调用
			hasToolCall = true
			// 把当前输出转换成具体的函数调用结构体
			call := item.AsFunctionCall()
			// 将此次工具调用，添加到session里面
			a.session.AddMessage(responses.ResponseInputItemUnionParam{
				OfFunctionCall: &responses.ResponseFunctionToolCallParam{
					Arguments: call.Arguments,
					CallID:    call.CallID,
					Name:      call.Name,
				},
			})
			// 将工具调用记录到历史记录中
			// PreviewArguments用于生成参数预览，并限制长度是300
			a.session.AddHistory("tool_call", call.Name+" "+approval.PreviewArguments(call.Arguments, 300))
			// 从仓库中拿到工具
			tool, ok := a.registry.Get(call.Name)
			// 定义工具执行结果
			var toolResult string
			// 工具不存在
			if !ok {
				toolResult = "工具不存在: " + call.Name
			} else { // 工具存在
				// 检查是否需要审批
				// 如果需要用户审批
				if tool.RequiresApproval {
					workspace := ""
					// 将工具参数解析成map
					var parsed map[string]any
					_ = jsonUnmarshalArgs(call.Arguments, &parsed)
					// 从工具参数中提取工作目录
					if parsed != nil {
						if v, ok := parsed["workdir"].(string); ok {
							workspace = v
						}
						if v, ok := parsed["path"].(string); ok && workspace == "" {
							workspace = v
						}
					}
					// 请求用户批准本次工具调用
					// 向用户展示工具名字，工具描述，工具参数，工具的工作目录
					err := a.approvals.Ask(ctx, approval.Request{
						ToolName:    call.Name,
						Description: tool.Description,
						Arguments:   call.Arguments,
						Workspace:   workspace,
					})
					// 用户不批准
					if err != nil {
						toolResult = "本次工具调用未获批准，未执行。原因: " + err.Error()
					}
				}
				// 用户通过审批，或者工具不需要审批
				if toolResult == "" {
					// 调用工具
					result, callErr := tool.Call(ctx, call.Arguments)
					if callErr != nil {
						toolResult = "工具执行失败: " + callErr.Error()
					} else {
						toolResult = result
					}
				}
			}
			// 将工具执行结果添加到历史记录里面
			a.session.AddHistory("tool_result", toolResult)
			// 将工具调用信息添加到session里面
			a.session.AddMessage(responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: openai.String(toolResult),
					},
					CallID: call.CallID,
				},
			})
			// 保存当前session
			_ = a.persistSession()
		}
		// 如果没有工具调用，结束循环，本轮对话结束
		if !hasToolCall {
			finalText = resp.OutputText()
			a.session.AddMessage(responses.ResponseInputItemParamOfMessage(finalText, responses.EasyInputMessageRoleAssistant))
			a.session.AddHistory("assistant", finalText)
			_ = a.persistSession()
			break
		}
	}
	// 达到最大循环次数
	if finalText == "" {
		finalText = "本轮工具调用已达到最大循环次数，请继续描述下一步。"
	}
	// 检查当前会话是否需要生成摘要
	if err := a.maybeSummarize(ctx); err != nil {
		// 摘要生成失败不能影响当前轮次已经得到的回答。
		finalText += "\n\n(提示:本轮会话摘要未能自动保存:" + err.Error() + ")"
	}
	_ = a.persistSession()
	// 返回最终回答
	return finalText, nil
}

// 生成会话摘要
func (a *Agent) maybeSummarize(ctx context.Context) error {
	// 历史记录数量没有达到阈值 不生成摘要
	if a.session.Len < a.threshold {
		return nil
	}
	// 生成摘要
	// 获取完整历史记录
	transcript := strings.TrimSpace(a.session.HistoryText())
	// 历史记录为空 不生成摘要
	if transcript == "" {
		return nil
	}
	// 摘要提示词
	prompt := "请将下面的 Code Agent 对话整理为可供后续会话继续工作的摘要。保留：用户目标、项目背景、已完成事项、关键技术决策、未解决问题、下一步。不要编造。\n\n" + transcript
	// 带着历史记录，向模型发送请求
	resp, err := a.client.Responses.New(ctx, responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: responses.ResponseInputParam{
				responses.ResponseInputItemParamOfMessage(prompt, responses.EasyInputMessageRoleUser),
			},
		},
		Model:     a.model,
		Reasoning: shared.ReasoningParam{Effort: shared.ReasoningEffortNone},
	})
	// 模型请求失败
	if err != nil {
		return err
	}
	// 获取模型返回结果，得到摘要
	summary := strings.TrimSpace(resp.OutputText())
	if summary == "" {
		return errors.New("模型返回了空摘要")
	}
	// 将摘要保存到持久化存储
	if err := a.memory.SaveSummary(a.session.ID, summary); err != nil {
		return err
	}
	// 把历史对话记录替换成系统提示词和会话摘要，压缩模型向下文
	a.session.Compact(a.system, summary)
	// 将压缩结果持久化到会话json文件
	return a.persistSession()
}

// 保存会话到磁盘
func (a *Agent) persistSession() error {
	return a.session.Save(a.memory.SessionPath(a.session.ID))
}

// 解析json参数
func jsonUnmarshalArgs(raw string, target any) error {
	return json.Unmarshal([]byte(raw), target)
}
