#!/usr/bin/env node
// Browser end-to-end test for the Web UI streaming path.
//
// It starts a mock streaming model, boots `dsh web` against it, drives a real
// headless Chrome over the DevTools protocol and asserts that the answer is
// rendered incrementally into the chat bubble. No npm dependency is used:
// Node's global WebSocket talks to Chrome directly.
//
// Usage:
//   node scripts/webui-e2e.mjs --dsh ./bin/dsh.exe --chrome <path-to-chrome>
//
// Exits 0 on success, 1 on any assertion or startup failure.

import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';

function parseArgs(argv) {
  const args = { webPort: 18300, cdpPort: 18301, timeout: 30000 };
  for (let i = 2; i < argv.length; i += 2) {
    const key = argv[i].replace(/^--/, '');
    const value = argv[i + 1];
    if (key === 'dsh') args.dsh = value;
    else if (key === 'chrome') args.chrome = value;
    else if (key === 'web-port') args.webPort = Number(value);
    else if (key === 'cdp-port') args.cdpPort = Number(value);
    else if (key === 'timeout') args.timeout = Number(value);
    else throw new Error(`unknown argument: ${argv[i]}`);
  }
  if (!args.dsh) throw new Error('--dsh <path> is required');
  if (!args.chrome) throw new Error('--chrome <path> is required');
  return args;
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(description, probe, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const value = await probe();
      if (value) return value;
    } catch (error) {
      lastError = error;
    }
    await sleep(50);
  }
  throw new Error(`timed out waiting for ${description}${lastError ? `: ${lastError.message}` : ''}`);
}

async function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

// startMockModel serves an OpenAI-compatible streaming completion whose chunks
// are paced so an incremental renderer can be observed between them.
async function startMockModel() {
  const chunks = ['web', '-', 'stream'];
  const server = http.createServer((request, response) => {
    if (request.method !== 'POST') {
      response.writeHead(404).end();
      return;
    }
    request.resume();
    response.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
    });
    let index = 0;
    const timer = setInterval(() => {
      if (index < chunks.length) {
        const frame = { choices: [{ delta: { content: chunks[index] }, finish_reason: null }] };
        response.write(`data: ${JSON.stringify(frame)}\n\n`);
        index += 1;
        return;
      }
      clearInterval(timer);
      response.write(`data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: 'stop' }] })}\n\n`);
      response.write('data: [DONE]\n\n');
      response.end();
    }, 200);
    // IncomingMessage 'close' fires when the request body is consumed, so the
    // handler must watch the response instead; otherwise the timer dies before
    // the first chunk and the client waits forever.
    response.on('close', () => clearInterval(timer));
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  return { server, port: server.address().port };
}

class DevTools {
  constructor(socket) {
    this.socket = socket;
    this.nextId = 1;
    this.pending = new Map();
    this.listeners = new Map();
    this.events = [];
    socket.addEventListener('message', (event) => {
      const message = JSON.parse(event.data);
      if (message.id && this.pending.has(message.id)) {
        const { resolve, reject } = this.pending.get(message.id);
        this.pending.delete(message.id);
        if (message.error) reject(new Error(message.error.message));
        else resolve(message.result);
        return;
      }
      if (message.method) {
        this.events.push(message);
        for (const handler of this.listeners.get(message.method) ?? []) {
          handler(message.params);
        }
      }
    });
  }

  static async connect(url) {
    const socket = new WebSocket(url);
    await new Promise((resolve, reject) => {
      socket.addEventListener('open', resolve, { once: true });
      socket.addEventListener('error', () => reject(new Error('devtools websocket failed')), { once: true });
    });
    return new DevTools(socket);
  }

  on(method, handler) {
    const handlers = this.listeners.get(method) ?? [];
    handlers.push(handler);
    this.listeners.set(method, handlers);
  }

  send(method, params = {}) {
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.socket.send(JSON.stringify({ id, method, params }));
    });
  }

  async evaluate(expression) {
    const result = await this.send('Runtime.evaluate', {
      expression,
      returnByValue: true,
      awaitPromise: true,
    });
    if (result.exceptionDetails) {
      throw new Error(`page evaluation failed: ${result.exceptionDetails.text}`);
    }
    return result.result.value;
  }

  close() {
    this.socket.close();
  }
}

