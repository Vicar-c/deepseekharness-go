# Go 改写架构说明

本文描述 `deepseekharness-go` 当前骨架的实现结构与设计取舍。目标是渐进式重建上游
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
| `internal/profiles` | 启动组合：bundle → profile patch → home patch → `--patch` 层叠，`--dump-config` 输出每行/每设置的来源 |
| `internal/llm` | `Client` 接口（可选 `StreamingClient`）、消息/工具调用模型、DeepSeek 兼容 HTTP + SSE 客户端 |
| `internal/tool` | `Tool` 接口、`Registry`、`Pipeline`（guard/超时/panic/截断/事件）与批次调度（`ConcurrencySafe` + barrier + 模型序结果） |
| `internal/builtin` | 内置插件：`read`、`write`、`list_dir`、`edit`、`glob`、`grep`、`read_image`、`bash`/`pwsh`、`now`、`todo_write`、`working_directory` |
| `internal/agent` | 单会话 agent loop，负责模型调用、工具批次执行与结果按模型序回填 |
| `internal/session` | 会话 v4 事件流（格式/投影/写入器/尾部修复）的内存模型与持久化 |
| `internal/server` | 内嵌 Web UI + JSON/SSE API + 优雅退出 |
| `internal/config` | JSON 配置 + 环境变量覆盖 + 设置来源追踪 |
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

失败结果带**结构化错误码**（`Result.Code`，写入 `tool/result` 的 `code` 字段）：`TOOL_TIMEOUT`（超时）、`TOOL_NOT_FOUND`（未注册）、`TOOL_BLOCKED`（guard 拒绝）、`TOOL_PANICKED`（panic 恢复）、`ABORTED_BEFORE_DISPATCH`（批次取消没启动）。这样日志与 UI 不必解析自然语言就能区分失败类型。

### 批次执行（`Pipeline.ExecuteBatch`）

一次模型回复里的多个工具调用按以下规则调度：

1. 实现 `tool.ConcurrencySafe` 且返回 true 的调用进"安全段"，段内并发上限 = `config.maxParallelToolCalls`（默认 10，`1` = 串行）；
2. 其余调用（未实现接口、返回 false、或名字未注册）各自独占，前后形成 barrier；
3. 结果写入固定下标，**永远按模型给出的顺序**返回与落盘；
4. ctx 取消后不再启动新调用，已启动的等待结束，未启动的返回 `isError` 结果（不中断 turn）。

内置工具里只有只读的 `read`/`list_dir`/`glob`/`grep`/`read_image`/`now` opt-in；`agent.Loop` 先按模型序写全部 `tool/call`，再按模型序写 `tool/result`。

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

文件：`<sessionDir>/<id>.jsonl`，**DSH v4 事件流**（首行 header，其后每行一个事件，`seq` 从 0 连续递增）：

```jsonc
{"type":"session","version":4,"id":"...","createdAt":1763...,"cwd":"...","isSeeded":false,"delegationDepth":0,"agentPreset":"default"}
{"type":"turn/start","data":{"turn":1},"seq":1,"time":...}
{"type":"user/message","data":{...},"surfaceOp":"append","seq":2,"time":...}
{"type":"step/start","data":{"turn":1,"step":1},"seq":3,"time":...}
{"type":"assistant/message","data":{"message":{...,"content":[{"type":"tool-call",...}]}},"surfaceOp":"append","seq":4,"time":...}
{"type":"tool/call","data":{"callId":"...","name":"read","arguments":"{}"},"seq":5,"time":...}
{"type":"tool/result","data":{"message":{...}},"surfaceOp":"append","seq":6,"time":...}
{"type":"step/end","data":{...},"seq":7,"time":...}
{"type":"turn/end","data":{"reason":{"kind":"completed"}},"seq":8,"time":...}
```

