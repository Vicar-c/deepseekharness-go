# T-0010 · 批次 2 收口：Store 与 agent.Loop 接入 v4

- **状态**：done
- **更新**：2026-11-21
- **关联决策**：D-0002（用户"继续"授权）、D-0007（补测试默认授权）

## 目标

把 T-0008/T-0009 的格式层、投影层、写入器接进真实路径：CLI 与 Web 的会话落盘改为 v4 事件流，`Load` 走"修复 → 解码 → 投影"，`agent.Loop` 一次对话产出一条合法的 v4 日志。

## 步骤

1. **Store 事件化**（`internal/session/session.go`）：
   - `Create` 写 header（`type:session`、version 4、id、createdAt(ms)、cwd、isSeeded、delegationDepth）+ 一条 `session/title` 事件；
   - 新增 `AppendEvent`（线上任意事件）与 `SetWorkspace` / `Close`（统一释放写者）；
   - `Load` = `RepairTail` → `ReadRecording` → `Project`，标题从 `session/title` 取、`UpdatedAt` 取最后事件、`Turn` 取最大 `turn/start`；
   - 保留 `Append(llm.Message)` 作为兼容入口（Web 测试等只持有消息的调用方使用）。
2. **Loop 发射事件**（`internal/agent/loop.go`）：一次 `Run` 写 `turn/start → user/message → (step/start → assistant/message → tool/call* → tool/result* → step/end)+ → turn/end`；错误路径补 `step/end` 与 `turn/end{kind:error}`。
3. **接线**（`cmd/dsh/main.go`）：`store.SetWorkspace(cfg.Workspace)`；`runtime.close()` 关闭 Store。
4. **测试**：更新旧格式断言（jsonl 行数改为 header+title+2 事件），所有 Store 用法加 `t.Cleanup(Close)`（Windows 句柄会阻塞临时目录清理），新增落盘事件序列测试。

## 验证

| 验证 | 结果 |
|---|---|
| `TestLoopPersistsV4EventStream` | 事件序列与预期**逐项一致**（session/title、turn/start、user/message、step/start、assistant/message、tool/call、tool/result、step/end、step/start、assistant/message、step/end、turn/end）；4 个 surface 事件带 `append`；投影出 4 条消息；`tool/call` 的 name/arguments/callId 与 `turn/end{kind:completed}` 均正确 |
| `TestStoreCreateAppendLoad` | 文件 4 行（header + title + 2 事件）；`Load` 投影出 2 条消息且标题正确 |
| 真实 CLI + 本地 mock 模型 | 退出码 0，输出 "mocked answer"；落盘 8 行，事件序列 `title → turn/start → user/message → step/start → assistant/message → step/end → turn/end` |
| **真实 DeepSeek API** | 模型调用 `now` 工具：退出码 0，输出 `2026-11-21 08:50:30 UTC.`；落盘 **13 行**，含 `tool/call` + `tool/result` 与第二个 step；CLI 打印 `[tool] now ok 0ms` |
| 缺 key 路径 | 明确报错 `dsh: llm: missing API key`，仍写出 header + `session/title`，不产生半截 turn |
| 质量门禁 | `gofmt` clean、`go vet` 0、`staticcheck` 0、`go test ./...` 8 包全绿 |
| 规模 | session 包 26 个用例、全仓库 85 个 |
| `scripts/build.ps1` | 构建通过（commit 注入） |

## 自动决策

- **`Turn` 从事件恢复**：`Load` 扫描 `turn/start` 取最大值，`Run` 每次 +1，resume 后不会重号。
- **错误路径也闭合日志**：模型调用失败或超迭代上限时补 `step/end`（若 step 未闭合）与 `turn/end{kind:"error"}`，避免留下无结尾的 turn。
- **`turn/end.reason` 用对象**：上游是 `{kind:"completed"}` 这类结构化对象，不是字符串。
- **`assistant/message.stream` 写空数组**：P1 还没有流式，字段在 schema 中是必填数组。
- **`tool/result` 写 `isError` 标志**：`llm.Message` 没有该字段，但线上格式有，投影时暂不还原。
- **`Load` 前先 `RepairTail`**：与上游"打开即修复"一致，崩溃后第一次读取即可恢复。
- **Store 持有写者并用 `Close` 释放**：避免每次 append 都重新解码整个文件；测试补 `t.Cleanup` 是因为 Windows 上未关闭的句柄会阻塞 `TempDir` 清理（本轮真实踩到）。
- **保留 `Store.Append`**：Web 测试与只持有 `llm.Message` 的调用方仍可用；它内部映射成对应 surface 事件。

## 遗留与下一步

- **跨进程单写者锁**（需要平台实现或依赖，Q-0002 默认不引）。
- **流式（批次 3）**：`llm.Stream` + SSE + `assistant/stream` 事件。
- **profiles / `--dump-config`（批次 4）**、投影缓存、compaction 的 `replace` 生产。
