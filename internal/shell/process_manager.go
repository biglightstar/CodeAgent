// ====================管理后台shell进程的生命周期：启动 查询 等待 取消 关闭====================
package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ==================== 错误定义 ====================

var (
	ErrTaskNotFound   = errors.New("任务不存在")
	ErrTaskNotRunning = errors.New("任务没有处于运行状态")
	ErrManagerClosed  = errors.New("进程管理器已关闭")
)

// ==================== 任务状态 ====================

type ProcessStatus string

const (
	ProcessRunning   ProcessStatus = "running"   // 正在执行
	ProcessSucceeded ProcessStatus = "succeeded" // 执行成功
	ProcessFailed    ProcessStatus = "failed"    // 执行失败
	ProcessTimedOut  ProcessStatus = "timed_out" // 执行超时
	ProcessCancelled ProcessStatus = "cancelled" // 任务被取消
)

// ==================== 事件类型 ====================

type ProcessEventType string

const (
	ProcessEventStarted  ProcessEventType = "started"
	ProcessEventFinished ProcessEventType = "finished"
)

// ProcessEvent 描述任务状态变化，agent能够及时知道任务现在的状态
type ProcessEvent struct {
	TaskID   string           // 发生事件的任务id
	Type     ProcessEventType // 事件类型
	Status   ProcessStatus    // 事件发生时任务对应的状态
	Time     time.Time        // 事件发生的时间
	ExitCode int              // 操作系统进程的退出码
	Error    string           // 执行过程中的错误描述
}

// ProcessRequest 命令执行请求
type ProcessRequest struct {
	Command string
	WorkDir string
	Timeout time.Duration // 0 表示使用管理器默认值；默认值也为 0 时不设超时
}

// ProcessInfo 是对外提供的任务信息快照
// 启动任务之后，返回任务执行情况
type ProcessInfo struct {
	ID              string        // 任务唯一标识
	Command         string        // 执行的命令
	WorkDir         string        // 命令执行目录
	Status          ProcessStatus // 任务当前状态，ProcessStatus类型
	ExitCode        int           // 进程退出码
	Stdout          string        // 标准输出
	Stderr          string        // 标准错误输出
	StdoutTruncated bool          // 标准输出是否被截断
	StderrTruncated bool          // 标准错误输出是否被截断
	Error           string        // 执行过程中产生的错误信息
	StartedAt       time.Time     // 开始时间
	FinishedAt      time.Time     // 结束时间
	Duration        time.Duration // 执行耗时
}

// ==================== 管理器配置 ====================

// 进程管理器的配置，控制默认超时、单路输出上限和事件队列容量
type ProcessManagerConfig struct {
	DefaultTimeout          time.Duration // 默认任务超时时间
	MaxOutputBytesPerStream int           // 每一路输出最多保存多少字节
	EventBufferSize         int           // 事件通道的缓冲容量
}

// 返回默认配置ProcessManagerConfig
func DefaultProcessManagerConfig() ProcessManagerConfig {
	return ProcessManagerConfig{
		DefaultTimeout:          0,           // 默认不设置自动超时
		MaxOutputBytesPerStream: 1024 * 1024, // 默认每路输出最多保存1MB
		EventBufferSize:         128,         // 默认事件通道容量大小128
	}
}

// ==================== 输出缓冲区 ====================

// processOutputBuffer 用来保存shell进程产生的输出，把这个输出封装一下
// 这个属于写操作，需要加互斥锁，防止并发冲突
type processOutputBuffer struct {
	mu        sync.Mutex   // 互斥锁，保护buf truncated两个字段
	buf       bytes.Buffer // 保存输出内容的缓冲区
	max       int          // 最大保存容量，单位是字节
	truncated bool         // 标记该输出是否被截断
}

// 创建输出缓冲区
func newProcessOutputBuffer(max int) *processOutputBuffer {
	return &processOutputBuffer{max: max}
}

