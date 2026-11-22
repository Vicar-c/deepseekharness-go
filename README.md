# deepseekharness-go

DeepSeek Harness（DSH）的 **Go 重写实现**：保留上游的产品语义（一切皆插件、工具流水线、事件溯源会话、agent loop），用 Go 标准库重新实现。

- 上游项目（TypeScript + Cordis）：<https://github.com/deepseek-ai/deepseek-harness>（MIT）
- 目标：一个可运行、可扩展、跨平台的 DSH 实现，**零第三方 Go 依赖**
- 当前状态：P1 主体完成（批次 0–4）。真实 DeepSeek API、真实 `dsh web` 进程、真实 headless Chrome 均已验证；P1 剩余缺口见 [T-0016 差距审计](docs/tasks/T-0016-gap-audit.md)

> 仓库名、Go 模块路径与运行时数据目录统一为 `deepseekharness-go`；旧目录 `~/.deekseekharness-go/` 会在首次启动时自动迁移一次（见 [D-0005](docs/decisions/D-0005-data-directory-rename.md)）。

## 特性

| 能力 | 说明 |
|---|---|
| 会话 | DSH **v4 事件流**（header + 事件、`seq` 连续、`surfaceOp` 投影、崩溃尾部修复）；被杀进程留下的未闭合轮次会在续接时自动补齐；能读上游真实会话样本 |
| 模型 | OpenAI 兼容的 DeepSeek 客户端，非流式 + **SSE 流式**，工具调用分片组装 |
| 工具 | 11 个内置工具；执行流水线（guard、超时、panic 恢复、输出截断、事件） |
| 工具批次 | 并发安全的调用可重叠（默认上限 10），独占调用形成 barrier，结果始终按模型序回填 |
| 组合 | **profiles**：`bundle → profile patch → home patch → --patch` 层叠，`--dump-config` 打印每行与每个设置的来源 |
| CLI | `version` / `web` / `chat` / `tools` / `config`；`--prompt` 单轮、`--session` 续接 |
| Web | 内嵌 UI（`fetch` + `ReadableStream` 逐字渲染）、`/api/chat` 支持 JSON 与 SSE（Accept 协商） |
| 测试 | 142 个 Go 用例（`internal/*` 覆盖率 65%–84%）+ 真实浏览器 E2E（CDP 驱动 Chrome） |

## 快速开始

要求：Go 1.27+（开发时 go1.27.4），无需其它依赖。

```powershell
# 从远程安装
go install github.com/Vicar-c/deepseekharness-go/cmd/dsh@latest

# 从源码运行
go run ./cmd/dsh version
go run ./cmd/dsh tools

# 配置密钥后对话（密钥只从环境变量读取）
$env:DEEPSEEK_API_KEY = "sk-..."
go run ./cmd/dsh chat --prompt "解释一下这个仓库的目录结构"

# 交互式终端对话
go run ./cmd/dsh chat

# 启动 Web UI（默认 http://127.0.0.1:3080/）
go run ./cmd/dsh web
```

## 命令行

<!-- cli:start -->
| 命令 | 作用 |
|---|---|
| `dsh version` | 打印版本、commit、构建时间与 Go 版本 |
| `dsh web [flags]` | 启动 Web UI（`--host`、`--port`） |
| `dsh chat [flags]` | 终端对话；`--prompt` 单轮执行，`--session` 续接已有会话 |
| `dsh tools` | 列出当前 profile 注册的工具 |
| `dsh config [init]` | 打印生效配置与密钥状态，或写入默认配置 |
| `dsh help` | 打印用法 |
<!-- cli:end -->

公共 flag：

- `--config <path>`：配置文件（默认 `$DSH_CONFIG` 或 `~/.deepseekharness-go/config.json`）
- `--profile <name>`：启动的 profile（默认 `default`；内建 `default` / `web` / `headless`）
- `--patch <path>`：追加 patch 层，可重复
- `--dump-config`：打印解析后的 profile 与配置（含来源）后退出
- `--model`、`--base-url`、`--workspace`、`--log-level`

退出码：成功 `0`、用法错误 `2`、运行时失败 `1`、`Ctrl+C`（SIGINT）`130`、SIGTERM `0`。收到终止信号后会先取消当前轮次，给进程 5 秒收尾，第二次信号立即退出。

## 内置工具

