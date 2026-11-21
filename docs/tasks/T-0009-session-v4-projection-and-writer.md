# T-0009 · 批次 2 第二/三步：v4 投影层与持久化写入器

- **状态**：done
- **更新**：2026-11-21
- **关联决策**：D-0002（用户"继续"授权）、D-0007（补测试默认授权）

## 目标

在 T-0008 的格式层之上补齐两块：**事件流 → 模型消息**的投影，以及**可安全追加、可崩溃修复**的 v4 JSONL 写入器。仍然不接线到 `Store`/`agent.Loop`，保持行为不变。

## 上游依据（实测）

| 事实 | 来源 |
|---|---|
| surface 事件仅 5 类：`system/message`、`developer/message`、`user/message`、`assistant/message`、`tool/result` | `types.ts` 的 `SurfaceEventType` |
| `user/message` 的 `data` 就是消息本身；其余 4 类是 `{turn, step, message}` 包一层 | 真实样本 |
| assistant 消息的 `content` 是块数组，含 `text` / `reasoning` / `tool-call`（`id/name/arguments`） | 真实样本 |
| `tool/call` 是**log-only**事件，提供 `callId → name`；`tool/result` 的 `message` 只有 `toolCallId`，没有名字 | 真实样本 |
| `surfaceOp` 的 replace 形如 `{op:"replace",startSeq,endSeq}` | `types.ts` |

## 范围

- **包含**：`internal/session/projection.go`（`Recording.Project()`、surface 重放、replace 替换）、`persist_jsonl.go`（`Writer`、`CreateSessionFile`、`OpenWriter`、`RepairTail`、`ReadRecording`）与对应测试。
- **不包含**：替换 `Store`、接入 `agent.Loop`、跨进程文件锁、投影缓存、compaction（replace 的生产者）。

## 验证

| 用例 | 结果 |
|---|---|
| `TestProjectUpstreamFixture` | 真实样本投影出 **16 条消息**（= 16 个 surface 事件）：user → assistant(2 个 tool calls) → tool(read，名字由 `tool/call` 反查) … |
| `TestProjectAppliesReplaceSurfaceOp` | replace 0..1 后表面只剩 2 条：替换节点 + 未受影响的第 3 条 |
| `TestProjectRejectsOutOfRangeReplace` | 越界 replace 报错而非静默 |
| `TestProjectMapsDeveloperMessageToSystem` | developer/message → system 角色 |
| `TestProjectIgnoresLogOnlyEvents` | `turn/start`、`tool/call`、`step/end` 不产生消息 |
| `TestCreateSessionFileWritesHeader` | 头部原子落盘，字段（id/cwd/delegationDepth/agentPreset）完整 |
| `TestWriterAppendsSequentialEvents` | seq 0、1；time 为毫秒时间戳 |
| `TestOpenWriterContinuesSequence` | 重新打开后 seq 从 2 续写 |
| `TestSecondWriterIsRejected` | 同一进程内第二个写者被拒（`ErrWriterExists`），Close 后可重新打开 |
| `TestWriterRejectsUnknownEventType` | 写入未知类型报错（与读取规则对称） |
| `TestRepairTailDropsPartialTrailingRecord` | 半行 JSON 被截断，且二次修复为 no-op |
| `TestRepairTailDropsInvalidCompleteRecord` | 以换行结尾但解析失败的行也被截断 |
| 质量门禁 | `gofmt` clean、`go vet` 0、`staticcheck` 0、`go test ./...` 8 包全绿 |
| 规模 | session 包 26 个用例、全仓库 84 个 |

## 自动决策

- **reasoning / image 块丢弃**：`llm.Message` 只有文本与工具调用字段，P1 不引入 reasoning 通道；assistant 若只有 reasoning，投影出空 Content。
- **`developer/message` 投影成 system**：llm 没有 developer 角色，语义上属于系统级指令。
- **tool 消息的 name 从 `tool/call` 反查**：`tool/result` 只带 `toolCallId`；不反查会丢失 `llm.Message.Name`。
- **`isError` 丢弃**：`llm.Message` 没有该字段，工具失败信息已经在 content 里；如需结构化，等接入 agent 时再加。
- **replace 现在就实现、但 P1 不生产**：compaction 是 P3 才写 replace；读侧先支持，避免以后读到大模型会话直接失败。
- **单写者锁只覆盖进程内**：跨进程文件锁需要平台相关实现或第三方依赖（Q-0002 默认不引依赖），留到沙箱步骤；`Writer` 内部 mutex + 进程级 registry 保证同一会话不会被同进程的两个写者交错。
- **提交语义**：只有"以换行结尾且 JSON 合法"的行才算已提交，崩溃尾部一律截断——对应上游"不丢已提交前缀"的验收。
- **创建走 temp+rename**：避免崩溃留下半个 header。

## 遗留与下一步

- **接入 `Store` 与 `agent.Loop`**：让 CLI/Web 的会话落盘改为事件流，`Load` 走 `ReadRecording` + `Project()`。
- **跨进程单写者锁**、**投影缓存**、**compaction replace 生产**、**reasoning/image 映射**。
