# Go 改写架构说明

本文描述 `deekseekharness-go` 当前骨架的实现结构与设计取舍。目标是渐进式重建上游
DeepSeek Harness（TS/Cordis）的能力，而不是一次性移植。

## 1. 设计原则

1. **一切皆插件**：能力通过 `kernel.Plugin` 组合，插件之间只通过 `Context` 暴露的服务与事件协作。
2. **事件驱动**：内核、工具、agent 的关键节点都发布到 `kernel.Events`，Web/CLI 只在边界处消费。
3. **工具优先**：模型看到的是 JSON Schema 工具目录，所有副作用经 `tool.Pipeline` 统一治理。
4. **可迁移的持久化**：会话使用追加式 JSONL（meta + message 行），便于后续与上游 session 格式做适配/迁移。
5. **零外部依赖**：当前只用 Go 标准库，`go build` 不需要网络；后续按需引入。
6. **安全默认**：文件工具限制在工作区、命令执行有超时与输出上限、路径 id 有白名单校验。

## 2. 分层

```
┌──────────────────────────── cmd/dsh ─────────────────────────────┐
│ version / web / chat / tools / config                            │
└───────────────┬───────────────────────────────┬──────────────────┘
                │ composition root              │ HTTP
      ┌─────────▼─────────┐            ┌────────▼─────────┐
      │ internal/config   │            │ internal/server  │
      └─────────┬─────────┘            └────────┬─────────┘
                │                               │ ChatRunner
      ┌─────────▼─────────┐            ┌────────▼─────────┐
      │ internal/kernel   │◄── services│ internal/agent   │
      │ 插件/服务/事件     │            │ agent loop       │
      └───────┬───────────┘            └───┬──────────┬───┘
              │ plugins                    │          │
   ┌──────────▼──────────┐      ┌──────────▼───┐  ┌───▼──────────┐
   │ internal/builtin    │      │ internal/llm │  │internal/tool │
   │ fs / shell / clock  │      │ DeepSeek     │  │registry+pipes│
   └─────────────────────┘      └──────────────┘  └───┬──────────┘
                                                      │ persist
                                              ┌───────▼──────────┐
                                              │ internal/session │
                                              │ JSONL store      │
                                              └──────────────────┘
```

| 包 | 职责 |
|---|---|
| `internal/kernel` | `Plugin` 生命周期（`Apply` → `Start` → `Stop` 逆序）、`Services` 类型化容器、`Events` 同步总线 |
| `internal/llm` | `Client` 接口、消息/工具调用模型、DeepSeek 兼容 HTTP 客户端 |
| `internal/tool` | `Tool` 接口、`Registry`、`Pipeline`（guard/超时/panic/截断/事件） |
| `internal/builtin` | 内置插件：`read`、`write`、`list_dir`、`bash`/`pwsh`、`now` |
| `internal/agent` | 单会话 agent loop，负责模型调用与工具结果回填 |
| `internal/session` | 会话内存模型与追加式 JSONL 持久化 |
| `internal/server` | 内嵌 Web UI + JSON API + 优雅退出 |
| `internal/config` | JSON 配置 + 环境变量覆盖 + 默认值 |
| `internal/version` | 构建期注入的版本信息 |

## 3. 插件内核

```go
type Plugin interface {
    Name() string
    Apply(*kernel.Context) error
}
type Starter interface{ Start(context.Context) error }
type Stopper interface{ Stop(context.Context) error }
```

- `App.Use(plugin)`：注册（去重、Boot 后禁止再注册）。
- `App.Boot(ctx)`：按注册顺序 `Apply`，再按顺序 `Start`；任一失败则回滚已应用插件（逆序 `Stop`）。
- `App.Shutdown(ctx)`：逆序 `Stop` + 关闭事件总线。
- `Context` 提供 `Logger`、`Events`、`Services`。

服务注入示例（内置工具插件就是这么拿注册表的）：

```go
registry, ok := kernel.Lookup[*tool.Registry](ctx.Services)
if !ok {
    return errors.New("builtin/fs: tool registry service is missing")
}
```

启动流程（`cmd/dsh/main.go` 的 `bootstrap`）：

1. `config.Load` → 合并环境变量；
2. `kernel.New` + 创建 `Registry` / `Pipeline` / `Session Store` 并 `kernel.Provide` 注入；
3. `Use` 三个内置插件；
4. `Boot`；
5. 按子命令挂接 CLI/HTTP 边界。

## 4. 一次对话的时序

```
POST /api/chat {sessionId?, input}
  └─ server.handleChat
       ├─ store.Load(id) 或 store.Create("New session")
       └─ ChatRunner(ctx, session, input)        // cmd/dsh 提供
            └─ agent.Loop.Run(input)
                 ├─ store.Append(user message)
                 ├─ 循环（最多 MaxIterations 次）：
                 │    ├─ llm.Client.Chat(messages, tools)
                 │    ├─ store.Append(assistant message)
                 │    ├─ 若有 tool_calls：
                 │    │    └─ tool.Pipeline.Execute(call)
                 │    │         └─ builtin tool.Execute(...)
                 │    │    └─ store.Append(tool result message)
                 │    └─ 否则返回最终文本
                 └─ 发布 agent/started、agent/step、agent/finished
  └─ 200 {sessionId, reply}
```

关键取舍：

- **工具失败不终止循环**：`Pipeline.Execute` 永远返回 `Result`，失败以 `IsError` 结果回填给模型，让模型自行纠错（与上游一致）。
- **系统提示词**：`agent.DefaultSystemPrompt`，可通过 `agent.Options.SystemPrompt` 覆盖。
- **消息顺序**：assistant(tool_calls) → 每个 tool 结果（带 `tool_call_id`）→ 下一次请求，符合 OpenAI/DeepSeek 协议。

