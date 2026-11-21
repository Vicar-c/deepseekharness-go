# testdata

## upstream-v4-turn-spacing.jsonl

- **来源**：`deepseek-harness` 上游仓库（MIT），commit `39163dd30f`
- **原路径**：`packages/experimental/webworker-runtime/tests/fixtures/vfs-example/home/sessions/--dsh-workspace--/preview-turn-spacing/session.v4.jsonl`
- **内容**：44 行 = 1 行 header + 43 个事件（`seq` 0…42 连续递增，`surfaceOp` 均为 `append`）
- **用途**：`internal/session` 的 v4 编解码测试夹具，验证"能读真实上游字节"。

注意：上游 `snapshots/**/session.v4.jsonl` 是**归一化的期望输出**（不含 `seq`/`time`，id 为 `{{session:1}}` 模板），不能用作物理格式样本；本文件取自未归一化的 fixture 目录。
