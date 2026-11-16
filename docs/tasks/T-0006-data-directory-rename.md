# T-0006 · 数据目录改名为 ~/.deepseekharness-go 并迁移

- **状态**：done
- **更新**：2026-11-16
- **关联决策**：[D-0005](../decisions/D-0005-data-directory-rename.md)

## 目标

把运行时数据目录从 `~/.deekseekharness-go/` 改为 `~/.deepseekharness-go/`，并保证已有 config 与历史会话无损迁移。

## 范围

- **包含**：`DataDirName` 常量与路径函数、一次性迁移函数与单元测试、CLI 启动时的迁移调用与提示、`--config` 帮助文本、相关文档与决策/任务记录。
- **不包含**：`DSH_CONFIG` / `DSH_SESSION_DIR` 等显式覆盖的语义变更；与安装版桌面应用 `~/.dsh/` 的任何交互；P1 功能。

## 步骤

1. `internal/config`：新增 `DataDirName = ".deepseekharness-go"` 与 `legacyDataDirName`；`FilePath`、`defaultSessionDir` 改用常量。
2. 新增 `MigrateLegacyDataDir()`（解析 home）与 `migrateDataDir(legacy, current)`（可测试核心）：旧存在且新不存在才 `os.Rename`。
3. `cmd/dsh`：`commonFlags.load` 启动时调用迁移，成功打印一行提示；`--config` 帮助文本由 `~/.dsh/config.json` 修正为 `~/.deepseekharness-go/config.json`。
4. 测试：`internal/config/migrate_test.go` 覆盖「迁移」「两者共存不动」「无旧目录」「经 home 解析且只迁移一次」四种情形；外部测试的默认路径期望同步更新。
5. 真机验证：本机确有旧目录（含 2 个会话），跑一次 CLI 完成迁移并复查。

## 验证

| 命令 | 结果 |
|---|---|
| `go test ./internal/config -count=1 -v` | 13 个用例全部 PASS（含 4 个迁移用例） |
| `go test ./... -count=1` | 8 个包全部 ok |
| `gofmt -l .` / `go vet ./...` | clean / exit 0 |
| 真机首跑 `dsh-migrate.exe config` | stderr 打印 `dsh: migrated data directory to ~/.deepseekharness-go`，`config file:` 指向新路径 |
| 真机二次运行 | 无迁移提示（幂等） |
| 迁移后文件系统 | 旧目录已不存在；新目录含 `sessions/`，**2 个历史会话完整保留** |
| GitHub Actions `CI` | 待本次推送后确认（本地已验证 Linux 可交叉编译） |

## 自动决策

- **迁移放在 CLI 边缘而非 `config.Load`**：避免库函数在单元测试中搬动开发者真实目录，也让迁移时机可预期。
- **新旧目录共存时不合并**：无法判断哪份更新，静默合并风险更大；保持两者原样并在文档中说明。
- **提示走 stderr 而非 stdout**：`dsh config` 的 stdout 是结构化配置输出，提示不能混进去破坏可解析性。
- **顺手修正 `--config` 帮助文本**：原文还写着 `~/.dsh/config.json`（上游安装版路径），与实现不符，属于同一次改动范围内的文档错误。

## 遗留与下一步

- **无**（Q-0009 已闭环）。
