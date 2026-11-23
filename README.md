# Code Agent

一个面向 Windows 本地开发环境的模块化 Code Agent，基于 OpenAI Responses API Function Calling 实现 Agent Loop，将本地工具与多个 MCP Server 提供的工具统一注册到 Tool Registry。

Code Agent 不只是一个能够回答编程问题的 AI 助手。它可以在用户授权的工作区中读取和修改项目文件、执行 Shell 命令、调用外部 MCP 工具，并通过浏览器截图和视觉模型检查网页效果，让 AI 参与实际的软件开发与验证过程。

> **项目状态**
>
> 当前版本为本机集成验证版本，尚未经过完整的安全审计和生产环境验证。
>
> Windows AppContainer 沙箱、第三方模型视觉兼容性、浏览器 MCP 的端到端运行效果，仍需要在目标 Windows 机器上实测。请勿将未经验证的安全能力用于重要项目或敏感环境。

## 功能特性

- **Agent Host**：基于 Responses API 实现多轮工具调用循环，支持最大调用轮数限制。
- **统一 Tool Registry**：本地工具和 MCP 工具使用统一的工具定义、参数 Schema 和调用入口。
- **多 MCP Server 管理**：支持多个 stdio Server 独立连接、工具发现、命名空间隔离，以及运行时连接、断开、重连和配置持久化。
- **Windows Shell**：通过独立 Runner 启动 PowerShell，使用 Windows AppContainer 限制资源访问，并将子进程加入 Job Object。
- **Workspace Manager**：支持多个授权项目目录、当前工作区切换、相对路径解析和越权路径拒绝。
- **Process Manager**：支持后台任务 ID、状态查询、取消、超时控制、标准输出和错误输出上限，以及任务事件通知。
- **统一人工审批**：Shell、MCP、工作区切换、长期记忆写入等需要审批的操作统一交由 Approval Manager 管理。
- **Skills**：扫描 `SKILL.md`，先向模型提供技能目录摘要，再根据任务需要加载完整技能说明。
- **Memory / Summary**：支持本地长期记忆、会话快照和模型生成的会话摘要。
- **Bubble Tea TUI**：提供命令行交互界面，支持聊天、人工审批和后台任务状态展示。
- **浏览器视觉复查**：通过 Playwright MCP 操作浏览器并获取截图，提取 MCP 返回的图片数据，调用视觉模型分析页面布局和 UI 问题，再将分析结果交还 Agent，支持后续修改与再次检查。

## 系统架构

```text
用户
 │
 ▼
Bubble Tea TUI ─────────────── 审批请求 / 任务事件
 │
 ▼
Agent Host / Agent Loop
 │  Responses API
 ▼
Tool Registry
 ├── Local Tools
 │    ├── Workspace Manager
 │    ├── Shell Tool
 │    │    └── Process Manager
 │    │         └── AppContainer Runner
 │    │              └── PowerShell
 │    ├── Skill Manager
 │    ├── Memory Manager
 │    └── MCP 管理工具
 │
 └── MCP Tools
      ├── MCP Server A
      ├── MCP Server B
      └── Playwright MCP
           └── 浏览器操作 / 页面截图
                └── Vision Client
                     └── Responses API
```

### Agent 调用流程

1. 用户通过 TUI 提交开发需求。
2. Agent Host 将用户输入和会话上下文发送给模型。
3. 模型根据任务需要返回工具调用请求。
4. Agent Host 从 Tool Registry 中查找对应工具。
5. 如果工具需要人工审批，则创建审批请求并等待用户决定。
6. 用户批准后，调用对应的本地 Handler 或 MCP Client。
7. 工具执行结果返回 Agent Host，并作为工具调用结果提交给模型。
8. 模型根据执行结果继续调用工具，或者生成最终答复。

### 浏览器视觉复查流程

```text
Agent 调用 Playwright MCP
          │
          ▼
浏览器打开目标网页
          │
          ▼
执行页面截图
          │
          ▼
MCP 返回图片内容
          │
          ▼
MCP Manager 提取 Base64 图片数据
          │
          ▼
Vision Client 转换为 Data URL
          │
          ▼
Responses API 接收 input_image
          │
          ▼
视觉模型分析页面
          │
          ▼
分析文本作为工具结果返回 Agent
          │
          ▼
Agent 根据分析结果决定后续操作
```

当前视觉分析通过一次独立的 Responses API 请求完成，再将分析文本作为 MCP 工具结果返回 Agent。Agent 可以继续调用开发工具修改页面，并再次调用浏览器工具检查效果。

## 项目结构

