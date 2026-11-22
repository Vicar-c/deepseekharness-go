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
| `internal/kernel` | `Plugin` 生命周期（`Apply` → `Start` → `Stop` 逆序）、`Services` 类型化容器、`Events` 同步总线（`Emit` / `Bail` / `Serial` / `Parallel` / `Waterfall` 五种分发模式） |
| `internal/profiles` | 启动组合：bundle → profile patch → home patch → `--patch` 层叠，`--dump-config` 输出每行/每设置的来源 |
| `internal/llm` | `Client` 接口（可选 `StreamingClient`）、消息/工具调用模型、DeepSeek 兼容 HTTP + SSE 客户端、失败分类（`Failure`）与重试策略（`RetryPolicy`） |
| `internal/tool` | `Tool` 接口、`Registry`、`Pipeline`（`tools/pre-execute` waterfall/超时/panic/截断/事件）与批次调度（`ConcurrencySafe` + barrier + 模型序结果） |
| `internal/prompt` | 系统提示静态组装：`Section` 序列 + `Harness(Runtime)`（base 指令 + model/workspace/tools） |
| `internal/builtin` | 内置插件：`read`、`write`、`list_dir`、`edit`、`glob`、`grep`、`read_image`、`bash`/`pwsh`、`now`、`todo_write`、`working_directory` |
| `internal/agent` | 单会话 agent loop，负责模型调用（含重试）、工具批次执行与结果按模型序回填 |
| `internal/session` | 会话 v4 事件流（格式/投影/写入器/尾部修复）的内存模型与持久化 |
| `internal/server` | 内嵌 Web UI + JSON/SSE API + 优雅退出 |
| `internal/config` | JSON 配置 + 两层 `.env` + 环境变量覆盖 + 设置来源追踪 |
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

### 事件分发模式

`kernel.Events` 提供与上游 Cordis 对齐的五种分发方式，订阅者与分发者显式选择模式：

| 模式 | Go API | 语义 |
|---|---|---|
| emit | `On` + `Emit` | 顺序同步投递，忽略返回值，panic 被隔离 |
| bail | `OnHook` + `Bail` | 顺序执行，遇到"bail 值"（非 nil 且非 `false`）立即停止并返回 |
| serial | `OnHook` + `Serial` | 与 `bail` 相同：Go 里监听器都是普通函数调用，上游"同步/等待"的区分不存在 |
| parallel | `OnHook` + `Parallel` | 并发执行全部监听器，`errors.Join` 汇总错误 |
| waterfall | `OnWaterfall` + `Waterfall` | 洋葱模型：每个层收到 `next`，调用则继续、不调用即否决（含内置终点行为）；`next` 重复调用返回错误 |

真实消费者：工具流水线的 `tools/pre-execute`（见 §5），后续的 `tools/execute`、`agent/turn-stopping` 与 `webserver/authorize` 也走同一套模式。监听器 panic 统一归一化为 `*kernel.PanicError`（`errors.As` 可判别），调用方不必解析字符串。

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
       ├─ store.Resume(id) 或 store.Create("New session")   // Resume 会先补齐中断轮次
       └─ ChatRunner(ctx, session, input)        // cmd/dsh 提供
            └─ agent.Loop.Run(input)             // 先取会话租约，一次只允许一个 turn
                 ├─ store.Append(turn/start)
                 ├─ store.Append(system/message)  // 会话首轮写一次；历史已有则跳过
                 ├─ store.Append(user message)
                 ├─ 循环（最多 MaxIterations 次）：
                 │    ├─ store.Append(step/start)
                 │    ├─ store.Append(request/header)   // 信封变化才写，否则沿用
                 │    ├─ store.Append(request/context)  // 每个 loop 一次
                 │    ├─ llm.Client.Chat(messages, tools)
                 │    ├─ store.Append(assistant message)
                 │    ├─ 若有 tool_calls：
                 │    │    └─ tool.Pipeline.ExecuteBatch(calls)  // 并发安全可重叠，其余独占
                 │    │         └─ builtin tool.Execute(...)
                 │    │         └─ store.Append(state events)      // todo/write、working-directory/change 先落盘
                 │    │    └─ store.Append(tool result message)  // 按模型序
                 │    └─ 否则返回最终文本
                 └─ 发布 agent/started、agent/step、agent/finished
  └─ 200 {sessionId, reply}
