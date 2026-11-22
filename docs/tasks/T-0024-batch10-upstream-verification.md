# T-0024 · 批次 10：上游读取器验证、staticcheck 进 CI、重试回归入网

- **状态**：done
- **更新**：2026-11-22
- **关联决策**：D-0002（用户"继续"授权批次 10）、D-0007（补测试默认授权）、D-0008（文档同批更新）
- **待办来源**：[BACKLOG](../BACKLOG.md) 批次 10 → B-14 / B-40 / B-43

## 目标

把 P1 最后一条验收（上游能读我们写的日志）真做掉，并把两项只在本机手工做过的检查（`staticcheck`、假端点重试）装进 CI。

## 范围（写作用域）

- `scripts/upstream-scan.mts`（新增，验证工具）
- `internal/agent/loop.go` + `loop_v4_test.go`（事件顺序修正）
- `cmd/dsh/retry_e2e_test.go`（新增）
- `.github/workflows/ci.yml`（staticcheck 步骤）
- 文档：ARCHITECTURE / BACKLOG / STATUS / T-0016 §2 / 本记录

## 非目标

- 不引第三方 Go 依赖、不改会话格式版本。
- 不把上游验证放进 CI：它需要上游 checkout 与 Node，CI 上没有这份参考源码，B-14 保持"可复现的手工验证 + 回归用例锁顺序"。

## 设计与发现

### B-14：上游读取器真的读出了不兼容

做法：用上游 checkout 的**源码级扫描器**（`packages/session/session-persistence-jsonl/src/format.ts` 的 `scanLog`）直接读 Go 写出的 `.jsonl`，由 Node 24 的类型转换直接跑 TypeScript。第一次运行就**失败**：

```
SCAN_FAILED: system/message does not match an open turn and step
```

原因在上游 `session-format-v3-to-v4/src/relationships.ts`：

- `STEP_EVENT_TYPES = {system/message, developer/message, assistant/attempt}` 要求这些事件位于**已打开的 turn 与 step 之内**（且 `data.turn/data.step` 与当前一致）；
- `system/message` 还要求是**第一个 surface 事件**（protected head）。

我们原来的顺序是 `turn/start → system/message → user/message → step/start → …`，两条都不满足。上游真实样本（`apps/cli/tests/.../session.expected.jsonl`）确认正确顺序是：

```
turn/start → step/start → system/message → user/message → request/header → request/context → …
```

**修正**：把系统提示与用户消息移进**第一个 step 内**（`iteration == 1` 分支，紧跟 `step/start`），其余不变。修正后：

| 验证 | 结果 |
|---|---|
| 单轮真实会话（22 事件，含工具调用、`todo/write`、`working-directory/change`） | `scan: ok`，`seq` 0…21 连续，`committedBytes == fileBytes`，类型全部被上游接受 |
| 续接后的两轮会话（30 事件） | `scan: ok`，连续，最后 `turn/end` |
| 上游 `turn/end` 原因、`request/header`、`tool/call`↔`tool/result`、`llm/retry` 关系校验 | 全部通过（扫描器会跑 `assertReleasedV4Relationships`） |

可复现命令（需要上游 checkout；本机路径见 [STATUS](../STATUS.md) 环境事实）：

```powershell
node --experimental-transform-types scripts/upstream-scan.mts <上游checkout> <session.jsonl>
```

> 本机复现注意事项：参考检出里的 `node_modules` 链接指向旧目录名（`deepseekharness-go\packages\...`），直接 import 会 `ERR_MODULE_NOT_FOUND`。本次验证临时在 Go 仓库根建了 `packages`/`node_modules`/`vendor`/`apps`/`native`/`benchmarks` 六个目录联接把路径接回去，跑完立即删除（**没有写进参考检出**，也没有留在仓库里）。

### B-40：staticcheck 进 CI

CI test job 增加一步（仅 Linux，避免双平台重复耗时）：`go install honnef.co/go/tools/cmd/staticcheck@v0.8.2` 后跑 `staticcheck ./...`。版本与本地门禁一致（`2026.2.2 (0.8.2)`），固定版本以免新版本改变旧提交的结果。

### B-43：重试回归进 CI

新增 `cmd/dsh/retry_e2e_test.go`：用 `httptest` 起一个假 DeepSeek 端点，前两次回 `429` + `rate_limit_error`，第三次回 SSE；通过 `run()` 真的跑一次 `dsh chat --prompt`，断言：

- 端点收到 **3** 次请求；
- stdout 有流式回答，stderr 有 **2** 行 `[retry] attempt … (RATE_LIMIT)`；
- 会话日志有 2×`llm/retry` + 2×`llm/retry-started`，第二次 `delayMs` 在 900–1100（500ms 起、翻倍、±10% 抖动），`turn/end{completed}`。

这是进程内 HTTP + 真实 CLI 装配，`go test ./...` 在 ubuntu/windows 都会跑，替代了之前的手工 python 脚本。

## 验证

| 验证 | 结果 |
|---|---|
| 上游扫描（单轮） | `scan: ok`、22 事件、`seq` 连续、字节完全消费 |
| 上游扫描（两轮续接） | `scan: ok`、30 事件、连续 |
| 事件顺序回归 | `TestLoopPersistsV4EventStream` 更新为 `turn/start, step/start, system/message, user/message, request/header, …` |
| 重试回归 | `TestChatRetriesAgainstARealEndpoint` 通过（约 1.5s，含两次真实退避） |
| 质量门禁 | `gofmt` 无输出、`go vet` 0、`staticcheck` 0、`go test ./...` 全绿（**269 个用例**）、`scripts/build.ps1` 通过、md 链接 0 缺失 |
| CI | 见推送后的 Actions 结果（staticcheck 步骤首次运行） |

## 自动决策

- **顺序修正而不是放宽读取端**：上游的规则是"模型可见即已记录"的结构约束，改顺序成本低、收益是两边日志真正互通；放宽我们自己的读取器没有意义。
- **系统/用户消息放进第一个 step**：上游要求 surface 事件在打开的 step 内，且首个 surface 必须是 system；把 user 留在 step 外会让 system 变成"非首个 surface"而被拒。
- **验证工具进仓库、但不进 CI**：`scripts/upstream-scan.mts` 让"上游能读"可复现；CI 没有参考源码，硬塞会把外部 checkout 变成构建依赖。
- **staticcheck 只跑 Linux**：结果与平台无关，双平台跑只是浪费时长；版本固定。
- **重试用 Go 测试而不是脚本**：进程内 `httptest` + `run()` 比启动子进程稳定，且自动进 CI 的双平台。

## 遗留与下一步

- 上游的**写方向**（把上游会话导进我们的读取器）已经有 T-0008 的夹具对拍；**反向**（我们写、上游读）现在有手工验证与顺序回归，但没有 CI 网——除非把参考检出作为 CI 的额外检出（成本/许可都需要决策）。
- `scripts/upstream-scan.mts` 依赖 Node 22.18+ 的类型剥离；Node 24 需要 `--experimental-transform-types`（上游源码用了参数属性），已在脚本头注释说明。
- 下一批：**批次 11**（B-24 compaction），见 [BACKLOG](../BACKLOG.md)。
