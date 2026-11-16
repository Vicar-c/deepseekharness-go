# T-0001 · 项目引导：Go 骨架、构建与真实 API 验证

- **状态**：done
- **日期**：2026-11-16（单日完成，后续增量由新任务承接）
- **负责人**：lead agent（root）
- **关联决策**：—
- **关联提交**：`1d66d9a`、`23d8e6e`、`d33a5b4`、`3b412be`

## 目标（一句话）

在 `deekseekharness-go` 中建立一个可编译、可运行、可扩展的 Go 重写骨架，并在真实 DeepSeek API 环境下完成端到端验证。

## 非目标（明确不做）

- 不做完整功能对齐（P1–P4 由迁移计划承接）。
- 不迁移 Web 前端（74 个 client 包）、Desktop、沙箱、多 Agent/Team/Workflow。
- 不修改上游参考源码 `..\deepseekharness`。

## 范围与写作用域

| 类型 | 路径 |
|---|---|
| 新增 | `cmd/dsh/`、`internal/{kernel,llm,tool,builtin,session,agent,server,config,version}/`、`scripts/build.ps1`、`Makefile`、`README.md`、`docs/{ARCHITECTURE,MIGRATION_MAP}.md` |
| 修改 | — |
| 只读参考 | `C:\Users\ADMIN\Desktop\deepseekharness`（上游源码与文档） |

## 验收标准（可执行）

- [x] `go build ./...` / `go vet ./...` / `staticcheck ./...` 通过
- [x] `go test ./...` 全绿（8 个包，覆盖率 65%–84%）
- [x] `scripts/build.ps1` 产出 `bin\dsh.exe`，版本信息注入成功
- [x] CLI `version` / `tools` / `config` / `chat` / `web` 可用
- [x] 真实 DeepSeek API：普通对话、工具调用（write/read 真实落盘）、Web `/api/chat`、多轮上下文全部通过
- [x] 数据目录与安装版 DSH 隔离（`~/.deekseekharness-go` vs `~/.dsh`）
- [x] 密钥不落盘、不入库

## 实施步骤

1. 读取上游 `README.zh.md`、`docs/architecture.zh.md`、`docs/cordis-primer.zh.md` 等，确定包边界。
2. 落盘骨架：kernel（插件生命周期/服务容器/事件总线）→ llm（DeepSeek 客户端）→ tool（Registry/Pipeline）→ builtin（fs/shell/time）→ session（JSONL）→ agent（loop）→ server（HTTP+内嵌 UI）→ config/version → `cmd/dsh`。
3. 补齐 8 个测试文件；`go fmt` / `vet` / `staticcheck` 全绿。
4. 用本地 mock LLM 做首次端到端冒烟（工具调用、会话落盘、Web API）。
5. 用户提供真实 API key 后做真实 E2E 验证，并据真实表现迭代修复。
6. 初始化 Git 仓库并分批提交。

## 自动决策（Agent 自主决定）

| 决策 | 理由 | 影响 |
|---|---|---|
| 模块路径 `deekseekharness-go` | 与目录名一致、本地可用 | 后续可改 |
| 仅使用标准库 | 零依赖、离线可构建、便于审查 | P1 引入 zstd/sqlite 等需单独决策 |
| 内置工具名对齐上游（`read`/`write`/`pwsh`/`bash`） | 降低迁移与提示词迁移成本 | 无 |
| 会话 JSONL 采用简化 `meta`/`message` 两行型 | P0 先跑通闭环；v4 事件流留 P1 | 与上游格式不兼容，P1 重写 |
| 工具失败以 `isError` 结果回填而非中断循环 | 与上游语义一致，模型可自纠错 | 无 |
| 数据目录改为 `~/.deekseekharness-go/` | 真实测试发现会污染安装版 DSH 的 `~/.dsh/sessions` | 已清理误写入文件 |
| Windows `pwsh` 工具执行真正的 PowerShell 方言 | 真实测试中模型按 PS 语法调用失败 | 优先 pwsh 7，回退 powershell 5.1 |
| 系统提示词注入 model/workspace/tools | 修复模型无法回答自身运行信息、乱探索的问题 | 减少无效工具调用 |
| Git 初始化 + 4 个小提交 | 可回滚、可追溯 | 无 |

## 验证记录

```text
$ go test -count=1 ./...        → 8 包全部 ok
$ go vet ./...                  → exit 0
$ staticcheck ./...             → exit 0
$ .\scripts\build.ps1           → built bin\dsh.exe
真实 API（用户 key，不落盘）：
  - dsh chat --prompt "..."      → 正常回复
  - 工具调用 write + read        → 工作区真实生成 notes.txt，内容正确
  - Web /api/chat + 多轮上下文    → 正确
  - pwsh Write-Output 测试        → 206ms 返回 REAL-SHELL-OK
  - 模型身份询问                  → deepseek-chat（来自注入上下文）
  - 默认数据目录                  → ~/.deekseekharness-go/sessions 正常，~/.dsh 未写入
```

## 文件清单

| 操作 | 文件 | 说明 |
|---|---|---|
| 新增 | `cmd/dsh/main.go` 等 20 个 Go 文件 | 骨架与测试 |
| 新增 | `scripts/build.ps1`、`Makefile`、`README.md`、`LICENSE`、`.gitignore` | 工程与文档 |
| 新增 | `docs/ARCHITECTURE.md`、`docs/MIGRATION_MAP.md` | 架构与迁移映射（382 包） |

## 遗留问题与下一步

- P1：turn/step 事件、DSH v4 JSONL、流式 LLM、SSE、工具补齐（见 `docs/STATUS.md`）。
- 沙箱与审批（P2）。
- 迁移映射文档中 P1 的验收项需要逐条落地。

## 范围外发现（等待用户决策）

- 是否引入 `.gitattributes` 规范行尾（当前 Git 有 CRLF 警告）。
- 是否与上游 TS 实现做 session 互操作验证。
