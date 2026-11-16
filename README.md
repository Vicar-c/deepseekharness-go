# deepseekharness-go

DeepSeek Harness（DSH）的 **Go 重写实现**（进行中）。

- 上游项目（TypeScript + Cordis）：<https://github.com/deepseek-ai/deepseek-harness>（MIT）
- 本机参考源码：`../deepseekharness`（完整 clone，保持未改动）
- 目标：在保留 DSH 核心概念（一切皆插件、工具流水线、会话持久化、agent loop）的前提下，用 Go 重建一个可运行、可扩展、跨平台的实现
- 当前状态：**可运行骨架**已打通全链路（CLI / Web / agent loop / 工具执行 / JSONL 会话），单元测试全绿

> 仓库名、Go 模块路径与运行时数据目录统一为 `deepseekharness-go`；旧目录 `~/.deekseekharness-go/` 会在首次启动时自动迁移一次（见 [D-0005](docs/decisions/D-0005-data-directory-rename.md)）。

## 快速开始

要求：Go 1.27+（开发时使用 go1.27.4），无需其它依赖（标准库实现）。

从远程安装：

```powershell
go install github.com/Vicar-c/deepseekharness-go/cmd/dsh@latest
```

从源码运行：

```powershell
# 查看命令
go run ./cmd/dsh version
go run ./cmd/dsh tools
go run ./cmd/dsh config

# 启动 Web UI（默认 http://127.0.0.1:3080/）
$env:DEEPSEEK_API_KEY = "sk-..."
go run ./cmd/dsh web

# 终端对话
go run ./cmd/dsh chat

# 单轮对话（脚本化）
go run ./cmd/dsh chat --prompt "解释一下这个仓库的目录结构"
```

运行测试与构建：

```powershell
go test ./...
go vet ./...
.\scripts\build.ps1            # 产出 bin/dsh.exe（注入版本与 commit）
```

## 已实现（骨架）

| 模块 | 内容 |
|---|---|
| `internal/kernel` | 插件内核：`Use`/`Boot`/`Shutdown` 生命周期、`Plugin`/`Starter`/`Stopper` 接口、按类型注入的 `Services`、同步 `Events` 事件总线（panic 隔离）、事件 `kernel/booted` |
| `internal/llm` | `Client` 契约（带 tool-call 的消息模型）+ OpenAI 兼容的 DeepSeek 客户端（`/chat/completions`、工具定义、错误透传、缺少 API key 的保护） |
| `internal/tool` | `Tool` 契约、有序 `Registry`、执行流水线：guard 审批钩子、超时、panic 恢复、输出截断、`tool/started` / `tool/executed` 事件 |
| `internal/builtin` | 内置插件：`read` / `write` / `list_dir`（工作区沙箱路径校验）、`bash`（Windows 为 `pwsh`）、`now` |
| `internal/agent` | Agent loop：模型调用 → 工具执行 → 结果回填 → 循环直到最终回答；事件 `agent/started` / `agent/step` / `agent/finished`；迭代上限保护 |
| `internal/session` | 会话模型 + 追加式 JSONL 持久化（meta 行 + message 行），`Load`/`List`，id 白名单防路径穿越 |
| `internal/server` | 最小 Web 服务：内嵌 UI、`/healthz`、`/api/sessions*`、`/api/chat`、panic 恢复 |
| `internal/config` | `~/.deepseekharness-go/config.json` + `DSH_*` 环境变量覆盖、`dsh config init` 生成默认配置 |
| `cmd/dsh` | CLI：`version` / `web` / `chat` / `tools` / `config`，优雅退出 |

## 配置

默认配置路径：`$DSH_CONFIG` 或 `~/.deepseekharness-go/config.json`（文件不存在时使用默认值）。

> Go 实现的数据目录（`~/.deepseekharness-go/`）与已安装的 DSH 桌面应用（`~/.dsh/`）隔离，避免两套会话/配置格式互相干扰。旧目录 `~/.deekseekharness-go/` 在首次启动时自动迁移。

```json
{
  "model": "deepseek-chat",
  "baseUrl": "https://api.deepseek.com",
  "apiKeyEnv": "DEEPSEEK_API_KEY",
  "workspace": "C:\\path\\to\\workspace",
  "sessionDir": "C:\\Users\\<you>\\.deepseekharness-go\\sessions",
  "maxIterations": 12,
  "requestTimeoutSec": 120,
  "toolTimeoutSec": 60,
  "maxToolOutputBytes": 131072,
  "logLevel": "info"
}
```

