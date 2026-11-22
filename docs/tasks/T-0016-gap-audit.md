# T-0016 · 差距审计：现在缺什么（完整盘点）

- **状态**：done（只做盘点，不含实施）
- **更新**：2026-11-21
- **关联决策**：D-0002（本审计不构成实施授权；下一批见 Q-0011）
- **方法**：重读全部生产代码与文档，对照 [MIGRATION_MAP](../MIGRATION_MAP.md) §5 P1 范围/验收、§6 兼容性条目；用 `bin/dsh.exe` 实跑 CLI、用真实 DeepSeek API 复现崩溃恢复；`go test -cover` 量覆盖率。

## 1. 现状快照（2026-11-21 实测）

| 维度 | 现状 |
|---|---|
| 代码规模 | **51 个 .go 文件、8,844 行**（生产 ≈5,200 + 测试 ≈3,600）；`go.mod` 零第三方依赖，`go 1.27.4` |
| 包 | `kernel`、`config`、`profiles`、`llm`、`tool`、`builtin`、`session`、`agent`、`server`、`version` + `cmd/dsh` |
| CLI | 5 个子命令：`version` / `web` / `chat` / `tools` / `config [init]`；公共 flag 8 个；退出码：用法错误 2、其他失败 1、成功 0 |
| 工具 | 11 个（`read`/`write`/`list_dir`/`edit`/`glob`/`grep`/`read_image`/`working_directory`/`todo_write`/`now`/`pwsh`\|`bash`），4 个插件 |
| 会话 | v4 事件流（63 类型白名单 + 未知类型 `ignorable` 规则 + `surfaceOp` + 投影 + 尾部修复） |
| LLM | 非流式 + SSE 流式（可选 `StreamingClient`），工具调用分片组装 |
| Web | 6 条路由（`/healthz`、`/api/sessions` GET/POST、`/api/sessions/{id}`、`/api/chat`（JSON/SSE 协商）、`/`）；无鉴权 |
| 测试 | **122 个用例**；覆盖率 agent 80.5%、builtin 79.6%、config 65.3%、kernel 83.5%、llm 81.8%、profiles 81.0%、server 69.8%、session 75.5%、tool 81.3%、`cmd/dsh` **0%** |
| CI | ubuntu+windows：gofmt、vet、test、`test -race`（Linux）、build；外加 webui job（真实 Chrome CDP） |

## 2. P1 验收标准对照（MIGRATION_MAP §5）

| # | 验收标准 | 结论 | 证据 |
|---|---|---|---|
| ① | 录制真实会话与上游 JSONL 逐事件比对（字段/seq/surfaceOp） | **达标**（2026-11-22） | 读方向：上游样本对拍（T-0008）；写方向：`scripts/upstream-scan.mts` 用上游 `scanLog` 读 Go 写出的日志，单轮 22 事件与两轮续接 30 事件均 `scan: ok`、`seq` 连续、字节完全消费（T-0024）。期间发现并修正了 `system/message` 必须在 step 内 的顺序错误 |
| ② | 杀进程后 resume，补齐 `interruptedTurnClosers`，不丢已提交前缀 | **达标**（T-0018） | 四种截断位置 resume 后真实 API 均接受；崩溃补齐用例 |
| ③ | `-race` 下 100 并发会话读写无数据竞争 | **达标**（T-0019） | 100 并发会话压测；CI ubuntu 跑 `-race` |
| ④ | 会话文件可与上游 v4 读取器互开（只读方向） | **达标**（2026-11-22） | 同上 ①：上游读取器能读 Go 产物；Go 能读上游未压缩样本 |
| ⑤ | 工具超时/截断/错误以 `isError` 回填而非中断循环 | **达标** | `tool.Pipeline` 单测（超时/panic/截断/guard）+ 真实 API 工具调用 |

## 3. 缺口清单

### A. 阻塞"P1 完成"的四件事

