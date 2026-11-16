# D-0005 · 数据目录改名为 ~/.deepseekharness-go 并一次性迁移

- **状态**：生效
- **日期**：2026-11-16
- **决策人**：用户
- **关联任务**：[T-0006](../tasks/T-0006-data-directory-rename.md)
- **关联提交**：`feat(config)` 提交（数据目录改名与迁移）
- **取代/被取代**：补充 [D-0004](D-0004-rename-and-hardening.md) 的 D-0004-04（当时把该改名推迟为 Q-0009）

## 背景

[D-0004-04](D-0004-rename-and-hardening.md) 把项目名统一为 `deepseekharness-go` 时，运行时数据目录仍保留旧拼写 `~/.deekseekharness-go/`，理由是要迁移用户已有的 config 与历史会话，属于用户数据变更，需单独授权；该问题登记为决策队列 Q-0009。用户指示：

> "Q-0009 先改"

即按建议方案 A 执行：改名 + 一次性迁移。

## 选项

| 选项 | 优点 | 缺点/代价 |
|---|---|---|
| A. 改名 `~/.deepseekharness-go/`，首次启动把旧目录整体改名过来 | 路径拼写全局统一；用户无感，数据不丢 | 需要一小段迁移逻辑与测试；迁移提示会出现在首次启动的 stderr |
| B. 保持旧名 | 零风险 | 公开仓库里永远留着一个错误拼写的目录名，`dsh config` 与文档不一致 |
| C. 改名但做"复制 + 保留旧目录" | 更保守 | 两份数据并存，后续读写哪份容易混乱；会话可能重复 |

## 决定

- **D-0005-01**：数据目录由 `~/.deekseekharness-go/` 改为 `~/.deepseekharness-go/`（常量 `config.DataDirName`）。
- **D-0005-02**：启动时执行一次性迁移：**仅当**旧目录存在且新目录不存在时，用 `os.Rename` 整体改名；两者都存在时保持原样（不合并、不覆盖、不报错），保证任何情况下都不丢数据。
- **D-0005-03**：迁移由 CLI 启动路径（`cmd/dsh` 的 `commonFlags.load` → `config.MigrateLegacyDataDir`）触发，库的 `Load`/`FilePath` 保持无副作用；迁移核心 `migrateDataDir(legacy, current)` 可独立测试。
- **D-0005-04**：迁移成功时向 stderr 打印一行 `dsh: migrated data directory to ~/.deepseekharness-go`，之后每次启动静默。

## 理由

- 方案 A 让 `dsh config` 显示的路径、README、AGENTS 与 GitHub 仓库名四处一致，消除"名字改了但目录还是错的"这一长期噪音。
- 用 `os.Rename` 而不是复制：同卷改名是原子操作，几毫秒完成，且不会产生两份会话数据。
- "两者都存在就不动"是唯一安全选择：此时无法判断哪份是用户的最新数据，静默合并的风险远大于收益。
- 迁移放在 CLI 边缘而不是 `Load` 内部：库函数保持可预测、可测试，不会在单元测试里意外搬动开发者的真实数据目录。

## 影响面

- **代码**：`internal/config`（`DataDirName` 常量、`FilePath`、`defaultSessionDir`、新增 `MigrateLegacyDataDir` 与 `migrateDataDir`）、`cmd/dsh`（`load` 增加迁移调用与 stderr 提示、`--config` 帮助文本修正为 `~/.deepseekharness-go/config.json`）。
- **数据**：首次启动时旧目录被整体改名，config.json 与 sessions 原样保留；新旧共存时不做任何改动。
- **文档**：README（快速开始、模块表、配置、隔离说明、示例 JSON）、AGENTS.md 数据目录条目、STATUS 环境事实与决策队列。
- **兼容性**：`DSH_CONFIG`、`DSH_SESSION_DIR` 等显式配置优先级不变，显式指定路径的行为完全不受影响。
- **安全/成本**：无网络与成本影响；迁移只发生在用户 home 下，不触碰 `~/.dsh/`。

## 回滚方式

1. 代码：把 `DataDirName` 改回 `".deekseekharness-go"` 并删除迁移函数与其调用、测试。
2. 数据：`Rename-Item "$env:USERPROFILE\.deepseekharness-go" ".deekseekharness-go"`（或 `mv`）即可搬回旧目录。

## 关联链接

- 任务：[T-0006-data-directory-rename.md](../tasks/T-0006-data-directory-rename.md)
- 前置决策：[D-0004-rename-and-hardening.md](D-0004-rename-and-hardening.md)
- 状态与决策队列：[../STATUS.md](../STATUS.md)
- 决策索引：[README.md](README.md)