```

关键取舍：

- **工具失败不终止循环**：`Pipeline.Execute` 永远返回 `Result`，失败以 `IsError` 结果回填给模型，让模型自行纠错（与上游一致）。
- **系统提示词**：由 `internal/prompt` 组装（base 指令 + runtime 上下文），首个 turn 作为 `system/message` 落盘；`agent.Options.SystemPrompt` 可覆盖。
- **请求信封**：`request/header` 自包含（provider/model + 工具 schema），信封不变就沿用上一条，所以一次回复的多个 step 只写一份；新进程续接写 `resume`。
- **模型失败先分类再重试**：一次失败的模型调用先归一化成 `llm.Failure{code, message, status?, providerRetryAfterMs?, requestId?}`，再由 `RetryPolicy` 决定是否重试（见 §10）。可重试且尚未向用户输出任何分片时，按 `llm/retry` → 等待 → `llm/retry-started` 的顺序重试同一次请求；**已经流出内容分片的尝试不重试**，避免终端/UI 出现重复文本。
- **消息顺序**：assistant(tool_calls) → 每个 tool 结果（带 `tool_call_id`）→ 下一次请求，符合 OpenAI/DeepSeek 协议。
- **工具状态先于结果落盘**：工具除了文本结果，还可以返回 `tool.Result.State`（例如 `todo/write`、`working-directory/change`）。loop 在写 `tool/result` 之前按模型序写入这些事件，并由 loop 补齐 `turn`/`step`——工具不知道自己在第几轮。这样"日志能重建状态"与"批次并发无关"同时成立。
- **取消保留已输出的前缀**：流式回答被中断时，用户已经看到的文本会作为 `assistant/message` 写入（紧随其后才是 `turn/end{aborted}`），下一次请求的历史与屏幕一致；非取消的流错误不写这条消息，避免伪造一个模型没说完的回答。
- **`turn/end` 记录结构化原因**：`completed`、`error`（带完整 `LlmFailure`：`error:{message,code,status?,providerRetryAfterMs?,requestId?}`）、`aborted`（带 `reason:{kind:"user"}`）、`max-tokens`（模型输出被 token 上限截断）；崩溃补齐的轮次写 `interrupted`。

## 5. 工具执行流水线

`Pipeline.Execute` 的固定顺序：

1. `tool/started` 事件；
2. `tools/pre-execute` waterfall：每个层拿到 `tool.Call` 与 `next`，调用 `next` 继续、不调用则否决（返回的 `tool.Result` 即最终结果）；
3. 查找工具（未知工具 → 错误结果）；
4. 加超时（默认 `config.toolTimeoutSec`）；
5. `recover` 工具 panic；
6. 输出按 `config.maxToolOutputBytes` 截断（UTF-8 边界安全）；
7. `tool/executed` 事件 + 结构化日志。

`Pipeline.AddGuard(fn)` 是 waterfall 上的一个便捷层：`fn` 返回 error 即拒绝（`TOOL_BLOCKED`），返回 nil 则放行。审批、沙箱、预算都会挂成 `tools/pre-execute` 的层，而不是改流水线本身。

新增工具：实现 `tool.Tool`（`Definition` + `Execute`），在某个 `kernel.Plugin.Apply` 里 `registry.Register` 即可。

工具失败带**结构化错误码**（`Result.Code`，写入 `tool/result` 的 `code` 字段）：`TOOL_TIMEOUT`（超时）、`TOOL_NOT_FOUND`（未注册）、`TOOL_BLOCKED`（guard/waterfall 拒绝）、`TOOL_PANICKED`（panic 恢复）、`ABORTED_BEFORE_DISPATCH`（批次取消没启动）。这样日志与 UI 不必解析自然语言就能区分失败类型。

工具还可以返回 `Result.State`：不进入对话、但需要从日志重建的状态变更。目前有两个消费者——`todo_write` 返回 `todo/write`，`working_directory` 在真正改变目录时返回 `working-directory/change`（只查询不产生事件）。事件类型必须在会话事件词汇表内，否则写入会被拒绝。

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
| `tools/pre-execute` | `tool.Call`（waterfall；层返回 `tool.Result`） | tool.Pipeline |
| `tool/started` | `tool.Call` | tool.Pipeline |
| `tool/executed` | `tool.ExecutionEvent{Call, Result, Duration}` | tool.Pipeline |
| `agent/started` | sessionId + input | agent.Loop |
| `agent/step` | `agent.StepEvent{Iteration, ToolCalls}` | agent.Loop |
| `agent/retry` | `agent.RetryEvent{Retry, MaxRetries, Mode, Code, Delay}` | agent.Loop |
| `agent/stream` | `agent.StreamEvent`（瞬态，不落盘） | agent.Loop |
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
{"type":"todo/write","data":{"turn":1,"step":1,"todos":[...]},"seq":6,"time":...}
{"type":"tool/result","data":{"message":{...}},"surfaceOp":"append","seq":7,"time":...}
{"type":"step/end","data":{...},"seq":8,"time":...}
{"type":"turn/end","data":{"reason":{"kind":"completed"}},"seq":9,"time":...}
```