// Write 实现 io.Writer，使 exec.Cmd 可以把子进程输出写入这里
// 子进程输出会分批写入这个方法
func (b *processOutputBuffer) Write(p []byte) (int, error) {
	// 加锁
	b.mu.Lock()
	// 函数返回时自动释放锁
	defer b.mu.Unlock()
	// 记录本次收到的数据长度
	originalLen := len(p)
	// 第一次write的时候，b.buf.len肯定是0，后面再写的时候，就不是0了
	// 计算剩余容量：最大容量-已经保存的数据长度
	remaining := b.max - b.buf.Len()
	// 如果剩余容量<=0 说明缓冲区已经满了
	if remaining <= 0 {
		// 标记本次数据需要截断
		if originalLen > 0 {
			b.truncated = true
		}
		// 返回本次传入的数据长度
		return originalLen, nil
	}
	// 如果本次数据总量超过剩余容量
	if originalLen > remaining {
		// 还有多少剩余就先写多少
		_, _ = b.buf.Write(p[:remaining])
		// 标记本次数据被截断
		b.truncated = true
	} else {
		// 如果本次数据总量没有超过剩余容量，就全部写入缓冲区
		_, _ = b.buf.Write(p)
	}

	// 告诉写入方本次数据已被接收
	return originalLen, nil
}

// 输出快照：获取当前缓冲区内容和截断状态
func (b *processOutputBuffer) Snapshot() (string, bool) {
	// 锁
	b.mu.Lock()
	defer b.mu.Unlock()
	// 目前缓冲区保存的内容，以及是否发生截断
	return b.buf.String(), b.truncated
}

// ==================== 内部任务结构 ====================
// 这个是进程管理器使用的任务结构体
type managedProcess struct {
	info   ProcessInfo          // 任务信息
	cmd    *exec.Cmd            // cmd进程对象，代表一个外部进程
	ctx    context.Context      // 上下文
	cancel context.CancelFunc   // 与ctx配套的取消函数
	done   chan struct{}        // 任务完成通知通道
	stdout *processOutputBuffer // 当前任务的标准输出缓冲区
	stderr *processOutputBuffer // 当前任务的标准错误输出缓冲区
}

// ==================== 进程管理器 ====================

type ProcessManager struct {
	adapter      ShellAdapter               //shell适配器
	config       ProcessManagerConfig       // 管理器配置
	mu           sync.RWMutex               // mu 用于保护 tasks、nextID、closed 以及每个任务的 info
	tasks        map[string]*managedProcess // 任务管理表
	nextID       uint64                     // 任务id的递增序号，生成唯一的任务id
	closed       bool                       // 标记管理器是否已经关闭
	events       chan ProcessEvent          // Agent可以从这个通道接收任务状态变化通知
	wg           sync.WaitGroup             // 统计所有任务监控 goroutine
	shutdownDone chan struct{}              // 所有任务监控goroutine退出后，会关闭这个通道
}

// 创建进程管理器
func NewProcessManager(adapter ShellAdapter, config ProcessManagerConfig) (*ProcessManager, error) {
	// 检查shell适配器是否初始化
	if adapter == nil {
		return nil, errors.New("Shell 适配器未初始化")
	}
	// 默认超时时间不能是负数
	if config.DefaultTimeout < 0 {
		return nil, errors.New("默认超时时间不能为负数")
	}
	// 如果没有配置输出大小，默认1MB
	if config.MaxOutputBytesPerStream == 0 {
		config.MaxOutputBytesPerStream = 1024 * 1024
	}
	// 输出大小不能为负数
	if config.MaxOutputBytesPerStream < 0 {
		return nil, errors.New("输出大小限制不能为负数")
	}
	// 默认配置事件通道容量128
	if config.EventBufferSize == 0 {
		config.EventBufferSize = 128
	}
	// 事件通道容量不能为负数
	if config.EventBufferSize < 0 {
		return nil, errors.New("事件缓冲容量不能为负数")
	}
	// 返回一个进程管理器
	return &ProcessManager{
		adapter:      adapter,
		config:       config,
		tasks:        make(map[string]*managedProcess),
		events:       make(chan ProcessEvent, config.EventBufferSize), // 管道类型和大小
		shutdownDone: make(chan struct{}),
	}, nil
}

