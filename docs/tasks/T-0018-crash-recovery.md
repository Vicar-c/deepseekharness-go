# T-0018 · 批次 5：崩溃恢复补齐、取消语义与错误码

- **状态**：done
- **更新**：2026-11-22
- **关联决策**：D-0002（用户"先 A"授权批次 5）、D-0007（补测试默认授权）、D-0008（文档同批更新）
- **待办来源**：[BACKLOG](../BACKLOG.md) 批次 5 → B-01 / B-02 / B-03 / B-04 / B-05

## 目标

修掉**唯一已实测损坏**的一处：进程在工具调用中途被杀后，会话无法 resume（真实 DeepSeek API 返回 400）。同时把同批相关的小缺口补齐：同会话 turn 互斥、会话文件 id 校验、BOM 容忍、工具结果错误码、取消语义与 CLI 退出码。

## 范围（写作用域）

- `internal/session/`（`interrupted.go` 新增、`session.go`、`persist_jsonl.go`、`format.go`）
- `internal/tool/`（`tool.go`、`pipeline.go`、`batch.go`）
- `internal/agent/loop.go`
- `cmd/dsh/`（`main.go`、`signals.go`）
- 文档：README / ARCHITECTURE / BACKLOG / STATUS / 本记录

## 非目标

- 不做跨进程写者锁（B-10，批次 7）。
- 不做每批 fsync 屏障（B-09，批次 7）。
- 不做 `request/header` 与系统提示落盘（B-07/B-08，批次 6）。
- 不引入第三方依赖。

## 设计

1. **恢复事件是普通事件**：resume 时把"未闭合的轮次"补齐为一批正常事件（`tool/result` → `step/end` → `turn/end{interrupted}`），通过同一写入器追加，投影后历史自然配平。恢复逻辑做成纯函数 `InterruptedTurnClosers(recording)`，不碰 IO，便于对三种截断形态做表驱动测试。
2. **写所有权**：`Store.Resume(id)` 负责"读 → 算闭合事件 → 追加 → 重新投影"，`Store.Load(id)` 保持只读（Web 的 `GET /api/sessions/{id}` 不得改日志）。
3. **同会话互斥**：`Store.BeginTurn(id)` 用一个进程内租约表拒绝并发 turn；`agent.Loop.Run` 开头取租约、结束释放，第二个并发 turn 直接报 `ErrSessionOwned`，不再交错写 `turn/start`。
4. **文件身份**：`Load` 校验 header 的 `id` 与文件名一致，不一致时报明确错误（而不是拿着 header id 去找不存在的文件）。
5. **BOM**：`Decode` 在首行剥掉 UTF-8 BOM；`RepairTail` 校验尾行时同样剥掉，否则带 BOM 的单行文件会被误判为撕裂尾部而**截断成空文件**。
6. **错误码**：`tool.Result` 增加 `Code`，流水线在超时/取消/未注册/被 guard 拒绝/panic 五条路径上给出稳定代码；`tool/result` 载荷带 `code` 字段（仅 `isError` 时）。
7. **取消**：模型调用因 ctx 取消而失败时 `turn/end` 记 `cancelled`；CLI 首次 SIGINT 取消并宽限 5 秒、退出码 130，SIGTERM 退出码 0，第二次信号立即退出。

## 步骤

1. `tool`：`Result.Code` + 代码常量 + 流水线/批次赋值。
2. `session`：`interrupted.go`（closers 纯函数）、`Resume`、`BeginTurn`、`Load` id 校验、BOM。
3. `agent`：取/放租约、`cancelled` reason、`tool/result` 带 code。
4. `cmd/dsh`：`signals.go` + `runChat`/`runWeb` 接信号与退出码。
5. 测试与真实 API 验证。

## 验证

