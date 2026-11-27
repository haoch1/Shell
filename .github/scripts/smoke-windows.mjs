// End-to-end Windows verification for Shell v2:
// 1. Launches the real compiled Shell.exe with an isolated profile and verifies Win32 window + WebView2 renderer startup & clean exit.
// 2. Renders the exact built frontend/dist bundle in headless Edge (1280x820 @2x) via CDP, asserts zero errors, exercises all views, and captures UI screenshots.
import assert from 'node:assert/strict';
import { execFile, spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const snapshotDir = path.resolve('build/preview');
await mkdir(snapshotDir, { recursive: true });

// =========================================================================
// Stage 1: Real Shell.exe native WebView2 startup & clean shutdown test
// =========================================================================
async function verifyNativeBinary() {
  const exePath = path.resolve('build/Shell-Windows-x64/Shell.exe');
  assert.ok(existsSync(exePath), 'Expected compiled Shell.exe at ' + exePath);

  const testProfile = await mkdtemp(path.join(process.env.RUNNER_TEMP || os.tmpdir(), 'shell-native-'));
  const client = spawn(exePath, [], {
    env: { ...process.env, LOCALAPPDATA: testProfile, APPDATA: testProfile, SHELL_EXIT_ON_CLOSE: '1' },
  });
  const exited = once(client, 'exit');

  try {
    let wvReady = false;
    const deadline = Date.now() + 25000;
    while (Date.now() < deadline) {
      if (client.exitCode !== null) {
        throw new Error('Shell.exe exited prematurely with code ' + client.exitCode);
      }
      const ps = `
        $p = Get-Process -Id ${client.pid} -ErrorAction SilentlyContinue
        $wv = @(Get-CimInstance Win32_Process -Filter "Name='msedgewebview2.exe'" -ErrorAction SilentlyContinue | Where-Object { $_.CommandLine -like '*${path.basename(testProfile)}*' -and $_.CommandLine -like '*--type=renderer*' })
        [PSCustomObject]@{ alive = ($null -ne $p); title = if ($p) { $p.MainWindowTitle } else { '' }; hwnd = if ($p) { [int64]$p.MainWindowHandle } else { 0 }; renderers = $wv.Count } | ConvertTo-Json -Compress
      `;
      const { stdout } = await execFileAsync('powershell.exe', ['-NoProfile', '-Command', ps]);
      const state = JSON.parse(stdout.trim());
      if (state.alive && state.hwnd !== 0 && state.renderers >= 1) {
        wvReady = true;
        assert.equal(state.title, 'Shell', 'Expected native Win32 window title "Shell"');
        break;
      }
      await sleep(300);
    }
    assert.ok(wvReady, 'Timed out waiting for Shell.exe window and WebView2 renderer process');
    await sleep(1200);

    // Send WM_CLOSE cleanly via MainWindow and verify exit code 0
    await execFileAsync('powershell.exe', [
      '-NoProfile',
      '-Command',
      `$p = Get-Process -Id ${client.pid} -ErrorAction SilentlyContinue; if ($p) { $null = $p.CloseMainWindow() }`,
    ]);
    const [code] = await Promise.race([
      exited,
      sleep(8000, null, { ref: false }).then(() => {
        throw new Error('Shell.exe did not exit cleanly after CloseMainWindow');
      }),
    ]);
    assert.equal(code, 0, 'Expected Shell.exe clean exit code 0');
    console.log('[Stage 1] Verified real Shell.exe Win32 window, WebView2 renderer startup, and clean shutdown.');
  } finally {
    if (client.exitCode === null) client.kill();
  }
}

// =========================================================================
// Stage 2: Interactive Frontend UI & Visual Verification via Edge CDP
// =========================================================================
function findBrowser() {
  const candidates = [
    path.join(process.env['ProgramFiles(x86)'] || 'C:\\Program Files (x86)', 'Microsoft\\Edge\\Application\\msedge.exe'),
    path.join(process.env.ProgramFiles || 'C:\\Program Files', 'Microsoft\\Edge\\Application\\msedge.exe'),
    path.join(process.env.ProgramFiles || 'C:\\Program Files', 'Google\\Chrome\\Application\\chrome.exe'),
  ];
  return candidates.find((p) => existsSync(p));
}

async function freePort() {
  const srv = net.createServer();
  srv.listen(0, '127.0.0.1');
  await once(srv, 'listening');
  const p = srv.address().port;
  await new Promise((r) => srv.close(r));
  return p;
}

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'application/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.json': 'application/json',
};

