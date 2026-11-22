# TODO 与优先级（唯一待办来源）

> 本文件是**跨会话的待办优先级表**：每次会话从这里取下一项，不再临时决定。
> 完成一项就把状态改成 `done` 并写上关联任务/提交；新增事项必须给 ID、优先级与验收要点。
> 相关：[T-0016 差距审计](tasks/T-0016-gap-audit.md)（缺口的证据来源）、[T-0022 P2 盘点与排序](tasks/T-0022-p2-backlog-and-priorities.md)、[MIGRATION_MAP](MIGRATION_MAP.md) §5（阶段范围）、[STATUS](STATUS.md)（当前进度）。
> 最近一次重排：**2026-11-22**（T-0022，用户授权代理拍板 P2 顺序）。

## 优先级定义

| 优先级 | 含义 | 处理方式 |
|---|---|---|
| **P0** | 已实测损坏或数据安全（不做就无法声称 P1 完成） | 立即做，单独提交 |
| **P1** | P1 验收标准或 §6 A/B 兼容条目要求 | 按顺序做，是"P1 收口"的定义 |
| **决策** | 需要用户拍板才能开工（依赖、安全、产品交互） | 在会话里以表格提问，未拍板不动工 |
| **P2** | P2 阶段范围（功能闭环 / 安全 / 工具生态 / 可运维） | 按下面的顺序做 |
| **P3/P4** | 后续阶段；当前不做，只登记 | 不排期 |

规模：XS ≤ 15 分钟、S ≈ 0.5–1 小时、M ≈ 1.5–2.5 小时、L ≈ 0.5 天、XL ≈ 1 天以上。

## 状态总览（2026-11-22 审计）

| 层 | 定义 | 剩余 |
|---|---|---|
| P0 | 实测损坏与数据安全 | **0**（B-01…B-03 全 done） |
| P1 | P1 验收 + §6 A/B 兼容 | **1 项，且是验证类**（B-14 反向互操作；不改产品代码） |
| 决策 | 需用户拍板 | **2 项**（B-11 zstd 读、B-12 Web 鉴权） |
| P2 | 功能闭环 / 安全 / 生态 / 运维 | **11 项**（15 项中 4 项已完成） |
| 工程化 | 穿插做，不单独排阶段 | **5 项** |
| P3/P4 | 只登记不排期 | 见 [MIGRATION_MAP](MIGRATION_MAP.md) §3/§5 |

**结论：代码侧 P0/P1 已经清零**。P1 只剩"用上游读取器验证 Go 写出的日志"这一条验收（T-0016 §2 的 ①/④），它不需要改产品代码；其余全部是 P2 及以后。

## P1 残余（1 项）

| ID | 事项 | 规模 | 依赖 | 验收要点 | 状态 |
|---|---|---|---|---|---|
| B-14 | **反向互操作验证**：用上游读取器解析 Go 写出的 v4 日志（T-0016 §2 ①/④ 的唯一残留） | S | 上游 checkout（本机 `%USERPROFILE%\Desktop\deepseekharness` 可用） | 上游解析器读一份 Go 会话：header 字段、事件数、`seq` 连续、事件类型与载荷全部一致；把结论写回 T-0016 §2 | todo |

> 注意：只验证**逻辑事件流**。物理目录布局有意不同（Go 用扁平 `<sessionDir>/<id>.jsonl`，上游用 `--<cwd>--/<id>/session.vN.jsonl`），因为两者数据目录严格隔离，不做目录级互通。

## 已完成的 P0 / P1（历史，保留以便追溯）

