# 项目状态（跨会话第一入口）

> 更新规则：**每次会话结束前必须更新本文件**。任何 Agent/人接手项目，先读本文件，再读相关任务与决策记录。
> 最后更新：2026-11-16 · 维护者：lead agent（root）

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
| 数据目录 | `~/.deekseekharness-go/`（config + sessions，**沿用旧目录名**；改名涉及已有数据迁移，见决策队列 Q-0009） |
| 网络 | npmmirror / goproxy.cn 已配置；GitHub 直连不稳，必要时用 OpenSSL 后端与代理 |
| 远端仓库 | `origin` → <https://github.com/Vicar-c/deepseekharness-go>（2026-11-16 由 `deekseekharness-go` 改名，模块路径同步改为 `github.com/Vicar-c/deepseekharness-go`）；**Git 不读 Windows 系统代理**，已写入仓库级 `http.proxy = http://127.0.0.1:7890` |
| 提交身份 | `Vicar-c <244100401@qq.com>`（仓库级 `user.name`/`user.email` 固定，规则见 [D-0003](decisions/D-0003-commit-identity.md)） |

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

## 进行中

- 无功能实施。发布与改名整理已完成（[T-0005](tasks/T-0005-rename-and-hardening.md)）；等待用户逐项决策 Q-0001…Q-0005、Q-0009，收到明确"实现"指令后才启动 P1。

## 决策队列（与会话内表格同源）

> 规则（D-0002）：逐项决策；未决项存在时不实施；只有用户明确说"实现"才进入实施。

| ID | 待决问题 | 选项 | 建议 | 状态 |
|---|---|---|---|---|
| Q-0001 | P1 先做哪条主线？ | A. 会话格式 v4 / B. 流式 SSE / C. 工具补齐 | A | 待决策 |
| Q-0002 | 是否引入第三方 Go 依赖？ | A. 暂不引入（P1 全标准库）/ B. 允许 zstd、sqlite、websocket 等按需引入 | A | 待决策 |
| Q-0003 | 是否规范化行尾？ | A. 保持现状 / B. 增加 `.gitattributes`（`* text=auto eol=lf`） | B | 待决策 |
| Q-0004 | 是否与上游 TS 实现做 session 互操作验证？ | A. 暂不做 / B. P1 一起做 | B | 待决策 |
| Q-0005 | 是否确认 D-0001-06…14 的代理推导基调？ | A. 全部接受 / B. 部分修订（指出条目）/ C. 否决 | A | 待决策 |
| Q-0006 | 是否给模块路径补 host 前缀？ | A. 改为 `github.com/Vicar-c/deepseekharness-go` + 同步 import / B. 保持现状 | A | **resolved**：已按 A 处理（[T-0005](tasks/T-0005-rename-and-hardening.md)） |
| Q-0007 | 公开仓库中文档里的本机绝对路径是否替换？ | A. 替换为可移植写法 / B. 保持现状 | A | **resolved**：已按 A 处理（[T-0005](tasks/T-0005-rename-and-hardening.md)） |
| Q-0008 | 是否补最小 CI（`.github/workflows/ci.yml`）？ | A. 补 / B. 暂不补 | A | **resolved**：已按 A 处理（[T-0005](tasks/T-0005-rename-and-hardening.md)） |
| Q-0009 | 运行时数据目录是否从 `~/.deekseekharness-go/` 改名为 `~/.deepseekharness-go/`？ | A. 改名，并对旧目录做一次性迁移（旧目录存在且新目录不存在时自动改名）/ B. 保持旧名 | A | 待决策 |

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
- 残留检查：仓库内无 `C:\Users\ADMIN`，无裸 `deekseekharness-go/internal/` import；仅数据目录名与历史占位身份保留旧拼写

## 相关入口

- 协作契约：[AGENTS.md](../AGENTS.md)
- 工作流与模板：[AGENT_WORKFLOW.md](AGENT_WORKFLOW.md)
- 架构：[ARCHITECTURE.md](ARCHITECTURE.md)
- 迁移映射：[MIGRATION_MAP.md](MIGRATION_MAP.md)
- 任务索引：[tasks/README.md](tasks/README.md)
- 决策索引：[decisions/README.md](decisions/README.md)
