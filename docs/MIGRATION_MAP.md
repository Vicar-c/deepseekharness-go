# DSH → Go 迁移映射

> 参考仓库（只读）：`%USERPROFILE%\Desktop\deepseekharness`，`master`，`git describe dsh-v0.2.2-rc.3-608-g39163dd30f`，HEAD `39163dd30f703a79079e2d2446545cff3c453738`（2026-11-13），`git rev-list --count HEAD` = 27,219。
> 目标仓库：`%USERPROFILE%\Desktop\deepseekharness-go`（Go 1.27.x，module `github.com/Vicar-c/deepseekharness-go`）。
> 本文所有结论均来自实际阅读的上述文件；引用一律使用参考仓库内的相对路径。
> 写作时落盘核对（交付前复核）：P0 骨架已全部落盘——`cmd/dsh` 与 `internal/{version,config,kernel,llm,tool,builtin,session,agent,server}` 均有实现与测试。当前实现是**迁移起点而非目标态**：会话是两行型简化 JSONL（`meta`/`message`）、LLM 仅非流式、事件总线仅同步 emit、Web 仅 `/healthz`+`/api/sessions`+`/api/chat`。本文其余章节按 DSH 的目标契约描述，并在 §5 标明每阶段必须补齐的差距。

---

## 1. 上游仓库概览

### 1.1 版本与规模

| 项目 | 实测值 | 来源 |
|---|---|---|
| 产品版本 | `0.2.2-rc.3`（根 manifest 版本，MIT） | `package.json` |
| 提交数 / HEAD | 27,219 / `39163dd3`（2026-11-13），tag 链 `dsh-v0.2.2-rc.3` | `git describe`、`git rev-list` |
| 包管理器 / Node | `pnpm@11.28.5` / `node >=24.9.0` | `package.json` |
| pnpm workspace 目录 | **406**（`packages/*/*` 382 + `apps/*` 6 + `vendor/*` 9 + `native/system` 1 + `native/system/packages/*` 5 + `benchmarks` + `website` + `python/sdk-runtime`） | `pnpm-workspace.yaml` 逐目录统计（任务描述的 407 为 1 个口径差异，可能把仓库根或 `benchmarks` 内包计入） |
| `packages/` 模块组 | **53** 个（`acp`…`workspace`，`experimental` 单组 65 包） | `packages/` 目录统计 |
| `apps/` 应用 | **6** 个（`cli`、`web`、`desktop`、`desktop-host`、`desktop-publisher`、`plugin-market-generator`） | `apps/*/package.json` |
| 文档 | `docs/` 747 个 `.md`（371 个 `.zh.md`）；`docs/subsystems/` 69 个中文页；`docs/cookbook/` 13 个中文页 | 目录统计 |
| 生成型目录 | `docs/module-graph.zh.md` 1,957 行；`docs/capability-seams.zh.md` 779 行；`docs/persistence-catalog.zh.md` 9,449 行 / 63 个规范会话事件；`docs/persistence-schema.json` 216 KB；`docs/tool-catalog.zh.md` 收录 37 个工具贡献包；`docs/config-catalog.zh.md` 253 KB | 上述生成文件 |
| 语言/构建 | TypeScript 6.0.3、ESM、tsdown、vitest、oxlint、`tsconfig.host.json`+`tsconfig.client.json` 双面 | `package.json`、`docs/development.zh.md` |
| 许可证 | MIT；第三方清单 `THIRD_PARTY_NOTICES.md`；vendor 为 MIT 源码拷贝并 rescope | `LICENSE`、`THIRD_PARTY_NOTICES.md`、`vendor/README.md` |

### 1.2 关键目录

| 目录 | 职责（AGENTS.md 的 repository layout 原义转述） |
|---|---|
| `vendor/` | 源码 vendor 的 Cordis 框架与基础库（`cordis`、`loader`、`include`、`group`、`timer`、`hmr`、`schemastery`、`cosmokit`、`logger-console`），全部 rescope 到 `@deepseek-ai/*` |
| `packages/` | 53 组 `@deepseek-ai/dsh-*` workspace，形态统一为 `packages/<group>/<pkg>/` |
| `apps/` | `cli`（唯一的 `dsh` bin）、`web`（浏览器前端 vite 产物）、`desktop`/`desktop-host`/`desktop-publisher`（Electron 与发布）、`plugin-market-generator` |
| `native/` | `@deepseek-ai/node-addon-system` 原生源码：Landlock launcher、POSIX `flock` 绑定、5 个平台包 |
| `python/` | `sdk`（`deepseek_harness`，NDJSON JSON-RPC 客户端）与 `sdk-runtime`（把 `dsh --profile sdk` 打成 wheel） |
| `benchmarks/` | 性能门禁；`website/` VitePress 文档站投影；`scripts/` 生成器与校验门禁；`.agents/` Agent Note 决策记录 |
| `docs/` | 中英双语设计/参考文档；`docs/subsystems/` 是生成的服务/事件参考 |
| `snapshots/`、`apps/cli/tests/` | 无密钥录制会话快照与启动失败矩阵（迁移回归的金标准） |

### 1.3 架构要点

1. **一切皆插件，没有特权内核**：模型适配器、工具注册表、会话日志、agent loop 本身都是插件；扩展方式是把插件挂在其他插件旁边，注册是副作用，插件卸载时撤销（`docs/architecture.zh.md`）。
2. **Cordis 五概念**：插件=实现 Service 的对象；上下文=服务容器（`ctx.<key>`）；`inject` 声明依赖并决定启动顺序；类型化事件用于通信；注册是可逆副作用（`ctx.effect()`/`ctx.on()`）。事件分发模式 **emit / waterfall / parallel / serial / bail** 五种，waterfall 监听器必须调用 `next()` 才委托（`docs/cordis-primer.zh.md`）。
3. **Profile 与组合包**：运行中的 `dsh` 是"空根 + 有序 patch 层"叠出的插件树；层序 = `dsh.profile.bundles` 各 bundle patch → profile `cordis.patch.yml` → `$DSH_HOME/cordis.patch.yml` → `--patch`；一条 patch 替换目标整行 `config` 或插入新行（`docs/architecture.zh.md`、`apps/cli/reference/README.md`）。
4. **能力 seam 三角色**：Service Definition（拥有 `ctx.<key>`）/ Service Provider / Consumer；seam 是完整能力而非单一角色，典型范例是 `shell`：`dsh-shell` + `dsh-bash-local`/`dsh-bash-sandbox` + `dsh-tool-bash`（`docs/glossary.zh.md`、`docs/capability-seams.zh.md`）。
5. **三事件域**：会话事件（持久，走 `session/event`）、Agent 事件（`agent/*`，实时协调）、能力事件（`fs/*`、`tools/*`、`telemetry/*`）（`docs/architecture.zh.md#events`）。
6. **模型可见即已记录**：进入模型请求的一切必须能从会话日志重建；`deriveMessages()` 从日志投影模型历史；`request/header` 是自包含的可重建请求信封快照（`packages/core/session/README.md`）。
7. **Typert**：业务方法用 `@Remote` 标注 → 生成 `InvocationDescriptor` → Gateway（Host `ctx.typertGateway`）与 Client（`ctx.remote`）消费同一套描述符、编解码器与 `RemoteError` 词汇；装饰器只留最小 marker，完整反射归构建管线（`packages/typert/protocol/README.md`、`docs/api-gateway.zh.md`）。
8. **轮次/步骤模型**：一个 turn 含 0..N 个 step，step = 一次模型请求 + 其触发的工具；turn 在领取首条输入前打开，在不再欠工作时关闭（`docs/agent-lifecycle.zh.md`、`docs/architecture.zh.md#turn-flow`）。
9. **规模数字**：63 个持久会话事件类型、37 个工具贡献包、`client/` 74 个包（其中 62 个 `ui-*`）、65 个 experimental 包、113 个 `ctx.*` 服务声明与 203 个参与能力图的包（`docs/capability-seams.zh.md` 生成图节点实测）。

---

## 2. Go 目标架构总览

### 2.1 包结构

```text
deepseekharness-go/
├── cmd/dsh/                 CLI 入口：version / web / chat / session 子命令
├── internal/
│   ├── version/             构建身份（-ldflags 注入）
│   ├── config/              JSON 配置 + 环境变量覆盖 + 路径解析
│   ├── kernel/              Cordis 风格内核：Plugin/App/Services/Events
│   ├── llm/                 LLM 抽象 + DeepSeek HTTP 客户端（tool calls）
│   ├── tool/                Tool 接口 / Registry / 执行流水线
│   ├── builtin/             内置工具插件（文件、shell、time 等）
│   ├── session/             会话模型 + JSONL 持久化（+ format/repair 子包）
│   ├── agent/               agent loop：LLM → 工具 → 回填 → 循环
│   ├── server/              最小 Web 服务（/healthz、/api/sessions、/api/chat、内嵌 UI）
│   ├── prompt/       (P1)   system prompt 组装 + tool schema 投影
│   ├── profiles/     (P1)   profile / bundle / patch 层叠 + 启动诊断
│   ├── fs/           (P2)   文件系统 seam + local/sandbox provider
│   ├── subprocess/   (P2)   子进程 seam（进程树、取消、环境净化）
│   ├── shell/        (P2)   bash/pwsh executor + 后台任务
│   ├── sandbox/      (P2)   三模式文件效果策略 + 平台后端
│   ├── spill/        (P2)   工具输出保留/落盘策略
│   ├── web/          (P2)   web_fetch / web_search provider + tool
│   ├── workspace/    (P2)   工作区实体、cwd、路径归一
│   ├── compaction/   (P2)   上下文压缩 seam + basic/prune 实现
│   ├── interaction/  (P2)   审批 seam、user questions、权限预设、命令
│   ├── guard/        (P2)   超时/重复调用守卫
│   ├── mcp/          (P3)   MCP client / manager / resources
│   ├── lsp/          (P3)   LSP seam + stdio provider + lsp 工具
│   ├── skill/        (P3)   skill 注册表 + 文件系统发现 + skill 工具
│   ├── subagent/     (P3)   subagent seam + in-process provider + 工具
│   ├── team/         (P3)   共享任务板 + team 工具
│   ├── jobs/         (P3)   后台 job 运行时 + job 工具
│   ├── goal/         (P3)   同会话目标持久化
│   ├── storage/      (P3)   非会话存储 hub + json/sqlite 后端
│   ├── settings/     (P3)   用户设置 seam
│   ├── credentials/  (P3)   凭据引用/记录 + env/.env provider + 授权流
│   ├── sessionquery/ (P3)   会话检索/SQLite FTS/导出
│   ├── preset/       (P3)   按会话组装 agent（工具/提示词/skill 子集）
│   ├── api/          (P4)   兼容 /api 与 /api/remote.mux 的 handler 层
│   ├── sdk/          (P4)   NDJSON JSON-RPC SDK server/client
│   ├── acp/          (P4)   ACP stdio server
│   ├── workflow/     (P4)   工作流引擎（替代 PTC Node 运行时）
│   ├── ptc/          (P4)   代码执行工具（run_code）沙箱运行时
│   ├── document/     (P4)   Office 渲染/转换/重算
│   ├── telemetry/    (P4)   OTel 上报
│   ├── ui/           (P4)   embed 前端静态产物 + index 注入
│   ├── desktop/      (P4)   桌面宿主（Wails 或保留 Electron 壳）
│   └── testkit/      (持续) 回放/快照/mock LLM 测试支撑
└── docs/MIGRATION_MAP.md    本文
```