| ID | 事项 | 状态 |
|---|---|---|
| B-01 | 崩溃恢复补齐 `interruptedTurnClosers`（`TOOL_OUTCOME_UNKNOWN`/`TOOL_NOT_STARTED` + `step/end` + `turn/end{interrupted}`） | done（[T-0018](tasks/T-0018-crash-recovery.md)） |
| B-02 | 同会话 turn 互斥 + `Store.Load` 校验 header id | done（[T-0018](tasks/T-0018-crash-recovery.md)） |
| B-03 | 会话日志容忍 UTF-8 BOM | done（[T-0018](tasks/T-0018-crash-recovery.md)） |
| B-04 | 工具结果结构化错误码（含 `TOOL_NOT_STARTED`/`TOOL_OUTCOME_UNKNOWN`/`ABORTED_BEFORE_DISPATCH`） | done（[T-0018](tasks/T-0018-crash-recovery.md)） |
| B-05 | 取消/中断原因与 CLI 信号语义（SIGINT=130、SIGTERM=0、5 秒优雅退出） | done（[T-0018](tasks/T-0018-crash-recovery.md)；原因字面量在 [T-0021](tasks/T-0021-operability-batch.md) 修正为 `aborted`） |
| B-06 | 100 并发会话压测（`-race`） | done（[T-0019](tasks/T-0019-request-envelope.md)） |
| B-07 | `request/header` + `request/context` 落盘 | done（[T-0019](tasks/T-0019-request-envelope.md)） |
| B-08 | `system/message` 落盘 + `internal/prompt` 静态组装 | done（[T-0019](tasks/T-0019-request-envelope.md)） |
| B-09 | 每步 fsync 屏障（`Store.Flush`/`SyncCount`） | done（[T-0020](tasks/T-0020-durability-lock.md)） |
| B-10 | 跨进程单写者锁（POSIX `flock` / Windows `LockFileEx`，纯标准库） | done（[T-0020](tasks/T-0020-durability-lock.md)） |
| B-27 | LLM 失败分类 + 退避重试 + `turn/end.error` | done（[T-0021](tasks/T-0021-operability-batch.md)） |
| B-28 | 两层 `.env`（项目 + 数据目录，拒绝引导变量） | done（[T-0021](tasks/T-0021-operability-batch.md)） |
| B-29 | 日志文件 + 启动失败诊断（`Full diagnostics:`） | done（[T-0021](tasks/T-0021-operability-batch.md)） |
| B-30 | 内核四种分发模式 + `tools/pre-execute` 消费者 | done（[T-0021](tasks/T-0021-operability-batch.md)） |

## 决策阻塞（2 项，未拍板不动工）

| ID | 事项 | 需要的决策 | 影响 | 何时必须先解决 |
|---|---|---|---|---|
| B-11 | 读取上游 `.jsonl.zstd` 会话 + generation 选择 | 是否允许引第三方 zstd（`klauspost/compress`）／自研解码／只支持未压缩 | 决定能否直接打开安装版的真实历史会话 | 做 B-14 的可选延伸或"导入上游会话"时 |
| B-12 | Web 鉴权（token / `authorize` 三态） | 安全与产品交互：默认是否要 token、如何传给前端 | 本机其它进程可驱动 harness 的现状是否接受 | **B-33（Web 会话列表）开工前**：UI 暴露面越大，无鉴权风险越高 |

> **B-13（P2 起点）已收敛**：用户在 2026-11-22 授权代理拍板，结论见下面的「P2 优先级」，不再作为待决项。

## P2 优先级（2026-11-22 已拍板）

**排序原则**（按"不做就无法日常使用 → 不做就有安全风险 → 不做就体验受限"三段）：

1. **先清小件**：无依赖、半小时级、用户立刻能感知的功能缺口先补齐，避免它们一直排在"大件"后面。
2. **再解死局**：长会话撞上下文上限后，即使错误码分类正确也无法继续——这是当前唯一的**功能性死局**，必须尽早在 P2 前面解决。
3. **再打安全地基**：沙箱需要先有 seam（否则策略只能写死在内置工具里），审批又依赖沙箱的三态决策。
4. **然后输出与上下文治理**：spill、context providers、系统提示变更都建立在 seam 与 compaction 之上。
5. **最后生态与运维**：Web 工具、UI、日志轮转、清理。价值真实，但不阻塞日常闭环。

