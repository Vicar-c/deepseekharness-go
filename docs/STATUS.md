# 项目状态（跨会话第一入口）

> 更新规则：**每次会话结束前必须更新本文件**。任何 Agent/人接手项目，先读本文件，再读相关任务与决策记录。
> 最后更新：2026-11-21 · 维护者：lead agent（root）

## 当前阶段

**P1 单 agent 闭环已完成主体（批次 0–4）；真实 API 与真实浏览器均已验证。**

项目：DeepSeek Harness（DSH）的 Go 重写实现，运行入口 `cmd/dsh`。
参考源码：`%USERPROFILE%\Desktop\deepseekharness`（上游 TS 实现，只读）。

> **实施门禁（D-0002）**：用户逐轮的"继续/开工"即为对应批次的实施授权；新的范围仍需单独决策。

## 环境事实

| 项 | 状态 |
|---|---|
| Go | `go1.27.4 windows/amd64`，位于 `%LOCALAPPDATA%\go` |
| Go 工具 | gopls v0.24.0、dlv、staticcheck 2026.2.2、golangci-lint v2.15.0、goimports、air、wire（`%USERPROFILE%\go\bin`） |
| Node/pnpm | Node v24.22.3、pnpm 11.28.5（仅供参考仓库使用） |
| 密钥 | `DEEPSEEK_API_KEY` 已配置于**用户环境变量**（不落盘、不入库）；当前 DSH 会话内需手动设置后才可见 |
| 数据目录 | `~/.deepseekharness-go/`（config + sessions）；旧目录 `~/.deekseekharness-go/` 已在本机一次性迁移完毕，迁移逻辑见 [D-0005](decisions/D-0005-data-directory-rename.md) 与 [T-0006](tasks/T-0006-data-directory-rename.md)。**不读上游 `DSH_HOME`**：安装版应用导出 `DSH_HOME=~/.dsh`，读它会让 Go 版加载另一个产品的 profiles（见 [T-0014](tasks/T-0014-profiles-and-dump-config.md)） |
| 网络 | npmmirror / goproxy.cn 已配置；GitHub 直连不稳，必要时用 OpenSSL 后端与代理 |
| 远端仓库 | `origin` → <https://github.com/Vicar-c/deepseekharness-go>（2026-11-16 由 `deekseekharness-go` 改名，模块路径同步改为 `github.com/Vicar-c/deepseekharness-go`）；**Git 不读 Windows 系统代理**。2026-11-21：本机代理未运行、直连可用，已移除仓库级 `http.proxy`；若直连再次失败，重新设置 `git config --local http.proxy http://127.0.0.1:7890` |
| 提交身份 | `Vicar-c <244100401@qq.com>`（仓库级 `user.name`/`user.email` 固定，规则见 [D-0003](decisions/D-0003-commit-identity.md)） |
| 工作区状态 | 用户会在代码里写中文学习注释，**不提交、不还原**（[D-0006](decisions/D-0006-user-study-comments.md)）；Agent 提交只显式列自己的路径，禁用 `git add -A` |
| 补测试 | 为已实现行为补测试**无需询问**，直接执行（[D-0007](decisions/D-0007-test-backfill-authorization.md)）；发现缺陷需改实现时回到 D-0002 门禁 |

## 已完成

