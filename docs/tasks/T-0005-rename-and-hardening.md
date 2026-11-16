# T-0005 · 仓库改名 deepseekharness-go、模块路径与文档/CI 加固

- **状态**：done
- **更新**：2026-11-16
- **关联决策**：[D-0004](../decisions/D-0004-rename-and-hardening.md)

## 目标

按用户指示把项目名统一为 `deepseekharness-go`，并处理发布后暴露的三个待决问题（Q-0006 / Q-0007 / Q-0008，均取方案 A）：

1. 仓库改名 `Vicar-c/deepseekharness-go`，模块路径补 host 前缀，使 `go install` 可用；
2. 公开文档去掉本机绝对路径；
3. 补最小 CI。

## 范围

- **包含**：GitHub 仓库改名、`go.mod` 与 import、构建脚本 ldflags、项目显示名、文档中的项目名与路径、CI 工作流、决策/任务记录与 STATUS。
- **不包含**：运行时数据目录改名（Q-0009 待决策）、本地目录改名（本次在仓库外单独执行，见「附」）、任何功能实现、P1 主线。

## 步骤

1. **改名**：`PATCH /repos/{owner}/{repo}` 将仓库改为 `deepseekharness-go`（用 GCM 已存的 OAuth token，避免把 token 写进会话）；`git remote set-url` 同步。
2. **模块路径**：`go.mod` → `module github.com/Vicar-c/deepseekharness-go`；脚本批量把 `"deekseekharness-go/internal/` 替换为 `"github.com/Vicar-c/deepseekharness-go/internal/`（15 个文件、48 处），并逐条校验替换次数。
3. **显示名**：`main.go` 版本横幅、`version.go` ldflags 注释、`Makefile`、`scripts/build.ps1`、`LICENSE`、README/AGENTS/ARCHITECTURE 标题。
4. **路径可移植**：`C:\Users\ADMIN\...` → `%USERPROFILE%\...` / `%LOCALAPPDATA%\...`，涉及 AGENTS.md、STATUS.md、MIGRATION_MAP.md、T-0001、T-0004、build.ps1。
5. **CI**：新增 `.github/workflows/ci.yml`（ubuntu + windows；gofmt 仅 Linux；go vet / go test / go build）。
6. **记录**：新增本任务与 D-0004，更新两个索引与 STATUS（Q-0006…Q-0008 标记 resolved，新增 Q-0009）。

## 验证

| 命令 | 结果 |
|---|---|
| `gofmt -l .` | 无输出（clean） |
| `go vet ./...` | exit 0 |
| `go test ./... -count=1` | 8 个包全部 ok（新模块路径） |
| `go build ./cmd/dsh` | exit 0，`dsh version` → `deepseekharness-go 0.1.0-dev` |
| `GOOS=linux go build ./...` | exit 0（Linux 可交叉编译，CI 的 ubuntu 作业前提） |
| `python scripts/check_md_links.py` | 20 个 md、77 条相对链接、0 缺失 |
| 残留检查 | 仓库内已无 `C:\Users\ADMIN`，无裸 `"deekseekharness-go/internal/` import |
| GitHub | 仓库 `Vicar-c/deepseekharness-go`，`main` 推送成功 |
| GitHub Actions `CI` | 首跑 ubuntu 失败（`TestApplyFlags` 用了 Windows 绝对路径）→ 修复后 `43d262d` **ubuntu + windows 全绿** |
| `go install github.com/Vicar-c/deepseekharness-go/cmd/dsh@latest` | 成功（3.1s），安装到 `%USERPROFILE%\go\bin\dsh.exe`，`dsh version` → `deepseekharness-go 0.1.0-dev` |

## 自动决策

- **用 API 改名而不是让用户点鼠标**：GCM 已缓存 `Vicar-c` 的 OAuth token，`PATCH /repos` 一次成功，减少用户操作。
- **用脚本批量替换 import 并校验次数**：48 处 import 手改易漏；脚本对每一对 `(文件, 原文, 期望次数)` 断言，次数不符即中止。
- **数据目录不同步改名**：属于用户数据与用户可见路径变更，按 D-0002 单独登记 Q-0009，不在本任务内实施。
- **CI 双平台**：`builtin` 的 shell 工具按 `runtime.GOOS` 分派，测试里有 GOOS 分支，双平台可真实覆盖；Linux 作业额外做 `gofmt` 检查。
- **修 CI 首跑暴露的测试缺陷**：`TestApplyFlags` 直接把 `C:\work` 当绝对路径断言 `filepath.IsAbs`，在 Linux 上必然失败；这不是产品缺陷（`ApplyFlags` 只做赋值，绝对化在 `Normalize`）。改为 `t.TempDir()` 断言赋值语义，并新增 `TestNormalizeResolvesWorkspace` 保留"归一化后必须是绝对路径"的原意。

## 遗留与下一步

- **Q-0009 待决策**：数据目录是否改名 `~/.deepseekharness-go/`（建议改名 + 一次性迁移）。
- **CI 已绿**：`43d262d` 起 ubuntu 与 windows 两个作业均通过（此前 ubuntu 因 `TestApplyFlags` 平台相关断言失败，已修复）。

## 附：仓库外的本地操作

- 本地目录已由 `%USERPROFILE%\Desktop\deekseekharness-go` 改名为 `%USERPROFILE%\Desktop\deepseekharness-go`，与仓库名一致。首次尝试被语言服务器 `gopls.exe` 占用（`Rename-Item` 报「另一个进程正在使用此文件」），编辑器释放句柄后重试成功；改名后 `git status` / `go test` 均正常。
- 该操作与仓库内容无关，不影响任何提交、构建或 CI 结果。
