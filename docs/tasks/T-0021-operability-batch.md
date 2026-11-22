# T-0021 · 批次 8：失败分类与重试、`.env`、日志与启动诊断、内核分发模式

- **状态**：done
- **更新**：2026-11-22
- **关联决策**：D-0002（用户"继续"授权批次 8）、D-0007（补测试默认授权）、D-0008（文档同批更新）、D-0009（范围修正）、Q-0002（零第三方依赖）
- **待办来源**：[BACKLOG](../BACKLOG.md) 批次 8 → B-27 / B-28 / B-29 / B-30

## 目标

把 P1 最后四项"可运维性"缺口补齐，并顺手修正与上游漂移的 `turn/end` 语义：

1. **B-27**：模型失败按 provider 无关码分类，按策略退避重试，重试过程可持久、可观察；
2. **B-28**：两层 `.env`（项目 + 数据目录）在配置之前套用，拒绝引导变量；
3. **B-29**：结构化日志落文件，启动失败保存 `Full diagnostics:` 报告；
4. **B-30**：内核补齐 bail / serial / parallel / waterfall 四种分发模式，并让工具流水线成为真实消费者。

## 范围（写作用域）

- `internal/llm/`：`failure.go`、`retry.go`（新增），`deepseek.go`（分类接入）
- `internal/session/`：`wire.go`（`TurnEndReason` 结构化）、`interrupted.go`（原因常量）
- `internal/agent/`：`loop.go`（重试、`turn/end` 原因、`agent/retry` 事件）
- `internal/config/`：`env.go`（新增，两层 `.env`）、`config.go`/`source.go`（`logFile`、`maxRetries`、`HomeDir`）
- `internal/kernel/`：`dispatch.go`（新增）、`kernel.go`（总线状态）
- `internal/tool/`：`pipeline.go`（`tools/pre-execute` waterfall）
- `cmd/dsh/`：`logging.go`、`diagnostics.go`（新增），`main.go`（`loaded`、诊断接线、重试提示行）
- 文档：README / ARCHITECTURE（§3、§4、§5、§6、§10）/ BACKLOG / STATUS / D-0009 / 本记录

## 非目标

- 不做权限模式/沙箱/审批（上游的 `permissionMode` 需要 `permission-presets` 与沙箱服务，属 P2）；**不发明 `DSH_PERMISSION_MODE`**（见 D-0009-01）。
- 不做 `tools/execute`/`tools/post-execute` 包装层与 `agent/pre-step`（P2）。
- 不做按 provider 配置的多套重试策略（本实现只有一个 provider 路由，`maxRetries` 可配、退避形状固定）。

## 设计