### 2.2 切分理由（与 DSH 的对应关系）

| 切分决策 | DSH 对应 | 理由 |
|---|---|---|
| `kernel` 单一包承载插件/服务/事件 | `vendor/cordis` + `vendor/loader` + `vendor/include` | Go 不能动态加载 TS 模块；用编译期注册 + 显式 `Use` 顺序替代 Loader，用接口类型作为服务键替代 `ctx.<key>` 字符串键 |
| `*/seam` 包与工具/消费者分离（`fs`+`builtin`、`shell`+`builtin`） | `packages/fs`、`packages/shell` 的 Definition/Provider/Consumer 三角色 | 保持"换一个 provider 就改变整个产品"的能力；Go 接口天然是 seam 声明 |
| `session` 一个包内含日志+派生+持久化 | `core/session` + `session/session-format*` + `session-persistence*` | Go 侧不需要"相邻迁移包"这种独立发布单元；格式版本用包内注册表 + 独立文件仍可演进 |
| `agent` 独立于 `session`/`tool` | `core/agent`（契约） vs `core/agent-loop`（实现） | 保留可替换驱动器的边界；Go 用 `AgentFactory` 接口保留后续替换能力 |
| `server` 直接出 HTTP | `host/webserver` + `api/*` + `bundle/web-app` | P1 只做最小 API；P4 再补 Remote/Typert 兼容层，避免一开始就实现代码生成管线 |
| 删除 Typert | `packages/typert/*` | Go 无装饰器与类型反射生成；用显式 handler 注册 + `encoding/json` + 手写 JSON Schema 常量，wire 契约靠测试固定 |
| 保留 TS 前端产物，Go 只托管 dist | `apps/web` + `packages/client/*`（74 包） | 74 个 React/slot 插件重写成本极高且与迁移主线无关；`internal/ui` + `//go:embed` 是最小风险路径 |
| 不迁移 `experimental`（65 包）与大部分 `client` | `packages/experimental`、`packages/client` | 上游自身不承诺支持；迁移目标应锁定产品主干 |

### 2.3 现状与目标对照（Go 包 ↔ 上游模块类）

| Go 包 | 现状 | 覆盖的上游模块类型 | 优先级 |
|---|---|---|---|
| `internal/kernel` | 已实现：`App`/`Plugin`/`Starter`/`Stopper`/`Context`/`Services`/`Events` | `vendor/cordis`、`vendor/loader`、`vendor/include` 的概念子集 | P0 |
| `internal/config` | 已实现：JSON 文件 + `DSH_*` 环境变量 + 默认值 | `dsh-settings` 的进程级子集、`dsh-home-paths` 的路径子集 | P0 |
| `internal/llm` | 已实现：`Client`/`Request`/`Message`/`ToolDefinition` + `DeepSeek.Chat` | `llm/llm`、`llm/llm-deepseek` 的非流式子集 | P0 |
| `internal/tool` | 已实现：`Tool`/`Registry`/`Pipeline`（guard、timeout、panic 恢复、截断、事件） | `core/tools` 的执行流水线子集 | P0 |
| `internal/session` | 已实现：会话内存模型 + `Store`（`meta`/`message` 两行型 JSONL、ID 校验、列表投影） | `core/session` + `session/session-persistence-jsonl` + `session/session-format*` 的起点；**P1 需重写为 DSH v4 事件流** | P1 |
| `internal/agent` | 已实现：LLM→工具→回填的基础循环（`agent/started`、`agent/step`、`agent/finished` 事件，`MaxIterations` 上限） | `core/agent` + `core/agent-loop` 的起点；**P1 需补 turn/step 事件与 `request/header`** | P1 |
| `internal/builtin` | 已实现：文件读写/目录、shell、time 等内置工具插件 | `fs/tool-fs`、`shell/tool-bash`、`util/` 的最小工具集；P2 重构到 `fs`/`shell` seam 上 | P0/P2 |
| `internal/server` | 已实现：`/healthz`、`/api/sessions`、`/api/chat`、内嵌 UI 页（`index.html`） | `host/webserver` + `bundle/web-app` 的最小 HTTP 面；P1 补会话读取与 SSE | P1 |
| `internal/version` | 已实现 | `apps/cli` 的 `--version` | P0 |
| 其余（P1–P4） | 未创建 | 见 §3/§5 | P1–P4 |

---

## 3. 模块映射表

### 3.1 `packages/` 53 个模块组

> 优先级：P0 内核必做（已完成骨架）/ P1 单 agent 可用闭环 / P2 工具生态与沙箱 / P3 能力 seam 扩展 / P4 平台层与实验性。包名为实际目录名（前缀 `@deepseek-ai/dsh-` 省略）。

