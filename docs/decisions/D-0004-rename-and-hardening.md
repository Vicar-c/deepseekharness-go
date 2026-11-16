# D-0004 · 仓库改名 deepseekharness-go 与文档/CI 加固

- **状态**：生效
- **日期**：2026-11-16
- **决策人**：用户
- **关联任务**：[T-0005](../tasks/T-0005-rename-and-hardening.md)
- **关联提交**：`chore(module)` / `docs` / `chore(ci)` 三个提交
- **取代/被取代**：—

## 背景

仓库首次发布时用的名字是 `deekseekharness-go`（比正确拼写少一个 s，源自最初本地目录名），`go.mod` 也只是裸名 `module deekseekharness-go`。发布后暴露出三个待决问题：模块路径无 host 前缀导致 `go install` 不可用（Q-0006）、公开文档含本机绝对路径（Q-0007）、无 CI（Q-0008）。用户指示：

> "deepseekharness-go，git 我改名么，然后先把 6,7,8 按照你推荐的处理了"

即：仓库名统一为 `deepseekharness-go`，且 Q-0006 / Q-0007 / Q-0008 按建议方案 A 全部落地。

## 选项

| 选项 | 优点 | 缺点/代价 |
|---|---|---|
| A. 仓库改名 + 模块路径补 host 前缀 + 文档去绝对路径 + 补 CI（本次采用） | 与 GitHub 路径一致，`go install` 可用；文档可公开；CI 防回归 | 改名产生一次提交哈希变化（未发布新历史，无下游影响）；CI 首跑需观察 |
| B. 只改仓库名，模块路径保持裸名 | 改动最小 | `go install github.com/...@latest` 永远失败，公开仓库仍不可直接安装 |
| C. 全部保持现状 | 零成本 | 拼写错误长期留在 URL；文档泄露账号名；无回归保护 |

## 决定

- **D-0004-01**：GitHub 仓库改名为 `Vicar-c/deepseekharness-go`（旧地址由 GitHub 自动重定向）；`go.mod` 改为 `module github.com/Vicar-c/deepseekharness-go`，15 个 Go 文件的 import 同步更新；`dsh version` 输出改为 `deepseekharness-go`。
- **D-0004-02**：公开文档不再出现本机绝对路径，统一改为 `%USERPROFILE%\...` / `%LOCALAPPDATA%\...` 可移植写法（命令仍可直接复制执行，同时不泄露本机账号名）。
- **D-0004-03**：新增 `.github/workflows/ci.yml`：`ubuntu-latest` + `windows-latest` 双平台执行 `gofmt -l`（仅 Linux）、`go vet ./...`、`go test ./... -count=1`、`go build ./cmd/dsh`。
- **D-0004-04**：运行时数据目录 `~/.deekseekharness-go/` **本次不改名**，避免迁移用户已有的 config 与历史会话；改名与迁移方案登记为决策队列 Q-0009。

## 理由

- 仓库名与模块路径一致，是 Go 生态里 `go install <module>@latest` 可用的前提；错拼写留在公开 URL 上会长期误导使用者。
- 用 `%USERPROFILE%` 而不是 `<home>` 之类占位符：文档里的命令仍然可以原样复制运行，只是去掉了账号名，信息量与可执行性都保住。
- CI 用双平台而非仅 Windows：README/AGENTS 声称"跨平台可编译"，需要机器验证而不是口头声明；Windows 平台与开发环境一致，作为主基线。
- 数据目录单独决策：它承载用户的 config 与会话数据，属于"数据格式/用户可见行为"变更，按 D-0002 必须单独授权。

## 影响面

- **代码**：`go.mod`、15 个 Go 文件的 import、`version` 注释、`main.go` 版本横幅；`Makefile`、`scripts/build.ps1` 的 ldflags 路径。行为无变化。
- **构建/发布**：`go build`/`go test` 已用新路径验证通过；GitHub Actions 首次运行结果需观察（本地只能做交叉编译验证 Linux 可构建）。
- **文档**：README、AGENTS.md、ARCHITECTURE、MIGRATION_MAP、STATUS、T-0001、T-0004 中的项目名与绝对路径；LICENSE 版权署名同步改为 `deepseekharness-go contributors`。
- **兼容性**：旧 URL 由 GitHub 301 重定向；无对外 API、无下游模块依赖，改模块路径不破坏任何已发布引用。
- **安全**：文档不再泄露本机用户名；CI 使用 `permissions: contents: read` 最小权限。

## 回滚方式

1. 仓库名：GitHub Settings 改回 `deekseekharness-go`，并 `git remote set-url origin https://github.com/Vicar-c/deekseekharness-go.git`。
2. 模块路径：`go.mod` 与全部 import 改回裸名 `deekseekharness-go`（可用同样的批量替换回退），`dsh version` 横幅改回。
3. CI：删除 `.github/workflows/ci.yml`。
4. 以上均按提交粒度独立回滚，互不牵连。

## 关联链接

- 任务：[T-0005-rename-and-hardening.md](../tasks/T-0005-rename-and-hardening.md)
- 前置决策：[D-0003-commit-identity.md](D-0003-commit-identity.md)
- 状态与决策队列：[../STATUS.md](../STATUS.md)
- 决策索引：[README.md](README.md)
