# agent

一个聚焦于“执行层”的 Go Agent 项目。

它的目标不是做成一个大而全的 Agent 平台，而是提供一个**稳定、清晰、可扩展**的 Agent execution core，并在这个核心之上逐步搭建 CLI、TUI、skills、profile 等上层能力。

## 项目定位

这个仓库重点解决的是：

- 一次 agent turn 如何被稳定地执行
- provider、tool、session、event 应该如何分层
- 如何在不破坏核心语义的前提下持续扩展能力

这个仓库**不**以这些为目标：

- 全功能工作流平台
- 大而全的插件市场
- 复杂的持久化和调度系统
- 产品级多 agent 编排系统

一句话概括：

**这是一个可运行、可理解、可扩展的 Agent 执行内核项目。**

更完整的设计边界见 [CORE_DESIGN.md](/Users/logeable/workstation/go/agent/CORE_DESIGN.md)。

## 当前能力

当前仓库已经具备这些能力：

- `pkg/agentcore`
  提供稳定的 execution core，包括：
  - turn loop
  - provider 抽象
  - tool 抽象
  - in-memory session
  - streaming
  - runtime events
  - approval flow

- `pkg/tools`
  提供第一批内置工具：
  - `read_file`
  - `edit_file`
  - `write_file`
  - `bash`
  - `web_fetch`

- `pkg/profile`
  支持通过 profile 声明一个 agent 实例，包括：
  - provider
  - model
  - identity
  - soul
  - max iterations
  - file access roots
  - enabled tools
  - skills roots

- `cmd/agentcli`
  提供两种使用方式：
  - 单次 CLI 模式
  - 交互式 TUI 模式

## 目录结构

```text
.
├── cmd/
│   └── agentcli/          # 可运行入口
├── internal/
│   ├── agentclirun/       # 单次 CLI 运行支持
│   └── agentclitui/       # TUI 交互实现
├── pkg/
│   ├── agentcore/         # 稳定执行内核
│   ├── profile/           # profile 装配
│   ├── skills/            # 本地 skills 支持
│   └── tools/             # 内置工具实现
├── ref/                   # 参考实现与提炼来源
└── CORE_DESIGN.md         # 核心设计边界
```

## 快速开始

要求：

- Go `1.26.0`
- 一个可用的 OpenAI-compatible 接口，或 OpenAI Responses 接口

安装依赖：

```bash
go mod tidy
```

运行测试：

```bash
go test ./...
```

### 交互式 TUI

```bash
go run ./cmd/agentcli \
  -provider openai \
  -api-key "$OPENAI_API_KEY" \
  -base-url https://api.openai.com/v1 \
  -model gpt-5
```

如果不传 `-m`，且标准输入是终端，`agentcli` 会进入 TUI。

### 单次消息模式

```bash
go run ./cmd/agentcli \
  -provider openai \
  -api-key "$OPENAI_API_KEY" \
  -base-url https://api.openai.com/v1 \
  -model gpt-5 \
  -m "请阅读 go.mod 并总结当前依赖"
```

### 使用标准输入

如果没有 `-m`，但标准输入有内容，标准输入会作为完整 user message：

```bash
cat prompt.txt | go run ./cmd/agentcli -provider openai -api-key "$OPENAI_API_KEY" -base-url https://api.openai.com/v1 -model gpt-5
```

如果同时提供了 `-m` 和标准输入，标准输入会作为补充内容追加到 `-m` 后面：

```bash
git diff | go run ./cmd/agentcli \
  -provider openai \
  -api-key "$OPENAI_API_KEY" \
  -base-url https://api.openai.com/v1 \
  -model gpt-5 \
  -m "请分析下面的变更："
```

这更适合 shell 管道和组合式用法。

## CLI 行为

`agentcli` 的设计目标是：

- TUI 模式更适合交互
- 单次 CLI 模式更适合脚本和管道

单次模式下：

- 正常结果默认输出到 `stdout`
- 错误和 approval 交互输出到 `stderr`
- runtime events 默认关闭

常用参数：

- `-m`
  单次消息模式
- `-profile`
  指定 profile 文件
- `-provider`
  `openai` 或 `openai_response`
- `-model`
  模型名
- `-base-url`
  provider 地址
- `-api-key`
  provider API key
- `-events`
  显示运行时事件，默认关闭
- `-stream`
  启用 streaming