| # | 上游模块组 | 代表包（实际目录） | 建议 Go 包/位置 | 优先级 | 关键职责与依赖说明 |
|---|---|---|---|---|---|
| 1 | `core`（10） | `session`、`agent`、`agent-loop`、`tools`、`scope`、`system-prompt`、`tool-executor-native`、`tool-executor-ptc`、`agent-default-model`、`agent-tool-presentation` | `internal/{session,agent,tool,prompt,kernel}` | **P0** | 产品 API 主干：事件溯源日志、Agent 契约与默认循环、工具注册表+流水线、提示词组装、按 agent 作用域。PTC executor 留到 P4 |
| 2 | `llm`（9） | `llm`、`llm-deepseek`、`llm-deepseek-api-key`、`llm-deepseek-account`、`llm-pi-ai`、`llm-retry`、`deepseek-llm-api-extensions`、`plugin-package-inventory-deepseek`、`token-meter` | `internal/llm`（+ `internal/llm/deepseek`） | **P0**（core+deepseek）/ P2（retry）/ P3（extensions、token-meter）/ P4（pi-ai） | 供应商无关的消息/块/流词汇 + 适配器注册表；DeepSeek wire 扩展见 §6；`llm-retry` 监听 `agent/request-error` |
| 3 | `session`（22） | `session-persistence`、`session-persistence-jsonl`、`session-format`、`session-format-v0-to-v1`…`v3-to-v4`、`session-projection(-cache)`、`session-title(-llm/-first-prompt-llm)`、`session-telemetry(-otel)`、`session-checkpoint-policy`、`session-log-deepseek`、`working-directory`、`tool-working-directory`、`session-stats`、`session-turn-outline`、`message-quotes` | `internal/session`（`format.go`/`persist_jsonl.go`/`repair.go`）、`internal/workspace` | **P1**（jsonl+format+repair）/ P3（projection/title/telemetry） | 持久会话数据面：句柄式 `create/open/stat/list`、单写者、flush 屏障、崩溃尾部修复、v0→v4 迁移链 |
| 4 | `attachment`（2） | `attachment`、`attachment-local` | `internal/attachment` | **P3** | 内容寻址的持久二进制附件 + 本地存储；被 LLM 图像/文件投影与工具结果的图片输出使用 |
| 5 | `sandbox`（4） | `sandbox`、`sandbox-local`、`sandbox-policy`、`sandbox-windows-acl` | `internal/sandbox` | **P2** | 三模式文件效果策略（read-only/workspace-write/danger-full-access）、逐调用策略、bwrap/Landlock/Seatbelt/Windows ACL 受限令牌 |
| 6 | `shell`（10） | `shell`、`bash-local`、`bash-sandbox`、`pwsh-local`、`pwsh-sandbox`、`shell-env`、`tool-bash`、`tool-bash-persistent`、`tool-pwsh`、`tool-pwsh-persistent` | `internal/shell` + `internal/subprocess` | **P2** | 唯一 executor 实现，前台/后台 run、有界输出、沙箱化变体、按平台二选一；`bash`/`pwsh` 工具名由工具包独占 |
| 7 | `fs`（8） | `fs`、`fs-local`、`fs-sandbox`、`fs-patch`、`fs-observation-policy`、`tool-fs`、`tool-fs-search`、`tool-str-replace-editor` | `internal/fs` + `internal/builtin` | **P2** | `ctx.fs` 契约 + 本地/沙箱后端 + 先读后编辑策略 + `read/write/edit/read_image/apply_patch` 与 `glob/grep` 工具 |
| 8 | `subprocess`（3） | `subprocess`、`subprocess-local`、`win32-process` | `internal/subprocess` | **P2** | 子进程树管理、取消、环境净化；shell/sandbox/agent 共享执行世界 |
| 9 | `compaction`（5） | `compaction`、`compaction-basic`、`compaction-image-offload`、`compaction-tool-result-pruner`、`command-compact` | `internal/compaction` | **P2** | 上下文压力 seam：pre-step 剪枝/摘要，`agent/request-error` 的溢出恢复，只会推进 surface replacement generation |
| 10 | `context`（7） | `agent-instructions`、`time-context`、`file-reference(-local)`、`session-reference`、`harness-dev-context`、`tmux-context` | `internal/context` | **P2**（time/instructions）/ P3（references） | 模型可见请求上下文 provider（作为 `user/message` 记录，走 `agent/pre-step`/`inject`） |
| 11 | `interaction`（5） | `user-approval`、`user-questions`、`commands`、`permission-presets`、`tool-ask-user` | `internal/interaction` | **P2**（approval/commands）/ P3（questions/presets） | 人类协作面：审批 waterfall、`ask_user`、斜杠命令分发、权限预设 |
| 12 | `guard`（2） | `timeout-policy`、`repeat-tool-reminder` | `internal/guard`（+ `tool.Pipeline` 钩子） | **P2** | 工具 `timeoutMs` 的协作式截止、重复调用提醒；靠 `tools/execute`/`tools/post-execute` 包装 |
| 13 | `todo`（1） | `tool-todo` | `internal/builtin` | **P2** | 模型侧 `todo_write` 工具 + `todo/write` 持久事件 |
| 14 | `plan`（1） | `plan-mode` | `internal/plan` | **P3** | 计划协作状态、直接进入命令、评审退出，`plan/mode` 事件 |
| 15 | `spill`（3） | `spill`、`spill-local`、`spill-policy` | `internal/spill` | **P2** | 工具结果落盘 seam + 本地实现 + 结果替换策略（对应 `tools/post-execute`） |
| 16 | `web`（5） | `web`、`web-fetch-http`、`web-search-deepseek`、`web-deepseek-api`、`tool-web` | `internal/web` | **P2**（fetch）/ P3（search） | `web_fetch`/`web_search` 工具与 provider 注册表；DeepSeek Search/Fetch 认证独立 |
| 17 | `workspace`（1） | `workspace` | `internal/workspace` | **P2** | 工作区实体、`workspace/session-*` 事件、会话归属与活动 |
| 18 | `mcp`（3） | `mcp-client`、`mcp-manager`、`mcp-resources` | `internal/mcp` | **P3** | 每服务器一个连接插件；工具名含 server 名；共享资源工具；服务器指令进系统提示词 |
| 19 | `lsp`（3） | `lsp`、`lsp-stdio`、`tool-lsp` | `internal/lsp` | **P3** | 按扩展名选 provider 的四类只读导航（definition/references/implementation/hover） |
| 20 | `skill`（5） | `skill`、`skill-filesystem`、`skill-office`、`tool-skill`、`tool-workspace-dependencies` | `internal/skill` | **P3** | 注册表合并多 provider；项目/自定义/用户目录优先级；会话技能目录 + `skill` 工具 + `/name` 直调 |
| 21 | `subagent`（9） | `subagent`、`subagent-spawn-in-process`、`subagent-fork-in-process`、`subagent-acp`、`subagent-codex`、`subagent-claude-code`、`subagent-dsh-sdk`、`tool-subagent`、`tool-subagent-control` | `internal/subagent` | **P3**（in-process）/ P4（外部 provider） | provider 注册表 + 一次性/可继续子 agent + 发现/消息/中断 |
| 22 | `team`（2） | `agent-team`、`tool-agent-team` | `internal/team` | **P3** | 根 Session 保存 Team 开关；共享任务板（`revision` CAS、`blockedBy` DAG、`writeScopes` 提示性前缀） |
| 23 | `jobs`（3） | `jobs`、`jobs-local`、`tool-jobs` | `internal/jobs` | **P3** | 后台任务运行时 + `job_*` 工具（读/停/列） |
| 24 | `goal`（4） | `goal`、`goal-round-driver`、`tool-goal`、`command-goal` | `internal/goal` | **P3** | 同会话单目标、revision 状态机（active/paused/blocked/complete）、Goal Round 上限 |
| 25 | `storage`（4） | `storage`、`storage-domain`、`storage-json`、`storage-sqlite` | `internal/storage` | **P3** | 非会话存储 hub + 域事件（`domain/changed`）+ JSON/SQLite 后端 |
| 26 | `settings`（1） | `settings` | `internal/settings` | **P3** | 用户设置 seam + 文件后端；`settings/document-updated` |
| 27 | `credentials`（4） | `credentials`、`credentials-local`、`authorization`、`deepseek-account` | `internal/credentials` | **P3** | 凭据引用/记录 seam、env 覆盖 `.env` 的 provider、需要人类参与的授权流 |
| 28 | `session-query`（3） | `session-query`、`session-query-sqlite`、`session-log-export` | `internal/sessionquery` | **P3** | 逻辑语料、有界读、lineage、语义过滤、SQLite 全文检索、导出 |
| 29 | `preset`（3） | `agent-preset`、`agent-preset-registry`、`persona` | `internal/preset` | **P3** | 用普通 Cordis 配置声明 agent 能力；会话记录 `agentPreset`；persona 组合 |
| 30 | `api`（9） | `gateway`、`remotes`、`remote-access`、`session-controller`、`settings-controller`、`workspace-controller`、`terminal-controller`、`job-controller`、`workspace-files` | `internal/api` + `internal/server` | **P3**（controller 子集）/ **P4**（Gateway 兼容） | Typert RPC 网关与各域控制器；`/api` FetchHandler + `/api/remote.mux` WS |
| 31 | `typert`（4） | `protocol`、`generator`、`loader`、`registry` | **不迁移**；概念替换为 `internal/api` 的显式 handler + JSON Schema 常量 | **P4**（仅当需 wire 级兼容） | 装饰器 marker + 生成 `InvocationDescriptor`；Go 侧无等价反射管线 |
| 32 | `boot`（6） | `app-boot`、`cmdline`、`config-editor`、`hmr`、`plugin-manager`、`plugin-market` | `internal/profiles`（app-boot/cmdline/config-editor）；pnpm 转发与市场留 P4 | **P1**（层叠与启动诊断）/ P4（HMR、plugin manager） | profile 解析、patch 层叠、应用参数边界、启动失败报告；HMR 依赖 N 个 watcher，Go 侧建议仅重启生效 |
| 33 | `bundle`（6） | `base`、`web-app`、`headless`、`sdk-app`、`sdk-minimal`、`acp-app` | `internal/profiles/bundles.go`（内置 bundle 常量 + JSON patch） | **P1** | 可安装 patch 层；Go 侧把 6 个 bundle 固化为编译期组合，保留 patch 覆盖语义 |
| 34 | `host`（11） | `webserver`、`frontend-static`、`authentication-token`、`authentication-desktop`、`directory-picker(-auto/-native/-browse)`、`open-in-app`、`plugin-inventory`、`product-telemetry-otel` | `internal/server`（webserver/frontend-static/auth）/ `internal/desktop`（picker/open-in-app） | **P1**（HTTP+静态）/ P4（其余） | 命名路由（exact→最长前缀→fallback）、upgrade 路由、index 注入、鉴权策略 required/anonymous/deny、TLS |
| 35 | `acp`（1） | `acp` | `internal/acp` | **P4** | 仅自动化的 ACP stdio server：会话建/列/恢复/关闭、挂 MCP、模型选项、prompt/cancel |
| 36 | `sdk`（3） | `protocol`、`client`、`server` | `internal/sdk` | **P4** | NDJSON JSON-RPC 2.0；方法与通知清单见 §6；Python SDK 是同一 wire 的对侧 |
| 37 | `schedule`（2） | `schedule`、`tool-schedule` | `internal/schedule` | **P4** | Host 拥有的定时跟进：`schedule/change` 事件、`schedule/resolve-agent` waterfall、`tool-schedule` 工具 |
| 38 | `client`（74） | `web`、`modules`、`connection`、`locale`、`shortcuts`、`store`、`resources`、`ui-*`（60+） | `internal/ui`（embed 产物）；不重写 | **P4** | 浏览器半侧全部 UI 插件与 slot 体系；重写性价比极低 |
| 39 | `telemetry`（1） | `otel` | `internal/telemetry` | **P4** | 共享 Cordis OTel 上报通道 |
| 40 | `identity`（1） | `anonymous-user-id` | `internal/identity` | **P3** | Harness home 下稳定匿名 UUID（也是 wire header 值） |
| 41 | `feedback`（2） | `message-feedback`、`command-feedback` | 直接作为会话事件 + `internal/interaction` 命令 | **P4** | 反馈写入会话日志，不触发模型工作 |
| 42 | `deliverables`（3） | `tool-present`、`workspace-changes`、`deliverable-file-references` | `internal/deliverables` | **P4** | 显式文件交付与工作区变更卡片（依赖 fs 意图事件） |
| 43 | `document`（2） | `office-kit`、`tool-office` | `internal/document` | **P4** | Office 渲染/转换/重算 + Office 工具；依赖外部引擎（libreoffice-kit） |
| 44 | `terminal`（2） | `terminal`、`terminal-bash` | `internal/terminal` | **P4** | 常驻 PTY 服务（owner 隔离会话）+ 本地 shell 后端；Windows 需 ConPTY |
| 45 | `ssh`（5） | `ssh`、`fs-ssh`、`subprocess-ssh`、`sandbox-ssh`、`ssh-helper-runtime` | `internal/ssh` | **P4** | POSIX 远程连接 + 成对的 fs/subprocess/sandbox provider |
| 46 | `ptc-runtime`（2） | `ptc-runtime`、`ptc-runtime-node` | `internal/ptc` | **P4** | PTC 执行 seam + 沙箱化 Node provider；Go 需决定替代运行时 |
| 47 | `workflow`（3） | `workflow`、`workflow-ptc`、`tool-workflow` | `internal/workflow` | **P4** | 模型编写编排脚本、扇出 subagent、返回 JSON 值；当前依赖 PTC Node 运行时 |
| 48 | `computer-use`（1） | `computer-use` | 不迁移（P4 评估） | **P4** | 独占命名桌面 provider 注册；依赖 CUA 驱动 |
| 49 | `browser-use`（1） | `browser-use` | 不迁移（P4 评估） | **P4** | 独占命名浏览器 provider 注册；依赖 Playwright/Stagehand 等 |
| 50 | `extensions`（4） | `cordis-host-runner`、`cordis-client-runner`、`tool-cordis`、`ui-cordis` | `internal/extensions`（仅只读自省） | **P4** | 运行期自修改：活插件/服务检查与模型写入挂载；Go 无动态加载等价物 |
| 51 | `test-support`（8） | `llm-mock-server`、`llm-replay`、`session-snapshot`、`tool-testkit`、`agent-loop-testkit`、`loader-smoke`、`client-runtime`、`remote-mock` | `internal/testkit` + `testdata/` | **P1 起持续** | 迁移回归基线：mock LLM、录制回放、会话快照、工具契约套件 |
| 52 | `util`（24） | `home-paths`、`values`、`timeout`、`output-retention`、`atomic-write`、`brand`、`error`、`ripgrep`、`text-search`、`crypto`… | `internal/util`（按需拆 `homepaths`/`jsonvalue`/`retention`） | **P1 起持续** | 零依赖工具：home 解析、无损 JSON 快照、超时/截止、输出保留、原子写 |
| 53 | `experimental`（65） | `timeline`、`hooks-*`、`cua-*`、`forge-*`、`ralph-*`、`webhook*`、`worktree`、`session-search`、`voice-input`… | 不迁移（按业务需要单独立项） | **不做/P4** | 上游明确"不承诺产品支持"；仅在主干能力稳定后按需吸收 |

### 3.2 `apps/` 6 个应用