<!-- tools:start -->
| 工具 | 作用 |
|---|---|
| `read` | 读取工作区内的 UTF-8 文本文件 |
| `write` | 创建或覆盖工作区内的 UTF-8 文本文件 |
| `list_dir` | 列出工作区内目录的条目 |
| `edit` | 精确字符串替换（默认要求唯一匹配，避免改错位置） |
| `glob` | 按 glob 模式查找文件（`**` 可跨目录，结果有序且有上限） |
| `grep` | 用 Go 正则搜索文件内容，返回 `path:line:text` |
| `read_image` | 读取图片：尺寸、媒体类型，足够小时附 base64 data URI |
| `pwsh` / `bash` | 在工作区运行命令，返回合并输出与退出码（Windows 用 `pwsh`，其它平台用 `bash`） |
| `working_directory` | 查看或切换内置工具共享的工作目录（不能离开工作区根） |
| `now` | 当前 UTC 与本地时间 |
| `todo_write` | 维护会话 todo 列表（空列表即清空） |
<!-- tools:end -->

工具的可见性由 profile 决定：`--patch` 可以禁用某一行（例如禁用 `tools.shell`），模型就再也看不到对应工具。

## 配置

默认配置路径：`$DSH_CONFIG` 或 `~/.deepseekharness-go/config.json`（文件不存在时使用默认值）。

> Go 实现的数据目录（`~/.deepseekharness-go/`）与已安装的 DSH 桌面应用（`~/.dsh/`）**严格隔离**：不读上游的 `DSH_HOME`，避免两套会话/配置互相干扰。旧目录 `~/.deekseekharness-go/` 在首次启动时自动迁移。

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
  "maxParallelToolCalls": 10,
  "logLevel": "info"
}
```

层级（低 → 高）：内置默认 → 配置文件 → 环境变量 → profile 层（bundle / profile patch / home patch / `--patch`）→ 命令行 flag。
`--dump-config` 会为每个设置打印来源，例如 `"model": "patch:C:\\tmp\\x.json"`。

常用环境变量：`DEEPSEEK_API_KEY`、`DSH_MODEL`、`DSH_BASE_URL`、`DSH_WORKSPACE`、`DSH_SESSION_DIR`、`DSH_LOG_LEVEL`、`DSH_MAX_ITERATIONS`、`DSH_MAX_PARALLEL_TOOL_CALLS`。
`DEEPSEEK_API_KEY` 未设置时 harness 仍可启动，但 `/api/chat` 与 `chat` 会返回明确错误。

## Profiles

启动组合由 profile 决定，层序与上游 loader 一致：

```
bundle patches → profile patch → home patch(cordis.patch.json) → --patch
```

- 内建 bundle：`base`（内置工具）、`web-app`（HTTP 传输）、`headless`（CLI 传输）
- 内建 profile：`default`（全部传输）、`web`、`headless`
- 用户 profile：`<配置目录>/profiles/<name>/package.json` 声明 `dsh.profile.bundles`，同目录可放 `cordis.patch.json`

示例 patch（按 `id` 定位行，`config` 整体替换）：

```json
[
  {"id": "tools.shell", "disabled": true},
  {"id": "runtime", "config": {"toolTimeoutSec": 7, "maxParallelToolCalls": 4}}
]
```

`runtime` 行按**字段**合并，所以后一层只覆盖它提到的设置；`--dump-config` 会显示每个字段来自哪一层。

## 会话格式

会话落在 `<sessionDir>/<id>.jsonl`，首行是 header，其后每个事件一行（`seq` 从 0 连续递增，只有 surface 事件带 `surfaceOp`）。读取端对未知事件类型要求 `ignorable:true`，否则拒绝。格式、投影与写入器细节见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) §7。

## 测试与质量门禁

```powershell
gofmt -l .                                  # 必须无输出
go vet ./...
& "$env:USERPROFILE\go\bin\staticcheck.exe" ./...
go test ./... -count=1                      # 142 个用例（含 README 命令/工具/配置契约测试）
.\scripts\build.ps1                         # bin\dsh.exe（注入版本与 commit）