- 只有 `system/message`、`developer/message`、`user/message`、`assistant/message`、`tool/result` 是 surface 事件（带 `surfaceOp`），其余是不进模型历史的记录事件；
- `system/message` 在会话首轮写一次（模型可见的系统提示必须可重建）；`request/header` 与 `request/context` 是记录事件，分别存请求信封（含工具 schema）与路由元信息；
- **读者不修盘**：`Load` 只读"已提交前缀"（尾部不完整或坏行在内存里丢弃），磁盘修复（`RepairTail`）只在写路径（`Resume` / `OpenWriter`）发生 —— 否则读者会截断写者正在写的行；
- 未知事件类型必须带 `ignorable:true`，否则读取端拒绝（前向兼容的安全阀）；
- `Recording.Project()` 把 surface 事件投影成模型消息（含 `replace` 语义与工具名反查）；
- 写入器保证原子创建、`seq` 续写与崩溃尾部修复；**同一会话只有一个写者**——进程内用占位表，跨进程用 sidecar 锁文件（`<id>.jsonl.lock`，POSIX `flock` / Windows `LockFileEx`，进程退出由内核释放）；`step/end` 与 `turn/end` 之后 `fsync`，掉电最多丢当前 step；
- **崩溃恢复**：日志若以打开的 `turn/start` 结束，`Store.Resume` 会在发布会话前补一批普通事件 —— 未应答的 `tool/call` 补 `TOOL_OUTCOME_UNKNOWN`、模型要过但没发起的补 `TOOL_NOT_STARTED`、补 `step/end`、补合成的 `turn/end{interrupted}`。`Store.Load` 保持只读，Web 的会话查看不会改日志；
- **轮次结束原因**：`completed`、`error`（带完整失败对象）、`aborted`（用户取消，带 `reason:{kind:"user"}`）、`max-tokens`（输出被上限截断）、`interrupted`（崩溃补齐）；
- **状态记录事件**：`todo/write`（完整 todo 列表）与 `working-directory/change`（root 相对路径）由工具通过 `Result.State` 产生，loop 补 `turn`/`step` 后落盘；续接会话时 `Store.Recording` 只读地回放这两类事件，把状态恢复到进程内的 `TodoStore` / `Workspace`（目录已被删除时退回工作区根，不阻断续接）；
- **`dsh sessions`** 用同一个只读路径列出会话：`turn/end` 原因与事件数来自日志，`messages` 来自投影；`--json` 输出同一结构；
- 细节与夹具出处见 [T-0008](tasks/T-0008-session-v4-format-layer.md)、[T-0009](tasks/T-0009-session-v4-projection-and-writer.md)、[T-0010](tasks/T-0010-session-v4-wiring.md)、[T-0018](tasks/T-0018-crash-recovery.md)、[T-0023](tasks/T-0023-batch9-sessions-and-state.md)。

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

## 10. 失败分类、重试、`.env` 与启动诊断

### 模型失败分类

`internal/llm/failure.go` 把任何模型调用失败归一化成 `*llm.Failure`：

```go
type Failure struct {
    Message              string `json:"message"`
    Code                 string `json:"code"`
    Status               int    `json:"status,omitempty"`
    ProviderRetryAfterMs int64  `json:"providerRetryAfterMs,omitempty"`
    RequestID            string `json:"requestId,omitempty"`
}
```

分类顺序（与上游 `transport.ts` 一致，先命中先返回）：`401/403` 或 `authentication_error` → `AUTH`；配额措辞或 `402` → `QUOTA`；`429` 或 `rate_limit_error` → `RATE_LIMIT`；上下文溢出的措辞规则 → `CONTEXT_WINDOW_EXCEEDED`；`400/413` 或 `invalid_request_error` → `INVALID_REQUEST`；`5xx` 或 `api_error`/`overloaded_error` → `SERVER`；其余 HTTP 状态 → `HTTP_<status>`。流内错误帧（无 HTTP 状态）落到 `SERVER`。上下文溢出与配额用正则识别**明确指向模型上下文**的措辞，所以"输出太长"不会被误判成上下文溢出。

另外：`DEEPSEEK_API_KEY` 缺失 → `MISSING_CREDENTIAL`，取消 → `ABORTED`，超时 → `TIMEOUT`，其余网络错误 → `TRANSPORT`，正常结束但无内容无工具调用 → `EMPTY_RESPONSE`（可重试）。

`Failure` 直接写进 `turn/end.error` 与 `llm/retry.failure`，字段名与上游 `LlmFailure` 相同，所以两种实现的日志可以互读。