| 上游应用 | 包名 | 职责 | Go 目标 | 优先级 |
|---|---|---|---|---|
| `apps/cli` | `@deepseek-ai/dsh` | 唯一的 Node 应用启动器：profile 启动、插件管理、config dump；`web/headless/sdk/sdk-minimal/acp` 都是 profile | `cmd/dsh` + `internal/profiles` | **P0/P1** |
| `apps/web` | `@deepseek-ai/dsh-web-frontend` | Web 前端（vite 构建，`dist/` 由 `dsh web` 托管） | `internal/ui`（embed `dist/`） | **P4**（P1 用最小内嵌页） |
| `apps/desktop` | `@deepseek-ai/dsh-desktop` | Electron 壳：私有 Host 进程、`dsh-app://` 载入打包 Web、IPC 注入、托盘/更新 | `internal/desktop`（建议先保留 Electron 壳 + Go host） | **P4** |
| `apps/desktop-host` | `@deepseek-ai/dsh-desktop-host` | 私有 Node-mode Host 进程 | 并入 `internal/server` + `internal/desktop` | **P4** |
| `apps/desktop-publisher` | `@deepseek-ai/dsh-desktop-publisher` | 发布元数据/灰度/官网发布器 | 不迁移（发布脚本另建） | **P4** |
| `apps/plugin-market-generator` | `@deepseek-ai/dsh-plugin-market-generator` | 插件市场索引生成器 | 不迁移/`cmd/dsh` 子命令（如需要） | **P4** |

### 3.3 仓库外围目录

| 上游目录 | 定位（实测） | Go 侧处理 | 优先级 |
|---|---|---|---|
| `native/` | `@deepseek-ai/node-addon-system`：Landlock launcher、POSIX `flock` 绑定、darwin/linux 平台包；供 JSONL 单写者锁与沙箱使用 | **删掉**：Go 直接用 `syscall.Flock`/`LockFileEx`，Landlock 用 `golang.org/x/sys/unix` 或 bwrap 子进程 | **P2** |
| `python/` | `sdk`（NDJSON JSON-RPC 客户端）+ `sdk-runtime`（把 `dsh --profile sdk` 打 wheel） | Go 二进制可直接充当 runtime；Python 客户端走 §6 的 SDK 协议 | P4 |
| `vendor/` | 9 个 Cordis 系源码拷贝（cordis 4.0.0-rc.12、loader 1.0.0-rc.8、schemastery 3.18.0 等），含 V1–V28 本地修改记录 | 概念映射到 `internal/kernel` + `internal/profiles`；schemastery → Go struct tag 校验；HMR 不迁移 | **P0**（概念）/ P1（loader 语义） |
| `benchmarks/` | 性能门禁（`BENCHMARK.md`、`vitest.bench.config.ts`） | `testdata/bench` + `go test -bench`，以会话回放/工具吞吐为指标 | P2 起持续 |
| `website/` | VitePress 文档站投影 | 不迁移；Go 文档另建（或复用 `docs/` Markdown 直出） | P4 |

### 3.4 工具级映射（37 个工具贡献包）

| 上游工具包 | 模型可见工具（文档口径） | Go 落点 | 优先级 |
|---|---|---|---|
| `fs/tool-fs` | `read`、`write`、`edit`、`read_image`、`apply_patch` | `internal/builtin`（先）+ `internal/fs`（P2 重构到 seam 上） | **P0/P2** |
| `fs/tool-fs-search` | `glob`、`grep`（打包 ripgrep） | `internal/builtin` | **P2** |
| `fs/tool-str-replace-editor` | `str_replace_editor`（view/create/str_replace/insert） | `internal/builtin`（可选注册） | P2 |
| `shell/tool-bash`、`tool-bash-persistent` | `bash` | `internal/shell` | **P2** |
| `shell/tool-pwsh`、`tool-pwsh-persistent` | `pwsh` | `internal/shell` | **P2** |
| `web/tool-web` | `web_search`、`web_fetch` | `internal/web` | **P2** |
| `todo/tool-todo` | `todo_write` | `internal/builtin` | **P2** |
| `session/tool-working-directory` | `working_directory` | `internal/workspace` | P2 |
| `interaction/tool-ask-user` | 向用户提问（user questions） | `internal/interaction` | P3 |
| `skill/tool-skill` | `skill`（技能目录/加载） | `internal/skill` | P3 |
| `lsp/tool-lsp` | `lsp` | `internal/lsp` | P3 |
| `mcp/mcp-resources` | 资源 list/read（显式 server 选择） | `internal/mcp` | P3 |
| `subagent/tool-subagent`、`tool-subagent-control` | `subagent`/`send_message`/interrupt/list | `internal/subagent` | P3 |
| `team/tool-agent-team` | Team 任务板工具 | `internal/team` | P3 |
| `jobs/tool-jobs` | `job_*`（输出/停止/列表） | `internal/jobs` | P3 |
| `goal/tool-goal` | 目标读写续行 | `internal/goal` | P3 |
| `session-query` 工具（experimental `tool-session-query`） | 会话检索 | `internal/sessionquery` | P3 |
| `plan/plan-mode` | 计划模式进入/退出 | `internal/plan` | P3 |
| `schedule/tool-schedule` | 定时任务 | `internal/schedule`（P4） | P4 |
| `workflow/tool-workflow`、experimental `tool-ralph` | `workflow`、`ralph` | `internal/workflow` | P4 |
| `core/tool-executor-ptc` | `run_code`（PTC 保留传输） | `internal/ptc` | P4 |
| `deliverables/tool-present` | 交付卡片 | `internal/deliverables` | P4 |
| `document/tool-office` | Office 读写 | `internal/document` | P4 |
| experimental `tool-terminal`/`terminal-panel` | 常驻终端工具 | `internal/terminal` | P4 |
| `boot/plugin-manager`、`boot/plugin-market` | 插件安装/市场 | `internal/profiles`（P4） | P4 |

---

## 4. 核心概念映射

### 4.1 Cordis：插件、服务、事件、scope、生命周期

| 上游概念（`docs/cordis-primer.zh.md`、`vendor/`） | 语义 | Go 落点 | 缺口与建议 |
|---|---|---|---|
| 插件对象 `apply(ctx)` ± `inject` | 插件=实现 Service 的对象，按依赖顺序激活 | `kernel.Plugin{Name(), Apply(*Context)}` + `App.Use` 显式顺序 | 缺 `inject` 依赖声明与拓扑激活；P1 加 `Requires() []string` 或服务就绪等待 |
| 上下文=服务容器 `ctx.<key>` | 稳定 key 查服务，禁止导入实现 | `kernel.Context{Services}` + `Provide[T]/Lookup[T]`（以 Go 接口类型为键） | 已覆盖；需补 `Must` 以外的显式错误路径与按 session 子容器 |
| 服务生命周期与 `ctx.effect()` 可逆注册 | 卸载时按逆序撤销贡献 | `Starter`/`Stopper` + `App.Shutdown` 逆序 stop | 缺"单次注册的 disposer"；P2 给 Registry 加 `Register() (dispose func())` |
| 五种分发模式（emit/waterfall/parallel/serial/bail） | 事件即扩展点，模式是公开契约 | `kernel.Events` 目前只有同步 `Emit`（订阅顺序、panic 隔离） | **P1 必修**：`Waterfall`（中间件、必须 `next()`）、`Serial`（`agent/turn-stopping`）、`Bail`（`webserver/authorize`）；`Parallel` 用 `errgroup` |
| `ctx.on` / 事件域划分 | 会话事件 / agent 事件 / 能力事件 | `Events.On(name, handler)` + 事件名常量 | 需约定命名（`session/event`、`agent/*`、`tools/*`、`fs/*`）与 payload 类型表 |
| scope / `agent.ctx` / isolate realm | 按 agent 的扁平两层注册与遮蔽 | 未建；`internal/preset`（P3）引入 per-session 注册表 | shadowing、restriction 与 lifecycle 绑定需成套实现，否则 preset 不可信 |
| Loader / YAML / `!!js` / patch 层叠 | 条目树、config 校验、热重载 | `internal/profiles`（P1）：内置 bundle → profile patch → home patch → `--patch` | `!!js` 表达式不迁移（改为 patch 中的字面 JSON）；HMR 建议"改配置需重启"并在启动诊断中说明 |
| profile / bundle / 应用参数边界 | 启动器只解析自己的 flag，其余透传 | `cmd/dsh` 子命令 + `internal/profiles` | 保留 `--profile`、`--patch`、`--dump-config` 语义；`ctx.cmdlineArgs` 等价物=解析后只读结构体 |

### 4.2 Typert / Remote

| 上游机制（`packages/typert/protocol/README.md`、`packages/api/gateway/README.md`） | Go 落地 | 优先级 |
|---|---|---|
| `@Remote`/`@RemoteScope` 装饰器 + `remoteMethods()` marker | 删除；Go 侧以方法集合接口 + 显式 `Handler` 注册表表达"哪些能力可远程调用" | P4 |
| 生成 `InvocationDescriptor`（参数/结果/lookup/Context/流/uplink/取消） | 删除；wire 上用固定 JSON 结构 + 显式 `json.Unmarshal` 校验 | P4 |
| `ctx.typertGateway.invoke/stream`、`ctx.remote.$mount/$on/$stream` | `internal/api`：`/api` FetchHandler + `/api/remote.mux` WS 兼容层 | P4 |
| `RemoteError{code:'<domain>/<reason>', details}` | `internal/util` 结构化错误：`Code string` + `Details any` + `Is/As` | P4（P1 起可用于工具错误） |
| 资源访问控制 `ctx.remoteAccess.decide` | `internal/api/access.go`：principal/action/resources 三元组决策 | P4 |
| 流帧：`item`/`end`/`cancel`、`streamInboxBytes=262144`、心跳 2s、deflate≥1024 | `internal/api/mux.go`（WebSocket 帧 + 心跳 + 有界 inbox） | P4 |

### 4.3 Tool 定义与执行管线

| 阶段（`docs/tool-execution-pipeline.zh.md`） | 上游语义 | Go 现状（`internal/tool`） | 后续要求 |
|---|---|---|---|
| 定义 | `ToolSchema` + `output.schema` + `execute(args, exec)` + `projectContent`/`finalizeContent`/`presentCall`/`presentResult`/`timeoutMs`/`isConcurrencySafe` | `Tool{Definition(), Execute(ctx, args)}`、`Definition{Name,Description,Parameters}` | P2 补 `Output` 声明与 `Project/Finalize` 回调；host-only 字段绝不进 `Definitions()` |
| 注册表 | `ctx.tools` + 按 agent scope + `restrict` | `Registry`（顺序 + 并发安全 + 重名拒绝） | P2 加 scope/restrict；P1 加 `Unregister` |
| 调用记录 | `tool/call` 先落日志再执行 | 缺（无会话事件） | **P1 必修**：`session.Append("tool/call")` 后执行，`tool/result` 单条模型可见结果 |
| 前置策略 | `tools/pre-execute` waterfall → 单调 guards → `ctx.approval` 询问 | `Guard` 链（顺序、可否决） | P2 改为 waterfall 链 + approval 一次性授权；guard 拒绝需可区分"策略拒绝/审批拒绝" |
| 执行包装 | `tools/execute` waterfall（超时/重试/指标） | `Pipeline` 内置 `context.WithTimeout` + panic 恢复 | P2 允许注册 wrapper；P1 保留内置超时 |
| 工具体 | 注册的 `execute()` | `Tool.Execute` | 同 |
| 文件意图 | `fs/write-intent`、`fs/edit-intent`、`fs/delete-intent` | 缺 | P2 与 `internal/fs` 同步实现（先读后编辑策略依赖它） |
| 内容投影 | `projectContent` → `tools/post-execute` → 规范化 → `finalizeContent` → `tools/result` | 仅 `clampOutput`（UTF-8 安全截断）+ 事件 | P2：post-execute 策略 + 结构化 error 身份；P2 起保留 `isError` |
| 并发 | `isConcurrencySafe` + `maxParallelToolCalls`（默认 10，1=串行）+ 独占 barrier + 模型序结果 | 缺 | **P1/P2**：批次执行器（并发上限、barrier、按模型顺序回填） |
| PTC | `run_code` 保留传输 + 子调用 `tool/ptc-dispatch` | 不做 | P4 |

