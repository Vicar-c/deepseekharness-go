# T-0002 · 协作契约与跨会话记录体系

- **状态**：done
- **日期**：2026-11-16
- **负责人**：lead agent（root）
- **关联决策**：[D-0001](../decisions/D-0001-project-baseline.md)
- **关联提交**：本次提交（docs）

## 目标（一句话）

把用户的协作原则固化为仓库级契约（AGENTS.md），建立"任务记录 + 用户决策记录 + 状态快照"的跨会话文档体系，并确定项目总体基调。

## 非目标（明确不做）

- 不修改任何 Go 代码与行为。
- 不引入 CI/工具链变更。
- 不替用户决定已列为"待决策"的事项。

## 范围与写作用域

| 类型 | 路径 |
|---|---|
| 新增 | `AGENTS.md`、`CLAUDE.md`、`docs/AGENT_WORKFLOW.md`、`docs/STATUS.md`、`docs/tasks/**`、`docs/decisions/**` |
| 修改 | `README.md`（新增协作契约入口） |
| 只读参考 | 上游 `AGENTS.md`、`docs/` 记录习惯 |

## 验收标准（可执行）

- [x] `AGENTS.md` 覆盖用户提出的 5 条原则，并给出必须让用户决策的场景清单
- [x] 任务记录模板 + 决策记录模板 + 索引可实际使用
- [x] 用户决策以任务维度单独成文件（`docs/decisions/D-0001-*.md`）
- [x] 项目基调（代理推导）有明确清单，用户可修订/否决
- [x] `docs/STATUS.md` 可作为跨会话第一入口，含待决策项
- [x] 所有文档内链可达、无密钥泄漏
- [x] `git status` 干净并提交

## 实施步骤

1. 提炼用户 5 条原则（落盘、用户决策单独记录、多 Agent、重构纪律、范围纪律）。
2. 按用户风格推导项目总体基调（结果导向/文档即记忆/最小扩散/证据驱动/稳定优先/执行彻底/隔离与最小权限/小步提交/中文文档/人类保留最终决策权）。
3. 设计文档体系：`AGENTS.md`（最高契约）+ `docs/AGENT_WORKFLOW.md`（流程细则）+ `docs/STATUS.md`（实时快照）+ `docs/tasks/`（任务记录）+ `docs/decisions/`（用户决策记录）+ 两个模板 + 索引。
4. 回填本次项目引导的既有实现记录（T-0001）与用户决策（D-0001），让体系从第一天起就有真实内容。
5. 更新 README 入口并校验链接。

## 自动决策（Agent 自主决定）

| 决策 | 理由 | 影响 |
|---|---|---|
| 采用 `AGENTS.md` 作为根契约，`CLAUDE.md` 仅作指针 | 兼容不同 Agent 工具的约定 | 无 |
| 任务/决策使用单调编号 `T-XXXX` / `D-XXXX` | 引用稳定、便于索引 | 无 |
| 用户决策与实现记录分离（decisions vs tasks） | 用户明确要求"用户决策单独有决策记录文件" | 无 |
| 增加 `docs/STATUS.md` 作为唯一实时入口 | 跨会话接力的最低成本方案 | 需要每次会话维护 |
| 代理推导的项目基调默认生效但标注"可修订" | 用户委托"可以针对我的风格再确定一些" | 用户可随时改 D-0001 |
| 不在本次引入 `.gitattributes` | 属于范围外，需用户决策 | 记入 STATUS 待决策 |

## 验证记录

```text
$ python scripts/check_md_links.py        → md_files=14 relative_links_checked=38 missing=0
$ go test -count=1 ./...                  → 8 个包全部 ok（docs-only 改动回归确认）
$ 密钥扫描（真实 key 匹配工作区文件）      → 0 命中
$ git status                              → 提交后干净
```

文档体系自检：任务/决策索引与模板可实际使用；T-0001/T-0002、D-0001 已作为首批记录落盘。

## 文件清单

| 操作 | 文件 | 说明 |
|---|---|---|
| 新增 | `AGENTS.md` | 仓库级协作契约（最高约束） |
| 新增 | `CLAUDE.md` | `@AGENTS.md` 指针 |
| 新增 | `docs/AGENT_WORKFLOW.md` | 工作流、模板用法、多 Agent 编排 |
| 新增 | `docs/STATUS.md` | 跨会话状态快照 |
| 新增 | `docs/tasks/README.md`、`docs/tasks/TEMPLATE.md` | 任务索引与模板 |
| 新增 | `docs/decisions/README.md`、`docs/decisions/TEMPLATE.md` | 决策索引与模板 |
| 新增 | `docs/tasks/T-0001-project-bootstrap.md` | 既有工作回填记录 |
| 新增 | `docs/tasks/T-0002-agent-guidelines.md` | 本任务记录 |
| 新增 | `docs/decisions/D-0001-project-baseline.md` | 用户决策 + 项目基调 |
| 修改 | `README.md` | 新增协作契约入口 |

## 遗留问题与下一步

- 等用户确认/修订 D-0001 中代理推导的基调。
- 后续每个任务开始前先登记任务记录（T-0003 起）。
- P1 启动时，按本体系建立 T-0003 并记录用户对 P1 主线的决策。

## 范围外发现（等待用户决策）

- `.gitattributes` 行尾规范（已登记 STATUS）。
- 是否增加 CI（GitHub Actions 本地/远程）与提交前钩子（lefthook）——暂不引入。
