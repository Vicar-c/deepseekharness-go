# T-0023 · 批次 9：会话管理、状态恢复与取消保留前缀

- **状态**：done
- **更新**：2026-11-22
- **关联决策**：D-0002（用户"继续"授权批次 9）、D-0007（补测试默认授权）、D-0008（文档同批更新）
- **待办来源**：[BACKLOG](../BACKLOG.md) 批次 9 → B-35 / B-31 / B-32 / B-34

## 目标

清掉 P2 的第一组"小件"：两个从不生效的模型设置、两类从不落盘的状态、一个缺失的会话入口、一处会丢数据的取消语义。

## 范围（写作用域）

- `internal/config/`：`config.go`、`source.go`（`maxTokens`、`contextWindow`）
- `internal/agent/`：`loop.go`（请求带上限、取消保留前缀、工具状态落盘）
- `internal/tool/`：`tool.go`（`Result.State`、`StateEvent`、`TurnStepSetter`）
- `internal/builtin/`：`todo_tools.go`（`TodoWriteData`、共享 `TodoStore`）、`fs_tools.go`（`WorkingDirectoryData`）、`workspace.go`（`ResetCwd`、`RestoreCwd`）
- `internal/session/`：`session.go`（只读 `Store.Recording`）
- `cmd/dsh/`：`sessions.go`（新子命令）、`main.go`（usage/switch、`restoreSession`、runtime 持有 workspace/todos）
- 文档：README / ARCHITECTURE / BACKLOG / STATUS / 本记录

## 非目标

- 不做 compaction（B-24）、不做 sandbox/seam（B-20/B-21）。
- 不做 Web UI 会话列表（B-33，需先拍 B-12）。
- 不做"取消后自动重试"或"续写半截回答"：只把已交付前缀记进历史。

## 设计

1. **B-35 模型上限**：`config` 新增 `maxTokens`、`contextWindow`，两者默认 `0` = 不设置（不是"缺失默认值"），只拒绝负数；`DSH_MAX_TOKENS` / `DSH_CONTEXT_WINDOW` 可覆盖。`agent.Options` 新增 `MaxTokens`，请求体带 `max_tokens`；`ContextWindow` 早就有字段但 `cmd/dsh` 从不赋值，现在写进 `request/context`。
2. **B-31 状态落盘**：`tool.Result` 新增 `State []StateEvent`；工具返回"要记的事件"，loop 在 `tool/result` 之前按模型序写入，并通过 `tool.TurnStepSetter` 补 `turn`/`step`（工具不知道自己在第几轮）。`todo_write` 产生 `todo/write`（含完整列表），`working_directory` **只在真正改变目录时**产生 `working-directory/change`（root 相对路径）。
3. **B-31 恢复**：新增只读 `Store.Recording(id)`（不修盘、不加锁、不建写者）。`cmd/dsh` 在每次开跑前 `restoreSession`：先重置（root + 空 todo，新会话必须干净），再按顺序回放两类事件；目录已被删除时退回工作区根而不是拒绝续接。恢复状态放在 `runtime` 上，`todo` 插件通过 `NewTodoPluginWithStore` 共享同一个 store。
4. **B-32 `dsh sessions`**：`dsh sessions` 列出 id/更新时间/轮次/消息数/最后 turn 结果/标题，`--json` 输出同一结构；`dsh sessions <id>` 打印详情（含日志路径与事件数）。"最后 turn 结果"来自日志里的 `turn/end` 原因，读失败时仍列出并标注 `unreadable`。usage、switch、README 命令表同批更新（由 `docs_test.go` 强制）。
5. **B-34 取消保留前缀**：`chat` 返回"本次尝试已经交付的文本 + 分片数"；取消时若有文本，先写 `assistant/message`（surface），再写 `step/end` 与 `turn/end{aborted}`，并镜像进内存历史，所以下一次请求与用户看到的屏幕一致。非取消的流错误**不写** assistant 消息（不伪造模型没说完的回答）。

## 验证