常用环境变量：`DEEPSEEK_API_KEY`、`DSH_MODEL`、`DSH_BASE_URL`、`DSH_WORKSPACE`、`DSH_SESSION_DIR`、`DSH_LOG_LEVEL`、`DSH_MAX_ITERATIONS`。
`DEEPSEEK_API_KEY` 未设置时 harness 仍可启动，但 `/api/chat` 与 `chat` 会返回明确错误。

## 安全边界（当前阶段）

- `read` / `write` / `list_dir`：路径解析限制在 `--workspace` 内，拒绝 `..` 逃逸。
- `pwsh` / `bash`：**目前没有沙箱**，命令以当前用户权限访问宿主文件系统（行为等价于上游的 `danger-full-access`）。上游的 `read-only` / `workspace-write` 三模式策略属于 P2 里程碑。
- 会话与配置不写入 Git；API key 通过环境变量提供，不落盘到仓库。
- 不建议在持有生产凭据的机器上让模型执行不可信提示词；如需最小权限，可给 `tool.Pipeline` 添加 guard（`AddGuard`）拒绝高风险命令。

## 目录结构

```
AGENTS.md                协作契约（最高优先级，Agent/协作者必读）
cmd/dsh/                 CLI 入口（version/web/chat/tools/config）
internal/kernel/         插件内核：生命周期 + 服务容器 + 事件总线
internal/llm/            LLM 契约与 DeepSeek 客户端
internal/tool/           工具契约、注册表、执行流水线
internal/builtin/        内置工具插件（fs / shell / time）
internal/agent/          Agent loop
internal/session/        会话模型与 JSONL 持久化
internal/server/         HTTP API + 内嵌 Web UI
internal/config/         配置加载与保存
internal/version/        版本/commit 注入
docs/STATUS.md           项目状态快照（跨会话第一入口）
docs/AGENT_WORKFLOW.md   任务/决策/多 Agent 工作流与模板
docs/tasks/              任务实现记录（T-XXXX，一个任务一个文件）
docs/decisions/          用户决策记录（D-XXXX，按任务维度，只追加）
docs/ARCHITECTURE.md     Go 架构设计说明（中文）
docs/MIGRATION_MAP.md    上游 TS 模块 → Go 包映射与迁移阶段（中文）
scripts/build.ps1        Windows 构建脚本
```

## 协作契约与记录体系

本仓库是长期跨会话项目，AI Agent 与人类协作必须遵守 [AGENTS.md](AGENTS.md)。核心约定：

| 文件/目录 | 作用 |
|---|---|
| [AGENTS.md](AGENTS.md) | 最高协作契约：决策/实现落盘、用户决策留痕、多 Agent 编排、重构与范围纪律、会话内决策表格与实施门禁 |
| [docs/STATUS.md](docs/STATUS.md) | 项目当前状态快照（新会话先读这里） |
| [docs/AGENT_WORKFLOW.md](docs/AGENT_WORKFLOW.md) | 任务生命周期、记录模板、多 Agent 拆分与验收 |
| [docs/tasks/](docs/tasks/) | 任务实现记录（目标/范围/验证/遗留，一个任务一个文件） |
| [docs/decisions/](docs/decisions/) | 用户决策记录（按任务维度，只追加不覆盖） |

## 设计文档

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)：分层、插件生命周期、一次对话的完整时序、工具流水线、JSONL 格式、HTTP API、扩展方式、与上游 Cordis 的差距
- [docs/MIGRATION_MAP.md](docs/MIGRATION_MAP.md)：上游 53 个模块组 → Go 包映射、迁移优先级（P0–P4）、数据/协议兼容性与风险
- [docs/PLAN_P1.md](docs/PLAN_P1.md)：P1 开工前的决策辅助材料（三条主线改动面与风险、依赖与行尾策略、上游互操作方案）

## 与上游的差距（路线图摘要）

已完成 P0–P1 的核心闭环（内核、LLM、工具、会话、agent loop、CLI、最小 Web）。
尚未迁移：完整工具生态（edit/apply_patch/glob/grep/todo/skill/schedule…）、MCP/LSP、沙箱策略、多 Agent/团队/工作流、Desktop 与前端 UI、Cordis 动态插件与 scope 机制。详见迁移映射文档。

## 许可证

MIT。上游 DeepSeek Harness 同为 MIT；本仓库是独立重写实现，参考上游公开源码与文档。
