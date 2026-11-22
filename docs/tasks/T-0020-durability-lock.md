# T-0020 · 批次 7：跨进程单写者锁与 fsync 屏障

- **状态**：done
- **更新**：2026-11-22
- **关联决策**：D-0002（用户"继续"授权批次 7）、D-0007（补测试默认授权）、D-0008（文档同批更新）、Q-0002（零第三方依赖）
- **待办来源**：[BACKLOG](../BACKLOG.md) 批次 7 → B-09 / B-10

## 目标

把持久化的两个承诺补齐：**同一会话任何时刻只有一个写者**（跨进程），以及**每个 step/turn 边界的数据真的落盘**（掉电最多丢当前 step）。

## 范围（写作用域）

- `internal/session/`：`lock.go` / `lock_flock.go` / `lock_windows.go` / `lock_unsupported.go`（新增）、`persist_jsonl.go`、`session.go`、`lock_test.go`（新增）
- `internal/agent/loop.go`（flush 边界）+ `loop_envelope_test.go`（flush 用例）
- 文档：README / ARCHITECTURE / BACKLOG / STATUS / 本记录

## 非目标

- 不做 zstd / generation 选择 / 历史迁移链（B-11/B-12，需决策）。
- 不做锁超时、租约续期（OS 锁随进程退出自动释放，不需要）。
- 不做集群/网络文件系统语义（NFS 上的 flock 语义不在承诺内）。

## 设计

1. **sidecar 锁文件**：每个会话 `<id>.jsonl.lock`，`OpenWriter` 在打开数据文件之前先取排他锁。
   - POSIX：`syscall.Flock(LOCK_EX|LOCK_NB)`；
   - Windows：`LockFileEx`（`kernel32` 通过 `syscall.NewLazyDLL` 调用，**不引入 `x/sys`**，遵守 Q-0002）；
   - 其它平台：构建期保留占位实现，打开写句柄直接报"不支持"，**不允许静默降级**。
2. **锁先于修复**：`OpenWriter` 现在按"进程内占位 → OS 锁 → 尾部修复 → 推导 seq → 打开追加"的顺序执行，`Resume` 也改成"先取写者（含锁）→ 再读日志算闭合事件"，这样修复和续写都不会与另一个进程的写者竞速。
3. **崩溃自动解锁**：锁由内核在进程退出时释放，所以被杀掉的 `dsh` 不会留下"永远锁住"的会话（有用例：helper 进程 `os.Exit` 不 Close，父进程随后能取到锁）。
4. **fsync 边界**：`Store.Flush` 在 `step/end` 与 `turn/end` 之后调用（`closeStep` / `closeTurn` 内），即"每批 append 后 fsync"里的批 = 一个 step；错误向上传递，因为无法确认落盘就不该假装成功。
5. **可观测**：`Writer.SyncCount()` + `Store.SyncCount(id)` 让调用方/测试能断言检查点真的发生。

## 验证

| 验证 | 结果 |
|---|---|
| `TestCrossProcessLockRejectsSecondWriter` | 子进程持锁（真实 OS 锁）→ 父进程 `OpenWriter` 返回 `ErrSessionLocked`；子进程退出后父进程取锁成功 |
| `TestLockReleasedWhenProcessDies` | helper 持锁后 `os.Exit(0)`（不 Close）→ 父进程仍能取到锁 → 崩溃不会永久锁死会话 |
| `TestResumeFailsWhenAnotherProcessOwnsTheLog` | 另一进程持锁时 `Resume` 返回 `ErrSessionLocked`，不会去修别人的日志 |
| `TestFlushCountsSuccessfulSyncs` | append 后计数 0，`Flush` 后 1；`Flush(nil)` 报错 |
| `TestLoopFlushesAtStepBoundaries` | 一次带工具往返的 turn（2 个 step）→ 3 次 flush（2×step + 1×turn） |
| **CLI 端到端（真实进程）** | 另一个进程持有会话锁时，`dsh chat --session <id>` → exit 1，输出 `log is locked by another process`；锁文件 `<id>.jsonl.lock` 与日志同目录 |
| **真实 DeepSeek API** | 新会话一轮 exit 0（`FLUSH-OK`）；随后 `--session` 续接 exit 0（`AGAIN`）；日志 18 个事件、`seq` 0…17 连续 |
| 跨平台编译 | `GOOS=linux go build ./...` 通过（flock 版本），Windows 用 `LockFileEx` 版本本机实测 |
| 质量门禁 | `gofmt` clean、`go vet` 0、`staticcheck` 0、`go test ./...` 全绿（**159 个用例**）、`scripts/build.ps1` 通过 |
| **未验证** | Linux 的 flock 路径只有 CI 的 `go test`（含 `-race`）覆盖，本机无 Linux；NFS 语义未验证 |

## 自动决策

- **锁文件而不是锁数据文件**：sidecar 不和追加写/读取的文件句柄语义纠缠，也方便"只锁不写"的探测；文件名 `<id>.jsonl.lock` 与上游 `session.lock` 同义。
- **锁文件不删除**：解锁后删除会与"另一个进程刚打开同名文件"竞态；留一个空文件无害（`List` 只认 `*.jsonl`）。
- **Windows 用 `syscall.NewLazyDLL` + `unsafe`**：`LockFileEx` 不在标准库，`golang.org/x/sys` 是第三方依赖；这与"零依赖"（Q-0002）冲突，因此手写 proc 调用，并把 `OVERLAPPED` 生命周期用 `runtime.KeepAlive` 固定。
- **不支持的平台直接拒绝写**：宁可让 `OpenWriter` 报错，也不在无锁状态下冒两个写者交错的风险。
- **flush 粒度 = step**：逐事件 fsync 会让一个 turn 产生十几次磁盘同步；step 边界是"模型请求 + 其工具"的完整批次，掉电最多丢进行中的 step。
- **flush 失败即返回错误**：不能确认落盘时继续运行会让调用方误以为成功。
- **`SyncCount` 是正式可观测项**，不是测试专用后门：与 metrics 同类，测试只是第一个消费者。

## 遗留与下一步

- **Linux flock 路径缺本机验证**：只有 CI（ubuntu 的 `go test` / `-race`）覆盖。
- **锁文件会累积**：每个会话一个空 `.lock` 文件；若将来做 session 清理，需要一起删。
- **掉电窗口**：step 内的 append 未 fsync，掉电可能丢掉当前 step 的部分事件；下一次写入前会做尾部修复，语义上"丢最后一批"符合上游承诺，但未做真实掉电测试（无法在本机安全模拟）。
- **Windows proc 调用是"手写绑定"**：若 Go 修改 `syscall.Overlapped` 布局需复核；已用 `runtime.KeepAlive` 固定生命周期。
- 下一批：**批次 8**（B-27 LLM 错误分类 + B-28 `.env`/`DSH_PERMISSION_MODE` + B-29 日志文件与启动诊断 + B-30 内核分发模式），见 [BACKLOG](../BACKLOG.md)。