| 验证 | 结果 |
|---|---|
| 配置用例 | `TestOptionalModelBounds`：默认 0、文件值、env 覆盖、来源、负数拒绝；`TestSetAcceptsZeroBounds`：patch 可显式清零、负数仍拒绝 |
| 请求用例 | `TestLoopAppliesModelBounds`：`max_tokens=512` 进请求、`request/context.contextWindow=65536`；`TestLoopOmitsUnsetBounds`：未设置时请求不带 `max_tokens`、`request/context` 不含 `contextWindow` |
| 工具用例 | `TestTodoWriteReportsState`（含清空仍记录）、`TestTodoPluginSharesStore`、`TestWorkingDirectoryReportsChanges`（查询不产生事件）、`TestWorkspaceRestoreCwd`（根相对、越界拒绝、缺失目录报错） |
| loop 用例 | `TestLoopPersistsToolState`：状态事件落在 `tool/call` 与 `tool/result` 之间，payload 的 `turn/step = 1/1` |
| 恢复用例 | `TestRestoreSessionReplaysState`（后写覆盖先写）、`TestRestoreSessionResetsForANewSession`、`TestRestoreSessionToleratesADeletedDirectory` |
| 只读路径 | `TestRecordingReadsWithoutRepairing`：撕裂尾行可读、**文件字节不变**、缺失会话报错 |
| 会话语义 | `TestCancelledTurnKeepsTheStreamedPrefix`（前缀先于 `turn/end{aborted}`、进入内存历史）、`TestFailedAttemptWithoutCancellationRecordsNoAnswer`（非取消失败不写 assistant 消息） |
| CLI 用例 | `TestSessionsListsStoredSessions`（表格 + `--json`）、`TestSessionsShowsOneSession`、`TestSessionsRejectsAnUnknownID`、`TestSessionsReportsNoSessions` |
| **真实 DeepSeek API** | 一轮里让模型调用 `todo_write` 与 `working_directory`：exit 0；日志出现 `request/context{contextWindow:65536}`、`todo/write{turn:1,step:1,todos:[...]}`、`working-directory/change{turn:1,step:2,directory:"sub"}` |
| **真实续接（新进程）** | 同一会话 `--session` 续接，要求模型调用 `working_directory`（无参数）：返回 `sub` —— 记录的工作目录被恢复 |
| **`dsh sessions` 实跑** | 表格行显示 `completed`、`TURNS 1`、`MESSAGES 7`；`--json` 字段齐全（id/title/times/messages/turns/events/lastTurn/path） |
| 质量门禁 | `gofmt -l .` 无输出、`go vet ./...` 0、`staticcheck ./...` 0、`go test ./...` 全绿（**268 个用例**，批次 8 后为 249）、`scripts/build.ps1` 通过 |
| **未验证** | 真实 Ctrl+C 下的前缀落盘（用单元测试的取消上下文覆盖，未在交互式终端实测）；Windows 之外 `RestoreCwd` 的大小写/符号链接语义只由 CI 覆盖 |

## 自动决策

- **`Result.State` 而不是"工具直接写会话"**：工具没有会话句柄，也不该知道 turn/step；把"要记什么"交给工具、"什么时候记"交给 loop，批次并发下顺序仍然确定（状态先于结果、结果按模型序）。
- **状态事件类型必须在会话词汇表内**：写入器会拒绝未知类型——这是有意的，宁可报错也不写出读者解释不了的日志。测试里用 `todo/write` 作为探针就是这个原因。
- **恢复前先重置**：新会话必须从 root + 空 todo 开始；否则 Web 下两个会话会互相继承状态。代价是进程内状态是"每轮开始时按日志恢复"，而不是"每会话独立对象"（共享 workspace 的既有模型，已在文档写明）。
- **`Store.Recording` 保持只读**：它会被 `dsh sessions` 与恢复路径调用，绝不能修盘——否则查看会话会截断写者正在写的行。
- **取消与非取消区别对待**：只有取消（用户已看到的文本确实属于这次回答）才补 `assistant/message`；流错误补记录会伪造一个模型没说完的回答，留给以后有 `assistant/attempt` 时再做。
- **`working_directory` 只在改变时记录**：查询是只读操作，写事件会让日志充满噪声。
- **`dsh sessions` 读日志算 `LAST TURN`**：`Summary` 不扩字段，避免改 Web 的列表契约；列表规模是本地几十条，逐条读可接受。

## 遗留与下一步

- **恢复是"每轮开始"而不是"每会话对象"**：同一进程内多个会话共享一个 `Workspace` 与 `TodoStore`，并发 Web 会话会互相覆盖（既有模型限制）；B-33 若要做多会话 UI，需要先按会话隔离这两份状态。
- **`dsh sessions` 没有分页/过滤**：会话多了以后需要 `--limit`/`--since`。
- **取消前缀只在 CLI/agent 层验证**：交互式终端真实 Ctrl+C 未实测。
- 下一批：**批次 10**（B-14 反向互操作 + B-40 staticcheck 进 CI + B-43 假端点重试回归），见 [BACKLOG](../BACKLOG.md)。
