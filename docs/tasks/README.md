# 任务记录索引

本目录按**任务维度**保存实现记录：一个任务一个文件，记录目标、范围、步骤、验证、自动决策、遗留与下一步。
规范见 [AGENTS.md](../../AGENTS.md) §3–§4 与 [AGENT_WORKFLOW.md](../AGENT_WORKFLOW.md)；模板见 [TEMPLATE.md](TEMPLATE.md)。

## 约定

- 文件命名：`T-XXXX-<kebab-slug>.md`，编号单调递增，不复用。
- 状态：`todo` → `doing` → `blocked` → `done`，取消用 `cancelled`（注明原因）。
- 任务记录由任务负责人维护；多 Agent 场景由根 Agent 合并定稿。
- 任务完成后**不要删除**记录；后续任务引用历史记录即可。

## 索引

| ID | 标题 | 状态 | 更新日期 | 关联决策 |
|---|---|---|---|---|
| [T-0001](T-0001-project-bootstrap.md) | 项目引导：Go 骨架、构建与真实 API 验证 | done | 2026-11-16 | — |
| [T-0002](T-0002-agent-guidelines.md) | 协作契约与跨会话记录体系 | done | 2026-11-16 | D-0001 |
| [T-0003](T-0003-decision-gate.md) | 决策门禁与会话内决策表格落地 | done | 2026-11-16 | D-0002 |
| [T-0004](T-0004-github-publication.md) | 发布到 GitHub 并固定提交身份 | done | 2026-11-16 | D-0003 |
| [T-0005](T-0005-rename-and-hardening.md) | 仓库改名 deepseekharness-go、模块路径与文档/CI 加固 | done | 2026-11-16 | D-0004 |
| [T-0006](T-0006-data-directory-rename.md) | 数据目录改名为 ~/.deepseekharness-go 并迁移 | done | 2026-11-16 | D-0005 |
| [T-0007](T-0007-batch0-1-tools.md) | 批次 0+1：行尾规范与内置工具补齐 | done | 2026-11-21 | D-0006 |
| [T-0008](T-0008-session-v4-format-layer.md) | 批次 2 第一步：v4 会话格式类型层与编解码 | done | 2026-11-21 | D-0002、D-0007 |
| [T-0009](T-0009-session-v4-projection-and-writer.md) | 批次 2 第二/三步：v4 投影层与持久化写入器 | done | 2026-11-21 | D-0002、D-0007 |
| [T-0010](T-0010-session-v4-wiring.md) | 批次 2 收口：Store 与 agent.Loop 接入 v4 | done | 2026-11-21 | D-0002、D-0007 |

## 新建任务步骤

1. 在索引中取下一个编号；
2. 复制 [TEMPLATE.md](TEMPLATE.md) 为 `T-XXXX-<slug>.md` 并填写；
3. 在本索引表格新增一行（状态 `todo`/`doing`）；
4. 在 [docs/STATUS.md](../STATUS.md) 的「进行中」登记。