| 顺序 | ID | 事项 | 规模 | 依赖 | 验收要点 | 状态 |
|---|---|---|---|---|---|---|
| 1 | B-35 | `contextWindow` / `maxTokens` 配置，并写入 `request/context` 与请求体 | XS | — | 配置后 `request/context.contextWindow` 有值、请求带 `max_tokens`；跨平台无回归 | done（[T-0023](tasks/T-0023-batch9-sessions-and-state.md)） |
| 2 | B-31 | `todo/write`、`working-directory/change` 落盘与恢复 | S | B-02 | 重启后 todo 列表与 shell cwd 与日志一致；两类事件真的出现在事件流里 | done（[T-0023](tasks/T-0023-batch9-sessions-and-state.md)） |
| 3 | B-32 | `dsh sessions` 子命令（列表/查看/续接提示） | S | — | 列出 id/标题/时间/最后一个 turn 原因；`--json` 可脚本化 | done（[T-0023](tasks/T-0023-batch9-sessions-and-state.md)） |
| 4 | B-34 | 取消时保留已流式输出的前缀（写 `assistant/message` + `aborted`） | S | — | Ctrl+C 后日志含已输出文本，下一次请求历史里也看得到（上游行为） | done（[T-0023](tasks/T-0023-batch9-sessions-and-state.md)） |
| 5 | B-14 | 反向互操作验证（上游解析器读 Go 日志） | S | 上游 checkout | 见 §P1 残余 | todo |
| 6 | B-24 | `compaction`（basic + 工具结果剪枝） | L | B-07/B-08 | 长会话撞上限后自动压缩并继续；压缩事件可重放；真实 API 验证一次 | todo |
| 7 | B-20 | `fs` / `shell` / `subprocess` seam 抽取 | L | — | `builtin` 只依赖接口，可用假实现替换；行为零变化（回归测试作证） | todo |
| 8 | B-21 | `sandbox` 三模式（read-only / workspace-write / danger-full-access）+ 平台后端 | XL | B-20 | 三模式各有用例；越界写被拒且错误可区分；`sandbox/mode` 事件落盘 | todo |
| 9 | B-23 | `tools/pre-execute` 审批策略层（ask/allow/deny + 一次性授权） | M | B-21 | 三态决策；`approval/asked`/`approval/decided` 事件；拒绝不伪装成失败 | todo |
| 10 | B-22 | 工具输出 spill / 落盘策略 | M | B-20 | 超限输出落盘并可再读；模型看到摘要与路径 | todo |
| 11 | B-26 | `context` providers（time / instructions） | S | B-08 | 每步注入的上下文可配置，并以 `user/message` 的合成来源落盘 | todo |
| 12 | B-36 | 系统提示变更（`system/message` 可再写 + `request/header{change}` 可达） | S | — | 换 profile/工具集后续接会写新 header 与新系统提示 | todo |
| 13 | B-33 | Web UI 会话列表/切换与刷新保留；工具调用卡片 | M | B-32、**B-12 决策** | 刷新不丢会话；可在历史会话间切换 | todo |
| 14 | B-25 | `web_fetch` / `web_search` | M | B-21 | 网络策略生效；私网目标默认拒绝；有脱网用例 | todo |
| 15 | B-37 | 日志轮转与大小上限 | S | — | 超过上限自动轮转；`dsh config` 显示当前文件与上限 | todo |
| 16 | B-38 | 会话与锁文件清理（`dsh sessions prune`） | S | B-32 | 清理孤儿 `.lock` 与空会话；默认 dry-run，`--yes` 才删 | todo |

**为什么是这个顺序（可推翻的三个点）**：

- 表中第 5 位的 **B-14 属于 P1 残余**，其余 15 项属于 P2；把它排进同一张表只是为了"按顺序取下一项"时不漏掉它。
- 1–4 是"半小时级但用户每天碰得到"的缺口；先做它们能让 P2 的第一个批次就有可见产出。
- 6（compaction）提前到安全之前，是因为它是**唯一会让会话彻底无法继续**的问题；沙箱影响的是"敢不敢跑不可信提示词"，两者性质不同。
- 如果你更看重**安全优先**，把 7–9 整体提到 1 之前即可（`B-20 → B-21 → B-23` 自成一条链，与 1–6 无依赖冲突）；如果你更想先要**可用界面**，把 13 提到 5 之前（但需先拍 B-12）。