const BRIDGE_MOCK_SCRIPT = `
<script>
(() => {
  const listeners = new Map();
  const emit = (ev, ...args) => { for (const fn of listeners.get(ev) || []) fn(...args); };
  let settings = {
    theme: 'light', terminalTheme: 'apple-dark',
    fontFamily: '"SF Mono", "JetBrains Mono", "Cascadia Mono", Consolas, monospace',
    fontSize: 14, lineHeight: 1.28, cursorStyle: 'bar', cursorBlink: true,
    scrollback: 10000, copyOnSelect: false, rightClickPaste: false,
    showTelemetry: true, webglEnabled: false, defaultKeepAlive: 30, confirmOnClose: true, closeToTray: true
  };
  let profiles = [
    { id: 'p1', name: 'Tokyo Production', group: '生产集群', color: 'green', icon: 'globe', pinned: true, host: '10.24.0.10', port: 22, user: 'deploy', auth: 'agent', keepAliveSec: 30, lastUsedAt: Math.floor(Date.now()/1000) - 300 },
    { id: 'p2', name: 'Database Bastion', group: '生产集群', color: 'blue', icon: 'database', host: 'bastion.internal.dev', port: 2222, user: 'ops', auth: 'password', remembered: true, keepAliveSec: 30, lastUsedAt: Math.floor(Date.now()/1000) - 7200 },
    { id: 'p3', name: 'Local GPU Box', group: '开发测试', color: 'purple', icon: 'gpu', host: '192.168.1.88', port: 22, user: 'haochi', auth: 'key', keyPath: 'C:\\\\Users\\\\haochi\\\\.ssh\\\\id_ed25519', remembered: true, keepAliveSec: 30, lastUsedAt: Math.floor(Date.now()/1000) - 86400 }
  ];
  let snippets = [
    { id: 's1', name: '系统负载与内存', category: '系统', command: 'uname -a && uptime && free -h', description: '快速查看内核、运行时间与内存使用', pinned: true },
    { id: 's2', name: '磁盘占用 Top 15', category: '磁盘', command: 'df -h && du -xh --max-depth=1 {{path}} | sort -hr | head -n 15', description: '定位大目录占用', pinned: true },
    { id: 's3', name: '监听端口一览', category: '网络', command: 'ss -tulpn', description: '列出所有正在监听的 TCP/UDP 端口及进程', pinned: true },
    { id: 's4', name: 'Docker 容器状态', category: 'Docker', command: 'docker ps --format "table {{.Names}}\\t{{.Status}}\\t{{.Ports}}"', description: '整洁展示运行中的容器' },
    { id: 's5', name: '校验并重载 Nginx', category: '服务', command: 'sudo nginx -t && sudo systemctl reload nginx', description: '先校验再重载' }
  ];
  window.runtime = {
    EventsOn(ev, cb) {
      if (!listeners.has(ev)) listeners.set(ev, new Set());
      listeners.get(ev).add(cb);
      return () => listeners.get(ev)?.delete(cb);
    },
    ClipboardGetText: async () => '',
    ClipboardSetText: async () => true,
    WindowMinimise() {},
    WindowToggleMaximise() {},
    WindowIsMaximised: async () => false,
    BrowserOpenURL() {},
    Quit() {},
  };
  window.go = { main: { App: {
    AppInfo: async () => ({ version: '1.0.0', dataDir: 'C:\\\\Users\\\\haochi\\\\AppData\\\\Local\\\\Shell' }),
    Profiles: async () => profiles,
    SaveProfile: async (p) => { const item = { ...p, id: p.id || 'p' + Date.now() }; profiles = [...profiles.filter(x => x.id !== item.id), item]; return item; },
    DuplicateProfile: async (id) => { const src = profiles.find(x => x.id === id); const c = { ...src, id: 'p' + Date.now(), name: src.name + ' 副本' }; profiles = [...profiles, c]; return c; },
    DeleteProfile: async (id) => { profiles = profiles.filter(x => x.id !== id); },
    ForgetSecret: async () => {},
    PickKey: async () => '',
    PickAndParseImportFiles: async () => ({
      detectedFormat: 'MobaXterm (.mxtsessions)',
      hosts: [
        { name: 'AWS-API-01', host: '54.12.34.56', port: 22, user: 'ubuntu', auth: 'password', group: 'Cloud/AWS', sourceFormat: 'MobaXterm', alreadySaved: false },
        { name: 'HK-Gateway', host: '47.88.12.9', port: 2222, user: 'ops', auth: 'key', group: 'Prod/HK', sourceFormat: 'MobaXterm', alreadySaved: false }
      ]
    }),
    ParseImportFilePaths: async () => ({ detectedFormat: '', hosts: [] }),
    DiscoverLocalSSHConfigImport: async () => ({
      detectedFormat: '本机 OpenSSH (~/.ssh/config)',
      hosts: [
        { name: 'prod-web', host: '10.24.0.21', port: 22, user: 'deploy', auth: 'key', proxyJump: 'bastion', sourceFormat: 'OpenSSH', alreadySaved: false }
      ]
    }),
    CommitImportedHosts: async () => [],
    Connect: async (id, p) => {
      setTimeout(() => {
        const banner = '\\r\\n\\x1b[32mWelcome to Ubuntu 24.04.1 LTS (GNU/Linux 6.8.0-45-generic x86_64)\\x1b[0m\\r\\n\\r\\n'
          + '  System load:  0.42              Processes:             142\\r\\n'
          + '  Usage of /:   38.4% of 98.3GB   Memory usage:          26%\\r\\n\\r\\n'
          + '\\x1b[1;34m' + p.user + '@' + p.name.toLowerCase().replace(/\\s+/g,'-') + '\\x1b[0m:\\x1b[1;36m~/app\\x1b[0m$ docker ps --format "table {{.Names}}\\t{{.Status}}\\t{{.Ports}}"\\r\\n'
          + 'NAMES          STATUS          PORTS\\r\\n'
          + 'api-gateway    Up 14 days      0.0.0.0:443->8443/tcp\\r\\n'
          + 'postgres-16    Up 14 days      127.0.0.1:5432->5432/tcp\\r\\n'
          + 'redis-cache    Up 14 days      127.0.0.1:6379->6379/tcp\\r\\n'
          + '\\x1b[1;34m' + p.user + '@' + p.name.toLowerCase().replace(/\\s+/g,'-') + '\\x1b[0m:\\x1b[1;36m~/app\\x1b[0m$ ';
        emit('ssh:data', id, btoa(unescape(encodeURIComponent(banner))));
        emit('ssh:latency', id, 18);
      }, 120);
    },
    Disconnect: async () => {},
    Input: async () => {},
    Resize: async () => {},
    ConfirmHost: async () => {},
    AnswerChallenge: async () => {},
    FetchStats: async () => ({
      hostname: 'tokyo-prod-01', os: 'Ubuntu 24.04.1 LTS', kernel: 'Linux 6.8.0-45',
      uptimeSec: 1214400, load1: '0.42', load5: '0.35', load15: '0.28',
      cpuCores: 8, cpuPercent: 18.4, memTotalMB: 16384, memUsedMB: 4280,
      diskTotalGB: 98.3, diskUsedGB: 37.8, diskPercent: 38,
      netRxBytes: 184290000, netTxBytes: 64120000, latencyMs: 18, collectedAt: Math.floor(Date.now()/1000)
    }),
    InstallPublicKey: async () => {},
    SaveTranscript: async () => '',
    RealPath: async (_id, d) => d === '.' ? '/home/deploy/app' : d,
    ListFiles: async () => [
      { name: 'docker', path: '/home/deploy/app/docker', isDir: true, isSymlink: false, size: 4096, mode: 0o755, perm: 'drwxr-xr-x', modTime: Math.floor(Date.now()/1000) - 3600 },
      { name: 'logs', path: '/home/deploy/app/logs', isDir: true, isSymlink: false, size: 4096, mode: 0o755, perm: 'drwxr-xr-x', modTime: Math.floor(Date.now()/1000) - 900 },
      { name: 'docker-compose.yml', path: '/home/deploy/app/docker-compose.yml', isDir: false, isSymlink: false, size: 2480, mode: 0o644, perm: '-rw-r--r--', modTime: Math.floor(Date.now()/1000) - 7200 },
      { name: 'nginx.conf', path: '/home/deploy/app/nginx.conf', isDir: false, isSymlink: false, size: 4120, mode: 0o644, perm: '-rw-r--r--', modTime: Math.floor(Date.now()/1000) - 86400 },
      { name: '.env.production', path: '/home/deploy/app/.env.production', isDir: false, isSymlink: false, size: 612, mode: 0o600, perm: '-rw-------', modTime: Math.floor(Date.now()/1000) - 43200 },
      { name: 'deploy.sh', path: '/home/deploy/app/deploy.sh', isDir: false, isSymlink: false, size: 1890, mode: 0o755, perm: '-rwxr-xr-x', modTime: Math.floor(Date.now()/1000) - 1800 }
    ],
    PickUploadFiles: async () => [],
    UploadLocalPaths: async () => [],
    UploadFile: async () => '',
    DownloadFile: async () => '',
    CancelTransfer: async () => {},
    MakeDir: async () => {},
    CreateRemoteFile: async () => {},
    RenameRemote: async () => {},
    DeleteRemote: async () => {},
    ChmodRemote: async () => {},
    ReadRemoteText: async () => 'worker_processes auto;\\n',
    WriteRemoteText: async () => {},
    StartTunnel: async (_id, spec) => ({ ...spec, active: true, connections: 2, bytesIn: 245760, bytesOut: 98304 }),
    StopTunnel: async () => {},
    ListTunnels: async () => [
      { id: 't1', name: 'PostgreSQL 生产库', mode: 'local', bindHost: '127.0.0.1', bindPort: 15432, targetHost: '127.0.0.1', targetPort: 5432, active: true, connections: 2, bytesIn: 482000, bytesOut: 124000 },
      { id: 't2', name: '内网 SOCKS5 代理', mode: 'dynamic', bindHost: '127.0.0.1', bindPort: 10808, active: true, connections: 5, bytesIn: 3145728, bytesOut: 891200 }
    ],
    Snippets: async () => snippets,
    SaveSnippet: async (s) => { const item = { ...s, id: s.id || 's' + Date.now() }; snippets = [...snippets.filter(x => x.id !== item.id), item]; return item; },
    DeleteSnippet: async (id) => { snippets = snippets.filter(x => x.id !== id); },
    GetSettings: async () => settings,
    SaveSettings: async (s) => { settings = { ...settings, ...s }; return settings; },
    KnownHosts: async () => [
      { line: 1, hosts: ['10.24.0.10'], keyType: 'ssh-ed25519', fingerprint: 'SHA256:k8xR2fQ9vN4mP7sW1yZ3aB6cD8eF0gH2iJ4kL6mN8oP' }
    ],
    LocalKeys: async () => [
      { name: 'id_ed25519', path: 'C:\\\\Users\\\\haochi\\\\.ssh\\\\id_ed25519', publicPath: 'C:\\\\Users\\\\haochi\\\\.ssh\\\\id_ed25519.pub', keyType: 'ssh-ed25519', fingerprint: 'SHA256:9xL2mN5pQ8rT1vW4yZ7bC0dE3fG6hJ9kM2nP5qR8sT1', publicKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKey haochi@windows', hasPublic: true, encrypted: false }
    ],
    GenerateSSHKey: async () => ({}),
    ExportBackup: async () => '',
    ImportBackup: async () => ({ profiles: 0, snippets: 0, secrets: 0 }),
    AIConfig: async () => ({ endpoint: 'http://127.0.0.1:11434/v1', model: 'qwen2.5-coder:7b', hasKey: false }),
    ConfigureAI: async () => {},
    TestAI: async () => {},
    CancelAI: async () => {},
    Suggest: async () => 'df -h',
    SuggestCommand: async () => ({ command: 'ss -tulpn | grep :8080', explanation: '查看占用 8080 端口的监听进程及 PID。', risk: 'safe' }),
    ExplainTerminal: async () => '分析完成。',
    RunAIAgent: async () => '巡检完成。',
    GetAgentStatus: async () => ({
      enabled: true, running: true, port: 17822, token: 'sh_demo_token_9f8e7d6c5b4a32109876543210abcdef',
      exePath: 'C:\\\\Tools\\\\Shell.exe',
      policy: { mode: 'smart', allowPatterns: ['kubectl get *'], denyPatterns: ['rm -rf /*'], deniedPaths: ['/etc/shadow', '*/.ssh/id_*'] },
      redaction: { enabled: true, maskSecrets: true, maskPII: true, maskIPs: false }
    }),
    SaveAgentConfig: async (cfg) => ({ ...cfg, running: cfg.enabled, token: 'sh_demo_token_9f8e7d6c5b4a32109876543210abcdef', exePath: 'C:\\\\Tools\\\\Shell.exe' }),
    RotateAgentToken: async () => 'sh_rotated_token_1234567890abcdef1234567890abcd',
    TestRedaction: async (txt) => ({
      text: txt.replace(/S3cr3tPass!/g, '[REDACTED:CREDENTIAL]').replace(/sk-proj-[A-Za-z0-9]+/g, '[REDACTED:OPENAI_KEY]').replace(/13812345678/g, '[REDACTED:PHONE]'),
      count: 4,
      hits: { DSN_CREDENTIAL: 1, OPENAI_KEY: 1, JWT: 1, PHONE: 1 }
    }),
    TestCommandPolicy: async () => ({
      decision: 'ask', risk: 'danger', reason: '匹配高危破坏性操作模式',
      segments: [{ raw: 'ls -la', program: 'ls', args: ['-la'], wrapped: false, hasRedir: false, risk: 'safe' }, { raw: 'rm -rf /tmp/test', program: 'rm', args: ['-rf', '/tmp/test'], wrapped: false, hasRedir: false, risk: 'danger' }]
    }),
    ResolveAgentApproval: async () => {},
    AuditLogs: async () => [
      { id: 'a1', timestamp: Math.floor(Date.now()/1000) - 45, source: 'mcp', clientName: 'Claude Code', host: 'Tokyo Production', tool: 'shell_exec', summary: 'docker ps --format table', risk: 'safe', decision: 'auto-allow', redactedCount: 0, durationMs: 42, ok: true },
      { id: 'a2', timestamp: Math.floor(Date.now()/1000) - 18, source: 'mcp', clientName: 'Antigravity', host: 'Tokyo Production', tool: 'shell_sftp_read', summary: 'sftp read /home/deploy/app/.env.production', risk: 'safe', decision: 'auto-allow', redactedCount: 3, durationMs: 28, ok: true }
    ],
    ClearAuditLogs: async () => {}
  }}};
})();
</script>`;