1. **失败分类**（`llm.Failure`）：`{message, code, status?, providerRetryAfterMs?, requestId?}`，字段名与上游 `LlmFailure` 相同。规则按上游 `transport.ts` 顺序移植：`AUTH` → `QUOTA` → `RATE_LIMIT` → `CONTEXT_WINDOW_EXCEEDED` → `INVALID_REQUEST` → `SERVER` → `HTTP_<status>`；流内错误帧无状态码时落 `SERVER`。上下文溢出与配额用**明确措辞**正则识别（8 条正例 / 5 条反例锁定）。`Classify` 还会把 `context.Canceled`/`DeadlineExceeded`/网络错误/空响应分别归一成 `ABORTED`/`TIMEOUT`/`TRANSPORT`/`EMPTY_RESPONSE`。
2. **重试策略**（`llm.RetryPolicy`）：默认 normal、5 次、500ms 起、10s 上限、±10% 抖动，可重试码 `EMPTY_RESPONSE`/`RATE_LIMIT`/`SERVER`/`TIMEOUT`/`TRANSPORT`；`always` 模式用于"重排到成功或取消"。`Retry-After`（秒或 HTTP date）优先，超过上限时 normal 放弃、always 退回本地退避。`policyKey` 用与上游相同的 JSON 数组形态（可重试码排序后），所以同一策略永远同一个键。
3. **重试落盘顺序**：`llm/retry`（等待前）→ 可取消等待 → `llm/retry-started`。取消发生在等待中时本轮记 `aborted`。**已输出分片的尝试不重试**（D-0009-03）。
4. **`turn/end` 结构化**：`completed` / `error{message,code}` / `aborted{reason:{kind:"user"}}` / `max-tokens`（`finish_reason == "length"`）/ `interrupted`（崩溃补齐，既有）；`blocked`/`forked` 只登记常量。
5. **`.env` 两层**：`LoadEnvLayers` 先解析两层、校验引导变量，再统一应用；项目层 `<cwd>/.env`、数据层 `<HomeDir>/.env`；cwd 与数据目录相同时只读一次并按数据层规则豁免。解析器支持 `export`、单/双引号、`\n`/`\t`/`\"`/`\\`、注释、CRLF、BOM、空值、重复键（后者覆盖前者位置不变）。
6. **日志**：`slog` 同时写 stderr 与 `logFile`（默认 `<数据目录>/logs/dsh.log`，`off` 关闭）；打不开只告警。`newLogger` 返回关闭函数，四个子命令 `defer` 它。
7. **启动诊断**：`reportStartupFailure` 打印原因 → 写 `<数据目录>/logs/startup-<时间戳>-<随机>.log`（目录 0700、文件 0600）→ 打印 `Full diagnostics: <path>`；写失败时把报告正文打到 stderr。报告含时间、版本、Go 版本、平台、profile、home、config 路径、完整错误，并带上游同款"可能含敏感值"警告。
8. **内核分发模式**：`Emit`（原有）/`Bail`/`Serial`/`Parallel`/`Waterfall`；`Bail` 与 `Serial` 在 Go 里语义相同（监听器都是普通函数调用，没有"同步 vs 等待"之分），保留两个名字是为了与上游词汇对齐。waterfall 的 `next` 可换 context、重复调用返回错误、不调用即否决；panic 统一成 `*kernel.PanicError`（`errors.As` 可判别）。监听器状态并入 `Events` 本身（不引入包级旁表），`Close` 一并清空。
9. **真实消费者**：`Pipeline.Execute` 的固定顺序改为 `tool/started` → `tools/pre-execute` waterfall → lookup → 超时 → panic 恢复 → 截断 → `tool/executed`；`AddGuard` 变成 waterfall 上的便捷层。层返回非 `tool.Result` 值、或返回错误，都算否决并给出可读原因；层的 panic 记 `TOOL_PANICKED`（不会伪装成"策略拒绝"）。
10. **CLI**：新增 `[retry] attempt N of M in 1s (RATE_LIMIT)` 提示行（订阅 `agent/retry`）；`dsh config` 多打印一行 `env files:`。

## 验证