## 工程化（穿插做，不单独排阶段）

| ID | 事项 | 规模 | 说明 | 状态 |
|---|---|---|---|---|
| B-40 | CI 增加 `staticcheck` | XS | 目前只在本地门禁跑；CI 的 gofmt/vet/test/race/build/链接已齐 | todo |
| B-41 | `cmd/dsh` 可测化（boot 注入），提升 CLI 覆盖率 | M | 现在只有文档契约与诊断用例；boot/装配仍靠手工验证 | todo |
| B-42 | 架构/迁移映射的自动漂移检查 | M | README 的三块（命令/工具/配置）已自动化，其余人工核对 | todo |
| B-43 | 假端点重试回归进 CI | S | 目前是手工脚本：429+`Retry-After` → SSE 成功，断言两次重试与日志形态 | todo |
| B-44 | 杀进程/掉电自动化回归 | M | T-0016 D4：`interrupt` 补齐只有单测与手工脚本，没有 CI 网 | todo |

## P3 / P4（只登记，不排期）

- **P3**：MCP / LSP / Skill / Subagent / Team / Jobs / Goal / Preset+Scope / Storage / Settings / Credentials / SessionQuery / 自动标题 / 历史格式迁移链 / projection 缓存。
- **P4**：SDK/ACP、Workflow/PTC、Office、terminal、telemetry（含 `DSH_TELEMETRY_*`）、Desktop 壳、上游前端托管、`/api/remote.mux`。
- 范围与优先级见 [MIGRATION_MAP](MIGRATION_MAP.md) §3 与 §5 的 P3/P4 行。

## 批次计划（供确认）

| 批次 | 内容 | 预估 | 为什么这个顺序 |
|---|---|---|---|
| **9** | B-35 + B-31 + B-32 + B-34 | ✅ 已完成 2026-11-22（[T-0023](tasks/T-0023-batch9-sessions-and-state.md)） | 小件批：无依赖、用户立刻可见；先拿掉"每天碰得到"的粗糙处 |
| **10** | B-14 + B-40 + B-43 | 0.5 天 | 补齐 P1 最后一条验收，并把 staticcheck 与重试回归进 CI，给后续改动装网 |
| **11** | B-24 | 0.5–1 天 | 唯一的功能性死局；单独一批便于回滚（压缩会改请求内容） |
| **12** | B-20 + B-22 | 1 天 | seam 与它的第一个消费者同批，避免做出一个没人用的空接口 |
| **13** | B-21 + B-23 | 1.5 天 | 沙箱与审批一起收口：三模式策论与三态决策本来就咬合 |
| **14** | B-26 + B-36 + B-33 | 1 天 | 上下文注入、系统提示变更与 Web 可见性（B-33 开工前先拍 B-12） |
| **15** | B-25 + B-37 + B-38 | 1 天 | 生态与运维收尾 |
| **16** | B-41 + B-42 + B-44 | — | 工程化债务，穿插或最后集中 |

**当前建议**：批次 9 已完成；下一批是**批次 10**（B-14 反向互操作 + B-40 staticcheck 进 CI + B-43 假端点重试回归，约半天）。B-11/B-12 两个决策不阻塞批次 10–12，但 B-12 必须在 B-33 之前拍板。

## 变更记录

- **2026-11-22（批次 9 后）**：B-31/B-32/B-34/B-35 done；P2 剩 11 项。下一批改为批次 10（B-14 + B-40 + B-43）。（[T-0023](tasks/T-0023-batch9-sessions-and-state.md)）
- **2026-11-22**：批次 8 收口（B-27…B-30 done，P1 代码侧清零）；新增 B-14（P1 残余）、B-34…B-38（P2 新发现缺口）；把 P2 从"分组清单"改为**有序表**并记录排序理由；新增工程化 B-43/B-44；B-13 由 Q-0015 收敛。（[T-0022](tasks/T-0022-p2-backlog-and-priorities.md)）
- 2026-11-21：差距审计后建立本表（T-0016），拆出批次 5–8。