## 5. 工具执行流水线

`Pipeline.Execute` 的固定顺序：

1. `tool/started` 事件；
2. 查找工具（未知工具 → 错误结果）；
3. 依次执行 `Guard`（审批/沙箱/预算钩子，返回 error 即拒绝）；
4. 加超时（默认 `config.toolTimeoutSec`）；
5. `recover` 工具 panic；
6. 输出按 `config.maxToolOutputBytes` 截断（UTF-8 边界安全）；
7. `tool/executed` 事件 + 结构化日志。

新增工具：实现 `tool.Tool`（`Definition` + `Execute`），在某个 `kernel.Plugin.Apply` 里 `registry.Register` 即可。

## 6. 事件目录

| 事件 | 载荷 | 发布者 |
|---|---|---|
| `kernel/booted` | 插件数量 | kernel |
| `tool/started` | `tool.Call` | tool.Pipeline |
| `tool/executed` | `tool.ExecutionEvent{Call, Result, Duration}` | tool.Pipeline |
| `agent/started` | sessionId + input | agent.Loop |
| `agent/step` | `agent.StepEvent{Iteration, ToolCalls}` | agent.Loop |
| `agent/finished` | `agent.FinishedEvent` | agent.Loop |

CLI 的 `[tool] write ok 0ms` 就是订阅 `tool/executed` 打印的。

## 7. 会话 JSONL 格式

文件：`<sessionDir>/<id>.jsonl`，每行一个 JSON 对象：

```jsonc
{"type":"meta","time":"2026-11-16T14:07:22Z","id":"20261116-140722-330507f7","title":"CLI session","createdAt":"2026-11-16T14:07:22Z"}
{"type":"message","time":"...","message":{"role":"user","content":"please write the file"}}
{"type":"message","time":"...","message":{"role":"assistant","tool_calls":[{"id":"call-mock-1","type":"function","function":{"name":"write","arguments":"{\"path\":\"hello.txt\",\"content\":\"hi from mock\"}"}}]}}
{"type":"message","time":"...","message":{"role":"tool","name":"write","tool_call_id":"call-mock-1","content":"wrote 12 bytes to hello.txt"}}
{"type":"message","time":"...","message":{"role":"assistant","content":"MOCK-REPLY"}}
```

- 追加写（append-only），加载时忽略未知 `type`（向前兼容）；
- session id 只允许 `[A-Za-z0-9_-]+`，防止路径穿越；
- 后续可与上游 `session-format-status`/`persistence-catalog` 对齐字段。

## 8. HTTP API

| 方法/路径 | 说明 |
|---|---|
| `GET /` | 内嵌 Web UI |
| `GET /healthz` | `{status,version,commit,model,workspace,chatEnabled}` |
| `GET /api/sessions` | 会话摘要列表（按更新时间倒序） |
| `POST /api/sessions` | `{title}` 创建会话 |
| `GET /api/sessions/{id}` | 会话详情（含 messages） |
| `POST /api/chat` | `{sessionId?, input}` → `{sessionId, reply}`；未配置 API key 返回 503 |

请求体上限 1 MiB、输入上限 64 KiB；所有 handler 有 panic 恢复。

## 9. 与上游 Cordis 的映射与差距

| 上游概念 | 本骨架的对应物 | 差距 |
|---|---|---|
| Cordis 插件 + 服务注入 | `kernel.Plugin` + `Services` | 无 scope 隔离、无声明式依赖、无热加载 |
| `ctx.tools` 注册表 | `tool.Registry` | 无按 Agent/Preset 的动态可见性 |
| 工具执行管线 | `tool.Pipeline` | 无审批策略、沙箱策略、spill 存储 |
| Typert 声明生成 | 手写 `tool.Definition` | 无代码生成与类型校验 |
| Session 持久化 | JSONL store | 未对齐上游字段/事件投影 |
| Web/Desktop 前端 | 内嵌单页 | 仅最小 UI，无 RPC 协议与多会话界面 |
| 多 Agent/团队/工作流 | 无 | 未迁移 |

迁移优先级与模块映射见 [MIGRATION_MAP.md](MIGRATION_MAP.md)。

## 10. 后续演进建议

1. **P1.5**：补齐 `edit`/`apply_patch`/`glob`/`grep`/`todo_write` 等高频工具；工具输出 spill。
2. **P2**：引入 `ctx.fs`、`ctx.shell` 等服务 seam，为沙箱策略（guard 链）与审批事件做铺垫。
3. **P3**：MCP 客户端、LSP、Skill 目录、Subagent 子会话与 Team 任务板。
4. **P4**：Web RPC/事件流（SSE）、多会话前端、Desktop 壳与插件市场。
5. **持续**：对齐上游 session JSONL / 工具 schema，保证跨实现可复现与可迁移。

## 11. 安全边界（当前阶段）

| 能力 | 限制 | 备注 |
|---|---|---|
| `read`/`write`/`list_dir` | 工作区路径白名单（拒绝 `..` 与绝对路径逃逸） | 已实现 |
| `pwsh`/`bash` | 无沙箱，继承宿主用户权限 | 等价上游 `danger-full-access`；P2 引入 read-only/workspace-write 策略 |
| 网络 | LLM 客户端仅访问配置的 `baseUrl` | 工具命令可发起任意网络请求（同 shell 限制） |
| 凭据 | API key 只从环境变量读取，不写入会话/仓库 | 会话 JSONL 只含模型消息与工具结果 |
| 审批 | `tool.Pipeline.AddGuard` 提供否决钩子 | 默认无 guard；P2 接入审批/策略链 |

> 设计取舍：P0/P1 优先跑通闭环，沙箱与审批按迁移计划 P2 再做；在此之前请把 harness 视为与用户同等权限的本地进程。
