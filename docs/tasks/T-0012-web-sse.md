# T-0012 · 批次 3 收口：Web SSE

- **状态**：done
- **更新**：2026-11-21
- **关联决策**：D-0002（用户"继续"授权）、D-0007（补测试默认授权）

## 目标

让 Web 端也享受流式：`POST /api/chat` 在客户端请求 `text/event-stream` 时改用 SSE 推送增量，前端逐字渲染；普通客户端行为不变。

## 设计

- **不新增路由，用 Accept 协商**：`Accept` 含 `text/event-stream` 才走 SSE，否则仍是原来的单 JSON 响应 —— 老的 curl / 测试 / 第三方调用零影响。
- **复用事件总线**：`Server.Options` 增加可选 `Events`；处理器订阅 `agent/stream`，按 `SessionID` 过滤后写成 `data: {...}` 帧。
- **帧类型**：`delta`（`text`）、`done`（`sessionId` + `reply`）、`error`（`message`）。
- **写失败即取消**：客户端断开后 `Write` 报错 → 取消 `Runner` 的 context，模型请求随之终止，不空转。
- **前端用 `fetch` + `ReadableStream` 而不是 `EventSource`**：`EventSource` 只支持 GET，而聊天是 POST。

## 步骤

1. `internal/server/server.go`：`Options.Events`、`wantsEventStream`、`streamChat`（SSE 头、`flusher`、订阅/退订、错误帧）。
2. `cmd/dsh/main.go`：`runWeb` 传入 `rt.app.Events()`。
3. `internal/server/index.html`：`bubble()` 改为带 `.body` 节点、新增 `setBubbleText`、`consumeSSE` 解析器、`submit()` 改为流式渲染（保留错误帧与异常兜底）。
4. 测试与真实进程验证。

## 验证

| 验证 | 结果 |
|---|---|
| `TestChatStreamsDeltasThenDone` | `Content-Type: text/event-stream`；2 个 `delta` 帧（`Hel`/`lo`）+ `done` 帧（`reply:"Hello"`） |
| `TestChatStreamIgnoresOtherSessions` | 其它会话的 `agent/stream` 事件被过滤，不泄漏 |
| `TestChatStreamReportsRunnerError` | `Runner` 失败 → `error` 帧带原消息 |
| `TestChatWithoutAcceptStaysJSON` | 不带 `Accept` 时仍是 `application/json` 单对象（向后兼容） |
| **真实 `dsh web` 进程 + mock 流式模型** | `/healthz` 正常；`/` 返回 6817 字节且含 `text/event-stream` 前端逻辑；`POST /api/chat`（Accept SSE）收到 `delta`×3 + `done`（`reply:"web-stream"`） |
| 质量门禁 | `gofmt` clean、`go vet` 0、`staticcheck` 0、`go test ./...` 全绿（server 9 个、全仓库 95 个） |

## 自动决策

- **Accept 协商优于新路由**：`/api/chat` 的语义不变，客户端只需加一个请求头；避免维护两条几乎相同的端点。
- **SSE 帧用 JSON**：前端 `JSON.parse` 即可，未来加字段（工具进度、用量）不破坏解析。
- **`Events` 可选**：未注入事件总线时仍能返回 `done` 帧（只是没有增量），便于嵌入与测试。
- **前端异常兜底**：`onFrame` 抛错时 `reader.cancel()` 并冒泡到 `try/catch`，把气泡标红而不是静默卡住。

## 遗留与下一步

- **浏览器端手工验证未做自动化**（无 headless 浏览器），JS 只做了内容级断言（页面含 SSE 逻辑）+ 协议级真机请求验证。
- **断线重连/续传**、`assistant/stream` 落盘、工具执行期间的进度帧。
- 批次 4（profiles + `--dump-config`）待开工。