- 参考源码完整 clone（27,219 commits，HEAD `39163dd3`）并从源码构建成功（10/10 gates）。
- Go 骨架：kernel / llm / tool / builtin / session / agent / server / config / version / CLI，20 个 Go 文件、约 3,365 行、8 个测试文件。
- 测试与质量：`go test` 全绿、`go vet` / `staticcheck` 0 告警、`scripts/build.ps1` 构建通过。
- 真实 DeepSeek API 端到端验证：CLI 普通对话与工具调用（write/read 真实落盘）、Web `/api/chat`、多轮上下文、`pwsh` PowerShell 方言、模型身份上下文。
- 修复：`pwsh` 工具方言、运行上下文注入、数据目录隔离（含清理误入 `~/.dsh/sessions` 的 1 个文件）。
- 文档：README、ARCHITECTURE、MIGRATION_MAP（578 行）。
- 协作契约：AGENTS.md、AGENT_WORKFLOW.md、任务/决策记录体系与本快照。
- 发布：仓库级提交身份固定为 `Vicar-c <244100401@qq.com>`（6 个历史提交已改写为该身份），远端 `origin` 指向 GitHub；记录见 [D-0003](decisions/D-0003-commit-identity.md) 与 [T-0004](tasks/T-0004-github-publication.md)。
- 改名与加固：仓库、模块路径、文档统一为 `deepseekharness-go`（`github.com/Vicar-c/deepseekharness-go`）；文档中的本机绝对路径替换为 `%USERPROFILE%` / `%LOCALAPPDATA%` 可移植写法；新增 GitHub Actions CI（ubuntu / windows 双平台）。记录见 [D-0004](decisions/D-0004-rename-and-hardening.md) 与 [T-0005](tasks/T-0005-rename-and-hardening.md)。
- 数据目录：`~/.deepseekharness-go/` 落地，旧目录首次启动自动迁移（本机实测 2 个历史会话完整保留）；`--config` 帮助文本同步修正。记录见 [D-0005](decisions/D-0005-data-directory-rename.md) 与 [T-0006](tasks/T-0006-data-directory-rename.md)。
- 工具面（批次 0+1）：`.gitattributes` 行尾规范；`builtin` 拆分为 6 个文件，新增 `edit`/`glob`/`grep`/`read_image`/`todo_write`/`working_directory`，文件与 shell 工具共享 `Workspace`，工具数 5 → **11**。记录见 [T-0007](tasks/T-0007-batch0-1-tools.md)。
- v4 格式层（批次 2 第一步）：`internal/session/format.go` 可读取**真实上游 v4 会话**（seq 连续性校验、未知类型 `ignorable` 规则、`surfaceOp` 两种形态），编解码往返保真；夹具含出处。记录见 [T-0008](tasks/T-0008-session-v4-format-layer.md)。
- v4 投影与写入器（批次 2 第二/三步）：`Recording.Project()` 把 surface 事件投影成 `[]llm.Message`（含 replace 语义、tool 名字反查），`Writer` 支持原子创建、seq 续写、进程内单写者、崩溃尾部修复。记录见 [T-0009](tasks/T-0009-session-v4-projection-and-writer.md)。
- v4 接线（批次 2 收口）：`Store` 写 header + 事件、`Load` 走"修复 → 解码 → 投影"，`agent.Loop` 一次对话写完整 turn/step/tool 事件序列；真实 CLI（mock 模型与**真实 API**）均产出合法 v4 日志。记录见 [T-0010](tasks/T-0010-session-v4-wiring.md)。
- 流式（批次 3 第一步）：`llm.StreamingClient`（可选接口，不破坏 `Client`）+ DeepSeek SSE 解析与增量组装、`agent/stream` 瞬态事件、CLI 逐字输出（只打印一次）；真实 API 流式已验证。记录见 [T-0011](tasks/T-0011-streaming-llm-agent-cli.md)。
- Web 流式（批次 3 收口）：`/api/chat` 按 `Accept: text/event-stream` 协商输出 SSE（`delta` / `done` / `error` 帧），前端 `fetch` + `ReadableStream` 逐字渲染；普通 JSON 调用保持兼容。记录见 [T-0012](tasks/T-0012-web-sse.md)。
- 浏览器端 E2E：`scripts/webui-e2e.mjs` 用真实 headless Chrome（CDP，零 npm 依赖）驱动真实 `dsh web`，断言增量渲染（采样到 `web`→`web-`→`web-stream`）且无错误气泡；CI 新增 `webui` job（ubuntu + windows）。记录见 [T-0013](tasks/T-0013-browser-e2e.md)。
- 配置来源（批次 4 基础）：`config.Config` 记录每个设置的来源（default / `file:` / `env:` / `flag:` / bundle / profile / home / `patch:`），新增 `MaxParallelToolCalls`（默认 10）与 `Set` 入口；本地 JSON 容忍 UTF-8 BOM。记录见 [T-0014](tasks/T-0014-profiles-and-dump-config.md)。
- profiles 层叠（批次 4）：`internal/profiles` 实现 `bundle → profile patch → home patch → --patch`，`--profile` / `--patch` / `--dump-config` 上线；bundle 为 `base` / `web-app` / `headless`，profile 由内建名或 `<config dir>/profiles/<name>/package.json` 定义；传输门禁让 `dsh web --profile headless` 这类错误组合快速失败。记录见 [T-0014](tasks/T-0014-profiles-and-dump-config.md)。
- 工具批次（批次 4）：`tool.ConcurrencySafe` 可选接口 + `Pipeline.ExecuteBatch`（并发上限、独占 barrier、模型序结果槽、取消语义），内置工具中仅只读的 6 个 opt-in；`agent.Loop` 先落全部 `tool/call` 再按模型序落 `tool/result`。记录见 [T-0015](tasks/T-0015-tool-batch.md)。
- 差距审计（2026-11-21）：重读全部代码与文档，对照 P1 范围/验收与 §6 兼容性条目，实跑 CLI 与真实 API 复现崩溃恢复；**P1 验收 ②（resume 补齐）与 ③（并发会话压测）实测未达标**，另有请求信封、系统提示落盘、fsync 屏障、跨进程锁、LLM 错误分类等缺口。记录见 [T-0016](tasks/T-0016-gap-audit.md)。
- 崩溃恢复与取消语义（批次 5，2026-11-22）：`Store.Resume` 在续接前补齐 `interruptedTurnClosers`（未应答 `tool/call` → `TOOL_OUTCOME_UNKNOWN`、未发起 → `TOOL_NOT_STARTED`、补 `step/end`、补 `turn/end{interrupted}`）；同会话 turn 租约、`Load` 校验 header id、会话日志 BOM 容忍（含 `RepairTail` 不误截断）；`tool.Result.Code` 五个错误码；`turn/end{cancelled}` 与 SIGINT=130 / SIGTERM=0。记录见 [T-0018](tasks/T-0018-crash-recovery.md)。`n- 文档同步机制（2026-11-21）：用户要求"文档随实现并行更新"→ [D-0008](decisions/D-0008-docs-in-sync.md)；重写 README（11 工具 / v4 / 流式 / profiles / 批次并发），新增 `cmd/dsh/docs_test.go` 把 **命令清单、工具表、配置示例** 钉成测试（负向验证过），AGENTS.md §1.14 规定改动↔文档对照表，CI 增加 Markdown 链接校验。记录见 [T-0017](tasks/T-0017-docs-in-sync.md)。

## 进行中

- 发布、改名与数据目录整理已完成（[T-0005](tasks/T-0005-rename-and-hardening.md)、[T-0006](tasks/T-0006-data-directory-rename.md)）。
- 批次 0+1 已完成（[T-0007](tasks/T-0007-batch0-1-tools.md)）：行尾规范 + 内置工具补齐。
- **批次 2 已完成**（[T-0008](tasks/T-0008-session-v4-format-layer.md)、[T-0009](tasks/T-0009-session-v4-projection-and-writer.md)、[T-0010](tasks/T-0010-session-v4-wiring.md)）：会话以 v4 事件流落盘，CLI / Web / agent 全链路接通；真实 DeepSeek API 已验证（13 行日志，含 `tool/call` + `tool/result`）。
- **批次 3 已完成**（[T-0011](tasks/T-0011-streaming-llm-agent-cli.md)、[T-0012](tasks/T-0012-web-sse.md)）：流式全链路 `llm.StreamingClient → agent/stream → CLI / Web SSE`，真实 API 与真实 `dsh web` 进程均已验证；[T-0013](tasks/T-0013-browser-e2e.md) 补上真实浏览器渲染断言。
- **批次 4 已完成**（[T-0014](tasks/T-0014-profiles-and-dump-config.md)、[T-0015](tasks/T-0015-tool-batch.md)）：profiles 层叠 + `--dump-config` + `--patch`，以及工具批次并发 / barrier / 模型序。
- 待办与优先级：**唯一来源是 [docs/BACKLOG.md](BACKLOG.md)**（P0/P1/P2/P3 + 规模 + 依赖 + 验收要点 + 批次拆分）。
  - **批次 5 已完成**（[T-0018](tasks/T-0018-crash-recovery.md)）：B-01 崩溃恢复、B-02 互斥/id 校验、B-03 BOM、B-04 错误码、B-05 取消语义与信号。
  - **下一批（批次 6）**：B-06 并发压测、B-07 `request/header`、B-08 系统提示落盘 —— 见决策 **Q-0011**。
  - 决策项：B-11 zstd 读取（依赖）、B-12 Web 鉴权、B-13 P2 起点（沙箱 vs seam）。

## 决策队列（与会话内表格同源）

> 规则（D-0002）：逐项决策；未决项存在时不实施；只有用户明确说"实现"才进入实施。
> 决策辅助材料：[PLAN_P1.md](PLAN_P1.md)（Q-0001…Q-0005 的改动面、风险、建议与开工顺序）。

| ID | 待决问题 | 选项 | 建议 | 状态 |
|---|---|---|---|---|
| Q-0001 | P1 先做哪条主线？ | A. 会话格式 v4 / B. 流式 SSE / C. 工具补齐 | A | **部分决策**：用户指定"开工 0->1"，即先做工具面（C）；批次 2（会话 v4）待确认 |
| Q-0002 | 是否引入第三方 Go 依赖？ | A. 暂不引入（P1 全标准库）/ B. 允许 zstd、sqlite、websocket 等按需引入 | A | **默认采纳 A**：批次 2 授权未提出异议；v4 先写 `compression:none`，如需 zstd 再单独决策 |
| Q-0003 | 是否规范化行尾？ | A. 保持现状 / B. 增加 `.gitattributes`（`* text=auto eol=lf`） | B | **resolved**：已加 `.gitattributes`（[T-0007](tasks/T-0007-batch0-1-tools.md)） |
| Q-0004 | 是否与上游 TS 实现做 session 互操作验证？ | A. 暂不做 / B. P1 一起做 | B | **默认采纳 B**：已用上游**未归一化**真实样本对拍（[T-0008](tasks/T-0008-session-v4-format-layer.md)） |
| Q-0005 | 是否确认 D-0001-06…14 的代理推导基调？ | A. 全部接受 / B. 部分修订（指出条目）/ C. 否决 | A | 待决策 |
| Q-0006 | 是否给模块路径补 host 前缀？ | A. 改为 `github.com/Vicar-c/deepseekharness-go` + 同步 import / B. 保持现状 | A | **resolved**：已按 A 处理（[T-0005](tasks/T-0005-rename-and-hardening.md)） |
| Q-0007 | 公开仓库中文档里的本机绝对路径是否替换？ | A. 替换为可移植写法 / B. 保持现状 | A | **resolved**：已按 A 处理（[T-0005](tasks/T-0005-rename-and-hardening.md)） |
| Q-0008 | 是否补最小 CI（`.github/workflows/ci.yml`）？ | A. 补 / B. 暂不补 | A | **resolved**：已按 A 处理（[T-0005](tasks/T-0005-rename-and-hardening.md)） |
| Q-0009 | 运行时数据目录是否从 `~/.deekseekharness-go/` 改名为 `~/.deepseekharness-go/`？ | A. 改名 + 一次性迁移 / B. 保持旧名 | A | **resolved**：已按 A 处理（[D-0005](decisions/D-0005-data-directory-rename.md)、[T-0006](tasks/T-0006-data-directory-rename.md)） |
| Q-0010 | 批次 4 之后下一批做什么？ | A. P1 收尾 / B. 直接进 P2 / C. 其他 | A | **已被 Q-0011 取代**：用户要求先做完整差距审计 |
| Q-0011 | 差距审计后先做哪一项？ | A. [BACKLOG](BACKLOG.md) 批次 5（P0：B-01 崩溃恢复 + B-02 互斥/id 校验 + B-03 BOM，带 B-04/B-05）/ B. 批次 5–8 一次授权全部 P1 收口 / C. 只做 B-01 / D. 直接进 P2 | A | **已按 A 完成批次 5**（[T-0018](tasks/T-0018-crash-recovery.md)）；下一批见 Q-0012 |
| Q-0012 | 批次 5 之后做什么？ | A. 批次 6（B-06 并发压测 + B-07 请求信封 + B-08 系统提示）/ B. 批次 6–8 一次授权做完 P1 收口 / C. 只做 B-07（请求信封）/ D. 直接进 P2 | A | 待决策 |

## 最近验证（2026-11-16）

- `go test -count=1 ./...` → 8 个包全部 ok
- `staticcheck ./...` / `go vet ./...` → exit 0
- `.\scripts\build.ps1` → `bin\dsh.exe`（commit 3b412be）
- `python scripts/check_md_links.py` → 14 个 md、38 条相对链接、0 缺失
- 密钥扫描（真实 key 匹配工作区文件）→ 0 命中
- 真实 API：`dsh chat --prompt` 正常；工具调用写文件成功；Web `/api/chat` 正常；多轮上下文正确
- 隔离验证：`~/.deekseekharness-go/sessions` 正常创建，`~/.dsh/sessions` 未被写入

### 改名后复验（T-0005）

- `gofmt -l .` → 无输出；`go vet ./...` → exit 0
- `go test -count=1 ./...` → 8 个包 ok（模块路径 `github.com/Vicar-c/deepseekharness-go`）
- `go build ./cmd/dsh` → exit 0；`dsh version` → `deepseekharness-go 0.1.0-dev`
- `GOOS=linux go build ./...` → exit 0（CI ubuntu 作业的前提）
- `python scripts/check_md_links.py` → 20 个 md、77 条相对链接、0 缺失
- 残留检查：仓库内无 `C:\Users\ADMIN`，无裸 `deekseekharness-go/internal/` import；旧拼写仅剩历史记录（占位提交身份、迁移说明）
- `go install github.com/Vicar-c/deepseekharness-go/cmd/dsh@latest` → 成功（3.1s）
- GitHub Actions `CI`（ubuntu + windows）→ `43d262d` 起全绿；`7add8f7`（数据目录改名）同样双平台 success
- 数据目录迁移：旧 `~/.deekseekharness-go` → `~/.deepseekharness-go`，2 个历史会话完整保留；二次运行无提示（幂等）
- `python scripts/check_md_links.py` → 22 个 md、95 条相对链接、0 缺失

### 批次 0+1（2026-11-21）

- `gofmt -l .` → 无输出；`go vet ./...` → exit 0
- `go test ./... -count=1` → 8 个包 ok（含 6 个新工具用例）
- `go build ./cmd/dsh` → exit 0；`dsh tools` → **11 tools**、`plugins=4`
- `.gitattributes`：索引本已是 LF，本次只改策略、无内容 diff
- `staticcheck ./...` → 0 告警；`scripts/build.ps1` → `bin/dsh.exe`（commit 注入正确）
- `internal/builtin` 19 个用例（含目录完整性、`working_directory` 对 shell 生效、`shell` 超时被杀、guard 拒绝、边界与流水线行为）；全仓库 63 个用例
- 提交：`c186947`（注释规则）、`8d0499b`（工具）、`e4dd2d1`（记录）、`b3bcfee`（补测）、`8052a11`（验证记录）；用户注释未入库

### 批次 2 第一步：v4 格式层（2026-11-21）

- 真实上游样本（44 行）解码成功 → header + 43 事件，`seq` 0…42 连续，16 个 `surfaceOp:"append"`
- 编解码往返：header 深比较一致，每个事件 `data` 原始字节完全一致
- 拒绝路径：version≠4、未知类型缺 `ignorable`、`seq` 断裂、坏 JSON 行均有专属错误
- `gofmt` clean / `go vet` 0 / `staticcheck` 0；`go test ./...` 8 包全绿
- 用例：session 包 13 个（新增 8）、全仓库 71 个
- 本步不改生产行为：`Store`/`Entry` 与 `agent.Loop` 未接线，可整体回滚

### 批次 2 第二/三步：投影与写入器（2026-11-21）

- 真实样本投影 **16 条消息**（= 16 个 surface 事件）；assistant 的 2 个 tool calls、tool 消息的 name 由 `tool/call` 反查均正确
- `surfaceOp` replace：表面替换成功；越界范围报错
- 写入器：原子创建 header、重新打开后 seq 续写、进程内第二写者被拒（Close 后可重开）
- 尾部修复：半行 JSON 与"以换行结尾但解析失败"的行都被截断；二次修复为 no-op
- session 包 26 个用例、全仓库 84 个；`gofmt`/`vet`/`staticcheck`/`go test` 全绿
- 仍未接线：`Store`、`agent.Loop`、CLI/Web 行为不变

### 批次 2 收口：v4 接线（2026-11-21）

- 单元：`TestLoopPersistsV4EventStream` 事件序列逐项匹配，投影回 4 条消息；session 26 / 全仓库 85 个用例
- 真实 CLI + 本地 mock 模型：退出码 0，落盘 8 行，事件序列完整
- **真实 DeepSeek API**：模型调用 `now` 工具，退出码 0，落盘 **13 行**（含 `tool/call` + `tool/result` 与第二个 step），输出 `2026-11-21 08:50:30 UTC.`
- 缺 key：明确报错，只写 header + `session/title`，不留半截 turn
- `gofmt` clean / `go vet` 0 / `staticcheck` 0 / `go test` 全绿；`scripts/build.ps1` 通过

### 批次 3 第一步：流式（2026-11-21）

- 单元：llm 5 个流式用例（增量顺序、tool-call 分片合并、HTTP 错误、回调中止、缺 key）+ loop 1 个（增量聚合、只落盘一次）；全仓库 91 个用例
- 真实 CLI + SSE mock：stdout 恰好 `Hello from mock`（出现 1 次，无重复）
- **真实 DeepSeek API（流式）**：输出 `stream-ok`，退出码 0，落盘 assistant 内容一致
- 仍有待做：Web `/api/chat` 的 SSE 与前端逐字渲染

### 批次 3 收口：Web SSE（2026-11-21）

- 单元：4 个 server 用例（delta+done、跨会话过滤、错误帧、非流式兼容）；server 包 9 个、全仓库 95 个用例
- **真实 `dsh web` 进程 + mock 流式模型**：`/healthz` 正常、首页含 SSE 前端逻辑、`POST /api/chat`（Accept SSE）收到 `delta`×3 + `done{reply:"web-stream"}`
- 前端浏览器渲染未做自动化（无 headless 浏览器），只做内容级断言 + 协议级真机请求
- `gofmt` / `vet` / `staticcheck` / `go test` 全绿

### 浏览器端 E2E（2026-11-21）

- `scripts/webui-e2e.mjs`：Node 内置 http（流式 mock）+ 真实 `dsh web` + 真实 headless Chrome（CDP 直连，零 npm 依赖）
- 结果：`{"ok":true,"finalText":"web-stream","samples":["web","web-","web-stream"]}` —— **采样到中间态，证明逐字渲染**
- 失败时会输出页面 DOM、浏览器异常/console、network 事件与服务端日志，便于 CI 定位
- CI 新增 `webui` job（ubuntu + windows，使用 runner 预装 Chrome）

### 批次 4：profiles 与工具批次（2026-11-21）

- `gofmt -l .` clean；`go vet ./...` / `staticcheck ./...` → exit 0；`go test ./... -count=1` → **9 个包 ok、122 个用例**（profiles 新增 11、tool 新增 8、config 新增 5+4 子用例、agent 1、builtin 1）
- `.\scripts\build.ps1` → `bin\dsh.exe`
- `dsh tools` → 11 tools（默认 profile 行为不变）；`--patch` 禁用 shell → 10 tools
- `dsh tools --dump-config` → 完整 JSON（profile/bundles/layers/plugins/config/sources），逐设置来源正确
- 用户 profile（`profiles/mini` + home patch）→ 4 层 `layers`、`plugins=3`、来源逐项正确
- 传输门禁：`dsh web --profile headless` / `dsh chat --profile web` → exit 1 + 明确错误
- **真实 DeepSeek API（并发批次）**：一次回复 2 个 `read` 调用，日志 15 行，`tool/call`(seq 5,6) 全部早于 `tool/result`(seq 7,8)，结果按模型序，`turn/end kind=completed`
- CI：新增 `go test -race`（仅 Linux）；本机无 gcc，`-race` 本地未跑（诚实记录）
- 提交：`feat(config)` / `feat(tool)` / `feat(profiles)` / `chore(ci)` / `docs`；用户中文学习注释未入库

### 差距审计（2026-11-21）

- 盘面：51 个 .go 文件 / 8,844 行；122 个用例；覆盖率 65.3%（config）–83.5%（kernel），`cmd/dsh` 0%
- **实测未达标 ①**：把真实会话截断到 `tool/call` 后 resume → DeepSeek **HTTP 400**（assistant `tool_calls` 未配对），日志里没有补任何 `tool/result` / `step/end` / `turn/end{interrupted}`
- **实测未达标 ②**：无并发会话用例（P1 验收 ③ 无网）
- **实测缺陷**：会话日志不容忍 BOM；`Store.Load` 相信 header id（改名文件 → 指向旧文件名报错）
- 静态核对缺口：`request/header`、`request/context`、`system/message` 从未写入；无 fsync 屏障；写者锁仅进程内；内核只有 `Emit`；LLM 无错误分类；CLI 无 SIGINT 130/启动诊断；Web 无鉴权；README 陈旧；CI 缺 staticcheck 与 md 链接检查

### 文档同步（2026-11-21）

- `go test ./cmd/dsh` → 4 个用例（3 个文档契约 + 1 个 dispatch/退出码），全仓库 **126 个用例**；负向验证：删 README 的 `read` 行 / 删配置示例的 `maxParallelToolCalls` 都会让测试失败并指出差异
- `python scripts/check_md_links.py` → 38 篇 md、173 条相对链接、0 缺失
- CI 新增 Markdown 链接校验（Linux）；README 命令/工具/配置三块由 `go test ./...` 在双平台强制
- 提交：README 重写、文档契约测试、CI 链接校验、AGENTS §1.14 + D-0008 记录

### 批次 5：崩溃恢复（2026-11-22）

- **真实 DeepSeek API 四种截断位置各 resume 一次，全部 exit 0**：补 `step/end`+`turn/end{interrupted}`、2×`TOOL_NOT_STARTED`、2×`TOOL_OUTCOME_UNKNOWN`、以及"只补未应答的那一个"；修复前同场景是 HTTP 400
- 全仓库 **142 个用例**（session +7、tool +5、agent +3、server +1、cmd +3）；`gofmt`/`vet`/`staticcheck`/`go test` 全绿
- 浏览器 E2E 仍通过（`samples=["web","web-","web-stream"]`）
- 未验证：真实 Ctrl+C（Windows 无法可靠向子进程注入 SIGINT），只测了退出码映射

## 相关入口

- 协作契约：[AGENTS.md](../AGENTS.md)
- 工作流与模板：[AGENT_WORKFLOW.md](AGENT_WORKFLOW.md)
- 架构：[ARCHITECTURE.md](ARCHITECTURE.md)
- 迁移映射：[MIGRATION_MAP.md](MIGRATION_MAP.md)
- 任务索引：[tasks/README.md](tasks/README.md)
- 决策索引：[decisions/README.md](decisions/README.md)
