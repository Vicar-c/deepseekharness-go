# TODO 与优先级（唯一待办来源）

> 本文件是**跨会话的待办优先级表**：每次会话从这里取下一项，不再临时决定。
> 完成一项就把状态改成 `done` 并写上关联任务/提交；新增事项必须给 ID、优先级与验收要点。
> 相关：[T-0016 差距审计](tasks/T-0016-gap-audit.md)（缺口的证据来源）、[MIGRATION_MAP](MIGRATION_MAP.md) §5（阶段范围）、[STATUS](STATUS.md)（当前进度）。

## 优先级定义

| 优先级 | 含义 | 处理方式 |
|---|---|---|
| **P0** | 已实测损坏或数据安全（不做就无法声称 P1 完成） | 立即做，单独提交 |
| **P1** | P1 验收标准或 §6 A/B 兼容条目要求 | 按顺序做，是"P1 收口"的定义 |
| **P2** | P2 阶段范围（沙箱 / 工具生态 / 可运维）或产品完整性 | P1 清空后再动 |
| **P3** | 后续阶段；当前不做，只登记 | 不排期 |
| **决策** | 需要用户拍板才能开工（依赖、安全、产品交互） | 在会话里以表格提问 |

规模：XS ≤ 15 分钟、S ≈ 0.5–1 小时、M ≈ 1.5–2.5 小时、L ≈ 0.5 天、XL ≈ 1 天以上。

## P0 · 立即修复

| ID | 事项 | 规模 | 依赖 | 验收要点 | 状态 |
|---|---|---|---|---|---|
| B-01 | 崩溃恢复补齐 `interruptedTurnClosers`：未应答的 `tool/call` 补 `TOOL_OUTCOME_UNKNOWN`、未发起的补 `TOOL_NOT_STARTED`、补 `step/end`、补合成的 `turn/end{interrupted}` | M | — | 三种截断位置（`tool/call` 后 / `tool/result` 后 / assistant 后）各 resume 一次：事件序列完整，**真实 DeepSeek API 接受历史**（复现脚本见 T-0016 §2②） | done（[T-0018](tasks/T-0018-crash-recovery.md)） |
| B-02 | 同会话 turn 互斥 + `Store.Load` 校验 header id 与文件名一致 | S | — | 两个 loop 并发写同一 session：第二个被拒（或串行化）；文件改名后 `--session` 报"id 不匹配"而不是指向旧文件名 | done（[T-0018](tasks/T-0018-crash-recovery.md)） |
| B-03 | 会话日志容忍 UTF-8 BOM | XS | — | 带 BOM 的 `.jsonl` 可读；补用例 | done（[T-0018](tasks/T-0018-crash-recovery.md)） |

## P1 · P1 收口（按此顺序）

| ID | 事项 | 规模 | 依赖 | 验收要点 | 状态 |
|---|---|---|---|---|---|
| B-04 | 工具结果结构化错误码（`TOOL_TIMEOUT` / `TOOL_NOT_STARTED` / `TOOL_OUTCOME_UNKNOWN` / `ABORTED_BEFORE_DISPATCH`） | S | 与 B-01 同批 | `tool/result` 载荷带 code；超时/取消/未知三条用例 | done（[T-0018](tasks/T-0018-crash-recovery.md)） |
| B-05 | `turn/end` 增加 `cancelled` / `interrupted`；CLI 信号语义（SIGINT=130、SIGTERM=0、5 秒优雅退出） | S | B-01 | Ctrl+C → 130 且日志 `turn/end{cancelled}`；SIGTERM → 0 | done（[T-0018](tasks/T-0018-crash-recovery.md)） |
| B-06 | 并发会话压力用例：`-race` 下 100 会话读写 | S | B-02 | CI Linux 通过；无数据竞争 | done（[T-0019](tasks/T-0019-request-envelope.md)） |
| B-07 | `request/header` + `request/context` 落盘（首轮/信封变化/替换后/恢复时写；未变步骤继承） | M | — | 同会话第二次请求不重复 header；改工具或模型则写新 header；golden 测试 | done（[T-0019](tasks/T-0019-request-envelope.md)） |
| B-08 | `system/message` 落盘 + `internal/prompt` 静态组装（片段 + 工具 schema） | M | B-07 | 日志含 system/message；resume 后请求与首轮逐字段一致 | done（[T-0019](tasks/T-0019-request-envelope.md)） |
| B-09 | 每批 append 后 fsync + `session/flush` 屏障 | S | — | step 边界可等待 flush；写完不 Close 直接 kill 后日志完整 | done（[T-0020](tasks/T-0020-durability-lock.md)） |
| B-10 | 跨进程单写者锁（Windows `LockFileEx` / POSIX `flock`，纯标准库） | L | — | 双进程抢写同一会话，第二个明确被拒；两平台各有用例；**不允许静默降级** | done（[T-0020](tasks/T-0020-durability-lock.md)） |

