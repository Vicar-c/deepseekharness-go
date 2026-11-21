# T-0011 · 批次 3 第一步：流式（LLM → agent → CLI）

- **状态**：done
- **更新**：2026-11-21
- **关联决策**：D-0002（用户"继续"授权）、D-0007（补测试默认授权）

## 目标

让模型输出以增量方式到达调用方：`llm` 提供可选流式契约，`agent.Loop` 在客户端支持时改用流式并把增量发成瞬态事件，CLI 逐字打印。Web SSE 留到下一步。

## 设计

- **不破坏既有契约**：新增 `llm.StreamingClient`（内嵌 `Client`），`DeepSeek` 同时实现两者；调用方用类型断言决定走流式还是阻塞式，非流式客户端（含测试 fake）完全不受影响。
- **`llm.Chunk` 单字段语义**：`Content` / `ToolCallID` / `ToolName` / `ToolArgs` / `FinishReason` 每次只承载一种增量。
- **组装与流式同源**：`ChatStream` 返回与 `Chat` 相同形状的 `Response`（完整 message + finish_reason），增量只是旁路。
- **瞬态 vs 持久**：`agent/stream` 是事件总线上的瞬态事件，不落盘；落盘的仍是完整的 `assistant/message`，`stream` 字段为空数组（P1 不记录逐帧时间线）。

## 步骤

1. `internal/llm/llm.go`：`Chunk` + `StreamingClient`。
2. `internal/llm/deepseek.go`：抽出 `newHTTPRequest`（`stream` 开关），实现 `ChatStream`（SSE 逐行解析、跳过注释/未知帧、`[DONE]` 结束、非 2xx 读体报错）+ `streamAssembler`（文本、按 index 合并的 tool-call 增量、finish reason）。
3. `internal/agent/loop.go`：`chat()` 类型断言，`ChatStream` 回调转成 `agent/stream` 事件；`Run` 改用它。
4. `cmd/dsh/main.go`：订阅 `agent/stream` 逐字打印；有流式输出时不再重复打印最终回复（`streamed` 标志）。
5. 测试与真机验证。

## 验证

| 验证 | 结果 |
|---|---|
| `TestChatStreamDeliversDeltasAndAssemblesResponse` | 请求带 `"stream":true` 与 `Accept: text/event-stream`；增量 `Hel`+`lo` 按序到达；组装结果 `Hello`/`stop`；SSE 注释行被忽略 |
| `TestChatStreamAssemblesToolCallDeltas` | tool-call 增量按 index 合并：`id`、`name`、分片 `arguments` 拼成 `{"path":"a"}`，finish `tool_calls` |
| `TestChatStreamReportsHTTPError` | 401 → 错误含 `HTTP 401` 与响应体摘要 |
| `TestChatStreamStopsWhenCallbackFails` | 回调返回错误立即中止并透传 |
| `TestChatStreamRequiresAPIKey` | 无 key → `ErrMissingAPIKey` |
| `TestLoopStreamsDeltasAndPersistsOnce` | 三个增量按序聚合成 `Hello world`；落盘只有 user + assistant 两条，assistant 内容完整且**只存一次** |
| 真实 CLI + SSE mock | stdout 恰好 `Hello from mock`（`Hello from mock` 出现 **1 次**，无重复）；落盘 assistant 内容一致 |
| **真实 DeepSeek API（流式）** | `--prompt "Reply with exactly: stream-ok"` → 输出 `stream-ok`，退出码 0；落盘 assistant 内容 `stream-ok` |
| 质量门禁 | `gofmt` clean、`go vet` 0、`staticcheck` 0、`go test ./...` 全绿（llm 9 个、全仓库 91 个） |

## 自动决策

- **可选接口而非改 `Client`**：避免所有 fake/第三方实现被迫跟进，也保证 Web 端非流式路径不受影响。
- **回调式而非 channel**：调用方在同一 goroutine 内接收增量，无需处理 goroutine 泄漏与背压；回调返回错误即中止。
- **流式事件不落盘**：上游 `assistant/message.data.stream` 记录逐帧时间线，形状复杂（`chunk`/`reasoning-chunks`/`tool-call-chunks`），P1 先写空数组，避免产出半正确的 schema。
- **CLI 只打印一次**：用 `streamed` 标志区分"已逐字输出"与"整段回复"，否则会重复。
- **SSE 解析容错**：跳过 `:` 注释与 `event:`/`id:` 行，只处理 `data:`；`[DONE]` 主动结束，防止服务端不关连接时挂死。

## 遗留与下一步

- **Web SSE**：`/api/chat` 支持 `text/event-stream`、前端 `EventSource` 渲染、断线处理。
- **`assistant/stream` 落盘**、流式下的 tool-call 进度展示、取消语义（客户端断开 → 中止请求）。
