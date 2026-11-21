# T-0017 · 文档随实现同步：README 重写与文档契约测试

- **状态**：done
- **更新**：2026-11-21
- **关联决策**：[D-0008](../decisions/D-0008-docs-in-sync.md)（用户要求文档随进度并行更新）、[T-0016](T-0016-gap-audit.md)（审计发现 README 滞后）

## 目标

把"文档滞后"从一次性修补变成**流程约束**：README 与实现对齐，同时让 CLI/工具/配置三块的关键事实由测试强制，避免再次漂移。

## 背景

用户指出：

> 像是文档相关这种应该要根据我们实现的进度并行更新才对，不能一直不更新

审计证据：README 仍描述 `meta` + `message` 两行型 JSONL、5 个工具（实际 11 个）、`internal/builtin` 只有 fs/shell/time（实际 4 个插件）、"尚未迁移 edit/glob/grep/todo"（批次 0+1 已实现），且完全没提 v4、流式、profiles、工具批次并发。

根因不是"忘记"，而是 AGENTS.md 只要求会话结束前更新 STATUS 与任务记录，对面向使用者的 README / 架构 / 迁移映射没有绑定。

## 做了什么

1. **重写 README**：状态（P1 主体完成 + 剩余缺口链接）、特性表、快速开始、CLI 表（带 `<!-- cli:start/end -->` 标记）、11 个内置工具表（带 `<!-- tools:start/end -->`）、配置示例（含 `maxParallelToolCalls`）、profiles 与 patch 示例、会话格式摘要、质量门禁、安全边界（补 Web 无鉴权）、目录结构、路线图摘要。
2. **文档契约测试** `cmd/dsh/docs_test.go`：
   - `TestReadmeCommandListMatchesTheCLI`：README 命令清单 = `main.go` 子命令 switch = usage 文本（三方一致）；
   - `TestReadmeToolTableMatchesTheRegistry`：README 工具表 = 实际注册的工具（`pwsh`/`bash` 视为同一工具的双拼写）；
   - `TestReadmeConfigExampleMatchesTheSettings`：配置示例键集合 = `config.Fields()`，且示例能被 `config.Config` 反序列化（`DisallowUnknownFields`）。
3. **AGENTS.md §1.14**：文档随实现同步的规则 + 「改动 ↔ 必须更新的文档」对照表；§6 DoD 增加一条。
4. **D-0008 决策记录**：背景/选项/决定/影响/回滚。
5. **CI 增加 Markdown 相对链接校验**（Linux，`python3 scripts/check_md_links.py`）。

## 验证

| 验证 | 结果 |
|---|---|
| `go test ./cmd/dsh -count=1` | 3 个用例通过（此前 `cmd/dsh` 覆盖率 0%） |
| **负向验证：删掉 README 的 `read` 行** | `TestReadmeToolTableMatchesTheRegistry` 失败：`tool "read" is registered but missing from the README tool table` |
| **负向验证：从配置示例删掉 `maxParallelToolCalls`** | `TestReadmeConfigExampleMatchesTheSettings` 失败并打印两边键集合 |
| 恢复 README 后 | 3 个用例恢复通过 |
| README 内容核对 | 11 个工具名与 `dsh tools` 实跑输出一致；6 个命令与 `dsh help` 一致；配置字段与 `config.Fields()` 一致 |
| `python scripts/check_md_links.py` | 38 篇 md、173 条相对链接、0 缺失 |
| 整体门禁 | `gofmt` clean、`go vet` 0、`staticcheck` 0、`go test ./...` 全绿 |

## 自动决策

- **用标记块而不是整篇解析**：`<!-- cli:start -->` / `<!-- tools:start -->` 让测试只解析明确声明的区域，README 其它部分可以自由排版。
- **三方比对命令清单**：只比 README 与 usage 会漏掉"usage 写了但 switch 没分发"的情况，所以加上了 `main.go` 的 switch 解析。
- **配置示例用 `DisallowUnknownFields` 反序列化**：既验证键集合，又验证示例本身是合法配置。
- **双拼写工具按"一个工具两行名"处理**：`pwsh`/`bash` 是同一工具在不同平台的名字，测试显式白名单，避免 Linux CI 误报。

## 遗留与下一步

- **架构/迁移映射没有自动检查**：目前靠 §1.14 的对照表与批次收尾清单约束，属于人工责任。
- CI 的链接校验只在 Linux 跑（Windows runner 无 `python3` 命令名差异），本地仍可手动跑。
- README 里的"特性"与"路线图"是叙述性内容，仍可能与实现漂移，下一次大改动时人工复核。