| ID | 缺口 | 后果 | 证据 |
|---|---|---|---|
| A1 | **崩溃恢复不补 `interruptedTurnClosers`**：无 `TOOL_OUTCOME_UNKNOWN` / `TOOL_NOT_STARTED` 结果、无补 `step/end`、无 `turn/end{interrupted}` | 工具调用中途被杀 → 会话**无法 resume**（真实 API 400）；长任务丢的是"能否继续"，不是数据 | 见 §2 ② |
| A2 | **`request/header` + `request/context` 从未写入**（只在已知类型表里） | 无法从日志重建"当时发给模型的确切请求"（tools 集合、采样参数、provider/model），违反"模型可见即已记录" | `format.go:254` 只有类型；`agent/loop.go` 无写入 |
| A3 | **`system/message` 从未写入**；系统提示是 `cmd/dsh/main.go` 里拼的字符串（`agent.DefaultSystemPrompt` + runtime context） | 日志里没有系统提示，resume 后无法复现同一请求；`internal/prompt`（§4.6 P1 静态组装）不存在 | `grep '"system/message"'` 只命中 `format.go`/`projection.go` |
| A4 | **每批 append 后没有 fsync / 没 `session/flush` 屏障** | 进程被杀不丢（Write 已进内核），但**机器掉电可能丢最后一批事件**；上游是"每批 append 后 fsync + flush 屏障" | `Writer.Sync()` 只在 `Close()` 调用；`AppendEvent` 只 `file.Write` |

### B. 规范符合性缺口（MIGRATION_MAP §6 A/B 级）

| ID | 缺口 | 级别 | 备注 |
|---|---|---|---|
| B1 | 内核事件只有 `Emit`，缺 `Waterfall` / `Serial` / `Bail` / `Parallel` 四种分发 | §4.1 标为 **P1 必修** | `agent/pre-step`、`system-prompt/assemble`、`webserver/authorize` 的前置能力 |
| B2 | 跨进程单写者锁缺失（只有进程内 `activeWriters` map） | §6-1 **A** | 两个 `dsh` 进程写同一会话会交错追加 |
| B3 | 无 generation 选择、无 `session.v4.jsonl.zstd` 读取、无历史迁移链 | §6-1/6 **A/B** | Q-0002 已把 zstd 写入推 P3，但**读**上游 `.jsonl.zstd`（安装版真实会话）仍不可用 |
| B4 | LLM 失败无分类：只有 `ErrMissingAPIKey` + 通用 `HTTP %d` 字符串 | §6-9 **B** | 缺 `AUTH` / `RATE_LIMIT` / `CONTEXT_WINDOW_EXCEEDED` / `NO_ADAPTER`，无 retry（retry 属 P2） |
| B5 | 工具结果无结构化错误码 | §6-8 **B** | 只有自由文本 + `isError`；缺 `TOOL_TIMEOUT` / `TOOL_NOT_STARTED` / `TOOL_OUTCOME_UNKNOWN` |
| B6 | CLI 信号语义不符：SIGINT 记 `turn/end{error}` + 退出码 1 | §6-18 **C** | 上游：SIGINT=130、SIGTERM=0、首个信号 5 秒优雅退出 |
| B7 | 无启动失败诊断（`Full diagnostics:` + 日志文件路径） | §6-18 **C** | 只有 stderr 上的一行 slog |
| B8 | 无 `.env` 两层加载、无 `DSH_PERMISSION_MODE` / `DSH_TELEMETRY_*` | §6-12 **B** | |
| B9 | Web 无鉴权（`webserver/authorize` 三态、token） | §6-13 **B** | 只靠默认 `127.0.0.1` 绑定；本机任意进程可驱动 harness |

### C. 实测发现的健壮性缺陷

| ID | 缺陷 | 复现 |
|---|---|---|
| C1 | 会话日志**不容忍 UTF-8 BOM**（config/profiles 已容忍） | 用 PowerShell 写 `.jsonl` → `session: malformed session log: line 1: invalid character '\ufeff'` |
| C2 | `Store.Load` 相信 header 里的 `id`，与文件名不一致时错误信息指向用户没提过的文件名 | 把会话文件改名后 `--session <新名>` → `session: read …<旧 id>.jsonl: 系统找不到指定的文件` |
| C3 | 同一会话并发 turn 无互斥 | Web 两个 `POST /api/chat` 打同一 `sessionId` → 两个 loop 交错写 `turn/start`/`step/*`（Writer 有行级 mutex，事件语义仍会乱） |
| C4 | `turn/end` 只有 `completed` / `error` | 取消、中断都没有独立 reason（上游 `cancelled` / `interrupted`） |
| C5 | `todo_write` 状态只在内存 | 重启丢失；上游写 `todo/write` 事件 |
| C6 | `working_directory` 不落盘 | resume 后 shell cwd 回不到原值 |
| C7 | 已知类型表里 `todo/write`、`working/directory` 等事件从不产生 | 日志无法恢复这两类状态 |

### D. 工程化与可观测性

