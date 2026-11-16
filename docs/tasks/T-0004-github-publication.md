# T-0004 · 发布到 GitHub 并固定提交身份

- **状态**：done
- **更新**：2026-11-16
- **关联决策**：[D-0003](../decisions/D-0003-commit-identity.md)

## 目标

把本仓库发布到 <https://github.com/Vicar-c/deekseekharness-go>，并且：

1. 发布前确认仓库可构建、测试全绿、无密钥/隐私泄漏；
2. 所有提交的作者与提交者归属用户本人（`Vicar-c <244100401@qq.com>`）。

## 范围

- **包含**：远端配置、仓库级 git 身份固定、未发布历史的作者改写、规则落盘（AGENTS.md / 决策 / 任务 / STATUS）、首次推送。
- **不包含**：功能实现、模块或目录重命名、CI 配置、文档绝对路径清理（后两项登记为遗留，等待用户决策）。

## 步骤

1. **预检**：确认 `git status` 干净、远端仓库为空（`git ls-remote` 无引用）、GitHub 直连可用（`ls-remote` 上游仓库 1 秒返回）。
2. **本地验证**：`go test` / `go vet` / `go build` / Markdown 链接检查 / 密钥扫描。
3. **身份固定**：仓库级 `user.name=Vicar-c`、`user.email=244100401@qq.com`。
4. **历史改写**：`git filter-branch --env-filter` 重写 6 个提交的 author/committer；改写前先建 `backup/pre-author-rewrite` 备份分支。
5. **规则落盘**：AGENTS.md §7 新增「提交身份（强制 · D-0003）」；新建 D-0003 与 T-0004；更新两个索引与 STATUS。
6. **推送**：`git push -u origin main`（凭据助手为 Git 自带的 GCM，仓库级配置）。

## 验证

| 命令 | 结果 |
|---|---|
| `go test ./... -count=1` | 8 个包全部 `ok`（agent/builtin/config/kernel/llm/server/session/tool） |
| `go vet ./...` | exit 0 |
| `go build ./cmd/dsh` | 成功，产物 11.7 MB，`dsh version` 正常输出 |
| `python scripts/check_md_links.py` | `md_files=16 relative_links_checked=46 missing=0` |
| 密钥扫描（`sk-` / `ghp_` / 私钥 / 真实 key） | 0 命中 |
| 最大受控文件 | `docs/MIGRATION_MAP.md` 74 KB（无大文件） |
| `git log --format='%an <%ae> | %cn <%ce>'` | 全部为 `Vicar-c <244100401@qq.com>` |
| 发布前 GitHub 仓库状态 | 空仓库（`ls-remote` 无引用） |

## 自动决策

- **改写历史用 `filter-branch`**：仓库只有 6 个提交且尚未发布，改写成本为零；`filter-repo` 未安装，不值得为此引入依赖。改写前建了备份分支，`refs/original/*` 亦保留旧哈希。
- **凭据助手用 Git 自带的 GCM**：机器上没有 `gh`、没有 PAT、没有 SSH GitHub key，但 Git for Windows 自带 `git-credential-manager.exe`，只需在仓库级配置即可用，避免引入新工具或把 token 写入会话。
- **只改仓库级而非全局 git 配置**：用户其它仓库的提交身份不应被本次任务影响。

## 遗留与下一步

- **模块路径缺 host 前缀**：`go.mod` 声明 `module deekseekharness-go`（无域名前缀），而远端路径是 `github.com/Vicar-c/deekseekharness-go`，因此 `go install github.com/Vicar-c/deekseekharness-go/cmd/dsh@latest` 会报 "module declares its path as: deekseekharness-go but was required as: github.com/Vicar-c/deekseekharness-go"。待用户在「改 `go.mod` 与 9 处 import 加 `github.com/Vicar-c/` 前缀 / 只当源码仓库（README 只写 clone 后本地构建）」中决策。
- **文档中的本机绝对路径**：`AGENTS.md`、`docs/STATUS.md`、`docs/MIGRATION_MAP.md` 含 `C:\Users\ADMIN\...`，公开仓库下属于信息噪音，待决策是否替换为占位符。
- **无 CI**：仓库没有 `.github/`，建议补一个 `go test` + `go vet` 的最小工作流。
- **`go` 未进用户 PATH**：位于 `C:\Users\ADMIN\AppData\Local\go\bin`，新终端需手动加入才能直接 `go run`。
