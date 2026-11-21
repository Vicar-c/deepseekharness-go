# T-0013 · 浏览器端 E2E：Web UI 流式渲染

- **状态**：done
- **更新**：2026-11-21
- **关联决策**：D-0002（用户"JS 渲染也需要，测试应该尽可能完整"）、D-0007

## 目标

补上 T-0012 里唯一没有自动化覆盖的部分：**浏览器里的逐字渲染**。用真实 headless Chrome 驱动真实 `dsh web` 进程，断言答案确实是增量渲染出来的，而不是一次性刷出。

## 方案（零 npm 依赖）

- **CDP 直连**：Node 24 自带全局 `WebSocket`，直接连 Chrome DevTools Protocol，不需要 Playwright/Puppeteer，也不需要 `npm install`。
- **三段式编排**（`scripts/webui-e2e.mjs`）：
  1. Node 内置 `http` 起一个 OpenAI 兼容的**流式 mock 模型**，每 200ms 发一个 delta（`web` / `-` / `stream`）；
  2. 启动 `dsh web --base-url <mock>`，等 `/healthz`；
  3. 启动 `chrome --headless=new --remote-debugging-port`，用 CDP 打开页面、填入消息、触发提交，然后高频采样最后一个气泡的 `.body` 文本。
- **判定条件**：
  - 最终文本 === `web-stream`；
  - 采样中出现过真前缀（如 `web`、`web-`），证明**增量渲染**而不是一次性替换；
  - 没有 `.bubble.error`；
  - 顺带捕获 `Runtime.exceptionThrown` / console error / `Network.*` 事件，失败时输出页面 DOM 与服务端日志。

## 验证

| 场景 | 结果 |
|---|---|
| 本机真实 Chrome + 真实 `dsh web` + 流式 mock | `{"ok":true,"finalText":"web-stream","samples":["web","web-","web-stream"],"checks":["incremental-render","no-error-bubble"]}`，退出码 0 |
| CI（新增 `webui` job） | ubuntu 与 windows 各跑一次（使用 runner 预装的 Chrome） |

### 调试过程中修掉的两个真问题

1. **mock 自身 bug**：`request.on('close')` 在请求体读完时就会触发（Node 的 `IncomingMessage` 语义），把发送定时器清掉，导致客户端永远等不到响应。改成监听 `response.on('close')`。
2. **清理竞态**：Chrome 退出时仍在写 `user-data-dir`，`rm -rf` 报 `EBUSY`；改为先 kill、等待、带重试清理。

> 这两个都不是产品缺陷，但第一个如果不修，测试会一直"看起来产品坏了"。

## 自动决策

- **不引入 Playwright**：一个 `node` 脚本 + 系统 Chrome 就够，避免 npm 依赖与浏览器下载（也符合仓库"尽量少依赖"的基调）。
- **采样间隔 50ms、mock 帧间隔 200ms**：既能稳定捕捉中间态，又不会让测试变慢。
- **CI 双平台**：Windows 与 ubuntu 的 Chrome 路径写进矩阵；runner 预装 Chrome，无需额外安装步骤。
- **失败时输出页面 DOM + 服务端日志 + network 事件**：否则 CI 上只看到 timeout，无法定位。

## 遗留

- 移动端/深色模式等视觉断言不在范围（只验证功能与流式行为）。
- 未覆盖断线重连后的 UI 状态。