| 验证 | 结果 |
|---|---|
| `go test ./internal/session` | 新增 7 组用例：closers 五种形态（含"两调用只答一个"）、修复幂等、Resume 端到端、并发 Resume 只修一次、header id 不符、BOM（含 `RepairTail` 不误截断）、turn 租约 |
| `go test ./internal/tool` | 新增 `TestResultCodes`（超时/未注册/guard 拒绝/panic 四条路径的 code）+ `TestBatchReportsAbortedCode` |
| `go test ./internal/agent` | 新增并发 turn 被拒（`ErrSessionOwned`）、取消轮次记 `turn/end{cancelled}`、失败后租约释放 |
| `go test ./internal/server` | 新增 `TestChatRepairsAnInterruptedSession`：`POST /api/chat` 续接被截断的会话 → 日志出现 `turn/end{interrupted}` |
| `go test ./cmd/dsh` | 新增信号退出码映射（SIGINT=130 / SIGTERM=0 / 首信号优先）与 `failureCode` |
| **真实 DeepSeek API：4 种截断位置各 resume 一次** | 全部 exit 0、模型接受修复后的历史：① 截到 `step/start` 后 → 补 `step/end` + `turn/end{interrupted}`；② 截到 assistant（模型要了 2 个 read 但没发起）→ 补 2×`TOOL_NOT_STARTED`；③ 截到 2 个 `tool/call` 后 → 补 2×`TOOL_OUTCOME_UNKNOWN`；④ 截到第一个 `tool/result` 后 → **只**给第二个调用补 `TOOL_OUTCOME_UNKNOWN`，已完成的那个保持原样 |
| 修复前基线 | 同样场景在批次 4 时被真实 API 以 **HTTP 400**（dangling `tool_calls`）拒绝（见 [T-0016](T-0016-gap-audit.md) §2②） |
| 浏览器 E2E | `samples=["web","web-","web-stream"]`，`no-error-bubble` |
| 质量门禁 | `gofmt` clean、`go vet` 0、`staticcheck` 0、`go test ./...` 全绿（**142 个用例**）、`scripts/build.ps1` 通过 |
| **未验证** | 真实 `Ctrl+C` 未自动化：Windows 无法可靠向子进程注入 SIGINT（`Stop-Process` 是强制终止、Node `child.kill` 也不会走 Go 的信号处理），只做了退出码映射与 `failureCode` 的单测 |

## 自动决策

- **Resume 与 Load 分离**：`Load` 保持只读（Web 的 `GET /api/sessions/{id}` 不能改日志），只有"即将写入"的入口（`dsh chat --session`、`POST /api/chat`）走 `Resume`。
- **恢复事件是普通事件**：补的 `tool/result` 带 `surfaceOp:"append"`，直接复用现有投影，不引入"虚拟消息"分支；`TOOL_NOT_STARTED`/`TOOL_OUTCOME_UNKNOWN` 按上游语义区分"记了 `tool/call` 但没结果"与"模型要了但没发起"。
- **同进程串行化**：`Store.repairMu` 保证两个并发 `Resume` 不会各补一份闭合事件（有用例）。
- **租约放在 Store**：同一 Store 下所有 loop 共享一张租约表，比放在 Loop 上更接近"每个会话单写者"的真实约束；`Run` 用 `defer` 释放，失败路径不会泄漏（有用例）。
- **错误码集中在 `tool` 包**：`session` 复用 `tool.CodeNotStarted` / `tool.CodeOutcomeUnknown`，避免两处字符串漂移。
- **BOM 同时修两处**：`Decode` 剥首行 BOM；`RepairTail` 校验尾行时也剥 —— 否则"带 BOM 的单行文件"会被判成撕裂尾部并**截断成空文件**（这是本次发现的次生 bug，已加用例钉住）。
- **信号策略**：首信号取消 + 5 秒宽限 + 第二信号立即退出；退出码映射抽成纯函数 `exitCodeForSignal` 以便单测，不把 `os.Exit` 写进可测路径。

## 遗留与下一步

- **跨进程互斥仍未做**（B-10）：`BeginTurn` 租约与 `repairMu` 都只在单进程内有效。
- **取消时不保留已流式输出的文本**：上游会把已交付的前缀落进 `assistant/message`，我们目前直接记 `turn/end{cancelled}`，下一条请求看不到用户已看到的半截回答。
- **恢复只处理"最后一个未闭合轮次"**：正常写入不会产生多个未闭合轮次，若外部手改日志则只补最后一个。
- **`turn/end{interrupted}` 在 CLI 里不显式提示**：用户只会看到下一次请求正常，日志里才看得到。
- 下一批：**批次 6**（B-06 并发压测 + B-07 `request/header` + B-08 系统提示落盘），见 [BACKLOG](../BACKLOG.md)。