- `-auto-approve`
  自动批准需要 approval 的工具操作

## Profile

profile 用来声明“这个 agent 实例是什么”。

当前 profile 关注的是实例装配，而不是复杂配置系统。

一个最小示例：

```toml
name = "default"

[provider]
kind = "openai"
api_key_env = "OPENAI_API_KEY"
base_url = "https://api.openai.com/v1"
model = "gpt-5"

[agent]
id = "default-agent"
identity = """
You are a general-purpose local agent.
You operate in the current environment and use available tools to help complete user requests.
"""
soul = """
Understand the current state before acting.
Use tools to inspect facts instead of guessing.
Take the smallest action that makes real progress.
Do not repeat tool calls without new information.
Report failures clearly and stay concise.
"""
max_iterations = 12

[files]
scope = "workspace"

[tools]
enabled = ["read_file", "edit_file", "write_file", "bash", "web_fetch"]

[tools.read_file]
max_bytes = 131072

[tools.bash]
timeout_ms = 30000
max_output_bytes = 65536
require_approval = true

[tools.web_fetch]
timeout_ms = 20000
max_bytes = 131072

[skills]
enabled = true
roots = ["${HOME}/.agents/skills"]
```

运行：

```bash
go run ./cmd/agentcli -profile ~/.agentcli/default.toml
```

## 文件访问与审批

文件工具共享统一的路径边界语义。

当前支持的文件作用域：

- `workspace`
  只能访问当前工作目录
- `any`
  不限制文件根目录
- `explicit`
  只允许访问显式声明的根目录

当 `read_file`、`edit_file`、`write_file` 访问路径逃出允许根目录时：

- 不会直接执行
- 会触发 approval flow
- CLI/TUI 会要求用户确认

这意味着：

- 根目录内的正常文件操作不需要额外批准
- 越界文件操作需要显式批准

`bash` 则支持实例级 `require_approval`。

## Skills

当前已经支持本地 skills 发现与加载。

profile 里可以声明：

```toml
[skills]
enabled = true
roots = [
  "${HOME}/.agents/skills",
  "${CWD}/skills",
  "${PROFILE_DIR}/skills",
]
```

当前 skills 机制是克制版本：

- 只做本地发现
- 只加载指定目录
- 只把必要信息注入 agent 上下文
- 不做自动安装
- 不做复杂市场和注册中心

## TUI

当前 TUI 的重点不是做成复杂终端应用，而是把 Agent 对话和执行状态组织得更清楚。

现在的 TUI 特点：

- 用户消息和 assistant 输出按块展示
- 工具调用会以工具块显示关键步骤和摘要
- approval 会以独立卡片打断
- 默认不再显示独立事件面板

这样更接近面向实际使用的 agent 终端交互，而不是日志面板。

注意：

- 为了支持终端原生鼠标选中复制，当前 TUI 不主动抢占鼠标事件

## Provider

当前支持两种 provider：

- `openai`
  基于 `chat/completions`
- `openai_response`
  基于 `responses`

两者都已接入当前的 loop、streaming 和 tool flow。

## 适合什么场景

这个项目适合：

- 学习 agent execution core 的基本结构
- 搭建自己的本地代码 agent
- 研究 provider / tool / event / approval 分层
- 在稳定 core 之上实验 profile、skills、UI、tooling

这个项目暂时不适合：

- 直接作为完整产品平台使用
- 需要复杂持久化 memory
- 需要大型 workflow engine
- 需要成熟多 agent orchestration

## 参考来源

这个仓库的设计和摘录过程参考了 [sipeed/picoclaw](https://github.com/sipeed/picoclaw)

当前仓库不是对参考项目的直接搬运，而是以“更小、更清楚、更稳定的 execution core”为目标重组后的结果。

## 致谢

本项目在 agent loop、provider 分层、tool 接入、skills、profile 和运行时设计等方面，参考并受益于 [sipeed/picoclaw](https://github.com/sipeed/picoclaw) 的思路与实现，在此致谢。

## 开发原则

这个项目持续遵循这些原则：

- 核心 contract 尽量稳定
- 核心机制尽量少
- 高级能力优先放到 core 之外
- 先把 execution semantics 讲清楚，再继续扩展

如果你准备继续扩展这个仓库，建议先读：

- [CORE_DESIGN.md](/Users/logeable/workstation/go/agent/CORE_DESIGN.md)