### 4.4 Agent loop 与 turn/step

| 顺序 | 事件/hook | 模式 | Go 落点 |
|---|---|---|---|
| 1 | `turn/start` | 持久 | `internal/session` 事件；`internal/agent.turn` |
| 2 | 领取 next-step 输入 + 一条排队消息 | — | inbox（P1 仅支持单条 followup；P2 加队列/steering） |
| 3 | `system-prompt/assemble` | waterfall | `internal/prompt`（P1） |
| 4 | `agent/pre-step` | waterfall（可 reject/改写） | `agent.PreStep` 钩子（P1 预留，P2 接 compaction/context） |
| 5 | `step/start` | 持久 | P1 |
| 6 | `agent/request`（已弃用，调整控制参数） | waterfall | P3 再补；P1 直接用 config |
| 7 | `prepareCall` 绑定适配器 | — | `llm.Client` 扩展 `PrepareCall`（P1） |
| 8 | `system/message`、`user/message`、`request/header`、`request/context` | 持久 | **P1 必修**：请求信封可重建（`request/header`） |
| 9 | 派生并冻结请求 | — | `deriveMessages()` 投影（P1） |
| 10 | `llm/stream` → `agent/assistant-stream` chunk* | waterfall + live | P1 流式（先把 `Chat` 扩成 `Stream`）；P2 时间戳/紧凑流 |
| 11 | `assistant/message` \| `assistant/attempt` | 持久 | P1 成功；P2 失败/重试 attempt |
| 12 | `tool/call`* → pre → execute → post → `tool/result`* | 见 §4.3 | P1（串行）/P2（并发） |
| 13 | `step/end` → 工具欠下一次请求或新输入 → 下一 step | 持久 | P1 |
| 14 | `agent/turn-stopping` | serial | P2 |
| 15 | `turn/end`（含 cancel cause） | 持久 | P1 |

Go 侧关键不变量（迁移验收点）：**模型可见即已记录**；取消保留已交付文本并记录 `turn/end` 原因；失败步骤前为未应答工具调用补齐 `TOOL_NOT_STARTED`/`TOOL_OUTCOME_UNKNOWN` 结果；`assistant/message` 才产生模型历史，`assistant/attempt` 不产生。

### 4.5 Session 与 JSONL 持久化

**信封字段**（`docs/persistence-catalog.zh.md`、`docs/deepseek-llm-api-wire-extensions.zh.md`）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `type` | string | 事件判别符，共 63 个规范类型（`turn/start`…`workspace/changes`） |
| `seq` | number | 连续序号，从 0 开始，永不重写 |
| `time` | number | Unix epoch 毫秒 |
| `data` | object | 事件负载（按 type 定 schema） |
| `surfaceOp` | `{op:'append'}` \| `{op:'replace',startSeq,endSeq}` | 仅 surface 事件（`system/message`、`developer/message`、`user/message`、`assistant/message`、`tool/result`）必带 |
| `sourceEventSeqs` | number[] | system/user/tool 事件可带，允许区间压缩表示 |
| `ignorable` | `true` | 未知可忽略事件；保留但不进模型历史 |

**Header**（`docs/subsystems/persistence.zh.md`、`packages/core/session/README.md`）：`version`（当前逻辑格式 **4**）、`id`、`createdAt`、`cwd?`、`parentSession?`、`isSeeded`、`origin?:'subagent'`、`delegationDepth?`、`agentPreset?`；fork 前缀长度 `inheritedEventCount` 属存储元数据，不进事件日志。

**物理布局**（`packages/session/session-persistence-jsonl/README.md`）：

```text
<root>/--<normalized-cwd>--/<encoded-id>/session.jsonl.zstd
                                        /session.v1.jsonl.zstd … session.v4.jsonl[.zstd]
```

规则：默认 zstd 帧（1 帧 header + 每批 1 帧，带 checksum），`compression:'none'` 为裸行；单写者（POSIX `flock(session.lock)`、Windows 内核信号量）+ 无覆盖发布；每批 append 后 `fsync`；committed generation 永不改名/覆盖/删除；读选最高规范 generation；撕裂尾部只丢不完整碎片，写句柄在首次新 append 前重写已恢复记录；崩溃保留未闭合 turn，由 resume 侧补 `interruptedTurnClosers`（缺失工具错误 + `step/end` + `turn/end{kind:'interrupted'}`）。

**Go 落地**：`internal/session` 内分 `types.go`（事件/header/信封 + `Encode/Decode`）、`store.go`（内存追加 + `deriveMessages` 投影 + surface replace 校验）、`persist_jsonl.go`（generation 选择、帧读写、锁、原子发布、尾部修复）、`migrate.go`（v0→v4 相邻迁移；Go 只需支持当前写入 4 与历史读）。P1 先实现 v4 单代 + `compression:none|zstd`，P3 再补历史迁移。

### 4.6 Preset / scope / system prompt

| 上游 | 机制要点 | Go 落点 | 优先级 |
|---|---|---|---|
| `system-prompt` | 有序片段 + 工具 schema + 变量；`system-prompt/assemble` waterfall；`change` 事件 | `internal/prompt`：`Sections []Section` + `Assemble(ctx, toolDefs) (string, error)` | P1（静态拼装）/ P2（hook） |
| `preset` | 普通 Cordis 配置声明 agent 能力；Host 共享 loop；每 agent 只见所选 revision 的工具/prompt/skill | `internal/preset`：`Profile{Name, Tools []string, Prompt, Skills}`；按 session 建 scoped registry + 独立 system prompt | P3 |
| `scope` | 扁平两层（全局/带 scope key）；shadowing；restriction 取交集 | `internal/kernel` 增加 `Scope`（key=session id）+ `Registry` 支持 scoped 覆盖 | P3 |
| `persona` | 可组合 agent 人格（提示词片段） | `internal/preset/persona.go`（模板 + 变量） | P3 |

### 4.7 Sandbox

| 上游 | 要点（`docs/subsystems/sandbox.zh.md`） | Go 落点 | 优先级 |
|---|---|---|---|
| 模式 | `read-only` / `workspace-write` / `danger-full-access`；只有前两者发给 provider；`danger` 直接 spawn | `sandbox.Mode` 常量 + `Confine(mode) bool` | **P2** |
| 执行完整性 | `full`/`partial`（旧 Landlock ABI、Windows ACL 边界） | `sandbox.Enforcement`，要求绝对保证的调用方须拒绝 `partial` | P2 |
| 逐调用策略 | `SandboxExecutionPolicy{mode, workspaceRoot, additionalWritableRoots?, sessionId?}`，由消费方解析 | `sandbox.Policy` 值传递，provider 视为完全给定 | P2 |
| 后端 | Linux：bwrap 优先、Landlock；macOS：Seatbelt（`sandbox-exec`）；Windows：ACL 受限令牌 + 私有 temp + SID | `internal/sandbox/backend_{linux,darwin,windows}.go`；Go 可直接实现 Flock/Landlock，Windows 需 `CreateRestrictedToken`+Job Object | P2 |
| argv 包装与失败分类 | 包装后 argv + `RunnerFailureRule`（非零退出 + stderr 致命签名） | `sandbox.Wrap(argv) (wrapped, classify func)` | P2 |
| 提权 | 一次性 `allowed-once` 审批后以更宽策略重试 | `internal/interaction` + `tools/pre-execute` | P2 |

### 4.8 MCP

| 上游 | 要点（`docs/subsystems/mcp.zh.md`、`packages/mcp/README.md`） | Go 落点 | 优先级 |
|---|---|---|---|
| 连接模型 | 每服务器一个 `mcp-client` 插件；SDK 负责协商/发现/校验/取消；stdio 与 Streamable HTTP | `internal/mcp/client.go`（JSON-RPC over stdio；HTTP 用 SSE/streamable） | P3 |
| 命名与作用域 | 配置 `serverName` 标识；跨服务器同名工具靠暴露名区分；未配置服务器则无 prompt/工具 | 注册到 `tool.Registry` 时加 server 前缀；无 server 则零贡献 | P3 |
| 结果适配 | 保留规范 MCP JSON；图像走附件；`isError` 结果抛 `McpToolError`（`MCP_TOOL_ERROR`/`MCP_CONNECTION_CLOSED`/`MCP_TIMEOUT`…） | `mcp.ToolResult` 结构化错误 + 内容块投影 | P3 |
| 资源 | `mcp-resources` 拥有共享工具，调用必须显式 server；支持分页与模板 | `internal/mcp/resources.go` | P3 |
| 指令 | 非空白 server instructions 作为带归属的 system-prompt 片段（受 `maxInstructionBytes`） | `internal/prompt` 注册片段 | P3 |
| 管理面 | `mcp-manager` 由 Settings 驱动的 fleet（增删改启停/状态/重启/探测） | `internal/mcp/manager.go` + `internal/settings` | P4 |

### 4.9 LSP

| 上游 | 要点（`packages/lsp/README.md`、`docs/subsystems/lsp.zh.md`） | Go 落点 | 优先级 |
|---|---|---|---|
| seam | `ctx.lsp`：按文件扩展名选 provider，四类只读操作（definition/references/implementation/hover）+ 结构化错误 | `lsp.Provider` 接口 + `lsp.Registry` | P3 |
| provider | `lsp-stdio` 驱动配置的 language server 命令，复用 `ctx.fs`/`ctx.subprocess` | `internal/lsp/stdio.go`（JSON-RPC + `initialize`/`initialized`） | P3 |
| 工具 | 仅 `tool-lsp` 拥有模型可见名/schema/提示，换 provider 不改模型请求 | `internal/builtin` 注册 `lsp` 工具 | P3 |

### 4.10 Skill