// ==================== 启动任务 ====================
// 启动一个后台shell任务
// 输入参数 父ctx、任务请求
func (pm *ProcessManager) Start(parentCtx context.Context, request ProcessRequest) (ProcessInfo, error) {
	// 管理器不能为空
	if pm == nil {
		return ProcessInfo{}, errors.New("进程管理器未初始化")
	}
	// 父ctx不能为空
	if parentCtx == nil {
		return ProcessInfo{}, errors.New("上下文不能为空")
	}
	// 执行命令不能为空
	if strings.TrimSpace(request.Command) == "" {
		return ProcessInfo{}, errors.New("执行命令不能为空")
	}
	// 任务请求中的超时时间不能为空
	if request.Timeout < 0 {
		return ProcessInfo{}, errors.New("任务超时时间不能为负数")
	}
	// 工作目录必须是绝对路径
	if request.WorkDir == "" || !filepath.IsAbs(request.WorkDir) {
		return ProcessInfo{}, errors.New("工作目录必须是绝对路径")
	}
	// 检查工作目录是否存在、是否可以访问
	dirInfo, err := os.Stat(request.WorkDir)
	if err != nil {
		return ProcessInfo{}, fmt.Errorf("工作目录不可访问: %w", err)
	}
	// 检查路径是否真的是目录
	if !dirInfo.IsDir() {
		return ProcessInfo{}, errors.New("工作路径不是目录")
	}
	// 没有指定超时时间，就使用管理器默认值，默认不设置超时时间，此值为0
	timeout := request.Timeout
	if timeout == 0 {
		timeout = pm.config.DefaultTimeout
	}

	var ctx context.Context
	var cancel context.CancelFunc

	if timeout > 0 {
		// 如果设置超时时间并且大于0，就创建有截止时间的ctx
		ctx, cancel = context.WithTimeout(parentCtx, timeout)
	} else {
		// 超时时间为0，就根据父ctx创建ctx，受父ctx所影响
		ctx, cancel = context.WithCancel(parentCtx)
	}

	// 加写锁
	pm.mu.Lock()
	// 如果管理器已经关闭，就不能再启动任务
	if pm.closed {
		pm.mu.Unlock()
		cancel()
		return ProcessInfo{}, ErrManagerClosed
	}
	// 检查父context是否已经被取消
	if err := parentCtx.Err(); err != nil {
		pm.mu.Unlock()
		cancel()
		return ProcessInfo{}, err
	}
	// 将命令转化为实际启动的程序和参数
	program, args := pm.adapter.BuildCommand(request.Command)
	if program == "" {
		pm.mu.Unlock()
		cancel()
		return ProcessInfo{}, errors.New("Shell 程序不能为空")
	}
	// 创建外部进程对象
	cmd := exec.CommandContext(ctx, program, args...)
	// 设置子进程工作目录
	cmd.Dir = request.WorkDir
	// 创建标准输出缓冲区
	stdout := newProcessOutputBuffer(pm.config.MaxOutputBytesPerStream)
	// 创建标准错误输出缓冲区
	stderr := newProcessOutputBuffer(pm.config.MaxOutputBytesPerStream)
	// 将标准输出交给stdout缓冲区收集
	cmd.Stdout = stdout
	// 将标准错误输出交给stderr缓冲区收集
	cmd.Stderr = stderr
	// 记录任务开始时间
	startTime := time.Now()
	// 真正开始只能修改命令，启动操作系统进程
	if err := cmd.Start(); err != nil {
		pm.mu.Unlock()
		cancel()
		return ProcessInfo{}, fmt.Errorf("启动 Shell 进程失败: %w", err)
	}
	// 递增任务序号
	pm.nextID++
	// 生成任务id，六位整数，左侧补0
	taskID := fmt.Sprintf("task-%06d", pm.nextID)
	// 创建任务对象task
	task := &managedProcess{
		info: ProcessInfo{
			ID:        taskID,
			Command:   request.Command,
			WorkDir:   request.WorkDir,
			Status:    ProcessRunning,
			ExitCode:  -1,
			StartedAt: startTime,
		},
		cmd:    cmd,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		stdout: stdout,
		stderr: stderr,
	}
	// 把任务保存到任务表当中
	pm.tasks[taskID] = task
	// goroutine+1
	pm.wg.Add(1)
	// 发送任务启动事件
	pm.emitLocked(ProcessEvent{
		TaskID: taskID,
		Type:   ProcessEventStarted,
		Status: ProcessRunning,
		Time:   startTime,
	})
	// 释放管理器锁
	pm.mu.Unlock()
	// 启动独立goroutine，监控进程结束
	go pm.wait(task)
	// 查询刚刚创建的任务，返回任务信息processInfo
	return pm.GetTask(taskID)
}

