# T-0014 · 批次 4 第一步：profiles 层叠与 `--dump-config`

- **状态**：done
- **更新**：2026-11-21
- **关联决策**：D-0002（用户"继续批次4"授权）、D-0007（补测试默认授权）

## 目标

把"启动时到底装了什么"从写死在 `cmd/dsh/main.go` 的插件列表，变成**可声明、可覆盖、可打印**的 profile 层叠，并提供 `--dump-config` 输出每一行的来源。

## 非目标

- 不做运行期热重载（上游 HMR 不迁移，见 [MIGRATION_MAP](../MIGRATION_MAP.md) §7 风险 1）。
- 不做插件安装 / 插件市场（P4）。
- 不引入 YAML 依赖：patch 文件用 JSON（Q-0002 零依赖）。

## 设计

- **层序**（优先级从低到高）：内置 bundle patch → profile 自己的 patch → home patch（`<config dir>/cordis.patch.json`）→ `--patch`；层序在环境变量之后、命令行 flag 之前。
- **bundle**：编译期组合，各是一条 `insert` patch；当前实现 `base`（内置工具）、`web-app`（HTTP 传输）、`headless`（CLI 传输）。上游的 `sdk-app` / `sdk-minimal` / `acp-app` 留到 P4，不伪造空壳。
- **profile**：内建名字（`default` / `web` / `headless`），或 `<config dir>/profiles/<name>/package.json`（`dsh.profile.bundles`）+ 同目录 `cordis.patch.json`；用户目录覆盖同名内建 profile。
- **patch 语义**：顶层是数组，元素要么 `{"insert":[行…]}`，要么 `{"id": …, "disabled": …, "config": …}`；按 `id` 定位，`config` **整体替换**（与上游一致），未知 `id` 直接报错。
- **`runtime` 行**：唯一的配置行，承载全局设置（model / 超时 / `maxParallelToolCalls` 等）。它按**字段合并**而不是整体替换 —— 因为 `--dump-config` 要逐字段报告来源，整体替换会让 home patch 意外抹掉 profile 的设置。
- **`--dump-config`**：JSON 输出 `profile` / `bundles` / `layers` / `plugins` / `config` / `sources`，机器可读、可断言。

## 验证

| 验证 | 结果 |
|---|---|
| `go test ./internal/profiles -count=1` | 11 个用例通过（层序、来源、覆盖、错误路径、dump、home 解析） |
| `dsh tools` | 仍是 **11 tools**、`plugins=4`（default profile 与旧行为一致） |
| `dsh tools --dump-config` | 完整 JSON：3 个 bundle、6 行、11 个设置项，`sources` 中 `sessionDir=env:DSH_SESSION_DIR`、其余 `default` |
| `--patch` 禁用 `tools.shell` | `dsh tools` → **10 tools**；dump 中该行 `disabled:true`、`source:"patch:<file>"` |
| `--patch` 设置运行时 | `model=patch-model`、`toolTimeoutSec=7`、`maxParallelToolCalls=2`，三者来源均为 `patch:<file>` |
| 用户 profile（`profiles/mini`） | `bundles=base,headless`；layers=`bundle:base｜bundle:headless｜profile:mini｜home`；`maxIterations=3`（profile:mini）、`toolTimeoutSec=9`（home）；`todo` 行 `disabled:true`；`dsh tools` → 10 tools、`plugins=3` |
| 传输门禁 | `dsh web --profile headless` → exit 1（`does not enable the web transport`）；`dsh chat --profile web` → exit 1（`does not enable the CLI transport`） |
| 未知 profile | exit 1，错误里列出内建 profile 与用户 profile 目录 |
| BOM 容忍 | 用 PowerShell 默认 UTF-8（带 BOM）写 patch 时原本报 `invalid character '\ufeff'`，现已容忍并有用例 |
| 真实 CLI | 以上全部用 `bin\dsh.exe` 实跑，非仅单测 |
| 质量门禁 | `gofmt` clean、`go vet` 0、`staticcheck` 0、`go test ./...` 全绿 |

## 自动决策

- **不读 `DSH_HOME`**：端到端验证时发现本会话环境里 `DSH_HOME=C:\Users\ADMIN\.dsh`（安装版桌面应用导出），按它解析会让 Go 版读到**另一个产品**的 `profiles/web/package.json`（内含 `@deepseek-ai/dsh-base`），既报错又违反 D-0005 的目录隔离。改为 profile 目录 = 配置文件所在目录（`DSH_CONFIG` 可整体重定向），并补 `TestHomeIgnoresUpstreamDSHHOME` 钉死这个行为。
- **`runtime` 行逐字段合并**：上游"patch 整体替换行 config"用于插件行；`runtime` 是全局设置的投影，逐字段合并才能让来源报告有意义。插件行若带 `config` 直接报错（Go 侧插件尚无可配字段，静默忽略会让用户以为生效）。
- **只实现三个 bundle**：`sdk-app` / `sdk-minimal` / `acp-app` 的传输在 P4，先不建空 bundle；未知 bundle 名报错并列出已知项。
- **profile 目录跟随配置文件**：让 `DSH_CONFIG` 一个变量就能把配置、profile、patch 全部重定向，避免再引一个 `DSH_*` 变量。
- **patch 用 JSON 不用 YAML**：Q-0002 决定零第三方依赖，`encoding/json` 足够；文件名仍沿用 `cordis.patch.json` 以标明对应关系。
- **`--dump-config` 用 JSON 而不是带注释的 YAML**：`sources` 映射本身就表达"来源"，且可直接被测试与工具消费。

## 遗留与下一步

- **不支持上游 `cordis.patch.yml`（YAML）**：需要时再决策是否引 YAML 依赖；当前 JSON 是本仓库的格式。
- **行 id 唯一、无参数化实例**：同一插件不能挂两份（上游靠不同 id + config 区分），等真有需求再做。
- **`--dump-config` 只有 JSON 形态**：无 `# source` 注释式输出。
- **热重载**：改 profile/patch 需重启（与 MIGRATION_MAP §7 的降级结论一致）。