```text
.
├── cmd/
│   └── codeagent/              # 程序入口及 sandbox-runner 子进程入口
│
├── configs/                    # MCP Server 配置示例
├── data/                       # 本地记忆和会话数据
├── docs/                       # 架构文档与实现状态
│
├── internal/
│   ├── approval/               # 全局审批管理
│   ├── config/                 # 环境变量配置
│   ├── core/                   # Agent Loop、Session
│   ├── mcpclient/              # 多 MCP Server 生命周期管理
│   ├── memory/                 # 长期记忆、会话摘要
│   ├── sandbox/                # Windows AppContainer / Job Object
│   ├── shell/                  # Shell Adapter、Process Manager、Shell Tools
│   ├── skills/                 # Skill 扫描和按需加载
│   ├── tools/                  # ToolDef、Tool Registry
│   ├── tui/                    # Bubble Tea 交互界面
│   ├── vision/                 # Responses API 图片输入客户端
│   └── workspace/              # 多工作区授权与切换
│
├── skills/                     # 内置 Skills
│   ├── frontend-design/
│   ├── frontend-development/
│   ├── frontend-review/
│   └── responsive-design/
│
├── .env.example
├── .gitignore
├── go.mod
└── README.md
```

## 环境要求

| 环境 / 组件 | 要求 |
|---|---|
| 操作系统 | Windows 10 / 11 x64 |
| Go | Go 1.23 或更高版本 |
| Shell | Windows PowerShell 5.1 |
| 模型服务 | 兼容 OpenAI Responses API |
| 视觉模型 | 模型及服务端需要支持 `input_image` |
| Node.js | 使用 Node.js 类型 MCP Server 时需要 |
| 浏览器环境 | 使用 Playwright MCP 时需要准备对应浏览器运行环境 |

> Node.js 不是 Code Agent 本身的运行依赖，而是 Playwright MCP 等 Node.js 类型 MCP Server 的运行依赖。

## 安装与启动

### 1. 克隆项目

```powershell
git clone <your-repository-url>
cd CodeAgent
```

将 `<your-repository-url>` 替换为实际的 GitHub 仓库地址。

### 2. 安装 Go 依赖

```powershell
go mod tidy
```

该命令会整理项目依赖并生成 `go.sum`。

建议将生成的 `go.sum` 一并提交到 GitHub，以便其他开发者使用相同的依赖校验信息。

### 3. 创建 MCP 配置文件

```powershell
Copy-Item .\configs\mcp_servers.example.json .\configs\mcp_servers.json
```

`mcp_servers.json` 用于配置需要接入的 MCP Server。

如果暂时不需要浏览器自动化，可以保持所有 Server 的 `enabled` 为 `false`。

### 4. 配置环境变量

在 PowerShell 中设置：

```powershell
$env:OPENAI_API_KEY = "你的模型服务 API Key"
$env:OPENAI_BASE_URL = ""
$env:OPENAI_MODEL = "deepseek-flash"

$env:WORKSPACE_ROOT = "D:\CodeAgentWorkspace"
$env:DATA_DIR = ".\data"
$env:SKILLS_DIR = ".\skills"
$env:MCP_CONFIG = ".\configs\mcp_servers.json"
```

配置项说明：

| 环境变量 | 作用 |
|---|---|
| `OPENAI_API_KEY` | 模型服务 API Key |
| `OPENAI_BASE_URL` | 模型服务 API 根地址 |
| `OPENAI_MODEL` | 使用的模型名称 |
| `WORKSPACE_ROOT` | 默认授权工作区 |
| `DATA_DIR` | 本地记忆和会话数据目录 |
| `SKILLS_DIR` | Skills 存放目录 |
| `MCP_CONFIG` | MCP Server 配置文件路径 |

注意：

- `OPENAI_BASE_URL` 留空时，程序默认连接 DeepSeek 官方服务地址（`https://api.deepseek.com`），对应默认模型 `deepseek-flash`。
- 如果要使用 OpenAI 官方服务或其他兼容服务，必须显式填写对应的 `OPENAI_BASE_URL`，并将 `OPENAI_MODEL` 改为该服务支持的模型名称。
- `WORKSPACE_ROOT` 必须指向已经存在的目录。
- 多个授权工作区可以通过 Windows 路径列表分隔符 `;` 配置。
- `.env.example` 仅作为配置模板，程序不会自动读取 `.env` 文件。

### 5. 启动项目

```powershell
go run ./cmd/codeagent
```

启动后，Code Agent 会读取配置、初始化本地工具、加载 MCP Server，并进入 TUI 交互界面。

如果没有配置 MCP Server，程序仍然可以使用已启用的本地功能，但无法执行浏览器自动化。

### 6. 运行测试

```powershell
go test ./...
go vet ./...
```

## 配置浏览器自动化

Code Agent 使用 Playwright MCP 提供浏览器操作能力。

**默认情况下，Playwright MCP 并未启用。** 需要完成 Node.js 环境准备和 MCP 配置后，Agent 才能调用浏览器工具。