| 验证 | 结果 |
|---|---|
| 分类用例 | `internal/llm/failure_test.go`：15 组状态/类型规则、8 条溢出正例 + 5 条反例、6 条配额正例 + 3 条反例、`Retry-After` 秒/小数/HTTP date/非法、请求 id、流内错误帧、`Classify` 的取消/超时/普通错误/包装保留、JSON 字段集 |
| 重试用例 | `internal/llm/retry_test.go`：默认值、可重试集合、`Exhausted`、指数退避 500ms→10s、抖动上下界、provider 延迟优先/超限放弃、`policyKey` 稳定性 |
| 循环用例 | `internal/agent/loop_retry_test.go`：429 两次后成功（3 次调用、2 组 retry 事件、`retryId` 相同、`turn/step` 与 `delayMs` 正确）、`AUTH` 不重试、预算耗尽、已流式不重试、等待中被取消记 `aborted`、`max-tokens`、空响应重试、always 模式、`agent/retry` 事件、deadline 优先 |
| `.env` 用例 | `internal/config/env_test.go`：20 个解析用例、优先级、项目层拒绝（9 组）、数据层豁免（代理/精确端点）与仍拒绝（4 组）、两层都校验后才应用、缺失/不可读、cwd==home、层元数据、BOM |
| 分发模式用例 | `internal/kernel/dispatch_test.go`：26 个用例（顺序、bail 值边界、错误中断、panic 归一、并发重叠、waterfall 嵌套/否决/重复 next/panic/nil terminal、退订幂等、Close 后行为） |
| 流水线用例 | `internal/tool/pipeline_waterfall_test.go`：guard 允许/拒绝/短路、层自决、层包裹（前后）、层序、退订、层 panic → `TOOL_PANICKED`、层错误 → `TOOL_BLOCKED`、返回类型错误、Close 后仍可执行 |
| CLI 用例 | `cmd/dsh/diagnostics_test.go`：报告落盘与内容、写入失败回退 stderr、时间戳可移植、坏配置端到端出报告、`.env` 应用到环境、`config` 打印 `env files:`/密钥状态、项目 `.env` 放 `PATH` 被拒且环境未变、`retryPolicy` 跟随预算 |
| **假端点端到端（真实 HTTP）** | 本地假端点前两次回 `429 + Retry-After: 1`，第三次回 SSE：`dsh chat --prompt` exit 0，stdout `recovered after retry`，stderr 两行 `[retry] attempt 1 of 5 in 1s (RATE_LIMIT)` / `attempt 2 …`；日志 14 个事件、`seq` 0…13 连续，含 2×`llm/retry` + 2×`llm/retry-started`，`policyKey`/`delayMs:1000`/`failure.code:RATE_LIMIT` 与上游字段一致，`turn/end{completed}` |
| **真实 DeepSeek API（坏 key）** | exit 1，stderr 末尾 `[AUTH]`；日志 `llm/retry` 计数 **0**；`turn/end{kind:"error", error:{code:"AUTH"}}` |
| **真实 DeepSeek API（`.env` 提供 key）** | 清空进程内的 `DEEPSEEK_API_KEY`、只把 key 放进项目 `.env` → `dsh chat --prompt` exit 0（真实模型回答），`turn/end{completed}`；随后删除 `.env`，无残留密钥 |
| **启动诊断端到端** | 坏 `--config` → exit 1，stderr 打印原因 + `Full diagnostics: …\startup-….log`；报告 571 字节，含警告、版本、Go 版本、平台、profile、home、config、错误 |
| 质量门禁 | `gofmt -l .` 无输出、`go vet ./...` 0、`staticcheck ./...` 0、`go test ./...` 全绿（**249 个用例**，原 159）、`scripts/build.ps1` 通过、`python scripts/check_md_links.py` 42 个 md / 213 链接 / 0 缺失 |
| **未验证** | 真实 429/5xx 的重试（无法让真 provider 按需限流，用假端点覆盖）；`always` 模式未接 CLI 配置（只做库级用例）；Windows 之外的 `.env` 大小写行为只由 CI 覆盖 |

## 自动决策

- **`Bail` = `Serial`**：Go 没有"未 await 的 Promise"，两个名字语义必然重合；保留别名并在文档里说明，比删掉一个更贴近上游词汇。
- **panic 用 `*kernel.PanicError` 而不是字符串前缀**：流水线要区分"策略拒绝"与"插件 bug"，`errors.As` 比匹配消息可靠。
- **监听器状态并入 `Events`**：子代理最初用包级 `sync.Map` 旁表（因为不改 `kernel.go`），集成时改为结构体字段——旁表会随每条总线泄漏。
- **`AddGuard` 保留**：既有调用方与用例不变，内部改成注册一个 waterfall 层。
- **重试码只实现可达子集**：见 D-0009-04。
- **`maxRetries` 可配、其余退避参数固定**：退避形状是 provider 属性，不是用户偏好；只暴露最常被调的预算。
- **诊断文件名含毫秒 + 随机后缀**：同一秒内多次失败不会互相覆盖；`O_EXCL` 创建避免并发写同一文件。
- **日志文件不因写不进去而失败**：可观测性不能反过来阻止程序启动。
- **`env files:` 打印在 `dsh config`**：让"某个变量到底从哪个文件来"可查，而不用猜。

## 遗留与下一步

- **未做真实 provider 限流下的重试验证**：假端点覆盖了协议与落盘形态，真实 429 只有坏 key 的 `AUTH` 反例。
- **`always` 模式未暴露到配置**：库级能力，等 P2 的 provider 配置面一起做。
- **`.env` 解析不支持多行引号值与 `$VAR` 展开**：与当前 Node `parseEnv` 的子集一致，遇到会报行号错误而不是静默截断。
- **日志没有轮转**：`dsh.log` 会一直增长；P2 的"运维"项里再加大小上限/轮转。
- **诊断报告可能含 provider 错误文本**：已带警告字样，但仍建议分享前自查。
- 下一批：**批次 9** —— 按 Q-0015 决策进入 P2（沙箱三模式 vs `fs`/`shell` seam 哪个先走），见 [BACKLOG](../BACKLOG.md)。