- 只有 `system/message`、`developer/message`、`user/message`、`assistant/message`、`tool/result` 是 surface 事件（带 `surfaceOp`），其余是不进模型历史的记录事件；
- 未知事件类型必须带 `ignorable:true`，否则读取端拒绝（前向兼容的安全阀）；
- `Recording.Project()` 把 surface 事件投影成模型消息（含 `replace` 语义与工具名反查）；
- 写入器保证原子创建、`seq` 续写、进程内单写者与崩溃尾部修复；
- **崩溃恢复**：日志若以打开的 `turn/start` 结束，`Store.Resume` 会在发布会话前补一批普通事件 —— 未应答的 `tool/call` 补 `TOOL_OUTCOME_UNKNOWN`、模型要过但没发起的补 `TOOL_NOT_STARTED`、补 `step/end`、补合成的 `turn/end{interrupted}`。`Store.Load` 保持只读，Web 的会话查看不会改日志；
- **轮次结束原因**：循环自己只发 `completed` / `error`；用户取消是 `cancelled`，恢复补齐的是 `interrupted`；
- 细节与夹具出处见 [T-0008](tasks/T-0008-session-v4-format-layer.md)、[T-0009](tasks/T-0009-session-v4-projection-and-writer.md)、[T-0010](tasks/T-0010-session-v4-wiring.md)、[T-0018](tasks/T-0018-crash-recovery.md)。

## 8. HTTP API

| 方法/路径 | 说明 |
|---|---|
| `GET /` | 内嵌 Web UI |
| `GET /healthz` | `{status,version,commit,model,workspace,chatEnabled}` |
| `GET /api/sessions` | 会话摘要列表（按更新时间倒序） |
| `POST /api/sessions` | `{title}` 创建会话 |
| `GET /api/sessions/{id}` | 会话详情（含 messages） |
| `POST /api/chat` | `{sessionId?, input}` → `{sessionId, reply}`；未配置 API key 返回 503；`Accept: text/event-stream` 时改为 SSE（`delta` / `done` / `error` 帧） |

请求体上限 1 MiB、输入上限 64 KiB；所有 handler 有 panic 恢复。SSE 分支复用的是同一个处理器（Accept 协商，不新增路由），客户端断开时取消该轮 turn。

## 9. 与上游 Cordis 的映射与差距

| 上游概念 | 本骨架的对应物 | 差距 |
|---|---|---|
| Cordis 插件 + 服务注入 | `kernel.Plugin` + `Services` | 无 scope 隔离、无声明式依赖、无热加载 |
| Loader / profile 层叠 | `internal/profiles`（bundle → profile → home → `--patch`，`--dump-config` 报来源） | 只认 JSON patch，不认上游 `cordis.patch.yml`；无插件安装/市场 |
| `ctx.tools` 注册表 | `tool.Registry` | 无按 Agent/Preset 的动态可见性 |
| 工具执行管线 | `tool.Pipeline`（含批次并发 / barrier / 模型序） | 无审批策略、沙箱策略、spill 存储、per-tool 重试 |
| Typert 声明生成 | 手写 `tool.Definition` | 无代码生成与类型校验 |
| Session 持久化 | v4 事件流 + 投影 + 写入器 | 无跨进程单写者锁、无历史版本迁移链、无 projection 缓存 |
| 流式 | `llm.StreamingClient` + `agent/stream` + Web SSE | `assistant/stream` 未落盘、无断线续传 |
| Web/Desktop 前端 | 内嵌单页（fetch + ReadableStream） | 仅最小 UI，无 RPC 协议与多会话界面 |
| 多 Agent/团队/工作流 | 无 | 未迁移 |

迁移优先级与模块映射见 [MIGRATION_MAP.md](MIGRATION_MAP.md)。

## 10. 后续演进建议

1. **P1 收尾**：resume 的 `interruptedTurnClosers`、`request/header` 可重建、`-race` 下并发会话压测。
2. **P2**：引入 `ctx.fs`、`ctx.shell`、`ctx.sandbox` 等服务 seam，为沙箱策略（guard 链）与审批事件做铺垫；工具输出 spill。
3. **P3**：MCP 客户端、LSP、Skill 目录、Subagent 子会话与 Team 任务板。
4. **P4**：Web RPC/事件流兼容层、多会话前端、Desktop 壳与插件市场。
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