| ID | 缺口 |
|---|---|
| D1 | CI 不跑 `staticcheck`（只在本地门禁） |
| D2 | CI 不跑 `scripts/check_md_links.py`（文档链接断裂不会失败） |
| D3 | `cmd/dsh` 覆盖率 0%：参数解析、profile 装配、退出码全靠手工验证 |
| D4 | 无"杀进程 + resume"的自动化回归（A1 没有网） |
| D5 | 无并发会话压力用例（P1 验收 ③ 没有网） |
| D6 | 无上游 `.zstd` 样本读取用例（B3 没有网） |

### E. 文档偏差

| ID | 偏差 |
|---|---|
| E1 | **README 陈旧**：仍写"meta 行 + message 行"的 JSONL、`internal/builtin`（fs/shell/time）、"尚未迁移 edit/glob/grep/todo"、"可运行骨架"；未提 v4、流式、profiles、工具批次并发 |
| E2 | MIGRATION_MAP §5 P1 验收没有"达标情况"列（本审计 §2 是唯一对照） |
| E3 | `docs/STATUS.md` 未记录 README 待同步 |

### F. 明确留到后续阶段（不算本轮缺口）

- **P2**：`fs`/`shell`/`subprocess`/`sandbox` seam、三模式策略、spill、compaction、guard、approval、`web_fetch`/`web_search`、context providers、工具重试。
- **P3**：MCP、LSP、skill、subagent、team、jobs、goal、plan、preset/scope、storage/settings/credentials/identity、session-query/FTS、projection 缓存、session 标题自动生成。
- **P4**：SDK/ACP、workflow/PTC、Office、terminal、telemetry、desktop、上游前端托管、`/api/remote.mux`。

## 4. 建议的下一批（P1 收口，按依赖排序）

| 顺序 | 任务 | 内容 | 验收命令 |
|---|---|---|---|
| 1 | **T-0017 resume 崩溃补齐** | `interruptedTurnClosers`（未应答工具调用补 `TOOL_OUTCOME_UNKNOWN`/`TOOL_NOT_STARTED`、补 `step/end`、补 `turn/end{interrupted}`）；`Load` 校验 header id 与文件名；同会话 turn 互斥；`cancelled` reason | 脚本：截断真实日志（三种位置：tool/call 后、tool/result 后、assistant 后）→ resume → 断言补齐事件 + 真实 API 接受历史 |
| 2 | **T-0018 请求信封与系统提示** | `system/message` + `request/header` + `request/context` 落盘；`internal/prompt` 静态组装（片段 + 工具 schema）；resume 后请求可重建 | 日志 golden：同会话第二次运行必须沿用同一 header；`Project` 与请求逐字段一致 |
| 3 | **T-0019 持久化硬化** | `session/flush` 屏障 + 每批 fsync；跨进程单写者锁（Windows `LockFileEx` / POSIX `flock`，纯标准库）；会话日志 BOM 容忍 | 双进程抢写 → 第二个被拒；掉电模拟（写完不 Close 直接 kill）→ 日志可读；BOM 文件可读 |
| 4 | **T-0020 LLM 错误分类 + CLI 语义** | `AUTH`/`RATE_LIMIT`/`CONTEXT_WINDOW_EXCEEDED`/`NO_ADAPTER` 分类；SIGINT 130 / SIGTERM 0 / 5 秒优雅退出；启动失败 `Full diagnostics:` | 假服务器返回 401/429/400 → 断言错误码；Ctrl+C → 130 且日志有 `turn/end{cancelled}` |
| 5 | **T-0021 内核分发模式** | `Waterfall`/`Serial`/`Bail`/`Parallel`（B1），为 T-0018 的 `agent/pre-step` 铺路 | 四种模式的顺序/中间件/中断语义单测 |
| 6 | 顺带 | CI 补 `staticcheck` + md 链接检查（D1/D2，10 分钟） | CI 日志出现两步 |

**建议顺序 1 → 2 → 3 → 4/5**：1 是唯一"实测已坏"的项；2 是数据完整性（越晚补越贵，历史会话缺 header）；3 的风险最高（跨平台锁），单独一批好回滚；4/5 可并行。

## 5. 未验证与风险

- **A4（fsync）与 B6（信号）是代码级结论**，没用真实掉电/Ctrl+C 测量；写进验收命令后再测。
- **B3（zstd 读）**需要决策是否引依赖（Q-0002 目前是零依赖），不引就只能读未压缩样本。
- **B2（跨进程锁）**在 Windows 上要用 `LockFileEx`，纯标准库可行但要小心 `syscall` + `unsafe`；失败模式必须是"拒绝第二个写者"，不能静默降级。
