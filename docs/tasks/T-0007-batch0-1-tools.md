# T-0007 · 批次 0+1：行尾规范与内置工具补齐

- **状态**：done
- **更新**：2026-11-21
- **关联决策**：D-0006（提交边界）、D-0002（实施门禁，用户已说"开工 0->1"）

## 目标

按 P1 范围补齐 `builtin` 工具面，并把仓库行尾规范化，为后续会话 v4（批次 2）腾出干净基线。

## 范围

- **包含**：`.gitattributes`；`internal/builtin` 拆分与 6 个新工具（`edit`、`glob`、`grep`、`read_image`、`todo_write`、`working_directory`）；跨插件共享的 `Workspace`；相应测试。
- **不包含**：会话格式 v4、流式、profiles、沙箱策略、P2 能力。

## 步骤

1. **批次 0**：新增 `.gitattributes`（`* text=auto eol=lf` + 二进制声明）。先核对 `git ls-files --eol`：索引已是 LF，因此本次只改策略，不产生内容 diff。
2. **批次 1**：抽出 `Workspace`（沙箱根 + 可切换工作目录 + 统一的 `Resolve`/`SetCwd`）；`builtin.go` 只保留插件与注册；按职责拆为 `fs_tools.go`、`search_tools.go`、`image_tools.go`、`todo_tools.go`、`shell_tools.go`。
3. 实现 6 个工具；`cmd/dsh` 注入共享 `Workspace` 与 `TodoPlugin`；`dsh tools` 列宽改为动态。
4. 验证并提交。

## 验证

| 命令 / 场景 | 结果 |
|---|---|
| `gofmt -l .` | 无输出 |
| `go vet ./...` | exit 0 |
| `go test ./... -count=1` | 8 个包全部 ok |
| `go build ./cmd/dsh` | exit 0 |
| `dsh tools` | **11 tools**、`plugins=4`（fs/shell/time/todo） |
| `edit` 单测 | 唯一替换成功；缺失字符串报错；重复出现要求 `replace_all`；`replace_all` 生效 |
| `working_directory` 单测 | 切换后相对路径生效；`../` 越界被拒；切到工作区外被拒 |
| `glob` 单测 | `**/*.go` 跨目录命中；`*.txt` 裸名匹配到嵌套文件；不返回不匹配文件 |
| `grep` 单测 | 大小写敏感/不敏感分别正确；无匹配返回 `no matches` |
| `read_image` 单测 | 3x2 PNG 返回尺寸与 data URI；文本文件被拒 |
| `todo_write` 单测 | 渲染 `[x]/[ ]/[~]`；清空；非法状态被拒 |
| `staticcheck ./...` | 0 告警 |
| `scripts/build.ps1` | 构建 `bin/dsh.exe`（7.9 MB），`dsh version` 正确注入 commit |
| 工具目录完整性 | `TestEveryRegisteredToolIsWellFormed`：11 个工具、名称齐全、`Parameters` 均为合法 JSON、无重复注册 |
| `working_directory` 对 shell 生效 | `TestShellToolFollowsWorkingDirectory`：切换到 `sub` 后 `pwd` / `Get-Location` 输出含 `sub` |
| 边界用例 | `edit` 保留 0600 权限；`glob` 的 `path`/`limit`/跳过 `.git`；`grep` 的 `glob` 过滤、二进制跳过、`max_matches`；`read_image` 大图跳过内联、>8 MiB 拒绝 |
| 流水线行为 | `TestBuiltinToolsBehaveThroughThePipeline`：输出被截断、缺文件与未知工具都转为 `IsError` 结果而非中断 |
| 测试规模 | `internal/builtin` 17 个用例；全仓库 61 个用例 |

## 自动决策

- **共享 `Workspace` 而非各自持有 root**：`working_directory` 必须同时影响文件工具与 shell，否则语义半残；因此 `NewFsPlugin`/`NewShellPlugin` 改为接收 `*Workspace`（内部 API 变更，仅本仓库使用）。
- **`edit` 要求唯一匹配**：多处命中时直接报错并提示加长上下文或显式 `replace_all`，避免"改了不该改的那一处"。
- **`read_image` 超过 64 KiB 不内联 base64**：工具流水线默认截断 128 KiB，直接塞大 base64 会被截成坏 payload；改为只返回元数据并说明。
- **搜索类工具上限**：`glob` 默认 500（上限 2000）、`grep` 默认 100（上限 1000），并跳过 `.git`/`node_modules`/`bin`/`dist`/`vendor` 等目录、跳过二进制与 >2 MiB 文件。
- **`dsh tools` 动态列宽**：新工具名（`working_directory`）超过原 `%-12s`，会破坏对齐。
- **`main.go` 的提交边界处理**：该文件同时含用户的 3 行中文注释，按 D-0006 不能整文件 `git add`。做法是：把"工作区内容减去用户注释行"写成临时文件，用 `git hash-object -w` + `git update-index --cacheinfo` 只把过滤后的 blob 放进索引，工作区保持原样。提交后 `git status` 仅剩用户的注释改动。

## 遗留与下一步

- **工具仍无沙箱**：`pwsh`/`bash` 以当前用户权限运行（行为等价 `danger-full-access`），三模式沙箱属 P2。
- **`todo_write` 状态在进程内**：重启即丢失；P1 可接受，P3 接 session 持久化时再迁移。
- **`read_image` 不做缩放/压缩**：只做尺寸报告与内联上限控制。
- **下一批**：批次 2（会话格式 v4），需用户确认开工。
