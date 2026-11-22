# T-0019 · 批次 6：请求信封、系统提示与并发压测

- **状态**：done
- **更新**：2026-11-22
- **关联决策**：D-0002（用户"继续"授权批次 6）、D-0007（补测试默认授权）、D-0008（文档同批更新）
- **待办来源**：[BACKLOG](../BACKLOG.md) 批次 6 → B-06 / B-07 / B-08

## 目标

补上"模型可见即已记录"的最后两块：请求信封（`request/header` + `request/context`）与系统提示（`system/message`），并用 100 并发会话压测把持久化路径钉进回归网。

## 范围（写作用域）

- `internal/session/`（`persist_jsonl.go`、`session.go`、`wire.go`、并发用例）
- `internal/agent/loop.go`（信封与系统消息）、`internal/prompt/`（新增）
- `cmd/dsh/main.go`（改用 prompt 包）
- 文档：ARCHITECTURE / BACKLOG / STATUS / 本记录

## 非目标

- 不做 `agent/pre-step` waterfall 与内核分发模式（B-30，属批次 8）。
- 不做历史版本迁移链与 zstd（B-11/B-12，需决策）。
- 不做压缩 / 上下文窗口管理（P2）。

## 设计

1. **信封**：`request/header` 载荷 = `{header:{config:{provider,model}, tools:[{name,description,parameters}]}, reason}`，自包含到"只靠它就能重建请求"；`reason` 用上游词汇 `initial` / `resume` / `change`。用 header 的 sha256 做指纹：**信封不变就沿用上一条**，所以一次回复的多个 step 不会重复写工具 schema。
2. **上下文**：`request/context` = `{provider, model, contextWindow?}`，每个 loop 写一次。
3. **系统提示**：`system/message` 是 surface 事件，在**每会话第一条用户消息之前**写一次；`messages()` 只在历史里没有 system 时才补，避免 resume 后重复。
4. **读不修盘**：`Load` 改为"只读已提交前缀"（`committedPrefix`），不再调用会截断文件的 `RepairTail`。这是 B-06 压测直接逼出来的修正：读者在写者 append 到一半时看到"撕裂尾行"，若按旧逻辑会**截断写者刚写的行**，导致 seq 断裂。磁盘修复只归写路径（`Resume`/`OpenWriter`）。
5. **prompt 包**：`Section{Name,Text}` + `Render()`（跳过空段、空行分隔）+ `Harness(Runtime)`（base 指令 + model/workspace/tools）。base 文案迁到 `prompt.Base`，`agent.DefaultSystemPrompt` 成为它的别名。

## 验证

| 验证 | 结果 |
|---|---|
| `go test ./internal/session` | 新增 `TestLoadNeverRepairsOnDisk`（Load 前后文件字节不变，`RepairTail` 才回收撕裂尾部）、`TestLoadWhileWriting`（4 个读者与写者并发，最终日志 seq 0…40 连续）、`TestConcurrentSessionsUnderRace`（100 会话 × 3 轮 × 6 事件并发写 + 并发 `List` + 并发 `Load`，最终 100 条摘要、每会话 6 条消息） |
| `go test ./internal/prompt` | 5 个用例：段序、空段跳过、首尾裁剪、Runtime 渲染、空 Runtime 不产生伪字段、确定性 |
| `go test ./internal/agent` | 新增 3 个：信封只写一次（两个 step 共用一条 header，工具 schema 完整且参数是合法 JSON）、新 loop 续接写 `resume`、系统提示只写一次且历史以它开头 |
| 既有用例 | 事件序列/消息数断言按新契约更新（`system/message` 进历史、`request/header` + `request/context` 进日志） |
| **真实 DeepSeek API：同一进程两轮** | 日志 16 行：1×`system/message`、1×`request/header{initial}`、1×`request/context`；第二轮**不重复**写 header/system —— 证明沿用语义 |
| **真实 DeepSeek API：新进程续接** | 第二轮为独立进程 → 新写 `request/header{resume}`，**不重复** `system/message`；两轮都 `turn/end{completed}` |
| 真实工具调用 | 第一轮模型调用 `read`，`request/header` 里的 11 个工具 schema 与 `dsh tools` 完全一致 |
| 浏览器 E2E | `{"ok":true,"samples":["web","web-","web-stream"]}` |
| 质量门禁 | `gofmt` clean、`go vet` 0、`staticcheck` 0、`go test ./...` 全绿（**154 个用例**）、`scripts/build.ps1` 通过 |
| **未验证** | `-race` 只在 CI Linux 跑（本机无 gcc）；`request/header` 的 `change` 分支当前不可达（工具与模型在单个 loop 内固定） |

## 自动决策

- **读不修盘（本轮最重要的修正）**：B-06 压测暴露出 `Load` → `RepairTail` 会在写者 append 到一半时把"看起来撕裂"的行截掉，直接破坏 seq 连续性。改为 `Load` 只读 `committedPrefix`，磁盘修复只保留在写路径（`Resume` 在 `repairMu` 下显式 `RepairTail`），语义变成"读者宽容、写者负责"。
- **指纹去重而不是每条都写 header**：工具 schema 有 11 条，每步都写会让日志膨胀；指纹相同即视为"沿用最近一条 header"，并把该规则写进 ARCHITECTURE。
- **`reason` 用上游词汇**：`initial`（会话首条）/`resume`（loop 在已有历史上首次写）/`change`（信封变化，暂不可达）/`series` 预留；没有自造字段名。
- **系统提示属于 surface 事件**：`system/message` 走 `surfaceOp:append`，由投影直接产出模型消息；`messages()` 只在历史缺 system 时兜底，避免 resume 重复。
- **prompt 包只做静态组装**：不引模板引擎、不做变量替换；`Harness` 是唯一入口，base 文案单点维护。
- **provider 默认 `deepseek`**：配置文件暂无 provider 字段，先由 `agent.Options.Provider` 默认值兜底，P2 接多 provider 时再暴露。

## 遗留与下一步

- **`change` 分支不可达**：单 loop 内工具与模型固定，等动态工具/preset（B-30/P3）真正接入后再补用例。
- **会话中途换提示词不重建**：历史里已有 system/message 就不会再写，换提示词需要新会话；上游用 `system/message` 替换语义处理，等合并上下文压缩时一起做。
- **`contextWindow` 未进配置**：`agent.Options.ContextWindow` 已预留，但 `config.Config` 没有该字段，`request/context` 里始终省略。
- **每次 resume 会重写一份 header**：因为 tool schema 要进日志，属可接受代价；若日志体积成为问题，可改为"header 只在变化时写 + 首条完整"。
- 下一批：**批次 7**（B-09 fsync/flush 屏障 + B-10 跨进程单写者锁），见 [BACKLOG](../BACKLOG.md)。
