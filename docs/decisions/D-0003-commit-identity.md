# D-0003 · 所有提交的作者与提交者固定为用户本人

- **状态**：生效
- **日期**：2026-11-16
- **决策人**：用户
- **关联任务**：[T-0004](../tasks/T-0004-github-publication.md)
- **关联提交**：docs 提交（提交身份规则与发布记录）
- **取代/被取代**：—

## 背景

仓库首次发布到 GitHub 之前，仓库级 git 身份是占位值 `deekseekharness-go <dev@deekseekharness-go.local>`，已有 6 个提交的作者与提交者都是它。这样的提交推送到 GitHub 后不会归属到用户账号（`Vicar-c`），且后续任何 Agent 接手都会继续沿用该占位身份。用户明确要求：

> "AGENTS.md 写上，以后所有的提交都写给我的名字。"

## 选项

| 选项 | 优点 | 缺点/代价 |
|---|---|---|
| A. 只在会话里口头约定 | 零成本 | 换会话/换 Agent 后必然失效，无任何机械保障 |
| B. 写入 AGENTS.md + 固化仓库级 git 身份，并回溯改写未发布历史 | 规则可被任何 Agent 读到，配置层再兜底；发布前改写不影响他人 | 需要重写历史（此时尚未推送，成本为零） |
| C. 每次提交用 `git commit --author` 临时指定 | 不改配置 | 忘记一次就产生错误身份，且没有自动校验手段 |

## 决定

- **D-0003-01**：本仓库所有提交的 author 与 committer 一律为 `Vicar-c <244100401@qq.com>`。
- **D-0003-02**：该身份固化在仓库级 git 配置（`user.name` / `user.email`），不依赖单个会话的记忆，也不依赖全局配置。
- **D-0003-03**：规则写入 [AGENTS.md](../../AGENTS.md) §7「提交身份（强制 · D-0003）」；Agent **不得**用 `git -c user.name=...`、`git commit --author=...`、`GIT_AUTHOR_*` / `GIT_COMMITTER_*` 临时替换为其它身份。
- **D-0003-04**：发布前的 6 个历史提交（`2dd26cf`…`261eab2`）已统一改写为该身份；改写前的历史保留在本地分支 `backup/pre-author-rewrite`（不推送，仅备份）。

## 理由

- 提交身份是**归属**问题：公开仓库里作者指向占位身份，等于把用户的工作挂在一个不存在的账号上。
- 写进 AGENTS.md 是因为该文件是本仓库最高优先级契约，任何 Agent 开工必读；只写会话记录等于没写。
- 同时固定仓库级配置，等于给规则加了一层机械兜底：Agent 即使偷懒，默认提交身份也是对的。
- 选择在首次推送前改写历史：此时代价为零（远端为空），发布后改写则需要 force push 并影响协作者。

## 影响面

- **代码/数据**：无。仅提交元数据（author/committer）变化，6 个提交的哈希因此全部改变。
- **文档**：新增本决策、[T-0004](../tasks/T-0004-github-publication.md)，并更新 AGENTS.md §7、`docs/decisions/README.md`、`docs/tasks/README.md`、`docs/STATUS.md`。
- **兼容性**：尚未发布，无下游引用旧哈希；本地 `backup/pre-author-rewrite` 与 `refs/original/*` 保留旧历史。
- **安全/成本**：无。身份信息（`244100401@qq.com`）本就写在全局 git 配置与提交里，属于用户主动选择的公开信息。

## 回滚方式

1. 规则回滚：删除 AGENTS.md §7「提交身份（强制 · D-0003）」小节，并把本决策状态改为「已取代」。
2. 配置回滚：`git config --local user.name "deekseekharness-go"; git config --local user.email "dev@deekseekharness-go.local"`。
3. 历史回滚（仅在确有必要时）：`git branch -f main backup/pre-author-rewrite` 后强制推送；**已发布后强制推送会重写远端历史**，需用户明确授权。

## 关联链接

- 契约：[AGENTS.md](../../AGENTS.md) §7
- 任务：[T-0004-github-publication.md](../tasks/T-0004-github-publication.md)
- 状态快照：[docs/STATUS.md](../STATUS.md)
- 决策索引：[README.md](README.md)
