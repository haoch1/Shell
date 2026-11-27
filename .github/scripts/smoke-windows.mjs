// End-to-end Windows verification for Shell v2:
// 1. Launches the real compiled Shell.exe with an isolated profile and verifies Win32 window + WebView2 renderer startup & clean exit.
// 2. Renders the exact built frontend/dist bundle in headless Edge (1280x820 @2x) via CDP, asserts zero errors, exercises all views, and captures UI screenshots.
import assert from 'node:assert/strict';
import { verifyLargeDirectory } from './sftp-large-directory.mjs';
import { verifyHostExport, verifyEmptyHostExport } from './host-export.mjs';
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
const { version: expectedVersion } = JSON.parse(await readFile(new URL('../../frontend/package.json', import.meta.url), 'utf8'));
const snapshotDir = path.resolve('build/preview');
await mkdir(snapshotDir, { recursive: true });

// =========================================================================
// Stage 1: Real Shell.exe native WebView2 startup & clean shutdown test
// =========================================================================
async function verifyNativeBinary() {
  const exePath = path.resolve('build/Shell-Windows-x64/Shell.exe');
  assert.ok(existsSync(exePath), 'Expected compiled Shell.exe at ' + exePath);

  const testProfile = await mkdtemp(path.join(process.env.RUNNER_TEMP || os.tmpdir(), 'shell-native-'));
  const store = path.join(testProfile, 'Shell');
  await mkdir(store);
  await writeFile(path.join(store, 'agent.json'), JSON.stringify({ enabled: true, port: await freePort() }));
  const env = { ...process.env, LOCALAPPDATA: testProfile, APPDATA: testProfile, SHELL_EXIT_ON_CLOSE: '1' };
  const cliVersion = await execFileAsync(exePath, ['--version'], { env, timeout: 8000, windowsHide: true });
  assert.equal(cliVersion.stdout.trim(), `Shell ${expectedVersion}`);
  const runtimeFile = path.join(store, 'bridge-runtime.json');
  const client = spawn(exePath, [], { env });
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
      if (state.alive && state.hwnd !== 0 && state.renderers >= 1 && existsSync(runtimeFile)) {
        wvReady = true;
        assert.equal(state.title, 'Shell', 'Expected native Win32 window title "Shell"');
        break;
      }
      await sleep(300);
    }
    assert.ok(wvReady, 'Timed out waiting for Shell.exe window and WebView2 renderer process');
    const advertised = JSON.parse(await readFile(runtimeFile, 'utf8'));
    assert.equal(advertised.pid, client.pid);
    assert.ok(advertised.pipeID, 'Expected the desktop to publish its local named pipe');
    const requests = [
      { id: 1, method: 'initialize', params: { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'smoke', version: '1' } } },
      { method: 'notifications/initialized' },
      { id: 2, method: 'tools/list' },
    ];
    const discovery = execFileAsync(exePath, ['--mcp'], { env, timeout: 8000, windowsHide: true });
    for (const req of requests) discovery.child.stdin.write(JSON.stringify({ jsonrpc: '2.0', ...req }) + '\n');
    discovery.child.stdin.end();
    const { stdout } = await discovery;
    const replies = stdout.trim().split(/\r?\n/).map(line => JSON.parse(line));
    assert.deepEqual(replies.map(reply => reply.id), [1, 2]);
    assert.equal(replies[0].result.protocolVersion, '2025-03-26');
    assert.equal(replies[0].result.serverInfo.version, expectedVersion);
    assert.ok(replies[1].result.tools.some(tool => tool.name === 'shell_connect'));
    console.log('[Stage 1] Packaged MCP client initialized and discovered the desktop tools.');

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
    theme: 'light', terminalTheme: 'auto',
    fontFamily: '"SF Mono", "JetBrains Mono", "Cascadia Mono", Consolas, monospace',
    fontSize: 14, lineHeight: 1.28, cursorStyle: 'bar', cursorBlink: true,
    scrollback: 10000, copyOnSelect: false, rightClickPaste: false,
    showTelemetry: true, syntaxHighlight: true, webglEnabled: false, defaultKeepAlive: 30, confirmOnClose: true, closeToTray: true, hideHostIp: false, groupOrder: []
  };
  let profiles = new URLSearchParams(location.search).has('empty-hosts') ? [] : [
    { id: 'p1', name: 'Tokyo Production', group: '生产集群', icon: 'ubuntu', pinned: true, host: '10.24.0.10', port: 22, user: 'deploy', auth: 'agent', keepAliveSec: 30, lastUsedAt: Math.floor(Date.now()/1000) - 300 },
    { id: 'p2', name: 'SG - AWS', group: '生产集群', icon: 'debian', host: '2406:da18:b15:8000:4839:f12a:9c3b:4e21', port: 58222, user: 'root', auth: 'password', remembered: true, keepAliveSec: 30, lastUsedAt: Math.floor(Date.now()/1000) - 7200 },
    { id: 'p3', name: 'Local GPU Box', group: '开发测试', icon: 'rocky', host: '192.168.1.88', port: 22, user: 'haochi', auth: 'key', keyPath: 'C:\\\\Users\\\\haochi\\\\.ssh\\\\id_ed25519', remembered: true, keepAliveSec: 30, lastUsedAt: Math.floor(Date.now()/1000) - 86400 }
  ];
  if (location.search === '?host-export') profiles.find(host => host.id === 'p2').jumpHostId = 'p1';
  let snippets = [
    { id: 's1', name: '系统负载与内存', category: '系统', command: 'uname -a && uptime && free -h', description: '快速查看内核、运行时间与内存使用', pinned: true },
    { id: 's2', name: '磁盘占用 Top 15', category: '磁盘', command: 'df -h && du -xh --max-depth=1 {{path}} | sort -hr | head -n 15', description: '定位大目录占用', pinned: true },
    { id: 's3', name: '监听端口一览', category: '网络', command: 'ss -tulpn', description: '列出所有正在监听的 TCP/UDP 端口及进程', pinned: true },
    { id: 's4', name: 'Docker 容器状态', category: 'Docker', command: 'docker ps --format "table {{.Names}}\\t{{.Status}}\\t{{.Ports}}"', description: '整洁展示运行中的容器' },
    { id: 's5', name: '校验并重载 Nginx', category: '服务', command: 'sudo nginx -t && sudo systemctl reload nginx', description: '先校验再重载' }
  ];
  window.runtime = {
    EventsEmit: emit,
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
    AppInfo: async () => ({ version: ${JSON.stringify(expectedVersion)}, dataDir: 'C:\\\\Users\\\\haochi\\\\AppData\\\\Local\\\\Shell' }),
    OpenDataDir: async () => {},
    CheckForUpdate: async () => ({
      currentVersion: ${JSON.stringify(expectedVersion)},
      latestVersion: ${JSON.stringify(expectedVersion)},
      hasUpdate: false,
      releaseUrl: 'https://github.com/haoch1/Shell/releases',
      notes: ''
    }),
    DownloadAndApplyUpdate: async () => {},
    TermWSURL: async () => '',
    Profiles: async () => profiles,
    SaveProfile: async (p) => { const item = { ...p, id: p.id || 'p' + Date.now() }; profiles = [...profiles.filter(x => x.id !== item.id), item]; return item; },
    GetProfileSecret: async () => 'demo-saved-password',
    SaveProfileWithSecret: async (p, secret, remember) => { const item = { ...p, id: p.id || 'p' + Date.now(), remembered: Boolean(remember && (secret || p.remembered)) }; profiles = [...profiles.filter(x => x.id !== item.id), item]; return item; },
    DuplicateProfile: async (id) => { const src = profiles.find(x => x.id === id); const c = { ...src, id: 'p' + Date.now(), name: src.name + ' 副本' }; profiles = [...profiles, c]; return c; },
    DeleteProfile: async (id) => { profiles = profiles.filter(x => x.id !== id); },
    DeleteProfiles: async (ids) => { const drop = new Set(ids); profiles = profiles.filter(x => !drop.has(x.id)); },
    SetProfilesGroup: async (ids, group) => { const set = new Set(ids); const g = (group || '').trim(); profiles = profiles.map(x => set.has(x.id) ? { ...x, group: g } : x); },
    ClearRecentHosts: async () => { profiles = profiles.map(x => ({ ...x, lastUsedAt: 0 })); },
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
      if (p.id === 'fail-test') {
        emit('ssh:status', id, 'jump', '正在连接跳板机 Tokyo Production…');
        await new Promise(r => setTimeout(r, 40));
        emit('ssh:status', id, 'dial', '正在连接 10.24.0.99:22…');
        await new Promise(r => setTimeout(r, 40));
        throw new Error('dial tcp 10.24.0.99:22: connectex: No connection could be made because the target machine actively refused it.');
      }
      setTimeout(() => emit('ssh:status', id, 'dial', '正在连接 ' + p.host + ':' + p.port + '…'), 20);
      setTimeout(() => emit('ssh:status', id, 'auth', '正在校验指纹与认证…'), 55);
      setTimeout(() => emit('ssh:status', id, 'shell', '正在打开远程终端…'), 90);
      setTimeout(() => {
        const promptPrefix = p.user + '@' + p.name.toLowerCase().replace(/\\s+/g,'-') + ':~/app$ ';
        const banner = '\\r\\n\\x1b[32mWelcome to Ubuntu 24.04.1 LTS (GNU/Linux 6.8.0-45-generic x86_64)\\x1b[0m\\r\\n\\r\\n'
          + '  System load:  0.42              Processes:             142\\r\\n'
          + '  Usage of /:   38.4% of 98.3GB   Memory usage:          26%\\r\\n'
          + '  Last login:   Fri Nov 27 22:18:04 2026 from 8.217.10.13\\r\\n\\r\\n'
          + promptPrefix + 'docker ps --format "table {{.Names}}\\t{{.Status}}\\t{{.Ports}}"\\r\\n'
          + 'NAMES          STATUS          PORTS\\r\\n'
          + 'api-gateway    Up 14 days      0.0.0.0:443->8443/tcp\\r\\n'
          + 'postgres-16    Up 14 days      127.0.0.1:5432->5432/tcp\\r\\n'
          + 'redis-cache    Up 14 days      127.0.0.1:6379->6379/tcp\\r\\n'
          + promptPrefix + 'tail -n 2 /var/log/app/gateway.log && curl -s https://127.0.0.1:8443/health\\r\\n'
          + '2026-11-27T22:18:10Z OK    upstream 10.24.0.10:8080 latency=14ms path=/var/log/app/gateway.log\\r\\n'
          + '2026-11-27T22:18:11Z ERROR upstream 10.24.0.11:8080 connection refused (retry 2/3)\\r\\n'
          + '{"status": "OK", "region": "ap-northeast-1", "mem": "428MB", "uptime": "14d"}\\r\\n'
          + promptPrefix;
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
    BrowseDirectory: async (id, dir) => {
      const api = window.go.main.App;
      const path = await api.RealPath(id, dir);
      return { path, entries: await api.ListFiles(id, path) };
    },
    ResolveFileLink: async () => { throw new Error('Unexpected link lookup'); },
    ListFiles: async () => [
      { name: 'docker', path: '/home/deploy/app/docker', isDir: true, isSymlink: false, size: 4096, mode: 0o755, perm: 'drwxr-xr-x', modTime: Math.floor(Date.now()/1000) - 3600 },
      { name: 'logs', path: '/home/deploy/app/logs', isDir: true, isSymlink: false, size: 4096, mode: 0o755, perm: 'drwxr-xr-x', modTime: Math.floor(Date.now()/1000) - 900 },
      { name: 'current', path: '/home/deploy/app/current', isDir: false, isSymlink: true, size: 32, mode: 0o777, perm: 'lrwxrwxrwx', modTime: Math.floor(Date.now()/1000) - 1200 },
      { name: 'docker-compose.yml', path: '/home/deploy/app/docker-compose.yml', isDir: false, isSymlink: false, size: 2480, mode: 0o644, perm: '-rw-r--r--', modTime: Math.floor(Date.now()/1000) - 7200 },
      { name: 'nginx.conf', path: '/home/deploy/app/nginx.conf', isDir: false, isSymlink: false, size: 4120, mode: 0o644, perm: '-rw-r--r--', modTime: Math.floor(Date.now()/1000) - 86400 },
      { name: 'deploy.sh', path: '/home/deploy/app/deploy.sh', isDir: false, isSymlink: false, size: 1890, mode: 0o755, perm: '-rwxr-xr-x', modTime: Math.floor(Date.now()/1000) - 1800 },
      { name: 'release-1.5.3.tar.gz', path: '/home/deploy/app/release-1.5.3.tar.gz', isDir: false, isSymlink: false, size: 14820400, mode: 0o644, perm: '-rw-r--r--', modTime: Math.floor(Date.now()/1000) - 21600 },
      { name: '.env.production', path: '/home/deploy/app/.env.production', isDir: false, isSymlink: false, size: 612, mode: 0o600, perm: '-rw-------', modTime: Math.floor(Date.now()/1000) - 43200 }
    ],
    PickUploadFiles: async () => [],
    PickUploadFolder: async () => '',
    UploadLocalPaths: async () => [],
    UploadFile: async () => '',
    DownloadFile: async () => '',
    DownloadDirectory: async () => '',
    BatchDownloadRemote: async () => '',
    CancelTransfer: async () => {},
    MakeDir: async () => {},
    CreateRemoteFile: async () => {},
    RenameRemote: async () => {},
    DeleteRemote: async () => {},
    DeleteRemoteBatch: async (_id, paths) => (paths || []).length,
    ChmodRemote: async () => {},
    ChmodRemoteRecursive: async () => {},
    CopyRemote: async () => {},
    StatRemote: async (_id, remotePath) => ({
      name: remotePath.split('/').pop() || remotePath,
      path: remotePath,
      size: remotePath.endsWith('nginx.conf') ? 4120 : 2480,
      mode: 0o644,
      octal: '0644',
      perm: '-rw-r--r--',
      modTime: Math.floor(Date.now()/1000) - 7200,
      accessTime: Math.floor(Date.now()/1000) - 600,
      isDir: false,
      isSymlink: remotePath.endsWith('/current'),
      linkTarget: remotePath.endsWith('/current') ? '/home/deploy/releases/20261127' : '',
      uid: 1000,
      gid: 1000,
      itemCount: 0
    }),
    RevealLocalPath: async () => {},
    ReadRemoteText: async (_id, remotePath) => {
      if (String(remotePath).endsWith('.yml')) {
        return [
          '# Production API Gateway & Database Stack',
          'services:',
          '  api-gateway:',
          '    image: ghcr.io/acme/gateway:1.5.3',
          '    restart: unless-stopped',
          '    ports:',
          '      - "443:8443"',
          '    environment:',
          '      NODE_ENV: production',
          '      LOG_LEVEL: info',
          '      UPSTREAM_TIMEOUT_MS: 5000',
          '    healthcheck:',
          '      test: ["CMD", "curl", "-sf", "http://127.0.0.1:8443/health"]',
          '      interval: 15s',
          '      retries: 3',
          '  postgres:',
          '    image: postgres:16-alpine',
          '    volumes:',
          '      - pgdata:/var/lib/postgresql/data',
          'volumes:',
          '  pgdata: {}',
          '',
        ].join('\\n');
      }
      return [
        '# Nginx Production Reverse Proxy',
        'worker_processes auto;',
        'events {',
        '    worker_connections 4096;',
        '}',
        'http {',
        '    include /etc/nginx/mime.types;',
        '    sendfile on;',
        '    keepalive_timeout 65;',
        '    upstream api_backend {',
        '        server 10.24.0.10:8080 max_fails=3 fail_timeout=15s;',
        '        server 10.24.0.11:8080 backup;',
        '    }',
        '    server {',
        '        listen 443 ssl http2;',
        '        server_name api.example.com;',
        '        location / {',
        '            proxy_pass http://api_backend;',
        '            proxy_set_header Host $host;',
        '        }',
        '    }',
        '}',
        '',
      ].join('\\n');
    },
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
    GetSettings: async () => ({ ...settings }),
    SaveSettings: async (s) => { settings = { ...s }; return { ...settings }; },
    UpdatePreferences: async (changes) => { settings = { ...settings, ...changes }; return { ...settings }; },
    DetectSystemProxy: async () => ({ enabled: true, protocol: 'http', host: '127.0.0.1', port: 7897, source: 'windows', summary: 'http://127.0.0.1:7897' }),
    KnownHosts: async () => [
      { line: 1, hosts: ['[56.10.113.238]:58222'], keyType: 'ecdsa-sha2-nistp256', fingerprint: 'SHA256:a0Skqj56tH4NyA2btG9Q42FObzEfH8hM53BL9Y3Xd2I' },
      { line: 2, hosts: ['10.24.0.10'], keyType: 'ssh-ed25519', fingerprint: 'SHA256:k8xR2fQ9vN4mP7sW1yZ3aB6cD8eF0gH2iJ4kL6mN8oP' }
    ],
    RemoveKnownHost: async () => {},
    RemoveKnownHosts: async () => {},
    ClearKnownHosts: async () => {},
    LocalKeys: async () => [
      { name: 'id_ed25519', path: 'C:\\\\Users\\\\haochi\\\\.ssh\\\\id_ed25519', publicPath: 'C:\\\\Users\\\\haochi\\\\.ssh\\\\id_ed25519.pub', keyType: 'ssh-ed25519', fingerprint: 'SHA256:9xL2mN5pQ8rT1vW4yZ7bC0dE3fG6hJ9kM2nP5qR8sT1', publicKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKey haochi@windows', hasPublic: true, encrypted: false }
    ],
    GenerateSSHKey: async () => ({}),
    ExportBackup: async () => '',
    ImportBackup: async () => ({ profiles: 0, snippets: 0, secrets: 0 }),
    AIConfig: async () => ({ mode: 'api', endpoint: 'https://api.anthropic.com/v1', model: 'claude-sonnet-5-5', hasKey: false, cliCommand: 'agy', ready: true }),
    FetchAIModels: async () => ['claude-sonnet-5-5', 'claude-opus-5-5', 'claude-fable-5-2', 'claude-haiku-5-5'],
    DetectAICLIs: async () => [
      { id: 'claude', name: 'Claude Code', command: 'claude', installed: true, path: 'C:\\\\Users\\\\haochi\\\\AppData\\\\Roaming\\\\npm\\\\claude.cmd' },
      { id: 'codex', name: 'Codex CLI', command: 'codex', installed: true, path: 'C:\\\\Users\\\\haochi\\\\AppData\\\\Roaming\\\\npm\\\\codex.cmd' },
      { id: 'agy', name: 'Antigravity CLI', command: 'agy', installed: true, path: 'C:\\\\Users\\\\haochi\\\\AppData\\\\Local\\\\agy\\\\bin\\\\agy.exe' }
    ],
    SaveAIConfig: async () => {},
    TestAIConfig: async () => {},
    ConfigureAI: async () => {},
    TestAI: async () => {},
    CancelAI: async () => {},
    Suggest: async () => 'df -h',
    SuggestCommand: async () => ({ command: 'ss -tulpn | grep :8080', explanation: '查看占用 8080 端口的监听进程及 PID。', risk: 'safe' }),
    ExplainTerminal: async () => '分析完成。',
    RunAIAgent: async () => '巡检完成。',
    GetAgentStatus: async () => ({
      enabled: true, running: true, port: 17822, execMode: 'silent', token: 'sh_demo_token_9f8e7d6c5b4a32109876543210abcdef',
      exePath: 'C:\\\\Tools\\\\Shell.exe',
      policy: { mode: 'trusted', allowPatterns: ['kubectl get *'], denyPatterns: ['rm -rf /*'], deniedPaths: ['/etc/shadow', '*/.ssh/id_*'] },
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
  const consoleErrors = [];
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
      if (msg.method === 'Runtime.consoleAPICalled' && msg.params.type === 'error') {
        const text = (msg.params.args || []).map((a) => a.value ?? a.description ?? '').join(' ');
        consoleErrors.push(text);
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

    await until(() => evaluate("document.querySelectorAll('.host-row').length === 3"), 'initial workbench render');
    await sleep(250);
    await shot('01-Workbench-Light');

    // Verify sidebar sort button popover actually opens inside viewport (regression guard)
    await click('#sortHostsBtn');
    const sortRect = await evaluate(`(() => { const el = document.querySelector('.sort-popover'); if (!el) return null; const r = el.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, width: r.width, height: r.height }; })()`);
    assert.ok(sortRect && sortRect.top > 0 && sortRect.bottom < 820 && sortRect.width > 100, 'Expected visible sort popover inside viewport, got ' + JSON.stringify(sortRect));
    await click('.sort-popover .context-menu-item:nth-child(2)');
    await sleep(150);

    // Toggle Dark Mode on Welcome Workbench to verify dark OS icons + IPv6 display
    await click('button[aria-label="切换深浅色外观"]');
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.theme === 'dark' && s.fontSize === 14)"), 'persisted dark theme and preserved font size');
    await shot('16-Workbench-Dark');
    await click('button[aria-label="切换深浅色外观"]');
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.theme === 'light')"), 'persisted light theme');

    // Verify IP privacy hiding toggle (#toggleHideIpBtn) omits IPs cleanly with zero layout jump
    const beforeHideLayout = await evaluate(`(() => ({
      rowHeights: [...document.querySelectorAll('.host-row')].map(el => el.getBoundingClientRect().height),
      cardHeights: [...document.querySelectorAll('.recent-card')].map(el => el.getBoundingClientRect().height),
    }))()`);
    await click('#toggleHideIpBtn');
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.hideHostIp === true)"), 'persisted hideHostIp=true');
    await sleep(240);
    const hiddenPrivacy = await evaluate(`(() => ({
      sidebarText: document.querySelector('.sidebar-list')?.textContent || '',
      recentText: document.querySelector('.recent-grid')?.textContent || '',
      hiddenSubCount: document.querySelectorAll('.host-row .host-sub[data-ip-hidden="true"]').length,
      rowHeights: [...document.querySelectorAll('.host-row')].map(el => el.getBoundingClientRect().height),
      cardHeights: [...document.querySelectorAll('.recent-card')].map(el => el.getBoundingClientRect().height),
    }))()`);
    assert.ok(
      hiddenPrivacy.hiddenSubCount === 3 &&
        !hiddenPrivacy.sidebarText.includes('10.24.0.10') &&
        !hiddenPrivacy.sidebarText.includes('2406:da18') &&
        !hiddenPrivacy.sidebarText.includes('192.168.1.88') &&
        !hiddenPrivacy.sidebarText.includes('•') &&
        !hiddenPrivacy.recentText.includes('10.24.0.10') &&
        !hiddenPrivacy.recentText.includes('2406:da18') &&
        !hiddenPrivacy.recentText.includes('192.168.1.88') &&
        !hiddenPrivacy.recentText.includes('•'),
      'Expected sidebar & recent cards to omit IPs cleanly with zero dots when hideHostIp is enabled, got ' + JSON.stringify(hiddenPrivacy),
    );
    assert.ok(
      hiddenPrivacy.rowHeights.every((h, i) => Math.abs(h - beforeHideLayout.rowHeights[i]) <= 2) &&
        hiddenPrivacy.cardHeights.every((h, i) => Math.abs(h - beforeHideLayout.cardHeights[i]) <= 2),
      'Expected stable row/card heights when hiding IPs (zero layout shift): before=' +
        JSON.stringify(beforeHideLayout) +
        ' after=' +
        JSON.stringify({ rowHeights: hiddenPrivacy.rowHeights, cardHeights: hiddenPrivacy.cardHeights }),
    );
    await shot('18-Privacy-HideIP');
    await click('#toggleHideIpBtn');
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.hideHostIp === false)"), 'persisted hideHostIp=false');
    await sleep(240);
    const restoredSubs = await evaluate("[...document.querySelectorAll('.host-row .host-sub')].map(el => el.textContent.trim())");
    assert.ok(
      restoredSubs.some(t => t.includes('10.24.0.10')) && restoredSubs.some(t => t.includes('192.168.1.88')),
      'Expected real host addresses restored when hideHostIp is disabled, got ' + JSON.stringify(restoredSubs),
    );

    // Verify refactored sidebar batch management (top checkbox header, zero jump, custom checkboxes, equal-width bottom bar, Esc exit, batch grouping)
    const beforeBatchHeaderRect = await evaluate(`(() => {
      const r = document.querySelector('.sidebar-action-row')?.getBoundingClientRect();
      return r ? { top: r.top, height: r.height } : null;
    })()`);
    await click('.sidebar-footer button[aria-label="批量管理"]');
    await until(() => evaluate("!!document.querySelector('.sidebar-select-header')"), 'sidebar batch select header');
    const batchMetrics = await evaluate(`(() => {
      const hdr = document.querySelector('.sidebar-select-header')?.getBoundingClientRect();
      const rowCbs = [...document.querySelectorAll('.host-row input[type="checkbox"]')].filter(
        el => getComputedStyle(el).appearance === 'none',
      ).length;
      const btns = [...document.querySelectorAll('.sidebar-footer.batch-bar .batch-action-btn')].map(el => ({
        text: el.textContent.trim(),
        width: el.getBoundingClientRect().width,
        disabled: el.disabled,
      }));
      return { hdr: hdr ? { top: hdr.top, height: hdr.height } : null, rowCbs, btns };
    })()`);
    assert.ok(
      beforeBatchHeaderRect && batchMetrics.hdr && Math.abs(beforeBatchHeaderRect.height - batchMetrics.hdr.height) <= 2,
      'Expected zero vertical jump entering batch select mode: ' + JSON.stringify({ beforeBatchHeaderRect, after: batchMetrics.hdr }),
    );
    assert.equal(batchMetrics.rowCbs, 3, 'Expected custom <Checkbox> on all 3 host rows, got ' + batchMetrics.rowCbs);
    assert.equal(batchMetrics.btns.length, 2, 'Expected 2 equal-width bottom batch buttons, got ' + JSON.stringify(batchMetrics.btns));
    assert.ok(
      Math.abs(batchMetrics.btns[0].width - batchMetrics.btns[1].width) <= 2,
      'Expected equal-width bottom batch buttons, got ' + JSON.stringify(batchMetrics.btns),
    );
    assert.equal(await evaluate("document.querySelector('#batchGroupBtn')?.disabled"), true, 'Batch group button must start disabled with 0 selected');
    await click('.host-row[data-profile-id="p3"]');
    assert.equal(await evaluate("document.querySelector('#batchGroupBtn')?.disabled"), false, 'Batch group button must enable when a host is selected');
    const batchIndeterminate = await evaluate(`(() => {
      const master = document.querySelector('.sidebar-select-header input[type="checkbox"]');
      const txt = document.querySelector('.sidebar-select-header')?.textContent || '';
      return { indeterminate: Boolean(master?.indeterminate), hasCount: txt.includes('已选 1/3') };
    })()`);
    assert.deepEqual(batchIndeterminate, { indeterminate: true, hasCount: true }, 'Expected indeterminate master checkbox with 1/3 selected');
    await shot('21-Sidebar-BatchSelect');
    // Verify Escape cleanly exits batch select mode
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('.sidebar-select-header')"), 'Esc exits batch select mode');

    // Verify batch host grouping (#batchGroupBtn popover: existing group + new group, restoring fixture cleanly)
    await click('.sidebar-footer button[aria-label="批量管理"]');
    await click('.host-row[data-profile-id="p3"]');
    await click('#batchGroupBtn');
    const groupPopRect = await evaluate(`(() => { const el = document.querySelector('#batchGroupPopover'); if (!el) return null; const r = el.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, width: r.width }; })()`);
    assert.ok(groupPopRect && groupPopRect.top > 0 && groupPopRect.bottom < 820 && groupPopRect.width >= 180, 'Expected visible batch group popover inside viewport, got ' + JSON.stringify(groupPopRect));
    await evaluate(`[...document.querySelectorAll('#batchGroupPopover .batch-group-list .context-menu-item')].find(el => el.textContent.trim() === '生产集群')?.click()`);
    await until(() => evaluate("window.go.main.App.Profiles().then(list => list.find(x => x.id === 'p3')?.group === '生产集群')"), 'batch assign existing group');
    await click('.sidebar-footer button[aria-label="批量管理"]');
    await click('.host-row[data-profile-id="p3"]');
    await click('#batchGroupBtn');
    await until(() => evaluate("!!document.querySelector('#batchGroupInput')"), 'batch group input');
    await evaluate(`(() => {
      const input = document.querySelector('#batchGroupInput');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, '开发测试');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    await click('#batchGroupPopover button[type="submit"]');
    await until(() => evaluate("window.go.main.App.Profiles().then(list => list.find(x => x.id === 'p3')?.group === '开发测试')"), 'batch assign custom group and restore');

    // Host modal (Basic + Jump/Proxy identical height & zero vertical clipping guard)
    await click('#addHost');
    await until(() => evaluate("!!document.querySelector('#connectDialog')"), 'host modal');
    const basicMetrics = await evaluate(`(() => {
      const sheet = document.querySelector('#connectDialog .modal-sheet');
      const body = document.querySelector('#connectDialog .modal-body');
      return {
        height: sheet.getBoundingClientRect().height,
        scrollHeight: body.scrollHeight,
        clientHeight: body.clientHeight,
      };
    })()`);
    assert.ok(
      basicMetrics.scrollHeight <= basicMetrics.clientHeight + 2,
      'Expected Basic tab in HostModal to render without vertical clipping at 1280x820, got ' + JSON.stringify(basicMetrics),
    );
    await shot('02-HostModal');
    await click('#connectDialog .modal-header .segmented button:nth-child(2)');
    await sleep(120);
    const jumpH = await evaluate(`document.querySelector('#connectDialog .modal-sheet').getBoundingClientRect().height`);
    assert.ok(Math.abs(basicMetrics.height - jumpH) <= 1, 'Expected identical HostModal height across tabs: ' + basicMetrics.height + ' vs ' + jumpH);
    await click('#connectDialog .modal-header .segmented button:nth-child(1)');
    await sleep(100);
    await click('#authMode [data-auth="key"]');
    await until(() => evaluate("!!document.querySelector('#keyPath')"), 'private key form');
    await click('#connectDialog [data-close]');

    // A stored password may follow an address change only if the user selects it.
    await evaluate(`document.querySelector('.host-row[title^="root@"] button[title="编辑主机"]').click()`);
    await until(() => evaluate("Boolean(document.querySelector('#password')?.value)"), 'saved test credential');
    await evaluate(`(() => {
      const address = document.querySelector('#address');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(address, 'new.example');
      address.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    await until(() => evaluate("!!document.querySelector('#reuseSavedCredential')"), 'changed target credential choice');
    assert.equal(await evaluate("document.querySelector('#password').value"), '', 'Changed host must default to an empty credential');
    await click('#reuseSavedCredential');
    assert.ok(await evaluate("Boolean(document.querySelector('#password').value)"), 'Explicit reuse should restore the saved test credential');
    await click('#connectDialog [data-close]');

    // The tray and MCP share an event, but only MCP waits for the credential dialog.
    await evaluate(`(() => {
      const api = window.go.main.App;
      window.canceledProfileIds = [];
      api.CancelVisibleConnect = async id => { window.canceledProfileIds.push(id); };
      const profiles = api.Profiles;
      window.restoreTestProfiles = () => { api.Profiles = profiles; };
      api.Profiles = async () => [...await profiles(), { id: 'needs-password', name: 'Test host', host: 'example.test', user: 'demo', port: 22, auth: 'password' }];
      window.runtime.EventsEmit('agent:connect_profile', 'needs-password', true);
    })()`);
    await click('#connectDialog [data-close]');
    assert.deepEqual(await evaluate('window.canceledProfileIds'), ['needs-password']);
    await evaluate(`window.runtime.EventsEmit('agent:connect_profile', 'needs-password')`);
    await click('#connectDialog [data-close]');
    assert.deepEqual(await evaluate('window.canceledProfileIds'), ['needs-password']);
    await evaluate('window.restoreTestProfiles()');

    // Command Palette
    await click('#paletteBtn');
    await until(() => evaluate("!!document.querySelector('#commandPalette')"), 'command palette');
    await shot('03-CommandPalette');
    await click('#commandPalette');

    // Connect to first host & double-click regression guard (must only create 1 tab!)
    await click('.host-row');
    await click('.host-row');
    await until(() => evaluate("!!document.querySelector('.xterm-screen') && !document.querySelector('#sftpBtn').disabled && !document.querySelector('.pane-connect-trace')"), 'active terminal session');
    const tabCountAfterDbl = await evaluate(`document.querySelectorAll('.tab-item').length`);
    assert.equal(tabCountAfterDbl, 1, 'Expected double-clicking a host to open strictly 1 tab');
    // Verify uniform icon-only right titlebar toolbar & removal of statusbar cols x rows readout
    const chromeAudit = await evaluate(`(() => {
      const ids = ['#sftpBtn', '#tunnelsBtn', '#snippetsBtn', '#aiOpen', '#paletteBtn'];
      const buttons = ids.map(sel => {
        const el = document.querySelector(sel);
        const r = el?.getBoundingClientRect();
        return { sel, text: el?.textContent?.trim() ?? null, w: r ? Math.round(r.width) : 0, h: r ? Math.round(r.height) : 0 };
      });
      const statusRight = document.querySelector('.statusbar-right')?.textContent || '';
      const legacyOverlay = document.querySelectorAll('.pane-connecting-center, .pane-loading-track').length;
      return { buttons, statusHasGridSize: /\\d+×\\d+/.test(statusRight), legacyOverlay };
    })()`);
    assert.ok(
      chromeAudit.buttons.every(b => b.text === '' && Math.abs(b.w - 28) <= 1 && Math.abs(b.h - 28) <= 1),
      'Expected uniform 28x28 icon-only titlebar buttons, got ' + JSON.stringify(chromeAudit.buttons),
    );
    assert.equal(chromeAudit.statusHasGridSize, false, 'Expected statusbar cols×rows readout removed');
    assert.equal(chromeAudit.legacyOverlay, 0, 'Expected legacy connecting overlays removed');
    // Verify ultra-quiet online indicator (no tab green bar / no "在线" pill; single quiet right-edge dot in sidebar)
    const statusIndicatorCheck = await evaluate(`(() => ({
      legacyLoud: document.querySelectorAll('.tab-status-bar, .host-live-pill').length,
      quietDot: document.querySelectorAll('.host-row.live .host-live-dot').length,
    }))()`);
    assert.deepEqual(statusIndicatorCheck, { legacyLoud: 0, quietDot: 1 }, 'Expected ultra-quiet online indicator');
    // Verify bottom sidebar-footer and statusbar share identical top edge & height
    const barAlign = await evaluate(`(() => { const f = document.querySelector('.sidebar-footer')?.getBoundingClientRect(); const s = document.querySelector('.statusbar')?.getBoundingClientRect(); return f && s ? { fTop: f.top, sTop: s.top, fH: f.height, sH: s.height } : null; })()`);
    assert.ok(barAlign && Math.abs(barAlign.fTop - barAlign.sTop) <= 1 && Math.abs(barAlign.fH - barAlign.sH) <= 1, 'Expected aligned bottom strip, got ' + JSON.stringify(barAlign));

    await sleep(900); // let telemetry poll populate
    await click('#sftpBtn');
    await until(() => evaluate("document.querySelectorAll('.sftp-row').length >= 4"), 'SFTP drawer files');
    const sftpProCheck = await evaluate(`(() => ({
      hasResizeHandle: !!document.querySelector('#filesPane .sftp-resize-handle'),
      hasBookmarksBtn: !!document.querySelector('#filesPane button[aria-label="目录书签"]'),
      hasSortSelect: !!document.querySelector('#filesPane button[aria-label="排序方式"]'),
      hasFooter: !!document.querySelector('#filesPane .sftp-footer'),
    }))()`);
    assert.deepEqual(sftpProCheck, { hasResizeHandle: true, hasBookmarksBtn: true, hasSortSelect: true, hasFooter: true }, 'Expected upgraded SFTP drawer chrome');
    await shot('04-Terminal-SFTP-Light');

    // Exercise SFTP bookmarks popover
    await click('#filesPane button[aria-label="目录书签"]');
    await until(() => evaluate("!!document.querySelector('.sftp-bookmark-popover')"), 'SFTP bookmarks popover');
    await click('#filesPane button[aria-label="目录书签"]');
    await until(() => evaluate("!document.querySelector('.sftp-bookmark-popover')"), 'closed SFTP bookmarks popover');

    // Exercise SFTP context menu -> 3x3 visual chmod matrix
    await evaluate(`(() => {
      const row = [...document.querySelectorAll('.sftp-row')].find(r => r.querySelector('.sftp-row-name')?.title === 'docker-compose.yml');
      row?.querySelector('button[aria-label="更多操作"]')?.click();
    })()`);
    await until(() => evaluate("!!document.querySelector('.sftp-ctx-menu')"), 'SFTP row context menu');
    await evaluate(`[...document.querySelectorAll('.sftp-ctx-menu [role="menuitem"]')].find(el => el.textContent.includes('修改权限'))?.click()`);
    await until(() => evaluate("document.querySelectorAll('.sftp-chmod-matrix input[type=\"checkbox\"]').length === 9"), 'SFTP 3x3 chmod matrix');
    assert.equal(await evaluate("document.querySelector('.sftp-chmod-octal')?.value"), '644');
    await click('.sftp-chmod-matrix input[aria-label="所有者 (Owner) 执行"]');
    assert.equal(await evaluate("document.querySelector('.sftp-chmod-octal')?.value"), '744', 'Toggling owner execute must update octal to 744');
    await shot('22-SFTP-Chmod-Matrix');
    await click('.sftp-modal-sheet button[aria-label="关闭"]');
    await until(() => evaluate("!document.querySelector('.sftp-modal-sheet')"), 'closed chmod modal');

    // Exercise SFTP context menu -> Properties detail modal
    await evaluate(`(() => {
      const row = [...document.querySelectorAll('.sftp-row')].find(r => r.querySelector('.sftp-row-name')?.title === 'current');
      row?.querySelector('button[aria-label="更多操作"]')?.click();
    })()`);
    await until(() => evaluate("!!document.querySelector('.sftp-ctx-menu')"), 'SFTP symlink context menu');
    await evaluate(`[...document.querySelectorAll('.sftp-ctx-menu [role="menuitem"]')].find(el => el.textContent.includes('属性详情'))?.click()`);
    await until(() => evaluate("(document.querySelector('.sftp-prop-grid')?.textContent || '').includes('/home/deploy/releases/20261127')"), 'SFTP properties detail');
    await shot('23-SFTP-Properties');
    await click('.sftp-modal-sheet button[aria-label="关闭"]');
    await until(() => evaluate("!document.querySelector('.sftp-modal-sheet')"), 'closed properties modal');

    // Exercise CodeMirror 6 Remote Editor (Light theme + YAML syntax highlighting)
    await evaluate(`(() => {
      const row = [...document.querySelectorAll('.sftp-row')].find(r => r.querySelector('.sftp-row-name')?.title === 'docker-compose.yml');
      row?.querySelector('button[title="在线编辑"]')?.click();
    })()`);
    await until(() => evaluate("!!document.querySelector('.remote-editor-cm-host .cm-editor .cm-content') && document.querySelectorAll('.cm-lineNumbers .cm-gutterElement').length >= 10"), 'CodeMirror 6 editor mounted');
    const cmLightCheck = await evaluate(`(() => ({
      filename: document.querySelector('.remote-editor-filename')?.textContent?.trim(),
      lang: document.querySelector('.remote-editor-lang-select')?.textContent?.trim(),
      footer: document.querySelector('.remote-editor-footer')?.textContent || '',
      lineCount: document.querySelectorAll('.cm-line').length,
      styledSpans: document.querySelectorAll('.cm-content span[class]').length,
    }))()`);
    assert.equal(cmLightCheck.filename, 'docker-compose.yml');
    assert.equal(cmLightCheck.lang, 'YAML', 'Expected YAML auto-detection, got ' + JSON.stringify(cmLightCheck));
    assert.ok(cmLightCheck.footer.includes('行 1, 列 1') && cmLightCheck.footer.includes('LF') && cmLightCheck.footer.includes('UTF-8'), 'Expected CodeMirror 6 footer metadata, got ' + cmLightCheck.footer);
    assert.ok(cmLightCheck.styledSpans > 5, 'Expected CodeMirror 6 syntax highlighting spans, got ' + cmLightCheck.styledSpans);
    await shot('24-Remote-Editor-Light');
    await click('.modal-sheet.editor button[aria-label="关闭"]');
    await until(() => evaluate("!document.querySelector('.modal-sheet.editor')"), 'closed CodeMirror 6 light editor');

    // A failed read must never turn the save shortcut into an empty file write.
    await evaluate(`(() => {
      const api = window.go.main.App;
      const original = api.ReadRemoteText;
      api.ReadRemoteText = async () => {
        api.ReadRemoteText = original;
        throw new Error('Read denied for test');
      };
      window.editorWrites = 0;
      api.WriteRemoteText = async () => { window.editorWrites++; };
      const row = [...document.querySelectorAll('.sftp-row')].find(r => r.querySelector('.sftp-row-name')?.title === 'nginx.conf');
      row?.querySelector('button[title="在线编辑"]')?.click();
    })()`);
    await until(() => evaluate("!!document.querySelector('.modal-sheet.editor .error-text')"), 'editor read error');
    await evaluate(`window.dispatchEvent(new KeyboardEvent('keydown', { key: 's', ctrlKey: true, cancelable: true }))`);
    assert.equal(await evaluate('window.editorWrites'), 0, 'Saving after a failed read must preserve the remote file');
    await click('.modal-sheet.editor button[aria-label="关闭"]');

    // Complete an old refresh after the user has already navigated to the parent.
    await evaluate(`(() => {
      const api = window.go.main.App;
      const original = api.ListFiles;
      api.ListFiles = () => {
        api.ListFiles = original;
        return new Promise(resolve => { window.finishOldListing = () => resolve([]); });
      };
    })()`);
    await click('#filesPane button[aria-label="刷新"]');
    await until(() => evaluate("typeof window.finishOldListing === 'function'"), 'deferred SFTP refresh');
    await click('#filesPane button[aria-label="上一级"]');
    const pathSelector = '#filesPane input[aria-label="远程路径"]';
    await until(() => evaluate(`document.querySelector('${pathSelector}').value === '/home/deploy'`), 'parent SFTP directory');
    await evaluate(`(async () => {
      window.finishOldListing();
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    })()`);
    assert.equal(await evaluate(`document.querySelector('${pathSelector}').value`), '/home/deploy', 'Late results must preserve the current directory');
    assert.ok(await evaluate("document.querySelectorAll('.sftp-row').length >= 4"), 'Late results must preserve the current file list');
    await click('#filesPane button[aria-label="主目录"]');
    await verifyLargeDirectory(evaluate, until, { click });

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

    // Settings Sheet Light + SSH/KnownHosts + Agent Governance + About + Dark
    await click('#settingsBtn');
    await until(() => evaluate("!!document.querySelector('#settingsDialog')"), 'settings modal');
    await shot('07-Settings-Light');

    await evaluate(`document.querySelector('.settings-nav-btn:nth-child(2)').click()`);
    await until(() => evaluate("!!document.querySelector('#settingsDialog .pref-group > .pref-select-header input[aria-label=\"全选主机指纹\"]')"), 'known_hosts select header');
    const knownHostsLayout = await evaluate(`(() => {
      const strayBtns = document.querySelectorAll('#settingsDialog .pref-section-head button').length;
      const master = document.querySelector('#settingsDialog .pref-select-header input[aria-label="全选主机指纹"]');
      const firstRowCb = document.querySelector('#settingsDialog .pref-group .pref-row input[type="checkbox"]');
      return {
        strayBtns,
        masterLeft: master ? master.getBoundingClientRect().left : -1,
        rowLeft: firstRowCb ? firstRowCb.getBoundingClientRect().left : -1,
      };
    })()`);
    assert.equal(knownHostsLayout.strayBtns, 0, 'Expected zero stray buttons in known_hosts .pref-section-head, got ' + JSON.stringify(knownHostsLayout));
    assert.ok(
      knownHostsLayout.masterLeft > 0 && Math.abs(knownHostsLayout.masterLeft - knownHostsLayout.rowLeft) <= 1.5,
      'Expected master checkbox aligned vertically with row checkboxes: ' + JSON.stringify(knownHostsLayout),
    );
    await click('#settingsDialog .pref-group .pref-row input[type="checkbox"]');
    const partialKnownState = await evaluate(`(() => {
      const m = document.querySelector('#settingsDialog .pref-select-header input[aria-label="全选主机指纹"]');
      const txt = document.querySelector('#settingsDialog .pref-select-header')?.textContent || '';
      return { checked: Boolean(m?.checked), indeterminate: Boolean(m?.indeterminate), hasCount: txt.includes('已选 1 / 2') && txt.includes('移除所选 (1)') };
    })()`);
    assert.deepEqual(partialKnownState, { checked: false, indeterminate: true, hasCount: true }, 'Expected indeterminate master checkbox when 1/2 known_hosts selected');
    await click('#settingsDialog .pref-select-header input[aria-label="全选主机指纹"]');
    assert.equal(await evaluate(`document.querySelector('#settingsDialog .pref-select-header input[aria-label="全选主机指纹"]')?.checked`), true, 'Master checkbox should select all');
    await click('#settingsDialog .pref-select-header input[aria-label="全选主机指纹"]');
    assert.equal(await evaluate(`document.querySelector('#settingsDialog .pref-select-header input[aria-label="全选主机指纹"]')?.checked`), false, 'Master checkbox should deselect all');
    await sleep(180);
    await shot('13-Settings-SSH-KnownHosts');

    // Settings -> SSH Keys right-edge alignment & screenshot
    await evaluate(`document.querySelector('.settings-nav-btn:nth-child(3)').click()`);
    await until(() => evaluate("!!document.querySelector('#settingsDialog .pref-section-head button') && document.querySelectorAll('#settingsDialog .pref-group .pref-row button').length >= 2"), 'SSH keys tab');
    await sleep(180);
    const keysAlign = await evaluate(`(() => {
      const genBtn = document.querySelector('#settingsDialog .pref-section-head button');
      const rowBtns = [...document.querySelectorAll('#settingsDialog .pref-group .pref-row button')];
      const deployBtn = rowBtns[rowBtns.length - 1];
      return {
        genText: genBtn?.textContent?.trim(),
        deployText: deployBtn?.textContent?.trim(),
        genRight: genBtn ? genBtn.getBoundingClientRect().right : -1,
        deployRight: deployBtn ? deployBtn.getBoundingClientRect().right : -1,
      };
    })()`);
    assert.ok(
      keysAlign.genRight > 0 && Math.abs(keysAlign.genRight - keysAlign.deployRight) <= 1.5,
      'Expected SSH keys Generate & Deploy buttons right edges aligned: ' + JSON.stringify(keysAlign),
    );
    await shot('20-Settings-Keys');

    await evaluate(`document.querySelector('.settings-nav-btn:nth-child(4)').click()`);
    await until(() => evaluate("document.querySelector('#model')?.value === 'claude-sonnet-5-5' && !!document.querySelector('#fetchModelsBtn')"), 'AI API tab with latest default model & refresh button');
    await click('#fetchModelsBtn');
    await until(() => evaluate("document.querySelectorAll('#aiModelMenu [role=\"option\"]').length === 4 && (document.querySelector('#aiModelMenu')?.textContent || '').includes('已获取 4 个在线模型')"), 'fetched AI models dropdown');
    await sleep(150);
    await shot('17-Settings-AI-API');
    await click('#aiModelMenu [role="option"]:nth-child(3)');
    assert.equal(await evaluate("document.querySelector('#model')?.value"), 'claude-opus-5-5', 'Selecting a refreshed model option must update #model');
    assert.equal(await evaluate("!!document.querySelector('#aiModelMenu')"), false, 'Selecting a model option must close #aiModelMenu');
    await evaluate(`document.querySelector('#settingsDialog [data-ai-mode="cli"]').click()`);
    await sleep(200);
    await shot('15-Settings-AI-CLI');

    await evaluate(`document.querySelector('.settings-nav-btn:nth-child(7)').click()`);
    await until(() => evaluate(`document.querySelector('#settingsDialog').textContent.includes(${JSON.stringify('v' + expectedVersion)})`), 'about version');
    await sleep(200);
    const aboutAlign = await evaluate(`(() => {
      const btns = [...document.querySelectorAll('#settingsDialog .btn-pref-action')].map(el => ({
        text: el.textContent.trim(),
        width: el.getBoundingClientRect().width,
        right: el.getBoundingClientRect().right,
      }));
      return btns;
    })()`);
    assert.ok(
      aboutAlign.length >= 3 &&
        aboutAlign.every(b => Math.abs(b.width - aboutAlign[0].width) <= 1.5 && Math.abs(b.right - aboutAlign[0].right) <= 1.5),
      'Expected About tab vertical action buttons aligned in width and right edge, got ' + JSON.stringify(aboutAlign),
    );
    await shot('14-Settings-About');

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
    // Verify syntax highlighting toggle
    const syntaxSwitchChecked = await evaluate(`(() => {
      const row = [...document.querySelectorAll('#settingsDialog .pref-row')].find(r => r.textContent?.includes('终端语法高亮'));
      return row?.querySelector('button[role="switch"]')?.getAttribute('aria-checked');
    })()`);
    assert.equal(syntaxSwitchChecked, 'true', 'Expected 终端语法高亮 switch to default to enabled');
    await evaluate(`(() => {
      const row = [...document.querySelectorAll('#settingsDialog .pref-row')].find(r => r.textContent?.includes('终端语法高亮'));
      row?.querySelector('button[role="switch"]')?.click();
    })()`);
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.syntaxHighlight === false)"), 'persisted syntaxHighlight=false');
    await evaluate(`(() => {
      const row = [...document.querySelectorAll('#settingsDialog .pref-row')].find(r => r.textContent?.includes('终端语法高亮'));
      row?.querySelector('button[role="switch"]')?.click();
    })()`);
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.syntaxHighlight === true)"), 'restored syntaxHighlight=true');

    // Verify Radix Select (终端字体) opens cleanly and updates fontFamily
    await until(() => evaluate("!!document.querySelector('#terminalFontSelect')"), 'Radix font select trigger');
    await evaluate(`document.querySelector('#terminalFontSelect')?.scrollIntoView({ block: 'center' })`);
    await sleep(120);
    await click('#terminalFontSelect');
    await until(() => evaluate("document.querySelectorAll('[data-radix-select-viewport] [role=\"option\"]').length >= 5"), 'Radix font select options');
    await click('[data-radix-select-viewport] [role="option"]:nth-child(3)');
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.fontFamily.includes('JetBrains Mono'))"), 'selected JetBrains Mono via Radix Select');
    await click('#terminalFontSelect');
    await until(() => evaluate("document.querySelectorAll('[data-radix-select-viewport] [role=\"option\"]').length >= 5"), 'reopened Radix font select');
    await click('[data-radix-select-viewport] [role="option"]:nth-child(1)');
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.fontFamily.includes('SF Mono'))"), 'restored default font via Radix Select');
    await evaluate(`document.querySelector('#settingsDialog .settings-content').scrollTop = 0`);
    await sleep(200);
    await shot('08-Settings-Dark');
    await click('#settingsDialog button[aria-label="关闭设置"]');
    await sleep(250);
    // Verify restored #1C2028 dark terminal background blending cleanly with chrome
    const darkTermBg = await evaluate(`getComputedStyle(document.querySelector('.pane-xterm-wrap')).backgroundColor`);
    assert.equal(darkTermBg, 'rgb(28, 32, 40)', 'Expected Termius Dark canvas background restored to #1C2028, got ' + darkTermBg);
    await shot('09-Terminal-Dark');

    // Open SFTP + CodeMirror 6 Remote Editor in Dark theme (Nginx config highlighting + #1C2028 canvas)
    await click('#sftpBtn');
    await until(() => evaluate("document.querySelectorAll('.sftp-row').length >= 4"), 'SFTP drawer in dark theme');
    await evaluate(`(() => {
      const row = [...document.querySelectorAll('.sftp-row')].find(r => r.querySelector('.sftp-row-name')?.title === 'nginx.conf');
      row?.querySelector('button[title="在线编辑"]')?.click();
    })()`);
    await until(() => evaluate("!!document.querySelector('.remote-editor-cm-host .cm-editor .cm-content') && document.querySelectorAll('.cm-lineNumbers .cm-gutterElement').length >= 10"), 'CodeMirror 6 dark editor');
    const cmDarkCheck = await evaluate(`(() => ({
      lang: document.querySelector('.remote-editor-lang-select')?.textContent?.trim(),
      bg: getComputedStyle(document.querySelector('.remote-editor-cm-host .cm-editor')).backgroundColor,
    }))()`);
    assert.equal(cmDarkCheck.lang, 'Nginx', 'Expected Nginx auto-detection, got ' + JSON.stringify(cmDarkCheck));
    assert.equal(cmDarkCheck.bg, 'rgb(28, 32, 40)', 'Expected Dark CodeMirror 6 editor canvas #1C2028, got ' + cmDarkCheck.bg);
    await shot('25-Remote-Editor-Dark');
    await click('.modal-sheet.editor button[aria-label="关闭"]');
    await until(() => evaluate("!document.querySelector('.modal-sheet.editor')"), 'closed dark editor');
    await click('#sftpBtn');
    await until(() => evaluate("!document.querySelector('#filesPane')"), 'closed dark SFTP drawer');

    // Verify inline SSH connection trace & error diagnosis UI (zero top-left corner cursor on initial failure)
    await evaluate(`(() => {
      const api = window.go.main.App;
      const origProfiles = api.Profiles;
      window.restoreFailProfile = () => { api.Profiles = origProfiles; };
      api.Profiles = async () => [
        ...(await origProfiles()),
        { id: 'fail-test', name: 'Staging DB', host: '10.24.0.99', port: 22, user: 'ops', auth: 'agent', keepAliveSec: 30 }
      ];
      window.runtime.EventsEmit('agent:connect_profile', 'fail-test');
    })()`);
    await until(() => evaluate("!!document.querySelector('.pane-connect-trace .pane-connect-error-title')"), 'connect error trace overlay');
    const traceAudit = await evaluate(`(() => {
      const host = document.querySelector('.pane-box.focused .pane-xterm-host');
      const title = document.querySelector('.pane-connect-error-title')?.textContent?.trim() || '';
      const raw = document.querySelector('.pane-connect-raw')?.textContent?.trim() || '';
      const btns = [...document.querySelectorAll('.pane-connect-actions button')].map(b => b.textContent.trim());
      const errSteps = document.querySelectorAll('.pane-connect-step.is-error').length;
      return {
        xtermHidden: Boolean(host?.classList.contains('is-hidden')),
        title,
        hasRaw: raw.includes('10.24.0.99:22'),
        btns,
        errSteps,
      };
    })()`);
    assert.equal(traceAudit.xtermHidden, true, 'Expected xterm host hidden before first connection (no green corner cursor)');
    assert.ok(traceAudit.title.includes('连接被拒绝'), 'Expected clear Chinese error diagnosis, got ' + JSON.stringify(traceAudit));
    assert.equal(traceAudit.hasRaw, true, 'Expected raw error detail shown');
    assert.ok(traceAudit.btns.includes('重新连接') && traceAudit.btns.includes('编辑配置'), 'Expected Reconnect + Edit Profile buttons, got ' + JSON.stringify(traceAudit.btns));
    assert.ok(traceAudit.errSteps >= 1, 'Expected at least 1 failed connection step');
    await shot('26-Connect-Error-Trace');
    await evaluate('window.restoreFailProfile()');

    const { verifySplitWorkspace } = await import('./split-workspace.mjs');
    await verifySplitWorkspace({ call, evaluate, until, click });
    const exportRect = await verifyHostExport({ call, evaluate, until, click, shot });
    await verifyEmptyHostExport({ call, evaluate, until, click, expectedRect: exportRect });

    assert.deepEqual(exceptions, [], 'Expected zero uncaught runtime exceptions');
    assert.deepEqual(consoleErrors, [], 'Expected zero console.error messages');
    console.log('[Stage 2] Verified all interactive UI views, zero exceptions/console errors, and captured 26 Retina screenshots.');
  } finally {
    socket?.close();
    if (edge.exitCode === null) edge.kill();
    server.close();
  }
}

await verifyNativeBinary();
await verifyFrontendUI();
