# T-0015 · 批次 4 第二步：工具批次语义（并发 / barrier / 模型序）

- **状态**：done
- **更新**：2026-11-21
- **关联决策**：D-0002（用户"继续批次4"授权）、D-0007（补测试默认授权）

## 目标

模型一次回复里给出多个工具调用时，不再无条件串行：**并发安全的调用可以重叠执行，不安全的调用形成独占 barrier，结果始终按模型给出的顺序回填**；并发上限可配置（默认 10，`1` = 串行）。

## 非目标

- 不做执行器插件化（上游 `ToolExecutor` seam 留 P2）。
- 不做 `tools/pre-execute` waterfall、审批（P2）。
- 不做工具内部并发（工具自身的并发安全由工具自己保证）。

## 设计

- **可选接口** `tool.ConcurrencySafe{ConcurrencySafe() bool}`：未实现或返回 false 视为独占 —— 默认安全，新增工具不会"默认可并发"。
- **调度**：把调用序列切成"安全段"与"独占单例"；安全段内并发上限 = `maxParallelToolCalls`，独占调用前后各有一个隐式 barrier。
- **结果顺序**：结果槽固定按输入下标写入，与完成顺序无关。
- **落盘顺序**：先按模型序写全部 `tool/call`，再按模型序写全部 `tool/result` —— 日志因果关系与完成顺序解耦。
- **取消**：ctx 已取消时不再启动新调用，已启动的等待结束；未启动的返回 `isError` 结果（不中断 turn）。
- **只看只读工具**才 opt-in：`read`、`list_dir`、`glob`、`grep`、`read_image`、`now`；`write`、`edit`、`working_directory`、`pwsh`/`bash`、`todo_write` 保持独占。

## 验证

| 验证 | 结果 |
|---|---|
| `go test ./internal/tool -count=1` | 15 个用例（新增 8：模型序、并发上限、串行、barrier、相邻安全段、未知工具、取消、墙钟重叠） |
| 并发上限 | 6 个 25ms 调用、limit=2 → 观测峰值并发 **恰好 2** |
| barrier | `safe → 独占 → safe` 观测峰值并发 **1**，执行顺序 `left,exclusive,right` |
| 墙钟 | 6 个 40ms 调用、limit=6 → 总耗时 < 200ms（串行需 ≈240ms），峰值 6 |
| 取消 | 预先 cancel 的批次：0 次启动，全部结果 `was not started` |
| `go test ./internal/agent` | 7 个用例（新增 `TestLoopRunsToolBatchInModelOrder`）：慢调用先完成，落盘与投影仍是模型序，且**所有 `tool/call` 都在第一个 `tool/result` 之前** |
| `go test ./internal/builtin` | 20 个用例（新增 `TestBuiltinConcurrencyClassification`）：逐个工具钉死安全/独占分类，新增工具必须显式登记 |
| **真实 DeepSeek API** | 一次回复给出 2 个 `read` 调用；会话日志 15 行：seq 4 `assistant/message`（text+2×tool-call）→ seq 5,6 `tool/call` → seq 7,8 `tool/result`（模型序）→ `step/end` → 第二个 step 收尾，`turn/end kind=completed`；最终答案正确复述两个文件内容 |
| CI | 新增 `go test -race`（仅 Linux，检测器需要 cgo） |
| 质量门禁 | `gofmt` clean、`go vet` 0、`staticcheck` 0、`go test ./...` 全绿（全仓库 122 个用例） |
| **未验证** | 本机无 gcc（`CGO_ENABLED=1` 报 `gcc not found`），`-race` 只能由 CI Linux 作业验证，本地未跑 |

## 自动决策

- **默认独占**：接口缺省视为独占，避免新增工具"默默并发"；未知工具也按独占处理（它只会快速失败）。
- **上限来自配置**：`maxParallelToolCalls`（默认 10）经 `config → bootstrap → Pipeline` 传递；`agent.Options.MaxParallelToolCalls` 可逐 agent 覆盖。
- **取消语义**：未启动的调用返回错误结果而不是让 turn 失败，保证工具调用历史完整（模型能看到"没跑"）。
- **先记 `tool/call` 再执行**：崩溃时留下完整的待执行调用，resume 侧可补 `TOOL_NOT_STARTED`。
- **只读工具才并发**：`working_directory` 虽然只是改 shell 的 cwd，也会影响后续 `pwsh`，因此独占；`todo_write` 改共享列表，独占。

## 遗留与下一步

- **工具执行进度事件**：并发执行时前端仍只看到 `tool/executed` 汇总，没有逐调用的 started/progress 帧。
- **资源级互斥**：目前只有"工具级"安全分类，没有"同一路径"级别的锁；写工具独占已覆盖主要风险，但 `read` 与 `glob` 之间的目录快照一致性未保证。
- **重试 / 超时策略**：仍是固定超时，未做 per-tool `timeoutMs` 与重试。
- **P2 计划**：`tools/pre-execute` waterfall、审批、`isConcurrencySafe` 之外的可配置并发。