# 浏览器端 E2E（真实 Chrome，CDP，零 npm 依赖）
node scripts\webui-e2e.mjs --dsh .\bin\dsh.exe --chrome "C:\Program Files\Google\Chrome\Application\chrome.exe"
```

CI（GitHub Actions）在 ubuntu 与 windows 上跑 gofmt / vet / test / `test -race`（Linux）/ build，另有 `webui` 作业用真实 Chrome 断言增量渲染；Markdown 相对链接也会在 CI 校验。

## 安全边界（当前阶段）

- `read` / `write` / `list_dir` / `edit` / `glob` / `grep` / `read_image` / `working_directory`：路径解析限制在 `--workspace` 内，拒绝逃逸出工作区。
- `pwsh` / `bash`：**目前没有沙箱**，命令以当前用户权限访问宿主文件系统（等价上游 `danger-full-access`）。上游的 `read-only` / `workspace-write` 三模式策略属 P2 里程碑。
- Web 服务默认只监听 `127.0.0.1`，且**目前没有 token 鉴权**：本机其它进程可以直接驱动 harness。
- 会话与配置不写入 Git；API key 只通过环境变量提供。
- 不建议在持有生产凭据的机器上让模型执行不可信提示词；最小权限可给 `tool.Pipeline` 加 guard（`AddGuard`）。

## 目录结构

```
AGENTS.md                协作契约（最高优先级，Agent/协作者必读）
cmd/dsh/                 CLI 入口（version/web/chat/tools/config）
internal/kernel/         插件内核：生命周期 + 服务容器 + 事件总线
internal/config/         配置加载、来源追踪与数据目录迁移
internal/profiles/      bundle/profile/patch 层叠与 --dump-config
internal/llm/            LLM 契约（含可选流式）与 DeepSeek 客户端
internal/tool/           工具契约、注册表、执行流水线与批次调度
internal/builtin/        内置工具插件（fs / shell / time / todo）
internal/agent/          Agent loop
internal/session/        v4 会话格式、投影、写入器与会话存储
internal/server/         HTTP API + 内嵌 Web UI（JSON/SSE）
internal/version/        版本/commit 注入
docs/STATUS.md           项目状态快照（跨会话第一入口）
docs/tasks/              任务实现记录（T-XXXX，一个任务一个文件）
docs/decisions/          用户决策记录（D-XXXX，按任务维度，只追加）
docs/ARCHITECTURE.md     Go 架构设计说明（中文）
docs/MIGRATION_MAP.md    上游 TS 模块 → Go 包映射与迁移阶段（中文）
scripts/build.ps1        Windows 构建脚本（注入版本/commit）
scripts/webui-e2e.mjs    浏览器端 E2E（真实 Chrome over CDP）
scripts/check_md_links.py Markdown 相对链接校验
```

## 协作契约与记录体系

本仓库是长期跨会话项目，AI Agent 与人类协作必须遵守 [AGENTS.md](AGENTS.md)。核心约定：

| 文件/目录 | 作用 |
|---|---|
| [AGENTS.md](AGENTS.md) | 最高协作契约：决策/实现落盘、文档随实现同步、多 Agent 编排、决策门禁 |
| [docs/STATUS.md](docs/STATUS.md) | 项目当前状态快照（新会话先读这里） |
| [docs/AGENT_WORKFLOW.md](docs/AGENT_WORKFLOW.md) | 任务生命周期、记录模板、多 Agent 拆分与验收 |
| [docs/tasks/](docs/tasks/) | 任务实现记录（目标/范围/验证/遗留，一个任务一个文件） |
| [docs/decisions/](docs/decisions/) | 用户决策记录（按任务维度，只追加不覆盖） |

## 设计文档

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)：分层、插件生命周期、一次对话的时序、工具流水线与批次执行、会话 v4 格式、HTTP API、与上游的差距
- [docs/MIGRATION_MAP.md](docs/MIGRATION_MAP.md)：上游 53 个模块组 → Go 包映射、迁移优先级（P0–P4）、兼容性与风险
- [docs/BACKLOG.md](docs/BACKLOG.md)：待办与优先级（P0–P3、规模、依赖、验收要点、批次拆分）
- [docs/tasks/T-0016-gap-audit.md](docs/tasks/T-0016-gap-audit.md)：P1 验收对照与当前缺口清单

## 与上游的差距（路线图摘要）

已完成：P0 骨架 + P1 单 agent 闭环（v4 会话、流式、工具批次、profiles、CLI、最小 Web、浏览器 E2E）。
**P1 剩余**：`request/header` 与系统提示落盘、fsync/flush 屏障、跨进程单写者锁、LLM 错误分类（详见 [docs/BACKLOG.md](docs/BACKLOG.md) 的 B-07…B-10、B-27）。
未迁移：沙箱三模式与 `fs`/`shell` seam（P2）、MCP/LSP/Skill/Subagent/Team（P3）、SDK/ACP/Workflow/Office/Desktop 与上游前端托管（P4）。排期与验收要点见 [docs/BACKLOG.md](docs/BACKLOG.md)。

## 许可证

MIT。上游 DeepSeek Harness 同为 MIT；本仓库是独立重写实现，参考上游公开源码与文档。