### 重试策略

```go
policy := llm.DefaultRetryPolicy() // normal, 5 次, 500ms 起, 10s 上限, ±10% 抖动
policy.MaxRetries = cfg.MaxRetries
```

- **可重试码**：`EMPTY_RESPONSE`、`RATE_LIMIT`、`SERVER`、`TIMEOUT`、`TRANSPORT`；`AUTH`/`QUOTA`/`INVALID_REQUEST`/`CONTEXT_WINDOW_EXCEEDED` 一次都不重试——重复请求治不好它们。
- **退避**：`min(initial × 2^(retry-1), max)`，再乘 `[1-j, 1+j]` 的对称抖动；provider 给出 `Retry-After`（秒数或 HTTP date）时按它等待，超过上限则 normal 策略放弃重试。
- **持久化顺序**：`llm/retry`（含 `retryId`、`policyKey`、`delayMs`、`failure`）先落盘，再等待，再写 `llm/retry-started`。等待可被 ctx 取消，取消后本轮记 `aborted`。
- **不重试已输出分片的尝试**：流式响应一旦向用户输出过内容，失败就直接结束本轮，避免重复文本。

### `.env` 两层加载

`internal/config/env.go` 在读取配置**之前**套用两层 `.env`：项目层（当前工作目录）与数据层（配置目录），优先级 `已导出环境变量 > 项目层 > 数据层`。两个文件都先解析、校验，再统一应用，所以被拒绝时环境完全不变。**引导变量**（`PATH`、`DSH_*`、`XDG_*`、`DYLD_*`、代理/TLS 信任锚、解释器与 VCS 钩子）只允许由启动环境设置；数据层额外允许代理名与精确拼写的 `DEEPSEEK_BASE_URL`（仓库自带的项目层不行）。

上游没有 `DSH_PERMISSION_MODE` 这个环境变量：权限模式在上游是 provider/headless 的 `permissionMode` 配置，真正生效要靠 `permission-presets` 与沙箱服务，属于 P2。本实现因此**不发明**该环境变量，权限模式随沙箱/审批一起做（见 [D-0009](decisions/D-0009-operability-batch.md)）。

### 日志与启动诊断

- 结构化日志（`log/slog`）同时写 stderr 与 `config.logFile`（默认 `<数据目录>/logs/dsh.log`，`off` 关闭）。日志文件打不开只告警、不阻断启动。
- 启动失败（`--config` 解析、profile 解析、`boot`）时：stderr 打印原因，并把报告写到 `<数据目录>/logs/startup-<时间戳>-<随机>.log`（权限 0600，目录 0700），末行打印 `Full diagnostics: <path>`。报告含时间、版本、Go 版本、平台、profile、home、config 路径与完整错误，并带"可能含敏感值，分享前先检查"的警告；写不出去时改为把报告正文打到 stderr。
- `dsh config` 额外打印实际套用的 `.env` 路径。

## 11. 后续演进建议

1. **P1 收尾**：resume 的 `interruptedTurnClosers`、`request/header` 可重建、`-race` 下并发会话压测。
2. **P2**：引入 `ctx.fs`、`ctx.shell`、`ctx.sandbox` 等服务 seam，为沙箱策略（guard 链）与审批事件做铺垫；工具输出 spill。
3. **P3**：MCP 客户端、LSP、Skill 目录、Subagent 子会话与 Team 任务板。
4. **P4**：Web RPC/事件流兼容层、多会话前端、Desktop 壳与插件市场。
5. **持续**：对齐上游 session JSONL / 工具 schema，保证跨实现可复现与可迁移。

## 12. 安全边界（当前阶段）

| 能力 | 限制 | 备注 |
|---|---|---|
| `read`/`write`/`list_dir` | 工作区路径白名单（拒绝 `..` 与绝对路径逃逸） | 已实现 |
| `pwsh`/`bash` | 无沙箱，继承宿主用户权限 | 等价上游 `danger-full-access`；P2 引入 read-only/workspace-write 策略 |
| 网络 | LLM 客户端仅访问配置的 `baseUrl` | 工具命令可发起任意网络请求（同 shell 限制） |
| 凭据 | API key 只从环境变量或 `.env` 读取，不写入会话/仓库 | 会话 JSONL 只含模型消息与工具结果；`.env` 拒绝引导变量 |
| 审批 | `tool.Pipeline.AddGuard` 提供否决钩子，`tools/pre-execute` waterfall 接受任意策略层 | 默认无 guard；P2 接入审批/策略链 |

> 设计取舍：P0/P1 优先跑通闭环，沙箱与审批按迁移计划 P2 再做；在此之前请把 harness 视为与用户同等权限的本地进程。