## 决策项（未拍板前不动工）

| ID | 事项 | 需要的决策 | 影响 |
|---|---|---|---|
| B-11 | 读取上游 `.jsonl.zstd` 会话 + generation 选择 | Q-0002 是否允许引依赖（zstd），或自研解码 / 仅支持未压缩 | 决定能否打开安装版的真实历史会话 |
| B-12 | Web 鉴权（token / `authorize` 三态） | 安全与产品交互：默认是否要 token、如何传给前端 | 本机其它进程可驱动 harness 的现状是否接受 |
| B-13 | P2 起点选哪条 | 沙箱三模式（安全）vs fs/shell seam（架构）vs spill（输出治理） | 决定下一个大阶段的推进顺序 |

## P2 · 工具生态与可运维

| ID | 事项 | 规模 | 依赖 | 状态 |
|---|---|---|---|---|
| B-20 | `fs` / `shell` / `subprocess` seam 抽取（把 `builtin` 从具体实现里解耦） | L | — | todo |
| B-21 | `sandbox` 三模式（read-only / workspace-write / danger-full-access）+ 平台后端 | XL | B-20 | todo |
| B-22 | 工具输出 spill / 落盘策略 | M | B-20 | todo |
| B-23 | `tools/pre-execute` waterfall + 审批 seam（替代裸 `Guard`） | M | B-17 | todo |
| B-24 | `compaction`（basic + 工具结果剪枝） | L | B-07/B-08 | todo |
| B-25 | `web_fetch` / `web_search` | M | B-21（网络策略） | todo |
| B-26 | `context` providers（time / instructions） | S | B-08 | todo |
| B-27 | LLM 失败分类（AUTH / RATE_LIMIT / CONTEXT_WINDOW_EXCEEDED / NO_ADAPTER）+ 重试 | M | — | todo |
| B-28 | `.env` 两层加载 + `DSH_PERMISSION_MODE` | S | — | todo |
| B-29 | 日志文件 + 启动失败诊断（`Full diagnostics:` + 日志路径） | S | — | todo |
| B-30 | 内核四种分发模式（Waterfall / Serial / Bail / Parallel） | M | — | todo |
| B-31 | `todo` / `working_directory` 状态落盘（`todo/write`、`working/directory` 事件） | S | B-02 | todo |
| B-32 | `dsh sessions` 子命令（列出/查看会话） | S | — | todo |
| B-33 | Web UI 会话列表/切换与刷新保留；工具调用卡片 | M | B-32 | todo |

## 工程化（穿插做，不单独排阶段）

| ID | 事项 | 规模 | 状态 |
|---|---|---|---|
| B-40 | CI 增加 `staticcheck` | XS | todo |
| B-41 | `cmd/dsh` 可测化（boot 注入），提升 CLI 覆盖率 | M | todo |
| B-42 | 架构/迁移映射的自动漂移检查（目前 README 三块已自动化，其余人工） | M | todo |

## P3 · 后续阶段（只登记，不排期）

MCP / LSP / Skill / Subagent / Team / Jobs / Goal / Preset+Scope / Storage / Settings / Credentials / SessionQuery / 自动标题 / 历史格式迁移链 —— 范围与优先级见 [MIGRATION_MAP](MIGRATION_MAP.md) §3 与 §5 的 P3/P4 行。

## 建议的批次拆分（供确认）

| 批次 | 内容 | 预估 | 为什么这个顺序 |
|---|---|---|---|
| **5** | P0：B-01 + B-02 + B-03，同批带上 B-04/B-05 | ✅ 已完成 2026-11-22（[T-0018](tasks/T-0018-crash-recovery.md)） | 唯一"实测已坏"的一组；崩溃恢复、互斥、错误码、取消语义互相咬合，一起做省一次回归 |
| **6** | B-06（并发压测）+ B-07 + B-08 | ✅ 已完成 2026-11-22（[T-0019](tasks/T-0019-request-envelope.md)） | 数据完整性：请求信封与系统提示越晚补，历史日志缺得越多；压测先把 B-02 的并发假设钉住 |
| **7** | B-09 + B-10 | ✅ 已完成 2026-11-22（[T-0020](tasks/T-0020-durability-lock.md)） | 持久化硬化，风险最高（跨平台锁），单独一批便于回滚 |
| **8** | B-27 + B-28 + B-29 + B-30 | 1 天 | 错误分类与 CLI/内核的"可运维性"，做完 P1 就真的收口 |
| **9** | 按 B-13 的决策进入 P2 | — | P2 的第一块取决于你要安全（沙箱）还是架构（seam）先走 |

**当前建议**：批次 7 已完成；下一批是**批次 8**（B-27 错误分类 + B-28 `.env`/`DSH_PERMISSION_MODE` + B-29 日志与诊断 + B-30 内核分发模式），见 Q-0013 的 B 选项。
