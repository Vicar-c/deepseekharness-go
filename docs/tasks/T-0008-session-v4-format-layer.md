# T-0008 · 批次 2 第一步：v4 会话格式的类型层与编解码

- **状态**：done
- **更新**：2026-11-21
- **关联决策**：D-0002（门禁：用户已说"开始吧"）、D-0007（补测试默认授权）

## 目标

把上游 Session 格式 v4 的**物理格式**用 Go 精确建模，做到"读得进真实上游文件、写得回可再读的字节"，为后续替换 `internal/session` 的持久化（批次 2 后续步骤）打地基。

本步**不改**现有 `Store`/`Entry` 的行为，新旧实现并存，可随时回滚。

## 上游依据（已核实）

| 来源 | 结论 |
|---|---|
| `docs/session-format-status.zh.md` | 工作区写入器版本 = **4**；定稿与发布记录均为 4 |
| `packages/core/session/src/types.ts` | `SessionHeader` 字段、`SessionEvent` 含 `type/seq/time/data`、`ignorable` 语义、`SurfaceOp` 只有 `append` 与 `{op:replace,startSeq,endSeq}` |
| `packages/core/session/src/known-event-types.ts` | 已知事件类型 62 个；**未知类型若无 `ignorable:true`，读取器必须拒绝重建** |
| 真实原始样本 `preview-turn-spacing/session.v4.jsonl`（44 行） | 首行 header `type:"session"`；事件行 `seq` 从 **0** 连续递增；`time` 为 epoch **毫秒**；16 个 `surfaceOp:"append"`；本样本无 `sourceEventSeqs` |

> 注意：`snapshots/**/session.v4.jsonl` 是被归一化的期望输出（json 里没有 `seq`/`time`、id 是 `{{session:1}}` 模板），**不能**直接当物理格式样本；真实样本取自 `packages/experimental/webworker-runtime/tests/fixtures/...`。

## 范围

- **包含**：`internal/session/format.go`（Header/Event/SurfaceOp/KnownEventTypes/Decode/Encode + 错误分类）、`format_test.go`、上游真实样本夹具（含出处说明）。
- **不包含**：投影（messages）、投影缓存、持久化写入器、`agent.Loop` 接入、`Store` 替换。

## 步骤

1. 从上游 clone 复制 44 行真实 v4 样本到 `internal/session/testdata/`，写 `README.md` 记出处与许可。
2. 建模：`Header`（10 个字段，`delegationDepth` 用指针保留"存在 vs 省略"）、`Event`（`surfaceOp` 自定义编解码，保留 `"append"` 与 replace 两种形态）、`SurfaceOp`。
3. 实现 `Decode`：首行必须是 header 且 `version==4`；`seq` 必须从 0 连续递增；未知类型按 `ignorable` 规则处理。
4. 实现 `Encode`：逐行 JSONL，关闭 HTML 转义以贴近 `JSON.stringify`。
5. 测试：真实样本、版本拒绝、未知类型（两种）、seq 断裂、坏行、往返。

## 验证

| 命令 / 用例 | 结果 |
|---|---|
| `TestDecodeUpstreamV4Fixture` | 真实上游 44 行样本解码成功：header 6 个字段逐一断言；43 个事件 `seq` 0…42；16 个 `surfaceOp:"append"`；事件类型全部在已知词汇内 |
| `TestEncodeRoundTrip` | 解码→编码→再解码：header 深比较一致；每个事件 `type/seq/time` 与 **`data` 原始字节**完全一致；输出 1+43 行 |
| `TestDecodeRejectsUnsupportedVersion` | version 3 → `ErrUnsupportedFormat` |
| `TestDecodeRejectsUnknownRequiredType` | 未知类型无 `ignorable` → `ErrUnknownEventType` |
| `TestDecodeKeepsIgnorableUnknownType` | 未知类型带 `ignorable:true` → 保留事件、不报错 |
| `TestDecodeRejectsSequenceGap` | `seq` 跳号 → `ErrMalformedLog`（含行号） |
| `TestDecodeRejectsMalformedLine` | 坏 JSON 行 → `ErrMalformedLog`（含行号） |
| `TestSurfaceOpReplaceRoundTrip` | `{"op":"replace","startSeq":1,"endSeq":2}` 解码/编码/再解码保持一致 |
| `gofmt` / `go vet` / `staticcheck` / `go test ./...` | clean / 0 / 0 / 8 包全绿（session 包 8 个新用例） |

## 自动决策

- **样本来源改用未归一化 fixture**：上游 `snapshots/**/session.v4.jsonl` 是期望输出（无 `seq`/`time`、id 模板化），不能当物理格式样本；实际样本取自 `packages/experimental/webworker-runtime/tests/fixtures/.../session.v4.jsonl`，并复制进 `internal/session/testdata/`（含出处与 commit）。
- **`delegationDepth` 用 `*int`**：上游 header 可省略该字段，指针能区分"存在且为 0"与"省略"，保证往返不改变字节语义。
- **`surfaceOp` 自定义编解码**：线上有 `"append"` 字符串与 `{"op":"replace",...}` 对象两种形态，`json.RawMessage` 会把两者混在一起，自定义类型能保留形态并在编码时还原。
- **强制 `seq` 从 0 连续**：上游写入器每个事件 +1；读入断裂即报错，比宽容跳过更安全（宁可拒读，也不要重建错误会话）。
- **未知类型按上游规则处理**：无 `ignorable` 拒绝，有则保留（供重写回原样），投影层再决定跳过。
- **Encode 关闭 HTML 转义**：贴近 `JSON.stringify` 的输出，减少与上游逐字节比对时的噪音。
- **本步只建模不接线**：不修改 `Store`/`Entry` 与 `agent.Loop`，新旧实现并存，可整体回滚。

## 遗留与下一步

- **投影层**：把事件流投影成 `[]llm.Message`（`user/message`、`assistant/message`、`tool/call`+`tool/result`、`system/message`），并处理 `surfaceOp` 的 replace 语义（P1 先支持 append）。
- **持久化写入器**：`persist_jsonl.go`（单写者锁、原子发布、崩溃尾部修复）替换现有 `Store` 的 `meta/message` 两行型。
- **接入 `agent.Loop`**：落盘从"消息"改为"事件"。
- 本步未改动任何生产行为，现有 63 个用例仍全绿。