### 1. 安装 Node.js

从 Node.js 官方网站安装适用于 Windows 的 LTS 版本：

https://nodejs.org/

安装完成后，重新打开 PowerShell，检查环境：

```powershell
node --version
npm --version
npx --version
```

确保三个命令均能够正常执行。

### 2. 配置 Playwright MCP

打开：

```text
configs/mcp_servers.json
```

在 `servers` 数组中添加或修改 Playwright 配置：

```json
{
  "id": "playwright",
  "enabled": true,
  "transport": "stdio",
  "command": "npx",
  "args": [
    "-y",
    "@playwright/mcp@latest"
  ],
  "env": {},
  "work_dir": ""
}
```

其中：

| 配置项 | 说明 |
|---|---|
| `id` | MCP Server 的唯一标识 |
| `enabled` | 设置为 `true`，启动时才会尝试连接 |
| `transport` | 当前使用 `stdio` |
| `command` | 使用 `npx` 启动 MCP Server |
| `args` | Playwright MCP 包及启动参数 |
| `env` | MCP Server 需要的环境变量 |
| `work_dir` | MCP Server 的工作目录 |

如果配置文件中还有其他 MCP Server，可以保留原来的配置，只需要保证每个 Server 的 ID 不重复。

### 3. 准备 Playwright 浏览器环境

Playwright MCP 需要对应的浏览器运行环境。

首次运行时，如果浏览器尚未准备好，请按照 Playwright MCP 的安装提示完成浏览器准备。

相关项目：

- Playwright MCP：https://github.com/microsoft/playwright-mcp
- Playwright：https://playwright.dev/

### 4. 启动并检查连接状态

重新启动 Code Agent：

```powershell
go run ./cmd/codeagent
```

程序会读取 `mcp_servers.json`，尝试连接所有 `enabled` 为 `true` 的 Server，并发现它们提供的工具。

也可以在 TUI 中请求：

```text
列出当前所有 MCP Server 的配置和连接状态。
```

确认 Playwright Server 已成功连接。

> 如果 Playwright 没有连接成功，Agent 就没有可用的 Playwright 浏览器工具。仅仅安装 Node.js 并不能自动获得截图能力。

### 5. 动态连接 MCP Server

除了修改配置文件，也可以在 TUI 中请求 Agent：

```text
连接一个 Playwright MCP Server，使用 npx 启动
@playwright/mcp@latest。
```

Agent 会调用 `connect_mcp_server`，创建连接请求并等待人工审批。

连接成功后，相关工具会动态注册到 Tool Registry。

## 使用方式

### 1. 普通开发任务

可以直接在 TUI 中描述任务：

```text
检查当前项目结构，先给出开发计划。
```

```text
在当前工作区创建一个响应式个人主页。
```

```text
分析当前项目的代码结构，找出可以改进的地方。
```

### 2. 工作区管理

```text
列出当前工作区和已授权的其他工作区。
```

```text
切换到指定的项目工作区。
```

### 3. Shell 任务

```text
检查当前项目的 Go 代码，并运行测试。
```

```text
查看项目中的文件结构，分析启动失败的原因。
```

### 4. MCP 管理

```text
列出所有 MCP Server 的连接状态。
```

```text
重新连接 Playwright Server。
```

```text
断开指定 MCP Server。
```

### 5. 浏览器视觉复查

在 Playwright MCP 成功连接、视觉模型可用的情况下，可以尝试：

```text
在当前工作区创建一个响应式个人主页。

完成后使用 Playwright 打开本地页面并截图，
检查页面的布局、间距、字体、颜色和响应式效果。

根据截图分析结果修改页面，
然后再次打开页面截图，检查修改是否有效。
```

这项功能的实际工作过程是：

1. Agent 根据需求编写或修改网页。
2. Agent 调用 Playwright MCP 打开网页。
3. Playwright MCP 返回页面截图。
4. MCP Manager 提取图片数据。
5. Vision Client 将截图作为 `input_image` 发送给视觉模型。
6. 视觉模型返回页面分析结果。
7. Agent 根据分析结果决定是否继续修改。
8. 如果需要，可以再次调用 Playwright MCP 截图复查。

注意：这不是一个固定次数的自动优化流程。是否继续修改和复查由 Agent 根据模型判断、工具可用性和执行结果决定。

### TUI 操作

| 按键 / 命令 | 功能 |
|---|---|
| `Enter` | 发送消息 |
| `Y` / `N` | 审批等待时批准 / 拒绝 |
| `通过` / `拒绝` + `Enter` | 通过文本进行审批 |
| 鼠标点击 | 在审批面板中选择允许或拒绝 |
| `Ctrl+C` | 退出程序 |
| `/quit` / `/exit` | 退出程序 |

## MCP Server 管理

项目目前支持多个 stdio MCP Server。

