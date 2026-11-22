# D-0009 · 批次 8 的范围修正：失败分类、`.env`、诊断与分发模式

- **状态**：生效（实现已落地）
- **日期**：2026-11-22
- **类型**：实现类决策（范围修正 + 跨实现兼容性）
- **关联**：[BACKLOG](../BACKLOG.md) B-27…B-30；[T-0021](../tasks/T-0021-operability-batch.md)；[ARCHITECTURE](../ARCHITECTURE.md) §10；[MIGRATION_MAP](../MIGRATION_MAP.md) §6-8/§6-9/§6-12/§6-18

## 背景

批次 8 的四项来自差距审计（T-0016）按 MIGRATION_MAP §6 归纳的"可运维性"缺口。真正开始实现前，逐条回读了安装版上游源码（`app.asar` 内的 `packages/`），发现三处与审计时的理解不一致，必须在写代码之前定下来，否则会把错误的东西固化进日志格式与配置层：

1. **`DSH_PERMISSION_MODE` 这个环境变量在上游不存在。** 它只出现在上游一份升级指引的**用户 patch 示例**里（`mode: !!js process.env.DSH_PERMISSION_MODE ?? 'workspace-write'`），是用户自己写的约定；上游真正的权限模式是 provider/headless 的 `permissionMode` 配置 + `permission-presets` 服务 + 沙箱服务，本实现都还没有（P2）。
2. **`turn/end` 的原因集合与本地实现已经漂移。** 上游 `TurnEndReasonMap` 是 `completed` / `aborted`（带 `reason:{kind}`）/ `blocked` / `error`（带 `error:{message,code}`）/ `max-tokens` / `interrupted` / `forked`；本地写的是 `cancelled`，且 `error` 不带错误码。
3. **重试的语义细节决定日志形态。** 上游把 `llm/retry`（含 `retryId`/`policyKey`/`delayMs`/`failure`）作为**等待前**的持久记录，等待成功后再写 `llm/retry-started`；默认策略是 5 次、500ms 起、10s 上限、±10% 抖动，可重试码只有 `EMPTY_RESPONSE`/`RATE_LIMIT`/`SERVER`/`TIMEOUT`/`TRANSPORT`。

## 选项与决定

### D-0009-01 `.env` 与权限模式

| 选项 | 含义 | 代价 |
|---|---|---|
| A（选定） | 只实现上游真实存在的两层 `.env`；权限模式留给 P2 的沙箱/审批一起做，并在文档里写明为什么没有这个环境变量 | B-28 的规模降为 S；不产生"看起来能配、其实不生效"的设置 |
| B | 自造 `DSH_PERMISSION_MODE`，读取后只落盘不生效 | 配置项会骗人；与"重写上游语义"的目标相反 |
| C | 完全不碰 `.env` | 放弃一个已在用的上游能力 |

**决定 A。** `.env` 按上游 `loadLayeredEnv` 语义实现：项目层（cwd）与数据层（配置目录），优先级 `已导出环境 > 项目层 > 数据层`，两层都先校验再统一应用；**引导变量**（`PATH`、`DSH_*`、`XDG_*`、`DYLD_*`、代理/TLS 信任锚、解释器与 VCS 钩子）只允许由启动环境设置，数据层额外允许代理名与精确拼写的 `DEEPSEEK_BASE_URL`（项目层随仓库传播，因此不行）。

### D-0009-02 `turn/end` 兼容性修正

| 选项 | 含义 | 代价 |
|---|---|---|
| A（选定） | 改成本地的 `aborted`（+`reason:{kind:"user"}`）、`error`（+`error:{message,code}`），并补 `max-tokens` | 旧日志里已有的 `cancelled` 仍可读（读取端只校验类型名，不校验载荷字段），不需要迁移 |
| B | 保持 `cancelled`，错误不带码 | 两套实现的日志语义不同；读者无法区分取消与失败原因 |

**决定 A。** 顺带把 `blocked`/`forked` 作为常量登记但**不发射**：本实现没有输入队列与 fork seed，写了就是死代码。

### D-0009-03 已输出分片的失败不重试

| 选项 | 含义 | 代价 |
|---|---|---|
| A（选定） | 一次尝试只要向用户输出过分片，失败即结束本轮 | 极少数"流到一半断线"的场景少一次恢复机会 |
| B | 无条件重试 | 终端/Web 会看到重复文本；总线无法撤回已发出的 `agent/stream` |

**决定 A。** 记 `turn/end{error}` 并返回错误，比让用户看到两遍同样的半句话更好。

### D-0009-04 失败码的实现范围

| 选项 | 含义 | 代价 |
|---|---|---|
| A（选定） | 只实现本实现能真正产生的码（`AUTH`/`RATE_LIMIT`/`QUOTA`/`CONTEXT_WINDOW_EXCEEDED`/`INVALID_REQUEST`/`SERVER`/`TIMEOUT`/`TRANSPORT`/`ABORTED`/`EMPTY_RESPONSE`/`MISSING_CREDENTIAL`/`UNKNOWN`/`HTTP_<n>`） | 与上游 49 个码相比是子集 |
| B | 全量复制上游码表 | 大量不可达常量，容易让人以为功能已实现 |

**决定 A。** 分类**规则**按上游 `transport.ts` 的顺序与措辞正则逐条移植（含"只有明确指向模型上下文才算溢出"的判别），所以将来补新码只是加常量，不用改规则。

## 理由

- 重写项目的价值在"语义一致"，不在"文件齐全"：宁可少一个假配置项，也不要让 `--dump-config` 里的设置不生效。
- `turn/end` 与 `llm/retry` 是**跨实现可读**的持久契约，字段名/码值错了以后要写迁移；趁现在只有本实现的数据，修正成本最低。
- 重试的默认值与可重试集合直接决定用户等待时间与费用，必须与上游默认一致，不能凭感觉。

## 影响

- 缺失 `DSH_PERMISSION_MODE` 这一项从 P1 待办里**移除**（不是推迟到 P2，而是确认它从来不属于上游）；BACKLOG 的 B-28 描述同步改写。
- `turn/end` 的读取端（本仓库的 `session.TurnEndReason`、未来的 `dsh sessions`、Web UI）现在能拿到失败码；`cancelled` 字面量在本仓库不再产生。
- `.env` 的引导变量拒绝逻辑有 40+ 用例，且 CLI 端到端验证过（把真实 key 放进项目 `.env`、清空进程环境变量后仍能对话）。

## 回滚方式

- 回滚 `.env`：删除 `internal/config/env.go` 与 `applyEnvFiles` 调用；配置读取回到只看进程环境。
- 回滚 `turn/end` 语义：改回 `cancelled` 并去掉 `error` 字段——但**已有日志会失去失败码**，不建议。
- 回滚重试：`config.maxRetries = 0` 即可关闭（不需要改代码）。
- 本记录只追加不覆盖；结论变化时新增条目并注明取代关系。