// ==================== 监控进程结束 ====================
// wait负责监控一个后台任务
// 等待进程结束，收集输出，更新任务状态，并通知等待者
func (pm *ProcessManager) wait(task *managedProcess) {
	// 函数退出时，将waitgroup计数减1
	defer pm.wg.Done()
	// 阻塞等待操作系统进程结束
	waitErr := task.cmd.Wait()
	// 记录进程结束时间
	finishedAt := time.Now()
	// 获取标准输出内容和截断状态
	stdout, stdoutTruncated := task.stdout.Snapshot()
	// 获取标准错误输出内容和阶段状态
	stderr, stderrTruncated := task.stderr.Snapshot()
	// 默认假设任务成功
	status := ProcessSucceeded
	exitCode := 0
	// 默认没有错误信息
	errorMessage := ""
	// 如果task.cmd.wait返回错误
	if waitErr != nil {
		// 默认无法取得有效退出码时使用-1
		exitCode = -1
		switch {
		// 排查错误原因，返回任务状态和错误msg
		case errors.Is(task.ctx.Err(), context.DeadlineExceeded):
			status = ProcessTimedOut
			errorMessage = "任务执行超时"
		case errors.Is(task.ctx.Err(), context.Canceled):
			status = ProcessCancelled
			errorMessage = "任务已取消"
		default:
			status = ProcessFailed
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			}
			errorMessage = waitErr.Error()
		}
	}
	// 获取写锁
	pm.mu.Lock()
	// 更新任务信息
	task.info.Stdout = stdout
	task.info.Stderr = stderr
	task.info.StdoutTruncated = stdoutTruncated
	task.info.StderrTruncated = stderrTruncated
	task.info.Status = status
	task.info.ExitCode = exitCode
	task.info.Error = errorMessage
	task.info.FinishedAt = finishedAt
	task.info.Duration = finishedAt.Sub(task.info.StartedAt) // 计算任务耗时
	// 关闭任务完成通知的通道
	// 所有等待这个通道的waittask调用都会被唤醒
	close(task.done)
	// 发送任务结束事件
	pm.emitLocked(ProcessEvent{
		TaskID:   task.info.ID,
		Type:     ProcessEventFinished,
		Status:   status,
		Time:     finishedAt,
		ExitCode: exitCode,
		Error:    errorMessage,
	})
	pm.mu.Unlock()

	// cmd.Wait 已结束，释放 Context 相关资源。
	task.cancel()
}

// ==================== 查询任务 ====================

func (pm *ProcessManager) GetTask(taskID string) (ProcessInfo, error) {
	if pm == nil {
		return ProcessInfo{}, errors.New("进程管理器未初始化")
	}
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	// 从任务管理表中查找
	task, ok := pm.tasks[taskID]
	// 没有找到，就返回空的任务信息和错误
	if !ok {
		return ProcessInfo{}, ErrTaskNotFound
	}
	// 找到，返回对应结果
	return pm.snapshot(task), nil
}

// 返回任务表中所有的任务信息，以[]ProcessInfo形式
func (pm *ProcessManager) ListTasks() []ProcessInfo {
	if pm == nil {
		return nil
	}
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]ProcessInfo, 0, len(pm.tasks))
	for _, task := range pm.tasks {
		result = append(result, pm.snapshot(task))
	}
	return result
}

