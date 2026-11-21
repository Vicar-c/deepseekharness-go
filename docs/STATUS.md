# 项目状态（跨会话第一入口）

> 更新规则：**每次会话结束前必须更新本文件**。任何 Agent/人接手项目，先读本文件，再读相关任务与决策记录。
> 最后更新：2026-11-21 · 维护者：lead agent（root）

## 当前阶段

**P0 骨架完成 + 真实 API 验证通过；P1 尚未启动（决策门禁中）。**

项目：DeepSeek Harness（DSH）的 Go 重写实现，运行入口 `cmd/dsh`。
参考源码：`%USERPROFILE%\Desktop\deepseekharness`（上游 TS 实现，只读）。

> **实施门禁（D-0002）生效**：决策队列未清空、且用户未明确说"实现"之前，不进行任何功能实施。

## 环境事实

| 项 | 状态 |
|---|---|
| Go | `go1.27.4 windows/amd64`，位于 `%LOCALAPPDATA%\go` |
| Go 工具 | gopls v0.24.0、dlv、staticcheck 2026.2.2、golangci-lint v2.15.0、goimports、air、wire（`%USERPROFILE%\go\bin`） |
| Node/pnpm | Node v24.22.3、pnpm 11.28.5（仅供参考仓库使用） |
| 密钥 | `DEEPSEEK_API_KEY` 已配置于**用户环境变量**（不落盘、不入库）；当前 DSH 会话内需手动设置后才可见 |
| 数据目录 | `~/.deepseekharness-go/`（config + sessions）；旧目录 `~/.deekseekharness-go/` 已在本机一次性迁移完毕，迁移逻辑见 [D-0005](decisions/D-0005-data-directory-rename.md) 与 [T-0006](tasks/T-0006-data-directory-rename.md) |
| 网络 | npmmirror / goproxy.cn 已配置；GitHub 直连不稳，必要时用 OpenSSL 后端与代理 |
| 远端仓库 | `origin` → <https://github.com/Vicar-c/deepseekharness-go>（2026-11-16 由 `deekseekharness-go` 改名，模块路径同步改为 `github.com/Vicar-c/deepseekharness-go`）；**Git 不读 Windows 系统代理**，已写入仓库级 `http.proxy = http://127.0.0.1:7890` |
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

## 进行中

- 发布、改名与数据目录整理已完成（[T-0005](tasks/T-0005-rename-and-hardening.md)、[T-0006](tasks/T-0006-data-directory-rename.md)）。
- 批次 0+1 已完成（[T-0007](tasks/T-0007-batch0-1-tools.md)）：行尾规范 + 内置工具补齐。
- **批次 2 已完成**（[T-0008](tasks/T-0008-session-v4-format-layer.md)、[T-0009](tasks/T-0009-session-v4-projection-and-writer.md)、[T-0010](tasks/T-0010-session-v4-wiring.md)）：会话以 v4 事件流落盘，CLI / Web / agent 全链路接通；真实 DeepSeek API 已验证（13 行日志，含 `tool/call` + `tool/result`）。
- 下一步：批次 3（流式 SSE）或批次 4（profiles + `--dump-config`），待用户指定。

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

## 相关入口

- 协作契约：[AGENTS.md](../AGENTS.md)
- 工作流与模板：[AGENT_WORKFLOW.md](AGENT_WORKFLOW.md)
- 架构：[ARCHITECTURE.md](ARCHITECTURE.md)
- 迁移映射：[MIGRATION_MAP.md](MIGRATION_MAP.md)
- 任务索引：[tasks/README.md](tasks/README.md)
- 决策索引：[decisions/README.md](decisions/README.md)