async function verifyFrontendUI() {
  const browserPath = findBrowser();
  if (!browserPath) {
    console.log('[Stage 2] Edge/Chrome not found; skipping headless UI screenshot pass.');
    return;
  }

  const distDir = path.resolve('frontend/dist');
  const httpPort = await freePort();
  const server = http.createServer(async (req, res) => {
    try {
      const reqPath = (req.url || '/').split('?')[0];
      const rel = reqPath === '/' ? 'index.html' : reqPath.replace(/^\/+/, '');
      const full = path.join(distDir, rel);
      const ext = path.extname(full);
      let body = await readFile(full);
      if (rel === 'index.html') {
        body = Buffer.from(body.toString('utf8').replace('<head>', '<head>' + BRIDGE_MOCK_SCRIPT), 'utf8');
      }
      res.writeHead(200, { 'Content-Type': MIME[ext] || 'application/octet-stream' });
      res.end(body);
    } catch {
      res.writeHead(404);
      res.end('Not found');
    }
  });
  server.listen(httpPort, '127.0.0.1');
  await once(server, 'listening');

  const cdpPort = await freePort();
  const userData = await mkdtemp(path.join(process.env.RUNNER_TEMP || os.tmpdir(), 'shell-edge-'));
  const edge = spawn(browserPath, [
    '--headless=new',
    '--disable-gpu',
    '--no-first-run',
    '--no-default-browser-check',
    '--window-size=1280,820',
    '--remote-debugging-address=127.0.0.1',
    '--remote-debugging-port=' + cdpPort,
    '--remote-allow-origins=*',
    '--user-data-dir=' + userData,
    'http://127.0.0.1:' + httpPort + '/',
  ]);

  let socket;
  const outstanding = new Map();
  const exceptions = [];
  let nextId = 0;

  function call(method, params = {}) {
    const id = ++nextId;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        outstanding.delete(id);
        reject(new Error('CDP timeout: ' + method));
      }, 6000);
      outstanding.set(id, { resolve, reject, timer });
      socket.send(JSON.stringify({ id, method, params }));
    });
  }

  async function evaluate(expression) {
    const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) {
      throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);
    }
    return result.result.value;
  }

  async function until(fn, label, timeout = 15000) {
    const start = Date.now();
    let last;
    while (Date.now() - start < timeout) {
      try {
        const v = await fn();
        if (v) return v;
      } catch (e) {
        last = e;
      }
      await sleep(100);
    }
    throw new Error('Timeout waiting for ' + label + (last ? ': ' + last.message : ''));
  }

  async function click(selector) {
    const pt = await until(
      () =>
        evaluate(
          `(() => { const el = document.querySelector(${JSON.stringify(selector)}); if (!el) return null; const r = el.getBoundingClientRect(); return r.width && r.height ? { x: r.x + r.width/2, y: r.y + r.height/2 } : null; })()`,
        ),
      selector,
    );
    await call('Input.dispatchMouseEvent', { type: 'mouseMoved', ...pt });
    await call('Input.dispatchMouseEvent', { type: 'mousePressed', button: 'left', clickCount: 1, ...pt });
    await call('Input.dispatchMouseEvent', { type: 'mouseReleased', button: 'left', clickCount: 1, ...pt });
    await sleep(220);
  }

  async function shot(name) {
    const { data } = await call('Page.captureScreenshot', { format: 'png' });
    await writeFile(path.join(snapshotDir, name + '.png'), Buffer.from(data, 'base64'));
  }

  try {
    const target = await until(async () => {
      const resp = await fetch('http://127.0.0.1:' + cdpPort + '/json/list');
      const list = await resp.json();
      return list.find((p) => p.type === 'page' && p.url.includes('127.0.0.1:' + httpPort) && p.webSocketDebuggerUrl);
    }, 'Edge CDP page target');

    socket = new WebSocket(target.webSocketDebuggerUrl);
    await once(socket, 'open');
    socket.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.method === 'Runtime.exceptionThrown') {
        exceptions.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
      }
      const task = outstanding.get(msg.id);
      if (!task) return;
      outstanding.delete(msg.id);
      clearTimeout(task.timer);
      if (msg.error) task.reject(new Error(msg.error.message));
      else task.resolve(msg.result);
    });

    await Promise.all([
      call('Page.enable'),
      call('Runtime.enable'),
      call('Emulation.setDeviceMetricsOverride', { width: 1280, height: 820, deviceScaleFactor: 2, mobile: false }),
    ]);

    await until(() => evaluate("document.querySelectorAll('.host-row').length === 3"), 'initial Apple workbench render');
    await sleep(250);
    await shot('01-Workbench-Light');

    // Host modal (Basic + Private Key)
    await click('#addHost');
    await until(() => evaluate("!!document.querySelector('#connectDialog')"), 'host modal');
    await shot('02-HostModal');
    await click('#authMode [data-auth="key"]');
    await until(() => evaluate("!!document.querySelector('#keyPath')"), 'private key form');
    await click('#connectDialog [data-close]');

    // Spotlight Command Palette
    await click('#paletteBtn');
    await until(() => evaluate("!!document.querySelector('#commandPalette')"), 'command palette');
    await shot('03-CommandPalette');
    await click('#commandPalette');

    // Connect to first host -> Active Terminal + Telemetry + SFTP Drawer
    await click('.host-row');
    await until(() => evaluate("!!document.querySelector('.xterm-screen') && !document.querySelector('#sftpBtn').disabled"), 'active terminal session');
    await sleep(900); // let telemetry poll populate
    await click('#sftpBtn');
    await until(() => evaluate("document.querySelectorAll('.sftp-row').length >= 4"), 'SFTP drawer files');
    await shot('04-Terminal-SFTP-Light');

    // Tunnels Drawer
    await click('#tunnelsBtn');
    await sleep(250);
    await shot('05-Terminal-Tunnels');

    // AI Drawer
    await click('#aiOpen');
    await until(() => evaluate("!!document.querySelector('#aiDrawer')"), 'AI drawer');
    await shot('06-Terminal-AI');
    await click('#aiOpen');

    // Universal Importer Modal
    await click('#importSshBtn');
    await until(() => evaluate("!!document.querySelector('#importDialog')"), 'universal import modal');
    await click('#pickImportFilesBtn');
    await sleep(250);
    await shot('10-Universal-Import');
    await click('#importDialog button[aria-label="关闭"]');

    // Settings Sheet Light + Agent Governance & Redaction + Dark
    await click('#settingsBtn');
    await until(() => evaluate("!!document.querySelector('#settingsDialog')"), 'settings modal');
    await shot('07-Settings-Light');

    // Open Agent & Security tab -> screenshot MCP + Unified Security/Redaction view
    await evaluate(`document.querySelector('.settings-nav-btn:nth-child(5)').click()`);
    await sleep(250);
    await shot('11-Agent-MCP-Settings');
    await evaluate(`document.querySelector('#settingsDialog .settings-content .segmented button:nth-child(2)').click()`);
    await sleep(200);
    await shot('12-Redaction-Engine');
    // Also exercise collapsible advanced rules & redaction tester
    await evaluate(`document.querySelector('#advancedGuardFold summary').click()`);
    await sleep(150);
    await click('#runRedactTestBtn');
    await sleep(150);

    // Switch back to Appearance and switch to Dark theme
    await evaluate(`document.querySelector('.settings-nav-btn:nth-child(1)').click()`);
    await sleep(200);
    await evaluate(`document.querySelector('#settingsDialog .segmented button:nth-child(2)').click()`);
    await sleep(250);
    await shot('08-Settings-Dark');
    await click('#settingsDialog button[aria-label="关闭设置"]');
    await sleep(250);
    await shot('09-Terminal-Dark');

    assert.deepEqual(exceptions, [], 'Expected zero uncaught runtime exceptions');
    console.log('[Stage 2] Verified all interactive UI views, zero exceptions, and captured 12 Retina screenshots.');
  } finally {
    socket?.close();
    if (edge.exitCode === null) edge.kill();
    server.close();
  }
}

await verifyNativeBinary();
await verifyFrontendUI();