// snapshot 在调用者持有 pm.mu 读锁或写锁时使用
func (pm *ProcessManager) snapshot(task *managedProcess) ProcessInfo {
	// 复制任务当前的基本信息
	info := task.info
	// 更新最新的标准输出内容和截断状态
	info.Stdout, info.StdoutTruncated = task.stdout.Snapshot()
	// 更新最新的标准错误输出内容和截断状态
	info.Stderr, info.StderrTruncated = task.stderr.Snapshot()
	return info
}

// ==================== 取消任务 ====================
// 根据任务id发送取消信号
func (pm *ProcessManager) CancelTask(taskID string) error {
	if pm == nil {
		return errors.New("进程管理器未初始化")
	}
	pm.mu.RLock()
	// 查找该任务
	task, ok := pm.tasks[taskID]
	// 没有找到
	if !ok {
		pm.mu.RUnlock()
		return ErrTaskNotFound
	}
	// 如果任务已经不是running状态，不再次取消
	if task.info.Status != ProcessRunning {
		pm.mu.RUnlock()
		return ErrTaskNotRunning
	}
	// 获得任务的取消函数
	cancel := task.cancel
	// 在释放管理器锁后发出取消信号
	pm.mu.RUnlock()
	cancel()
	return nil
}

// ==================== 等待指定任务完成 ====================
func (pm *ProcessManager) WaitTask(ctx context.Context, taskID string) (ProcessInfo, error) {
	if pm == nil {
		return ProcessInfo{}, errors.New("进程管理器未初始化")
	}
	if ctx == nil {
		return ProcessInfo{}, errors.New("上下文不能为空")
	}
	// 获取读锁
	pm.mu.RLock()
	// 获取任务对象
	task, ok := pm.tasks[taskID]
	// 释放读锁
	pm.mu.RUnlock()
	// 获取失败
	if !ok {
		return ProcessInfo{}, ErrTaskNotFound
	}
	// 等待任务完成
	select {
	// 如果done通道关闭，说明任务已经完成
	case <-task.done:
		// 查询并返回任务最终信息
		return pm.GetTask(taskID)
		// 如果是调用方的ctx被取消或者超时
	case <-ctx.Done():
		// 返回空任务信息和调用方的ctx错误
		return ProcessInfo{}, ctx.Err()
	}
}

// Events 返回只读事件通道，外部可以接收事件
func (pm *ProcessManager) Events() <-chan ProcessEvent {
	if pm == nil {
		return nil
	}
	return pm.events
}

// emitLocked 在持有 pm.mu 的情况下调用
// 发送事件
func (pm *ProcessManager) emitLocked(event ProcessEvent) {
	select {
	// 如果通道有空间就发送事件
	case pm.events <- event:
		// 如果通道已经满了，直接跳过，不等待
	default:
	}
}

// ==================== 关闭管理器 ====================
// 组织新任务、取消正在运行的任务，等待监控goroutine全部退出
func (pm *ProcessManager) Close(ctx context.Context) error {
	if pm == nil {
		return errors.New("进程管理器未初始化")
	}
	if ctx == nil {
		return errors.New("上下文不能为空")
	}

	pm.mu.Lock()
	// 如果管理器还没有关闭
	if !pm.closed {
		// 标记管理器已经关闭
		// 后续start会根据这个字段拒绝新任务
		pm.closed = true
		// 遍历所有任务
		for _, task := range pm.tasks {
			// 取消正在运行的任务
			if task.info.Status == ProcessRunning {
				task.cancel()
			}
		}
		// 启动一个goroutine
		go func() {
			// 等待所有任务goroutine退出
			pm.wg.Wait()
			// 关闭事件通道
			close(pm.events)
			// 通知调用方，管理器关闭完成
			close(pm.shutdownDone)
		}()
	}
	done := pm.shutdownDone
	pm.mu.Unlock()

	select {
	// 管理器关闭完成，返回结果
	case <-done:
		return nil
		// 调用方不愿意继续等待
	case <-ctx.Done():
		return ctx.Err()
	}
}