| 上游 | 要点（`packages/skill/README.md`、`docs/subsystems/skills.zh.md`） | Go 落点 | 优先级 |
|---|---|---|---|
| 注册表 | `ctx.skills` 合并多 provider 目录，按名解析胜出者 | `skill.Registry`（name→entry，来源优先级） | P3 |
| 本地发现 | 项目 / 自定义 / 用户目录优先级 + 变更监听；`~/.agents/skills` 也在扫描范围 | `internal/skill/fs.go`；`internal/util` 的 `~`/home 解析 | P3 |
| 模型面 | `tool-skill`：排序的会话技能目录 + 加载工具 + `/name` 直调；模型访问需至少一个来源 | `internal/skill/catalog.go` + `builtin.skill` | P3 |
| 内容格式 | `SKILL.md`（frontmatter 元数据 + 正文）；Office 技能携带结构化文件检查 | `internal/skill/frontmatter.go`（YAML frontmatter） | P3 |

### 4.11 Subagent / Team

| 上游 | 要点（`packages/subagent/README.md`、`packages/team/README.md`、`docs/subsystems/agent-team.zh.md`） | Go 落点 | 优先级 |
|---|---|---|---|
| 委派 seam | `ctx.subagents` provider 注册表；一次性运行 + 可继续子会话 + 任意深度发现 | `subagent.Service` + `Provider` 接口；子 agent = goroutine + 独立 session | P3 |
| in-process provider | fresh（`spawn`）/ 历史种子（`fork`）；共享进程与文件系统 | `subagent/spawn.go`、`subagent/fork.go`（复用 `agent`/`session`） | P3 |
| 外部 provider | ACP / Codex / Claude Code / dsh-sdk 子进程 | P4，先做 `dsh-sdk` 自举 | P4 |
| 控制工具 | `subagent`、`send_message`、interrupt、list；每个子项运行中/已存储都可见 | `builtin` 中的 subagent 工具族 | P3 |
| Team | 根 Session 的 `enabled` 开关 + 任务板：每 task 事件存整快照、`revision` CAS、`blockedBy` 无环 DAG、`writeScopes` 提示性前缀；任务变更**不**唤醒 agent | `internal/team`：`Task{id,revision,subject,description,status,owner,blockedBy,writeScopes}` + `team/mode`/`team/task` 事件 | P3 |

### 4.12 Workflow / PTC / Ralph

| 上游 | 要点（`packages/workflow/README.md`、`docs/subsystems/workflow.zh.md`） | Go 落点 | 优先级 |
|---|---|---|---|
| seam | `ctx.workflowEngine`，每个上下文只允许一个引擎（无命名注册表） | `workflow.Engine` 接口（单实例） | P4 |
| 启动请求 | `{script, meta, args?, subagentProvider?, maxTotalAgents?, parent, signal?}`；`meta` 用 schema 校验；`parent` 必填 | `workflow.StartRequest` 结构体 + 校验 | P4 |
| 结果 | `WorkflowResult{value, stopReason: completed/cancelled/error, error?, count}` | 同构结构体；非 completed 映射为 `isError` 工具结果 | P4 |
| 执行 | 脚本跑在共享 PTC Node 进程运行时（按调用 Session 的文件策略） | **关键决策点**：Go 侧可选 (a) 内嵌 JS 引擎（goja）复刻沙箱语义 (b) 用 Starlark 重定义脚本语言（破坏兼容）(c) 保留 Node sidecar 仅执行脚本 | P4 |
| Ralph | 由 workflow + subagent 组成的前台新 agent 循环（`ralph` 工具，实验包） | P4 或不做 | P4 |

### 4.13 Store / Storage / Settings / Credentials / Session-query

| 上游 | 要点 | Go 落点 | 优先级 |
|---|---|---|---|
| `storage`、`storage-domain` | 非会话存储 hub + 域事件 `domain/changed` + JSON/SQLite 后端 | `internal/storage`（`Store` 接口 + `jsonStore` + `sqliteStore`） | P3 |
| `settings` | 设置文档 + `settings/document-updated`；UI 与 Host 双向 | `internal/settings`（文档模型 + 变更事件 + 文件落盘） | P3 |
| `credentials` | 引用/记录 seam + env 覆盖 `.env` 的 provider + 人类授权流；DeepSeek 账号/API key 两路 | `internal/credentials`（`Resolver` 接口 + `.credentials.yaml`/env 实现 + 授权流程） | P3 |
| `session-query` | 逻辑语料/有界读/lineage/语义过滤 + SQLite FTS；只读观察不回写 | `internal/sessionquery`（复用 `session` 句柄只读打开 + FTS5） | P3 |
| `attachment` | 内容寻址的持久二进制附件 + 本地存储 | `internal/attachment`（sha256 存储 + 引用） | P3 |

### 4.14 Web API / 前端 / Desktop / ACP / SDK

| 上游 | 要点 | Go 落点 | 优先级 |
|---|---|---|---|
| HTTP 服务器 | `ctx.webServer`：exact→最长前缀→fallback；upgrade 路由；`webserver/request/authorize/upgrade` waterfall；`tls` 只读一次；`index` 注入行 + taps | `internal/server`（`net/http` + 路由表 + fallback；`//go:embed` index 模板） | **P1** |
| `/api` 与 mux | Connection 的 `/api` FetchHandler（trusted-host interceptor）+ `/api/remote.mux` WebSocket（流 id、uplink、cancel、`streamInboxBytes`、心跳 2s、deflate≥1024） | `internal/api`（P4）；P1 仅 `/api/sessions`、`/api/chat`、`/healthz` | P1/P4 |
| 前端 | `apps/web` vite 产物 + 74 个 client 插件 + slot 体系 + locale | `internal/ui` embed 产物；不重写插件 | P4 |
| Desktop | Electron 壳 + 私有 Host 进程（RunAsNode）+ `dsh-app://` + Node IPC（boot 注入/ready/致命错误/关闭）+ 每进程随机凭据 | `internal/desktop`。建议：**保留 Electron 壳**，把 Host 换成 Go 二进制，通过 HTTP/WS 与 IPC 桥接 | P4 |
| ACP | stdio JSON-RPC，仅自动化：会话建/列/恢复/关闭、挂 MCP、模型选项、prompt/cancel、权限请求 | `internal/acp` | P4 |
| SDK | NDJSON JSON-RPC 2.0；请求/通知表见 §6；`serverInfo.name` 固定 `deepseek-harness-sdk-runtime` | `internal/sdk`（server 先于 client） | P4 |

---

## 5. 迁移阶段计划

> 阶段的"验收标准"全部可执行：命令级检查 + 会话/协议回放比对。P0 骨架已全部落盘（`cmd/dsh` + `internal/{kernel,config,llm,tool,builtin,session,agent,server,version}`），下表从"已达成/待补齐"起算；括号内标注当前实现与目标态的差距。

### P0 内核 / LLM / 会话 / 工具骨架（已完成主体）

| 项 | 内容 |
|---|---|
| 目标 | 可编译、可启动、可单次对话的 Go 骨架；确立包边界与不变量 |
| 范围 | `kernel`（App/Plugin/Services/Events）、`config`（JSON+env）、`llm`（Client/Message/ToolDefinition + DeepSeek 非流式）、`tool`（Tool/Registry/Pipeline）、`builtin` 最小工具、`session` 模型、`agent` loop 骨架、`server` 三端点、`cmd/dsh`（version/web/chat） |
| 验收标准 | `go build ./...` 与 `go vet ./...` 通过；`dsh version` 输出含版本/commit/Go 版本；无 API key 时 `dsh chat` 以明确错误退出（`ErrMissingAPIKey`）；`dsh web` 起服务且 `/healthz` 200；工具流水线单测覆盖：重名拒绝、guard 否决、超时、panic 恢复、UTF-8 截断 |
| 风险 | 事件总线语义过弱（只有 emit）导致后续 agent 事件无落点；LLM 只有非流式，P1 需改造接口 |

### P1 单 agent 闭环 + CLI/Web（第一个可用版本）

| 项 | 内容 |
|---|---|
| 目标 | "一个 cwd 内、一个模型、一组内置工具"的完整可用闭环，会话可持久化并 resume |
| 范围 | §4.4 的 turn/step 全流程；`session` 的 v4 JSONL（`compression:none` 或 `zstd`）+ 单写者锁 + 崩溃尾部修复；`request/header` 可重建；`llm.Stream` 流式 + tool calls；工具批次（串行优先）；`system` 提示组装（静态）；`profiles`（默认 profile + patch 覆盖 + `--dump-config`）；Web：会话列表/读取/`/api/chat`（SSE）；`builtin`：`read/write/edit/read_image/glob/grep`、`bash`、`pwsh`、`todo_write`、`working_directory` |
| 验收标准 | ① 录制一段真实会话并与上游 JSONL 逐事件比对（字段/seq/surfaceOp 一致）；② 杀掉进程后 resume，补齐 `interruptedTurnClosers` 且日志不丢已提交前缀；③ `-race` 下 100 并发会话读写无数据竞争；④ 会话文件可与上游 `session-version 4` 读取器互开（只读方向）；⑤ 工具超时/截断/错误以 `isError` 结果回填而非中断循环 |
| 风险 | 流式事件与持久 settle 的边界（`agent/assistant-stream` 是瞬态，`assistant/message` 才持久）；写路径批量/flush 语义若做错会导致崩溃丢事件 |

### P2 工具生态与沙箱（对齐 `dsh-base` 能力面）

| 项 | 内容 |
|---|---|
| 目标 | 具备"默认权限策略 + 沙箱化 shell + 完整文件工具 + 输出治理 + 基础压缩"的产品级工具面 |
| 范围 | `fs` seam（local/sandbox provider + 先读后编辑策略 + `fs/*` 意图事件）、`shell`（bash/pwsh + sandbox 变体 + 后台任务 + `shell-env`）、`subprocess`、`sandbox`（三模式 + bwrap/Landlock/Seatbelt/Windows ACL）、`spill`、`compaction`（basic + 工具结果剪枝）、`context`（time/instructions）、`interaction`（approval + 命令）、`guard`、`web`（fetch/search）、工具批次并发 + 独占 barrier + 模型序回填 |
| 验收标准 | ① `workspace-write` 下写工作区外被拒并报结构化拒绝（不是 panic）；`read-only` 下所有写工具失败；② 沙箱后端在 Linux/macOS/Windows 三平台 CI 各有一条通过用例（或明确标记 `partial`）；③ 工具输出 100 MB 时被截断/落盘且内存峰值有界（基准 < 64 MB）；④ 压缩后 `replace` 不删除历史事件，且 `deriveMessages` 前缀可复用（KV cache 语义有测试）；⑤ 审批拒绝路径返回一次 `allowed-once` 语义 |
| 风险 | 平台沙箱差异（Windows ACL 需受限令牌 + 私有 temp，非管理员不可用）；bwrap 在容器内不可用需回退 Landlock；先读后编辑策略会改变模型行为，必须与上游文档一致 |

### P3 能力 seam 扩展（MCP/LSP/Skill/Subagent/Team/Jobs/Goal/Storage）

