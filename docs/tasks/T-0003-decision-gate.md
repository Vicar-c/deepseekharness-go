# T-0003 · 决策门禁与会话内决策表格落地

- **状态**：done
- **日期**：2026-11-16
- **负责人**：lead agent（root）
- **关联决策**：D-0002
- **关联提交**：docs 提交（决策门禁规则）

## 目标（一句话）

把用户新提出的协作规则（会话内决策表格、逐项决策、未决策不实施、说"实现"才实施）写入仓库契约与工作流，并建立可复用的决策队列。

## 非目标（明确不做）

- 不实施任何功能开发（P1 及其他需求保持等待）。
- 不改动任何 Go 代码与运行行为。

## 范围与写作用域

| 类型 | 路径 |
|---|---|
| 新增 | `docs/decisions/D-0002-decision-gate.md`、`docs/tasks/T-0003-decision-gate.md` |
| 修改 | `AGENTS.md`、`docs/AGENT_WORKFLOW.md`、`docs/STATUS.md`、`docs/decisions/README.md`、`docs/tasks/README.md`、`README.md` |
| 只读参考 | — |

## 验收标准（可执行）

- [x] AGENTS.md 新增 §1.11「决策表格与实施门禁」，§2/§4/§8 与新规则一致
- [x] AGENT_WORKFLOW.md 新增 §3.4，任务生命周期改为"决策表格 → 逐项决策 → 说实现 → 实施"
- [x] `docs/STATUS.md` 建立「决策队列」（Q-XXXX），并声明实施门禁
- [x] 决策/任务记录与索引、README 入口同步
- [x] 文档链接检查通过；无密钥泄漏；无代码改动
- [x] git 提交完成

## 实施步骤

1. 将用户原话与意图整理为 D-0002（6 条规则）。
2. 更新 AGENTS.md：新增 §1.11；§2 提问方式、§4 工作流、§8 报告格式同步。
3. 更新 AGENT_WORKFLOW.md：会话检查清单、任务生命周期、§3.4 决策表格规范。
4. 更新 STATUS：把原「待用户决策」改为「决策队列」（Q-0001…），标注实施门禁。
5. 补索引与 README 入口；跑链接检查；提交。

## 自动决策（Agent 自主决定）

| 决策 | 理由 | 影响 |
|---|---|---|
| 决策队列使用 `Q-XXXX` 编号并保存在 STATUS | 与任务/决策 ID 区分，便于逐项勾选 | 用户可用 `Q-0001=A` 形式回复 |
| 决策表格要求每轮保留最新版 | 用户明确要求"每次都在会话内列好表格" | 回复会稍长，但可检查 |
| 只在文档层面落地本次规则，不动代码 | 遵守"未说实现不实施" | 功能开发继续等待授权 |

## 验证记录

```text
$ python scripts/check_md_links.py → md_files=16 relative_links_checked=46 missing=0
$ git diff --stat → 仅 AGENTS.md / README.md / docs/（无代码改动）
$ 密钥扫描 → 0 命中
```

## 文件清单

| 操作 | 文件 | 说明 |
|---|---|---|
| 修改 | `AGENTS.md` | §1.11 决策表格与实施门禁；§2/§4/§8 同步 |
| 修改 | `docs/AGENT_WORKFLOW.md` | 会话检查清单、任务生命周期、§3.4 决策表格规范 |
| 修改 | `docs/STATUS.md` | 决策队列（Q-0001…）与实施门禁声明 |
| 新增 | `docs/decisions/D-0002-decision-gate.md` | 用户决策记录 |
| 修改 | `docs/decisions/README.md`、`docs/tasks/README.md` | 索引 |
| 新增 | `docs/tasks/T-0003-decision-gate.md` | 本任务记录 |
| 修改 | `README.md` | 协作契约入口补充决策门禁说明 |

## 遗留问题与下一步

- 等待用户对 Q-0001…Q-0005 逐项决策；决策完成后，等待用户明确说"实现"再启动 P1（届时新建 T-0004）。

## 范围外发现（等待用户决策）

- 无新增（本次仅为流程规则落地）。
