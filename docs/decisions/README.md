# 决策记录索引

本目录保存**用户决策**，按任务维度一个文件（`D-XXXX-<slug>.md`）。
Agent 的自主实现决策不在此登记，写入对应任务记录的「自动决策」小节。

规范见 [AGENTS.md](../../AGENTS.md) §1.2 与 [AGENT_WORKFLOW.md](../AGENT_WORKFLOW.md) §3；模板见 [TEMPLATE.md](TEMPLATE.md)。

## 约定

- 决策 ID：`D-XXXX`（文件级）+ `D-XXXX-NN`（文件内条目）。
- **只追加不覆盖**；结论变化时新增条目并注明"取代 D-XXXX-NN"。
- 每条决策必须包含：背景、选项、决定、理由、影响、回滚方式。
- 用户未拍板的事项不写入决策正文，登记在 [docs/STATUS.md](../STATUS.md) 的「待用户决策」。
- 不得替用户决定：范围、安全、成本、产品交互、破坏性接口变更。

## 索引

| ID | 标题 | 状态 | 日期 | 关联任务 |
|---|---|---|---|---|
| [D-0001](D-0001-project-baseline.md) | 项目总体基调与协作原则（用户决策 + 代理推导基调） | 生效（代理部分待用户确认） | 2026-11-16 | [T-0002](../tasks/T-0002-agent-guidelines.md) |
| [D-0002](D-0002-decision-gate.md) | 会话内决策表格与实施门禁 | 生效 | 2026-11-16 | [T-0003](../tasks/T-0003-decision-gate.md) |
| [D-0003](D-0003-commit-identity.md) | 所有提交的作者与提交者固定为用户本人 | 生效 | 2026-11-16 | —（发布到 GitHub 时的用户指示） |
| [D-0004](D-0004-rename-and-hardening.md) | 仓库改名 deepseekharness-go，模块路径补 host 前缀，文档去绝对路径，补 CI | 生效 | 2026-11-16 | [T-0005](../tasks/T-0005-rename-and-hardening.md) |