async function main() {
  const args = parseArgs(process.argv);
  const workDir = await mkdtemp(path.join(tmpdir(), 'dsh-webui-'));
  const mock = await startMockModel();
  const children = [];
  let devtools;

  const cleanup = async () => {
    try {
      devtools?.close();
    } catch {}
    for (const child of children.reverse()) {
      try {
        child.kill();
      } catch {}
    }
    await sleep(500);
    await new Promise((resolve) => mock.server.close(resolve));
    for (let attempt = 0; attempt < 5; attempt++) {
      try {
        await rm(workDir, { recursive: true, force: true });
        break;
      } catch {
        await sleep(300);
      }
    }
  };

  try {
    const dsh = spawn(args.dsh, ['web', '--host', '127.0.0.1', '--port', String(args.webPort), '--base-url', `http://127.0.0.1:${mock.port}`], {
      env: { ...process.env, DEEPSEEK_API_KEY: 'test-key', DSH_SESSION_DIR: path.join(workDir, 'sessions') },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    children.push(dsh);
    let dshLog = '';
    dsh.stdout.on('data', (data) => { dshLog += data.toString(); });
    dsh.stderr.on('data', (data) => { dshLog += data.toString(); });

    const baseURL = `http://127.0.0.1:${args.webPort}`;
    await waitFor('dsh web health', async () => {
      const response = await fetch(`${baseURL}/healthz`);
      return response.ok;
    }, args.timeout);

    const chrome = spawn(args.chrome, [
      '--headless=new',
      '--disable-gpu',
      '--no-first-run',
      '--no-default-browser-check',
      '--no-sandbox',
      '--remote-allow-origins=*',
      `--remote-debugging-port=${args.cdpPort}`,
      `--user-data-dir=${path.join(workDir, 'chrome')}`,
      'about:blank',
    ], { stdio: ['ignore', 'ignore', 'pipe'] });
    children.push(chrome);

    await waitFor('chrome devtools endpoint', async () => {
      const response = await fetch(`http://127.0.0.1:${args.cdpPort}/json/version`);
      return response.ok;
    }, args.timeout);

    const targetResponse = await fetch(`http://127.0.0.1:${args.cdpPort}/json/new?about:blank`, { method: 'PUT' });
    const target = await targetResponse.json();
    devtools = await DevTools.connect(target.webSocketDebuggerUrl);
    const browserProblems = [];
    devtools.on('Runtime.exceptionThrown', (params) => {
      browserProblems.push(`exception: ${params.exceptionDetails?.text ?? 'unknown'}`);
    });
    devtools.on('Runtime.consoleAPICalled', (params) => {
      if (params.type === 'error' || params.type === 'warning') {
        browserProblems.push(`console.${params.type}: ${(params.args ?? []).map((arg) => arg.value ?? arg.description ?? '').join(' ')}`);
      }
    });
    await devtools.send('Page.enable');
    await devtools.send('Runtime.enable');
    await devtools.send('Network.enable');
    const network = [];
    devtools.on('Network.requestWillBeSent', (params) => {
      if (params.request?.url?.includes('/api/chat')) {
        network.push(`request accept=${params.request.headers?.Accept ?? '(none)'}`);
      }
    });
    devtools.on('Network.responseReceived', (params) => {
      if (params.response?.url?.includes('/api/chat')) {
        network.push(`response ${params.response.status} ${params.response.mimeType}`);
      }
    });
    devtools.on('Network.loadingFailed', (params) => {
      network.push(`failed ${params.errorText}`);
    });
    await devtools.send('Page.navigate', { url: `${baseURL}/` });
    await waitFor('page ready', async () => (await devtools.evaluate('document.readyState')) === 'complete', args.timeout);

    await devtools.evaluate(`
      (() => {
        const input = document.querySelector('#input');
        input.value = 'stream via browser';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        document.querySelector('#composer').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
        return true;
      })()
    `);

    const finalText = 'web-stream';
    const samples = [];
    let errorBubble = false;
    try {
      await waitFor('streamed answer', async () => {
        const state = await devtools.evaluate(`
          (() => {
            const bubbles = [...document.querySelectorAll('.bubble')];
            const last = bubbles[bubbles.length - 1];
            return {
              text: last?.querySelector('.body')?.textContent ?? '',
              error: bubbles.some((bubble) => bubble.classList.contains('error')),
            };
          })()
        `);
        if (state.error) errorBubble = true;
        if (state.text !== '' && state.text !== '…' && samples[samples.length - 1] !== state.text) {
          samples.push(state.text);
        }
        return state.text === finalText;
      }, args.timeout);
    } catch (error) {
      const dump = await devtools.evaluate(`document.querySelector('#log')?.innerText ?? '(no log)'`).catch(() => '(unavailable)');
      throw new Error(`${error.message}\npage log:\n${dump}\nbrowser problems: ${browserProblems.join(' | ') || '(none)'}\nnetwork: ${network.join(' | ') || '(none)'}\nserver log:\n${dshLog.slice(-2000) || '(empty)'}`);
    }

    if (errorBubble) throw new Error('an error bubble appeared in the chat log');
    const incremental = samples.some((sample) => sample.length > 0 && sample.length < finalText.length && finalText.startsWith(sample));
    if (!incremental) {
      throw new Error(`the answer was not rendered incrementally; samples=${JSON.stringify(samples)}`);
    }

    console.log(JSON.stringify({ ok: true, finalText, samples, checks: ['incremental-render', 'no-error-bubble'] }));
  } catch (error) {
    console.error(`webui-e2e failed: ${error.message}`);
    process.exitCode = 1;
  } finally {
    await cleanup();
  }
}

await main();