| 项 | 内容 |
|---|---|
| 目标 | 覆盖 `dsh-base` 之外的产品能力：外部工具、代码导航、技能、多 agent 协作、目标与定时 |
| 范围 | `mcp`（client + resources，manager 留 P4）、`lsp`、`skill`、`subagent`（spawn/fork in-process）、`team`、`jobs`、`goal`、`plan`、`preset`+`scope`（按 session 组装）、`storage`/`settings`/`credentials`/`identity`、`sessionquery`（只读 + FTS）、`session` 的 projection/title/telemetry、`api` 的 session/settings controller 子集 |
| 验收标准 | ① 一个 MCP stdio server 的 3 个工具可被模型调用并落 `tool/call`+`tool/result`；② `subagent` 创建子会话，父会话能 `send_message` 并在完成后继续；③ Team 任务板 `blockedBy` 无环 + `revision` CAS 冲突拒绝；④ 技能目录出现在系统提示中，`skill` 工具加载全文；⑤ preset 切换后 agent 只见所选工具（restriction 生效） |
| 风险 | scope/isolate 若实现不彻底，preset 会泄漏全局工具；subagent 共享 session store 时的写者归属（每个会话单写者）必须设计清楚 |

### P4 平台层（Web GUI / Remote / Desktop / Workflow / ACP / SDK / Office）

| 项 | 内容 |
|---|---|
| 目标 | 与上游产品形态对齐：浏览器 GUI、Remote RPC、桌面应用、自动化协议 |
| 范围 | `internal/ui`（embed 前端产物 + index 注入）、`internal/api`（`/api` + `/api/remote.mux` 兼容、RemoteError、access control）、`acp`、`sdk`、`workflow`（含 PTC 运行时选型）、`document`（Office）、`terminal`、`ssh`、`telemetry`、`desktop`、`extensions`（只读自省） |
| 验收标准 | ① 上游 `apps/web` 构建产物可被 Go 服务器托管并完成登录/会话/工具卡片交互（截图对比通过）；② 上游 TypeScript SDK 客户端能连 Go `dsh --profile sdk` 完成 `initialize`→`session/prompt`→收 `session.event`；③ ACP 客户端可建会话并 prompt；④ 桌面端可启动 Go host 并加载打包 UI |
| 风险 | Typert 兼容层工作量被低估（281 个 Remote 端点级别）；Workflow 依赖 Node PTC，重写等于重做一个沙箱 JS 运行时；Electron 与 Go host 的生命周期/凭据/IPC 桥接是高风险集成点 |

**阶段依赖**：P0 → P1 → （P2 ∥ P3 可并行，但 P3 的 approval/scope 需要 P2 的 interaction/fs 语义稳定）→ P4。建议每阶段结束打 tag 并固化 §6 的兼容性测试集。

---

## 6. 数据与协议兼容性

> 兼容性分三级：**A=必须逐字节/逐字段兼容**；**B=语义兼容（字段名与结构一致，允许实现差异）**；**C=仅流程兼容**。

| # | 格式/协议 | 级别 | 必须保持的要点 | 规范来源（相对路径） |
|---|---|---|---|---|
| 1 | Session JSONL 物理布局 | **A** | `--<normalized-cwd>--/<encoded-id>/`；`session.vN.jsonl[.zstd]`；zstd 连续独立帧（header 单独一帧、每批一帧、带 checksum）；选最高规范 generation；committed generation 永不改名/覆盖/删除 | `packages/session/session-persistence-jsonl/README.md`、`docs/subsystems/persistence.zh.md` |
| 2 | Session header（逻辑 v4） | **A** | `version`=4、`id`、`createdAt`、`cwd?`、`parentSession?`、`isSeeded`、`origin?`、`delegationDepth?`、`agentPreset?`；`inheritedEventCount` 属存储元数据 | `docs/subsystems/persistence.zh.md`、`docs/session-format-status.zh.md` |
| 3 | 事件信封 + 63 个事件类型 | **A** | `type/seq/time/data`、`surfaceOp`、`sourceEventSeqs`、`ignorable`；未知事件仅 `ignorable:true` 可保留；surface 五类必须带 `surfaceOp` | `docs/persistence-catalog.zh.md`、`docs/persistence-schema.json`、`packages/core/session/src/types.ts` |
| 4 | surface 投影/替换/派生 | **A** | `replace{startSeq,endSeq}` 为当前 surface 序的闭区间；空文本 system 节点投影为空消息；`deriveMessages()` 结果确定 | `packages/core/session/README.md`、`docs/subsystems/session.zh.md` |
| 5 | 崩溃恢复 | **B** | 撕裂尾部只丢不完整碎片；写句柄首次新 append 前重写恢复记录；resume 补 `interruptedTurnClosers`（`TOOL_NOT_STARTED`/`TOOL_OUTCOME_UNKNOWN` + `step/end` + `turn/end{interrupted}`） | `docs/subsystems/persistence.zh.md`、`packages/core/agent-loop/README.md` |
| 6 | 历史格式迁移 | **B** | 只支持已发布相邻链（v0→v1→v2→v3→v4）；只读 open 不发布后继；写 open 排他发布 successor；绝不降级 | `docs/persistence-changes/README.zh.md`、`docs/persistence-changes/historical-formats/README.zh.md`、`docs/session-format-status.zh.md` |
| 7 | 工具 JSON Schema 与 ToolDefinition | **B** | 模型只看到 `name/description/parameters`；`output/execute/timeoutMs/isConcurrencySafe/present*` 绝不进请求；schema DSL 允许 `string/number/integer/boolean/null/array/object/oneOf` + `required:true`；`additionalProperties` 显式 | `docs/tool-catalog.zh.md`、`docs/subsystems/tools.zh.md`、`docs/cookbook/adding-a-tool.zh.md` |
| 8 | 工具结果/错误语义 | **B** | 单条 `tool/result`；`isError` 优先；`data.error` 仅当 `isError===true`；结构化错误码（如 `TOOL_TIMEOUT`、`ABORTED_BEFORE_DISPATCH`、`MCP_*`、`LspError`） | `packages/guard/timeout-policy/README.md`、`docs/subsystems/tools.zh.md` |
| 9 | LLM 消息/块/流协议 | **B** | `Message`/`ContentBlock`/`StreamChunk` 词汇；`usage` 先于 `finish`；tool arguments 保持原始 JSON 字符串；终止 `finish` 后无帧；失败码 `NO_ADAPTER/MISSING_CREDENTIAL/AUTH/RATE_LIMIT/CONTEXT_WINDOW_EXCEEDED` 等 | `packages/llm/llm/README.md`、`docs/subsystems/llm-streaming.zh.md`、`docs/cookbook/adding-an-llm-adapter.zh.md` |
| 10 | DeepSeek wire 扩展 | **A**（若要与官方端点互操作） | 请求头：`user-agent: product/version (+url)` + `dsh-profile/<profile>`、`x-deepseek-harness-user-id`、`x-deepseek-harness-session-id`、`x-deepseek-harness-compact: 1`；正文扩展：`dsh_plugin_packages{version:1,packages[]}`、`dsh_session_log{version:1,sessionFormatVersion,session{…},afterSeq,throughSeq,events[]}`；扩展与基础字段同级且不增加 token | `docs/deepseek-llm-api-wire-extensions.zh.md` |
| 11 | 配置目录与 profile 层叠 | **B** | `$DSH_HOME`（默认 `~/.dsh`）优先于环境变量；`profiles/<name>/{package.json,cordis.patch.yml}`；层序 bundle→profile patch→home patch→`--patch`；patch 替换整行 `config`；`--dump-config` 输出带来源注释 | `packages/util/home-paths/README.md`、`apps/cli/reference/README.md`、`packages/bundle/base/README.md`、`packages/bundle/base/cordis.patch.yml` |
| 12 | 环境变量面 | **B** | `DSH_HOME`、`DSH_CONFIG`、`DSH_PERMISSION_MODE`、`DSH_TELEMETRY_MODE`/`DSH_TELEMETRY_OTLP_URL`、`DEEPSEEK_API_KEY`/`DEEPSEEK_BASE_URL`、`.env` 两层（cwd + `$DSH_HOME/.env`） | `docs/cli-help.zh.md`、`apps/cli/reference/README.md`、`AGENTS.md`（Secrets/.env 节） |
| 13 | Web HTTP/WS 边界 | **B** | 命名路由匹配（exact→最长前缀→fallback）、upgrade 精确匹配、`webserver/authorize` 三态（required/anonymous/deny）、`/api` 单一 FetchHandler、`/api/remote.mux` 帧（`item`/`end`/`cancel`）、`streamInboxBytes=262144`、心跳 2s、deflate≥1024 字符 | `packages/host/webserver/README.md`、`packages/api/gateway/README.md`、`docs/api-gateway.zh.md`、`docs/subsystems/web-server.zh.md` |
| 14 | SDK JSON-RPC | **A**（若保留 Python/TS SDK 生态） | NDJSON，一行一消息；方法集：`initialize`、`session/prompt`、`session/query`、`session/resume`、`session/wait`、`session/working-directory/get`、`session/working-directory/set`、`shutdown`；服务端→客户端：`approval/request`、`user-question/ask`、`user-question.cancelled`、`session.event`、`session.status`、`session.query.completed`、`subagent.started`、`subagent.finished`；`serverInfo.name` = `deepseek-harness-sdk-runtime` | `packages/sdk/protocol/README.md` |
| 15 | ACP | **B** | stdio JSON-RPC；会话建/列/恢复/关闭、挂 MCP server、模型选项、文本/图像 prompt、语义更新、权限请求、cancel | `packages/acp/README.md`、`packages/acp/acp/README.md` |
| 16 | MCP / LSP / Skill 输入格式 | **B** | MCP 走官方协议协商；LSP 走标准 JSON-RPC；Skill 为 `SKILL.md` + frontmatter + 目录优先级 | `docs/subsystems/mcp.zh.md`、`docs/subsystems/lsp.zh.md`、`docs/subsystems/skills.zh.md` |
| 17 | 设置/凭据/存储文件 | **B** | 设置文档与域事件；凭据优先级 env > `$DSH_HOME/.credentials.yaml` > cwd `.env` > `$DSH_HOME/.env`（顺序以参考文档为准）；匿名 id 文件在 home 下 | `packages/credentials/credentials/README.md`、`packages/settings/settings/README.md`、`packages/storage/storage-domain/README.md`、`packages/identity/anonymous-user-id/README.md` |
| 18 | CLI 行为面 | **C** | 退出码：one-shot `completed`=0/其他=1、用法错误非零、`SIGINT`=130、`SIGTERM`=0；首次信号 5 秒优雅退出；`--version`/`-V`；启动失败打印 `Full diagnostics:` 与日志路径 | `apps/cli/reference/README.md`、`apps/cli/README.md` |
| 19 | 快照/回归基线 | **B** | 以 `snapshots/` 与 `apps/cli/tests/` 的录制会话为对照；`test:snapshot` 无密钥可跑 | `AGENTS.md`（Commands）、`docs/testing.zh.md` |