运行时管理工具：

| 工具名称 | 功能 |
|---|---|
| `list_mcp_servers` | 查看 MCP Server 配置和连接状态 |
| `connect_mcp_server` | 动态添加配置并启动 Server |
| `disconnect_mcp_server` | 断开 Server 并从 Registry 移除其工具 |
| `reconnect_mcp_server` | 根据保存的配置重启 Server 并重新发现工具 |

MCP 工具使用命名空间：

```text
mcp__<server-id>__<tool-name>
```

这样可以避免多个 MCP Server 提供同名工具时发生冲突。

所有 MCP 工具调用均需要人工审批。连接、断开和重连操作也需要审批。

当前仅实现 stdio transport，暂未实现 Streamable HTTP、OAuth 和远程身份认证。

## Windows Shell 安全边界

Shell Tool 不直接把 PowerShell 命令交给当前用户权限下的普通子进程执行。

Process Manager 启动当前程序的 `--sandbox-runner` 子进程，由 Runner 创建受限执行环境。

主要实现包括：

1. 为单次任务创建临时 AppContainer Profile。
2. 为 AppContainer SID 临时授予当前授权工作区访问权限。
3. 创建不授予网络 capability 的 AppContainer 进程。
4. 将 PowerShell 加入 Job Object，限制活动进程数、进程和作业内存、CPU 使用率，并启用关闭 Job 时终止全部成员进程。
5. 仅向 PowerShell 传递必要的标准输入、输出句柄和经过筛选的环境变量，不继承模型 API Key 等环境变量。
6. 任务结束后清理工作区 ACL、临时目录和 AppContainer Profile。

### 网络访问限制

当前沙箱默认不授予网络访问权限，因此：

```powershell
npm install
go mod download
```

等需要联网的命令可能无法正常执行。

不要为了方便执行联网命令而退回到无隔离 Shell。

AppContainer 的实际隔离效果、ACL 清理和不同 Windows 版本的兼容性，仍需要在目标机器上进行验证。

## Skills 与本地数据

### 内置 Skills

```text
skills/
├── frontend-design/
├── frontend-development/
├── frontend-review/
└── responsive-design/
```

Agent 首先获取技能目录摘要，再根据任务需要加载对应的完整技能说明。

### 本地数据

```text
data/
├── memory/
│   └── long_term.md
└── sessions/
    ├── <session-id>.json
    └── <session-id>.summary.md
```

| 文件 / 目录 | 用途 |
|---|---|
| `data/memory/long_term.md` | 长期记忆 |
| `data/sessions/<session-id>.json` | 会话快照 |
| `data/sessions/<session-id>.summary.md` | 会话摘要 |

这些数据保存在本机。分享项目之前请检查 `data/`，避免将个人对话、项目细节或密钥提交到公开仓库。

## 已知限制

1. **MCP 通信方式**：目前仅实现 stdio transport，尚未支持 Streamable HTTP 和 OAuth。
2. **浏览器自动化**：需要单独安装 Node.js、配置并成功连接 Playwright MCP，未连接时无法使用浏览器操作和截图功能。
3. **视觉模型兼容性**：视觉分析要求模型服务支持 Responses API 的 `input_image`。当前 Vision Client 使用项目配置的模型名称和 API 服务地址；不同兼容服务的图片输入支持情况需要实际验证。
4. **图片处理限制**：当前视觉客户端支持 PNG、JPEG、WebP 和 GIF，单张图片大小限制为 8 MiB。
5. **Windows 沙箱**：AppContainer 和 Job Object 已有 Windows x64 编译实现，但尚未完成真实 Windows 主机上的完整集成测试和安全审计。如果系统策略或父进程 Job 限制阻止创建受限进程，Shell Tool 会执行失败，不会自动退回普通 Shell。
6. **视觉分析链路**：截图通过独立的 Responses API 请求进行分析，分析文本再返回 Agent；不是将截图直接追加到同一轮主 Agent 请求中的多模态上下文。
7. **TUI 会话管理**：目前以单个 Agent 会话为主，尚未实现多会话标签页和复杂任务看板。

## 安全提示

- 不要将真实 API Key 写入源码、配置模板或 Git 仓库。
- 如果真实凭据曾经提交到仓库或公开位置，应立即在服务提供方处轮换。
- 第三方 MCP Server 会以本机用户权限启动。连接前请确认 Server 的来源、启动命令和参数。
- MCP 工具调用需要人工审批，但审批机制不能替代对第三方工具可信度的判断。
- 请优先在空白测试工作区中验证项目功能，不要直接对重要项目执行未经确认的修改操作。
- 发布项目之前，检查 `.env`、`configs/mcp_servers.json` 和 `data/`，确保没有提交密钥和个人数据。

---

**Code Agent — 从理解需求，到执行开发，再到验证结果。**