**兼容性落地建议**：在 `internal/session` 与 `internal/tool` 各建一个 `testdata/compat/` 目录，放入上游导出的小样本（1 条会话、1 份工具 catalog、1 个请求头快照），用 golden test 双向校验：Go 读取上游样本（必须成功）、Go 写出的样本能被上游读取器解析（人工一次性验证）。

---

## 7. 风险与建议

| # | 风险 | 具体表现（上游证据） | 影响 | 建议 |
|---|---|---|---|---|
| 1 | **Cordis 动态性无法平移** | Loader 运行期读 `cordis.yml`/`!!js`、HMR 监听配置与模块、`ctx.effect()` 可逆注册、`isolate` realm（`vendor/README.md` V6–V28 记录了大量生命周期补丁） | 插件热插拔、配置热重载、按会话隔离全部需要重设计 | 明确降级：Go 侧插件=编译期注册、配置=启动时加载、重载=重启；用 `internal/profiles` 保留 patch 层叠语义，用 `Scope` 只实现 preset 需要的隔离 |
| 2 | **TypeScript 类型系统承载了大量契约** | 声明合并（`SessionEventMap`/`RemoteErrorDetailsMap`）、`Branded<B>`、`assertNever`、Schemastery schema、Typert 代码生成（`docs/module-graph.zh.md` 由 peer 依赖生成） | 迁移时容易漏掉"类型即契约"的部分，跨边界校验不足 | 把每个 TS 契约翻译成三类 Go 产物：① 结构体 + `json` tag；② 显式校验函数（parser/config/wire 边界必须校验）；③ golden 测试。不要在 Go 里模拟类型级合并 |
| 3 | **前端 74 个 client 包 + slot 体系** | `packages/client/` 74 个包；`apps/web` vite 构建；Electron 44.7.0；`ui-*` 插件靠 slot/商品化扩展 | 重写≈重做整个 GUI；与 agent 主干无关 | 保留上游前端产物，Go 只做静态托管 + `/api` 兼容层；若必须重写，按 `ui-session`→`ui-conversation`→`ui-chat`→`ui-tool` 的最小链路裁剪 |
| 4 | **原生沙箱是不可移植部分** | `native/system` 提供 Landlock launcher + POSIX `flock`；`sandbox-local` 三平台后端；Windows 用 ACL 受限令牌 + 私有 temp + SID；bwrap/Landlock/Seatbelt 语义不同且存在 `partial` 强制执行 | 沙箱是安全边界，做错比不做更危险 | 先交付 `workspace-write` 的**保守实现**（路径前缀校验 + 拒绝符号链接逃逸 + 平台原生 best-effort），并对外声明 `Enforcement`；Windows 受限令牌需单独里程碑与 CI 机器 |
| 5 | **持久化语义细节多** | 单写者 flock/信号量、每批 `fsync`、write-behind 窗口、flush 屏障、撕裂 zstd 帧部分解码、不可变 generation、绝不降级（`session-format-status.zh.md`） | 一处做错就丢用户数据，且不可回退 | P1 就把 §6 第 1–6 条做成契约测试；写路径禁止"先写临时再 rename 覆盖已提交代"；flush 必须在每个 step 边界可等待 |
| 6 | **并发与事件顺序** | 同步 `session/event` 通知、waterfall 注册顺序决定语义（timeout 外层/内层不同）、工具批次独占 barrier + 模型序结果、inbox claim 顺序 | Go 天然并发，易引入"事件乱序/重复投递" | 事件总线保持**同步、单 goroutine 交付**语义（必要时每会话串行队列）；工具批次用 `errgroup` + 形状固定的结果槽位；所有共享状态标注所有权 |
| 7 | **性能与内存** | 会话可非常大（单 turn 多 step 大量工具输出）、JSONL 压缩、工具输出 100 MB 级、`docs/BENCHMARK.md` 与 bench 门禁 | 迁移后性能回退会直接否决方案 | 早建基准：`go test -bench` 覆盖 JSONL 写/读/派生、工具调度、grep；输出治理走 `spill` 而不是全量进内存 |
| 8 | **生态协议兼容** | MCP 官方 SDK、ACP、LSP、Office（libreoffice-kit）、pi-ai/oar/Claude Code SDK、Python SDK | 少一环就失去既有用户/集成 | 按 §6 优先级实现；MCP/LSP 用 Go 现成库（如 mcp-go）但需对齐错误码与结果投影；Office 与外部 agent SDK 建议**保留 sidecar 进程** |
| 9 | **依赖与供应链** | `pnpm-workspace.yaml` 的 `allowBuilds` 默认拒绝安装脚本、`patches/` 12 个补丁、`minimumReleaseAgeExclude` 大量精确版本、`THIRD_PARTY_NOTICES.md` | Go 模块引入等价依赖时同样需要审计 | 建 `go.mod` 依赖白名单与许可证检查（`go-licenses`），对 zstd/sqlite/websocket 等关键依赖锁定版本并记录理由 |
| 10 | **许可证** | 仓库 MIT；vendor 保留上游 MIT LICENSE 并 rescope；第三方清单独立维护 | 若直接搬运上游代码（含 vendor）需保留版权/许可声明 | 仅搬**语义与文档**不搬代码；必须搬运时在文件头保留 MIT 归属并更新 `THIRD_PARTY_NOTICES` 等价物 |
| 11 | **测试基线不可省** | 上游 `test:coverage` 要求 `packages/*/*/src` 每文件 100%、`test:snapshot` 无密钥回放、`verify-*` 门禁 40+ | 没有基线就无法证明"迁移等价" | 建立 Go 侧三层测试：单元、golden/快照回放、协议互操作；把上游 `snapshots/` 转成 Go `testdata` |
| 12 | **范围蔓延** | 65 个 experimental 包 + 74 个 client 包 + computer/browser use + Office + PTC + Ralph | 无限扩张导致主干永不发布 | 用 §3 的优先级表当**唯一范围协议**；P4 之外的包默认"不迁移"，需要时单独立项 |

**总体建议**：把「输出兼容」而非「实现兼容」作为验收标准——Go 实现只需在 §6 的 A/B 级接口上与上游可互操作，内部结构应利用 Go 优势（接口、`context`、`errgroup`、`//go:embed`）重新设计，不要逐文件翻译 TS 架构。

---

## 8. 参考阅读清单

（给 Go 开发者，按建议顺序；全部为参考仓库内相对路径）

| # | 路径 | 读它的理由 |
|---|---|---|
| 1 | `README.zh.md` | 产品定位、运行方式、MIT 许可 |
| 2 | `AGENTS.md` | 仓库布局总表、约定、命令、不可违背的工程规则 |
| 3 | `package.json`、`pnpm-workspace.yaml` | 版本、workspace 划分、依赖治理（allowBuilds/patches） |
| 4 | `docs/architecture.zh.md` | 架构主干：Cordis、profile/bundle、轮次流程、事件域、新增行为放哪 |
| 5 | `docs/cordis-primer.zh.md` | 五概念 + 五种分发模式 + waterfall 语义 |
| 6 | `docs/glossary.zh.md` | seam/scope/turn/step/Goal Round 等术语的唯一权威定义 |
| 7 | `packages/README.md` | 53 组的分组职责表（本文 §3 的依据） |
| 8 | `docs/agent-lifecycle.zh.md` | turn/step 时序图（实现 agent loop 时对照） |
| 9 | `docs/tool-execution-pipeline.zh.md` | 工具流水线每个阶段的职责与顺序 |
| 10 | `docs/subsystems/session.zh.md` + `packages/core/session/README.md` | 事件溯源日志、surface 投影、请求头重建 |
| 11 | `docs/session-format-status.zh.md` + `docs/persistence-catalog.zh.md` | 会话格式版本策略与 63 个事件/信封 schema |
| 12 | `docs/subsystems/persistence.zh.md` + `packages/session/session-persistence-jsonl/README.md` | 持久化契约、单写者、崩溃恢复、物理布局 |
| 13 | `packages/core/agent-loop/README.md` | 默认驱动器的完整行为（请求头、重试、取消、修复） |
| 14 | `packages/llm/llm/README.md` + `docs/subsystems/llm-streaming.zh.md` | LLM 词汇、流协议、适配器契约 |
| 15 | `docs/deepseek-llm-api-wire-extensions.zh.md` | DeepSeek 线协议扩展（要做官方端点兼容时必读） |
| 16 | `docs/subsystems/tools.zh.md` + `docs/tool-catalog.zh.md` | `ToolDefinition` 全字段与模型可见 schema |
| 17 | `docs/subsystems/sandbox.zh.md` + `packages/sandbox/README.md` | 三模式、逐调用策略、平台后端 |
| 18 | `packages/host/webserver/README.md` + `docs/api-gateway.zh.md` | Web 服务器路由/鉴权/注入与 Remote 调用链 |
| 19 | `packages/sdk/protocol/README.md` + `packages/acp/README.md` | 两个对外协议（SDK JSON-RPC / ACP）的精确方法表 |
| 20 | `docs/event-producer-consumer.zh.md` + `docs/capability-seams.zh.md` | 事件矩阵与 seam 全景（决定 Go 接口划分时的对照表） |

补充（按需）：`docs/module-graph.zh.md`（peer 依赖图）、`docs/config-catalog.zh.md`（每个插件配置字段）、`docs/defensive-patterns/README.md`（生命周期/并发/teardown 规则）、`docs/cookbook/adding-a-tool.zh.md` 与 `adding-an-llm-adapter.zh.md`（扩展点实操）、`apps/cli/reference/README.md`（CLI 行为面）。

---

## 附录：本文统计

| 指标 | 数值 |
|---|---|
| 模块映射表行数 | 53（packages 组）+ 6（apps）+ 5（外围目录）= **64 行** |
| 覆盖上游模块数 | **53 个 packages 模块组 / 382 个包 + 6 个 apps + 5 类外围目录**（`native`、`python`、`vendor`、`benchmarks`、`website`） |
| 工具级映射行数 | 25 行（覆盖 tool-catalog 收录的 37 个工具贡献包） |
| 核心概念映射小节 | 14 节（Cordis、Typert、Tool、Agent loop、Session、Preset/scope、Sandbox、MCP、LSP、Skill、Subagent/Team、Workflow/PTC、Storage/Settings/Credentials、Web/Desktop/ACP/SDK） |
| 协议兼容条目 | 19 条（A 级 6 条、B 级 12 条、C 级 1 条） |
| 引用上游文件 | 40+ 个相对路径；统计口径均为本次实测（见 §1.1） |
