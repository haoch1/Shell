// End-to-end Windows verification for Shell v2:
// 1. Launches the real compiled Shell.exe with an isolated profile and verifies Win32 window + WebView2 renderer startup & clean exit.
// 2. Renders the exact built frontend/dist bundle in headless Edge (1280x820 @2x) via CDP, asserts zero errors, exercises all views, and captures UI screenshots.
import assert from 'node:assert/strict';
import { verifyLargeDirectory } from './sftp-large-directory.mjs';
import { verifyHostExport, verifyEmptyHostExport } from './host-export.mjs';
import { execFile, spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const { version: expectedVersion } = JSON.parse(await readFile(new URL('../../frontend/package.json', import.meta.url), 'utf8'));
function cliArg(flag) {
  const idx = process.argv.indexOf(flag);
  return idx !== -1 && process.argv[idx + 1] ? process.argv[idx + 1] : '';
}
const snapshotDir = path.resolve(cliArg('--screenshots') || cliArg('--out-dir') || process.env.SHELL_SMOKE_DIR || 'build/preview');
await mkdir(snapshotDir, { recursive: true });

// =========================================================================
// Stage 1: Real Shell.exe native WebView2 startup & clean shutdown test
// =========================================================================
async function verifyNativeBinary() {
  const exePath = path.resolve(cliArg('--exe') || 'build/Shell-Windows-x64/Shell.exe');
  assert.ok(existsSync(exePath), 'Expected compiled Shell.exe at ' + exePath);

  const testProfile = await mkdtemp(path.join(process.env.RUNNER_TEMP || os.tmpdir(), 'shell-native-'));
  const store = path.join(testProfile, 'Shell');
  await mkdir(path.join(store, 'config'), { recursive: true });
  await writeFile(path.join(store, 'config', 'agent.json'), JSON.stringify({ enabled: true, port: await freePort() }));
  const env = { ...process.env, LOCALAPPDATA: testProfile, APPDATA: testProfile, SHELL_EXIT_ON_CLOSE: '1' };
  const cliVersion = await execFileAsync(exePath, ['--version'], { env, timeout: 8000, windowsHide: true });
  assert.equal(cliVersion.stdout.trim(), `Shell ${expectedVersion}`);
  const runtimeFile = path.join(store, 'runtime', 'bridge.json');
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

    // Verify clean v1.6.0 5-folder data layout (ZERO loose files in Shell/ root!)
    const allowedRootDirs = new Set(['config', 'security', 'logs', 'runtime', 'cache']);
    const liveRootEntries = await readdir(store, { withFileTypes: true });
    const liveRootFiles = liveRootEntries.filter(e => !e.isDirectory()).map(e => e.name);
    const liveRootDirs = liveRootEntries.filter(e => e.isDirectory()).map(e => e.name);
    assert.deepEqual(liveRootFiles, [], 'Expected ZERO loose files in Shell/ root while running, got ' + JSON.stringify(liveRootFiles));
    assert.ok(
      liveRootDirs.every(d => allowedRootDirs.has(d)),
      'Expected only clean v1.6.0 subdirectories in Shell/ root, got ' + JSON.stringify(liveRootDirs),
    );
    assert.ok(existsSync(path.join(store, 'config', 'agent.json')), 'Expected config/agent.json');
    assert.ok(existsSync(path.join(store, 'security', 'agent-token.dpapi')), 'Expected security/agent-token.dpapi');
    assert.ok(existsSync(path.join(store, 'runtime', 'bridge.json')), 'Expected runtime/bridge.json');
    assert.ok(existsSync(path.join(store, 'cache', 'WebView2')), 'Expected cache/WebView2/');

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

    // Verify shutdown runtime cleanup left ZERO loose root files and cleaned runtime/bridge.json
    const exitRootEntries = await readdir(store, { withFileTypes: true });
    const exitRootFiles = exitRootEntries.filter(e => !e.isDirectory()).map(e => e.name);
    assert.deepEqual(exitRootFiles, [], 'Expected ZERO loose files in Shell/ root after clean exit, got ' + JSON.stringify(exitRootFiles));
    assert.equal(existsSync(runtimeFile), false, 'Expected runtime/bridge.json removed on clean shutdown');

    // Verify --cleanup-data refuses unrelated directories and cleanly removes isolated DataDir (without touching real HKCU registry)
    const decoyDir = await mkdtemp(path.join(process.env.RUNNER_TEMP || os.tmpdir(), 'shell-decoy-'));
    try {
      let refused = false;
      try {
        await execFileAsync(exePath, ['--cleanup-data', '--pid', '0', '--dir', decoyDir], { env, timeout: 8000, windowsHide: true });
      } catch (err) {
        refused = err?.code === 3;
      }
      assert.equal(refused, true, 'Expected --cleanup-data to strictly refuse non-DataDir paths with exit code 3');
      assert.equal(existsSync(decoyDir), true, 'Refused decoy directory must remain untouched');
    } finally {
      await rm(decoyDir, { recursive: true, force: true }).catch(() => {});
    }

    await execFileAsync(exePath, ['--cleanup-data', '--pid', '0', '--dir', store], { env, timeout: 15000, windowsHide: true });
    assert.equal(existsSync(store), false, 'Expected --cleanup-data to cleanly remove isolated Shell DataDir');
    console.log('[Stage 1] Verified real Shell.exe Win32 window, WebView2 renderer, clean 5-folder layout, shutdown cleanup, and --cleanup-data guard.');
  } finally {
    if (client.exitCode === null) client.kill();
    await rm(testProfile, { recursive: true, force: true }).catch(() => {});
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
    theme: 'light', terminalTheme: 'auto', defaultLocalShell: '',
    fontFamily: '"Cascadia Mono", "JetBrains Mono", "SF Mono", Consolas, "Sarasa Mono SC", "Maple Mono SC NF", "Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", "Noto Sans SC", monospace',
    fontSize: 13, lineHeight: 1.28, cursorStyle: 'bar', cursorBlink: true,
    scrollback: 10000, copyOnSelect: false, rightClickPaste: false,
    showTelemetry: true, syntaxHighlight: true, autoReconnect: true, confirmMultilinePaste: true, webglEnabled: false, defaultKeepAlive: 30, confirmOnClose: true, closeToTray: true, hideHostIp: false, groupOrder: []
  };
  let autoStartEnabled = true;
  window.__uninstallCalls = [];
  const localShells = [
    { id: 'pwsh', name: 'PowerShell 7', command: 'C:\\\\Program Files\\\\PowerShell\\\\7\\\\pwsh.exe', args: ['-NoLogo'], icon: 'powershell', category: 'shell', isDefault: true },
    { id: 'powershell', name: 'Windows PowerShell', command: 'C:\\\\Windows\\\\System32\\\\WindowsPowerShell\\\\v1.0\\\\powershell.exe', args: ['-NoLogo'], icon: 'powershell', category: 'shell' },
    { id: 'cmd', name: '命令提示符', command: 'C:\\\\Windows\\\\System32\\\\cmd.exe', icon: 'cmd', category: 'shell' },
    { id: 'git-bash', name: 'Git Bash', command: 'C:\\\\Program Files\\\\Git\\\\bin\\\\bash.exe', args: ['--login', '-i'], icon: 'git', category: 'shell' },
    { id: 'wsl:Ubuntu', name: 'WSL · Ubuntu', command: 'C:\\\\Windows\\\\System32\\\\wsl.exe', args: ['-d', 'Ubuntu', '--cd', '~'], icon: 'ubuntu', category: 'wsl' }
  ];
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
    GetAutoStart: async () => autoStartEnabled,
    SetAutoStart: async (enabled) => { autoStartEnabled = Boolean(enabled); return autoStartEnabled; },
    UninstallAndQuit: async (removeData) => { window.__uninstallCalls.push(Boolean(removeData)); },
    LocalShells: async () => localShells,
    StartLocalTerminal: async (id, shellId, cwd) => {
      const sh = localShells.find(x => x.id === shellId) || localShells[0];
      window.__lastLocalSessionId = id;
      setTimeout(() => {
        const isWsl = sh.category === 'wsl';
        const activeDir = cwd || (isWsl ? '/home/haochi/projects/shell' : 'C:\\\\Users\\\\haochi\\\\Projects\\\\Shell');
        const osc99 = '\\x1b]9;9;' + activeDir + '\\x07';
        const banner = isWsl
          ? osc99 + '\\x1b[32mhaochi@wsl-ubuntu\\x1b[0m:\\x1b[34m~/projects/shell\\x1b[0m$ git status -sb\\r\\n'
            + '\\x1b[32m## main...origin/main\\x1b[0m\\r\\n'
            + '\\x1b[32mhaochi@wsl-ubuntu\\x1b[0m:\\x1b[34m~/projects/shell\\x1b[0m$ '
          : osc99 + '\\x1b[36mPowerShell 7.5.1\\x1b[0m\\r\\n\\r\\n'
            + '\\x1b[32mPS ' + activeDir + '>\\x1b[0m git log -n 3 --oneline\\r\\n'
            + '\\x1b[33m8a4c21f\\x1b[0m feat(localpty): 原生 Windows ConPTY 本地终端与多 Shell/WSL 自动探测\\r\\n'
            + '\\x1b[33m5b1e90d\\x1b[0m feat(storage): 重构五目录本地数据架构与无痕卸载清理\\r\\n'
            + '\\x1b[33m3d7a012\\x1b[0m feat(ai): 支持本地 AI CLI 可用模型列表实时刷新\\r\\n'
            + '\\x1b[32mPS ' + activeDir + '>\\x1b[0m go test ./internal/localpty\\r\\n'
            + '\\x1b[32mok  \\tgithub.com/haoch1/Shell/internal/localpty\\t0.421s\\x1b[0m\\r\\n'
            + '\\x1b[32mPS ' + activeDir + '>\\x1b[0m ';
        emit('ssh:data', id, btoa(unescape(encodeURIComponent(banner))));
      }, 60);
      return sh;
    },
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
      if (p.id === 'progress-test') {
        emit('ssh:status', id, 'jump', '正在连接跳板机 Tokyo Production…');
        await new Promise(r => setTimeout(r, 35));
        emit('ssh:status', id, 'dial', '正在连接 ' + p.host + ':' + p.port + '…');
        await new Promise(r => setTimeout(r, 35));
        emit('ssh:status', id, 'auth', '正在校验指纹与认证…');
        await new Promise(resolve => { window.finishProgressConnect = resolve; });
        return;
      }
      if (p.id === 'fail-test') {
        emit('ssh:status', id, 'jump', '正在连接跳板机 Tokyo Production…');
        await new Promise(r => setTimeout(r, 40));
        emit('ssh:status', id, 'dial', '正在连接 10.24.0.99:22…');
        await new Promise(r => setTimeout(r, 40));
        throw new Error('dial tcp 10.24.0.99:22: connectex: No connection could be made because the target machine actively refused it.');
      }
      window.__lastConnectedSessionId = id;
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
    ProbeSession: async () => 18,
    Input: async () => {},
    Resize: async () => {},
    ConfirmHost: async () => {},
    AnswerChallenge: async () => {},
    FetchStats: async () => ({
      hostname: 'tokyo-prod-01', os: 'Ubuntu 24.04.1 LTS', kernel: 'Linux 6.8.0-45',
      uptimeSec: 1214400, load1: '0.42', load5: '0.35', load15: '0.28', processes: 142,
      cpuCores: 8, cpuPercent: 18.4, memTotalMB: 16384, memUsedMB: 4280,
      swapTotalMB: 2048, swapUsedMB: 512,
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
      { name: 'release-1.5.5.tar.gz', path: '/home/deploy/app/release-1.5.5.tar.gz', isDir: false, isSymlink: false, size: 14820400, mode: 0o644, perm: '-rw-r--r--', modTime: Math.floor(Date.now()/1000) - 21600 },
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
          '    image: ghcr.io/acme/gateway:1.5.5',
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
    FetchAIModels: async (cfg) => {
      if (cfg && cfg.mode === 'cli') {
        const cmd = String(cfg.cliCommand || '').trim().toLowerCase();
        if (cmd.includes('codex')) return ['gpt-6.2-sol', 'gpt-6.2-astra', 'gpt-6.1-terra', 'gpt-6.1-sol'];
        if (cmd.includes('agy')) return ['gemini-4-neon-high', 'gemini-4-neon-medium', 'claude-sonnet-5-5-high', 'claude-opus-5-5-high'];
        if (cmd.includes('claude')) return ['claude-sonnet-5-5', 'sonnet', 'opus', 'haiku'];
        return ['custom-local-model'];
      }
      return ['claude-sonnet-5-5', 'claude-opus-5-5', 'claude-fable-5-2', 'claude-haiku-5-5'];
    },
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
    RunAIAgent: async (sessionId, goal, history) => {
      window.__lastAiAgentCall = { sessionId, goal, history };
      emit('ai:agent_step', {
        step: 1,
        kind: 'tool_call',
        tool: 'shell_exec',
        arguments: JSON.stringify({ command: 'ss -tlnp | grep 8080' }),
      });
      await new Promise(r => setTimeout(r, 45));
      emit('ai:agent_step', {
        step: 1,
        kind: 'tool_result',
        tool: 'shell_exec',
        output: JSON.stringify({
          exitCode: 0,
          stdout: 'LISTEN 0 4096 0.0.0.0:8080 0.0.0.0:* users:(("nginx",pid=1842,fd=6))',
          stderr: '',
          durationMs: 18,
        }),
        risk: 'safe',
        decision: 'auto-allow',
        redactedCount: 1,
      });
      const summary = [
        '已定位到占用 \`8080\` 端口的进程为 **\`nginx\`**（PID \`1842\`，监听 \`0.0.0.0:8080\`），已自动脱敏 1 处敏感输出。',
        '',
        '如需校验配置并平滑重载服务可执行：',
        '\`\`\`bash',
        'sudo nginx -t && sudo systemctl reload nginx',
        '\`\`\`',
      ].join('\\n');
      emit('ai:agent_step', { step: 2, kind: 'final', output: summary });
      return summary;
    },
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

  const distDir = path.resolve(cliArg('--dist') || 'frontend/dist');
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
    '--disable-gpu-vsync',
    '--disable-background-timer-throttling',
    '--disable-backgrounding-occluded-windows',
    '--disable-renderer-backgrounding',
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
      }, 15000);
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
          `(() => { const el = document.querySelector(${JSON.stringify(selector)}); if (!el) return null; el.scrollIntoView({ block: 'nearest', inline: 'nearest' }); const r = el.getBoundingClientRect(); return r.width && r.height ? { x: r.x + r.width/2, y: r.y + r.height/2 } : null; })()`,
        ),
      selector,
    );
    await call('Input.dispatchMouseEvent', { type: 'mouseMoved', ...pt });
    await call('Input.dispatchMouseEvent', { type: 'mousePressed', button: 'left', clickCount: 1, ...pt });
    await call('Input.dispatchMouseEvent', { type: 'mouseReleased', button: 'left', clickCount: 1, ...pt });
    await sleep(220);
  }

  let shotCount = 0;
  async function shot(name) {
    const { data } = await call('Page.captureScreenshot', { format: 'png' });
    await writeFile(path.join(snapshotDir, name + '.png'), Buffer.from(data, 'base64'));
    shotCount += 1;
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
      call('Page.bringToFront'),
      call('Emulation.setFocusEmulationEnabled', { enabled: true }),
      call('Emulation.setDeviceMetricsOverride', { width: 1280, height: 820, deviceScaleFactor: 2, mobile: false }),
    ]);

    await until(() => evaluate("document.querySelectorAll('.host-row').length === 3"), 'initial workbench render');
    await sleep(250);
    await shot('01-Workbench-Light');

    // Verify v1.6.0 Workbench refinements: no .group-count, clean pointer cursor on draggable groups, local terminal entrypoints, morphing maximize icon, and calmed deep-crimson --red-solid
    const workbenchV16Audit = await evaluate(`(() => {
      const draggableHeaders = [...document.querySelectorAll('.group-header.draggable')];
      const draggableCursors = draggableHeaders.map(el => getComputedStyle(el).cursor);
      const maxIcon = document.querySelector('.win-max-icon');
      return {
        groupCountBadges: document.querySelectorAll('.group-count').length,
        draggableCount: draggableHeaders.length,
        draggableCursors,
        hasWelcomeLocalBtn: Boolean(document.querySelector('#welcomeLocalTermBtn')),
        hasWelcomeLocalMenuBtn: Boolean(document.querySelector('#welcomeLocalShellMenuBtn')),
        hasTitlebarLocalBtn: Boolean(document.querySelector('#newLocalTermBtn')),
        hasTitlebarLocalMenuBtn: Boolean(document.querySelector('#localShellMenuBtn')),
        hasMorphMaxIcon: Boolean(maxIcon && maxIcon.querySelector('.win-max-front') && maxIcon.querySelector('.win-max-back')),
        redSolid: getComputedStyle(document.documentElement).getPropertyValue('--red-solid').trim().toLowerCase(),
      };
    })()`);
    assert.equal(workbenchV16Audit.groupCountBadges, 0, 'Expected zero .group-count badges in sidebar, got ' + JSON.stringify(workbenchV16Audit));
    assert.ok(
      workbenchV16Audit.draggableCount >= 2 && workbenchV16Audit.draggableCursors.every(c => c === 'pointer'),
      'Expected cursor: pointer (not grab) on all .group-header.draggable headers, got ' + JSON.stringify(workbenchV16Audit),
    );
    assert.equal(workbenchV16Audit.hasWelcomeLocalBtn, false, 'Expected #welcomeLocalTermBtn removed from minimalist WelcomeView');
    assert.equal(workbenchV16Audit.hasWelcomeLocalMenuBtn, false, 'Expected #welcomeLocalShellMenuBtn removed from minimalist WelcomeView');
    assert.equal(workbenchV16Audit.hasTitlebarLocalBtn, false, 'Expected #newLocalTermBtn removed from simplified TitleBar');
    assert.equal(workbenchV16Audit.hasTitlebarLocalMenuBtn, false, 'Expected #localShellMenuBtn removed from simplified TitleBar');
    assert.equal(workbenchV16Audit.hasMorphMaxIcon, true, 'Expected unified morphing .win-max-icon in TitleBar');
    assert.equal(workbenchV16Audit.redSolid, '#b91c26', 'Expected calmed deep-crimson --red-solid #b91c26 in light mode, got ' + workbenchV16Audit.redSolid);

    // Verify sidebar sort button popover actually opens inside viewport and stays neutral after non-default sort
    await click('#sortHostsBtn');
    const sortRect = await evaluate(`(() => { const el = document.querySelector('.sort-popover'); if (!el) return null; const r = el.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, width: r.width, height: r.height }; })()`);
    assert.ok(sortRect && sortRect.top > 0 && sortRect.bottom < 820 && sortRect.width > 100, 'Expected visible sort popover inside viewport, got ' + JSON.stringify(sortRect));
    await click('.sort-popover .context-menu-item:nth-child(2)');
    await sleep(150);
    const chromeMinimalAudit = await evaluate(`(() => {
      const sortBtn = document.querySelector('#sortHostsBtn');
      const batchBtn = document.querySelector('#batchManageBtn');
      const toggleSidebar = document.querySelector('#toggleSidebar');
      const footer = document.querySelector('.sidebar-footer');
      const settingsBtn = footer?.querySelector('#settingsBtn.sidebar-footer-settings');
      const hideIpBtn = footer?.querySelector('#toggleHideIpBtn');
      const keysBtn = document.querySelector('#keysBtn');
      const sortStyle = sortBtn ? getComputedStyle(sortBtn) : null;
      const batchStyle = batchBtn ? getComputedStyle(batchBtn) : null;
      const fRect = footer?.getBoundingClientRect();
      const sRect = settingsBtn?.getBoundingClientRect();
      const hRect = hideIpBtn?.getBoundingClientRect();
      return {
        sortHasActive: Boolean(sortBtn?.classList.contains('active')),
        sortBgMatchesBatch: sortStyle && batchStyle ? sortStyle.backgroundColor === batchStyle.backgroundColor : false,
        sortColorMatchesBatch: sortStyle && batchStyle ? sortStyle.color === batchStyle.color : false,
        sortBorderMatchesBatch: sortStyle && batchStyle ? sortStyle.borderColor === batchStyle.borderColor : false,
        toggleSidebarHasActive: Boolean(toggleSidebar?.classList.contains('active')),
        hasKeysBtn: Boolean(keysBtn),
        settingsText: settingsBtn?.textContent?.trim() || '',
        settingsOnLeft: Boolean(fRect && sRect && hRect && sRect.left < hRect.left && sRect.right <= fRect.left + fRect.width * 0.55),
        hideIpOnRight: Boolean(fRect && hRect && hRect.left >= fRect.left + fRect.width * 0.55),
      };
    })()`);
    assert.deepEqual(
      chromeMinimalAudit,
      {
        sortHasActive: false,
        sortBgMatchesBatch: true,
        sortColorMatchesBatch: true,
        sortBorderMatchesBatch: true,
        toggleSidebarHasActive: false,
        hasKeysBtn: false,
        settingsText: '设置',
        settingsOnLeft: true,
        hideIpOnRight: true,
      },
      'Expected neutral #sortHostsBtn, non-active #toggleSidebar, absent #keysBtn, and balanced left-settings / right-hideIp footer: ' + JSON.stringify(chromeMinimalAudit),
    );

    // Verify --border-control is defined in both light & dark modes, and Dark Mode toggle preserves default fontSize 13
    const lightBorderControl = await evaluate("getComputedStyle(document.documentElement).getPropertyValue('--border-control').trim()");
    assert.ok(lightBorderControl, 'Expected --border-control CSS token defined in light mode');
    await click('button[aria-label="切换深浅色外观"]');
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.theme === 'dark' && s.fontSize === 13)"), 'persisted dark theme and preserved font size 13');
    const darkBorderControl = await evaluate("getComputedStyle(document.documentElement).getPropertyValue('--border-control').trim()");
    assert.ok(darkBorderControl, 'Expected --border-control CSS token defined in dark mode');
    await shot('16-Workbench-Dark');

    // Exercise Command Palette (+) in Dark Mode: verify 服务器 precedes 本地终端, capture 44-CommandPalette-Servers-First, and verify precise shell keyword matching
    await click('#newTabBtn');
    await until(() => evaluate("!!document.querySelector('#commandPalette')"), 'dark command palette via #newTabBtn');
    await sleep(160);
    const darkPaletteOrder = await evaluate(`(() => {
      const box = document.querySelector('#commandPalette .spotlight-results');
      const titles = [...(box?.querySelectorAll('.spotlight-group-title') || [])];
      return titles.map(el => ({ text: el.textContent.trim(), top: Math.round(el.getBoundingClientRect().top) }));
    })()`);
    const serversTitleIdx = darkPaletteOrder.findIndex(t => t.text === '服务器');
    const localTitleIdx = darkPaletteOrder.findIndex(t => t.text === '本地终端');
    assert.ok(
      serversTitleIdx === 0 && localTitleIdx === 1 && darkPaletteOrder[serversTitleIdx].top < darkPaletteOrder[localTitleIdx].top,
      'Expected 服务器 section strictly before 本地终端 in + Command Palette, got ' + JSON.stringify(darkPaletteOrder),
    );
    await shot('44-CommandPalette-Servers-First');
    const setPaletteQuery = async (val) => {
      await evaluate(`(() => {
        const input = document.querySelector('#commandPalette input');
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, ${JSON.stringify(val)});
        input.dispatchEvent(new Event('input', { bubbles: true }));
      })()`);
      await sleep(80);
      return evaluate(`[...document.querySelectorAll('#commandPalette [data-action-id^="local-"]')].map(el => el.getAttribute('data-action-id'))`);
    };
    assert.deepEqual(await setPaletteQuery('cmd'), ['local-cmd'], 'Expected "cmd" search to match only local-cmd');
    assert.deepEqual(await setPaletteQuery('git'), ['local-git-bash'], 'Expected "git" search to match only local-git-bash');
    assert.deepEqual(await setPaletteQuery('wsl'), ['local-wsl:Ubuntu'], 'Expected "wsl" search to match only local-wsl:Ubuntu');
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('#commandPalette')"), 'closed dark command palette');

    await click('button[aria-label="切换深浅色外观"]');
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.theme === 'light')"), 'persisted light theme');

    // Verify IP privacy hiding toggle (#toggleHideIpBtn) omits IPs cleanly with zero layout jump
    const beforeHideLayout = await evaluate(`(() => ({
      rowHeights: [...document.querySelectorAll('.host-row')].map(el => el.getBoundingClientRect().height),
      cardHeights: [...document.querySelectorAll('.recent-card')].map(el => el.getBoundingClientRect().height),
    }))()`);
    await click('#toggleHideIpBtn');
    await until(() => evaluate("window.go.main.App.GetSettings().then(s => s.hideHostIp === true)"), 'persisted hideHostIp=true');
    await call('Input.dispatchMouseEvent', { type: 'mouseMoved', x: 0, y: 0 });
    await sleep(240);
    const hiddenPrivacy = await evaluate(`(() => {
      const hideBtn = document.querySelector('#toggleHideIpBtn');
      const hideStyle = hideBtn ? getComputedStyle(hideBtn) : null;
      return {
        sidebarText: document.querySelector('.sidebar-list')?.textContent || '',
        recentText: document.querySelector('.recent-grid')?.textContent || '',
        hiddenSubCount: document.querySelectorAll('.host-row .host-sub[data-ip-hidden="true"]').length,
        rowHeights: [...document.querySelectorAll('.host-row')].map(el => el.getBoundingClientRect().height),
        cardHeights: [...document.querySelectorAll('.recent-card')].map(el => el.getBoundingClientRect().height),
        hideBtnHasActive: Boolean(hideBtn?.classList.contains('active')),
        hideBtnBg: hideStyle?.backgroundColor || '',
        hideBtnBorderWidth: hideStyle?.borderTopWidth || '',
        hideBtnBorderColor: hideStyle?.borderTopColor || '',
        hideBtnColor: hideStyle?.color || '',
      };
    })()`);
    assert.equal(hiddenPrivacy.hideBtnHasActive, false, 'Expected #toggleHideIpBtn not to carry blue .active class when toggled on');
    assert.equal(hiddenPrivacy.hideBtnBg, 'rgba(0, 0, 0, 0)', 'Expected zero blue background box on #toggleHideIpBtn when toggled on, got ' + hiddenPrivacy.hideBtnBg);
    assert.ok(
      hiddenPrivacy.hideBtnBorderWidth === '0px' || hiddenPrivacy.hideBtnBorderColor === 'rgba(0, 0, 0, 0)',
      'Expected zero visible border box on #toggleHideIpBtn when toggled on, got ' + JSON.stringify({ width: hiddenPrivacy.hideBtnBorderWidth, color: hiddenPrivacy.hideBtnBorderColor }),
    );
    assert.ok(
      hiddenPrivacy.hideBtnColor !== 'rgb(9, 105, 218)' &&
        hiddenPrivacy.hideBtnColor !== 'rgb(47, 129, 247)' &&
        hiddenPrivacy.hideBtnColor !== 'rgb(0, 113, 227)' &&
        hiddenPrivacy.hideBtnColor !== 'rgb(10, 132, 255)',
      'Expected neutral icon color on #toggleHideIpBtn when toggled on, got ' + hiddenPrivacy.hideBtnColor,
    );
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
    await click('#batchManageBtn');
    await until(() => evaluate("!!document.querySelector('.sidebar-select-header')"), 'sidebar batch select header');
    await sleep(260);
    const batchMetrics = await evaluate(`(() => {
      const hdr = document.querySelector('.sidebar-select-header')?.getBoundingClientRect();
      const masterCb = document.querySelector('.sidebar-select-header input[type="checkbox"]')?.getBoundingClientRect();
      const firstRowCb = document.querySelector('.host-row input[type="checkbox"]')?.getBoundingClientRect();
      const rowCbs = [...document.querySelectorAll('.host-row input[type="checkbox"]')].filter(
        el => getComputedStyle(el).appearance === 'none',
      ).length;
      const footer = document.querySelector('.sidebar-footer.batch-bar')?.getBoundingClientRect();
      const btns = [...document.querySelectorAll('.sidebar-footer.batch-bar .batch-action-btn')].map(el => {
        const r = el.getBoundingClientRect();
        return {
          text: el.textContent.trim(),
          width: r.width,
          top: r.top,
          bottom: r.bottom,
          disabled: el.disabled,
        };
      });
      return {
        hdr: hdr ? { top: hdr.top, height: hdr.height } : null,
        masterCbLeft: masterCb?.left ?? null,
        firstRowCbLeft: firstRowCb?.left ?? null,
        footer: footer ? { top: footer.top, bottom: footer.bottom, height: footer.height } : null,
        rowCbs,
        btns,
      };
    })()`);
    assert.ok(
      beforeBatchHeaderRect && batchMetrics.hdr && Math.abs(beforeBatchHeaderRect.height - batchMetrics.hdr.height) <= 2,
      'Expected zero vertical jump entering batch select mode: ' + JSON.stringify({ beforeBatchHeaderRect, after: batchMetrics.hdr }),
    );
    assert.ok(
      batchMetrics.masterCbLeft !== null && batchMetrics.firstRowCbLeft !== null && Math.abs(batchMetrics.masterCbLeft - batchMetrics.firstRowCbLeft) <= 0.5,
      'Expected master checkbox aligned pixel-for-pixel with host row checkboxes: ' + JSON.stringify(batchMetrics),
    );
    assert.equal(batchMetrics.rowCbs, 3, 'Expected custom <Checkbox> on all 3 host rows, got ' + batchMetrics.rowCbs);
    assert.equal(batchMetrics.btns.length, 2, 'Expected 2 equal-width bottom batch buttons, got ' + JSON.stringify(batchMetrics.btns));
    assert.ok(
      Math.abs(batchMetrics.btns[0].width - batchMetrics.btns[1].width) <= 2,
      'Expected equal-width bottom batch buttons, got ' + JSON.stringify(batchMetrics.btns),
    );
    assert.ok(
      batchMetrics.footer && (batchMetrics.btns[0].top - batchMetrics.footer.top) >= 7 && (batchMetrics.footer.bottom - batchMetrics.btns[0].bottom) >= 7,
      'Expected comfortable vertical breathing room in bottom batch bar: ' + JSON.stringify({ footer: batchMetrics.footer, btn: batchMetrics.btns[0] }),
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
    const batchDangerBg = await evaluate("getComputedStyle(document.querySelector('.sidebar-footer.batch-bar .btn-danger')).backgroundColor");
    assert.match(batchDangerBg, /^rgb\(\d+,\s*\d+,\s*\d+\)$/, 'Expected solid opaque rgb(...) background on batch .btn-danger (no translucent rgba wash), got ' + batchDangerBg);
    await shot('21-Sidebar-BatchSelect');
    // Verify Escape cleanly exits batch select mode
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('.sidebar-select-header')"), 'Esc exits batch select mode');

    // Verify batch host grouping (#batchGroupBtn popover: existing group + new group, restoring fixture cleanly)
    await click('#batchManageBtn');
    await click('.host-row[data-profile-id="p3"]');
    await click('#batchGroupBtn');
    const groupPopRect = await evaluate(`(() => { const el = document.querySelector('#batchGroupPopover'); if (!el) return null; const r = el.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, width: r.width }; })()`);
    assert.ok(groupPopRect && groupPopRect.top > 0 && groupPopRect.bottom < 820 && groupPopRect.width >= 180, 'Expected visible batch group popover inside viewport, got ' + JSON.stringify(groupPopRect));
    await evaluate(`[...document.querySelectorAll('#batchGroupPopover .batch-group-list .context-menu-item')].find(el => el.textContent.trim() === '生产集群')?.click()`);
    await until(() => evaluate("window.go.main.App.Profiles().then(list => list.find(x => x.id === 'p3')?.group === '生产集群')"), 'batch assign existing group');
    await click('#batchManageBtn');
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

    // Verify sidebar search box has zero shortcut badge & prominent unified split New Host CTA before opening HostModal
    const hasSidebarSearchKbd = await evaluate("Boolean(document.querySelector('.sidebar .search-box .kbd-hint'))");
    assert.equal(hasSidebarSearchKbd, false, 'Expected zero .kbd-hint shortcut badge inside .sidebar .search-box');

    const newHostStyle = await evaluate(`(() => {
      const split = document.querySelector('.sidebar-new-split');
      const btn = document.querySelector('#addHost');
      const caret = document.querySelector('#addHostMenuBtn');
      const r = btn?.getBoundingClientRect();
      return {
        splitBg: split ? getComputedStyle(split).backgroundColor : '',
        btnBg: btn ? getComputedStyle(btn).backgroundColor : '',
        btnColor: btn ? getComputedStyle(btn).color : '',
        hasCaret: Boolean(caret),
        btnCenter: r ? { x: r.x + r.width / 2, y: r.y + r.height / 2 } : null,
      };
    })()`);
    assert.equal(newHostStyle.splitBg, 'rgb(9, 105, 218)', 'Expected GitHub Light accent #0969DA .sidebar-new-split in light mode, got ' + JSON.stringify(newHostStyle));
    assert.equal(newHostStyle.btnBg, 'rgba(0, 0, 0, 0)', 'Expected transparent #addHost background inside unified .sidebar-new-split, got ' + JSON.stringify(newHostStyle));
    assert.equal(newHostStyle.btnColor, 'rgb(255, 255, 255)', 'Expected pure white text on #addHost, got ' + JSON.stringify(newHostStyle));
    assert.equal(newHostStyle.hasCaret, true, 'Expected #addHostMenuBtn dropdown caret inside split button');

    // Hovering #addHost must lift .sidebar-new-split uniformly while #addHost stays transparent (zero two-tone split disconnect)
    await call('Input.dispatchMouseEvent', { type: 'mouseMoved', ...newHostStyle.btnCenter });
    await sleep(220);
    const newHostHover = await evaluate(`(() => {
      const split = document.querySelector('.sidebar-new-split');
      const btn = document.querySelector('#addHost');
      const caret = document.querySelector('#addHostMenuBtn');
      return {
        splitHoverBg: split ? getComputedStyle(split).backgroundColor : '',
        btnHoverBg: btn ? getComputedStyle(btn).backgroundColor : '',
        caretBg: caret ? getComputedStyle(caret).backgroundColor : '',
      };
    })()`);
    assert.equal(newHostHover.splitHoverBg, 'rgb(5, 80, 174)', 'Expected .sidebar-new-split to lift uniformly to --accent-hover (#0550AE) when hovering #addHost, got ' + JSON.stringify(newHostHover));
    assert.equal(newHostHover.btnHoverBg, 'rgba(0, 0, 0, 0)', 'Expected #addHost to keep transparent background on hover (zero split-in-half disconnect), got ' + JSON.stringify(newHostHover));
    assert.equal(newHostHover.caretBg, 'rgba(0, 0, 0, 0)', 'Expected #addHostMenuBtn to remain transparent when hovering #addHost, got ' + JSON.stringify(newHostHover));
    await call('Input.dispatchMouseEvent', { type: 'mouseMoved', x: 0, y: 0 });
    await sleep(200);

    // Host modal (Basic + Jump/Proxy identical height, solid OS tiles, frosted Group ComboBox & zero vertical clipping guard)
    await click('#addHost');
    await until(() => evaluate("!!document.querySelector('#connectDialog')"), 'host modal');
    const basicMetrics = await evaluate(`(() => {
      const sheet = document.querySelector('#connectDialog .modal-sheet');
      const body = document.querySelector('#connectDialog .modal-body');
      const tileBadges = [...document.querySelectorAll('#connectDialog .icon-choice-btn.is-tile .host-icon-badge.size-sm')];
      const pickerButtons = [...document.querySelectorAll('#connectDialog .icon-choice-btn')];
      const topSet = new Set(pickerButtons.map(b => Math.round(b.getBoundingClientRect().top)));
      const colorRow = document.querySelector('#connectDialog .color-picker-row')?.getBoundingClientRect();
      const pinCb = [...document.querySelectorAll('#connectDialog label')].find(el => el.textContent.trim() === '置顶显示')?.getBoundingClientRect();
      return {
        height: sheet.getBoundingClientRect().height,
        scrollHeight: body.scrollHeight,
        clientHeight: body.clientHeight,
        solidTiles: tileBadges.length,
        singleRow: topSet.size === 1,
        hasDatalist: Boolean(document.querySelector('#connectDialog datalist')),
        pinAlignDelta: colorRow && pinCb ? Math.abs((colorRow.top + colorRow.height / 2) - (pinCb.top + pinCb.height / 2)) : 999,
      };
    })()`);
    assert.ok(
      basicMetrics.scrollHeight <= basicMetrics.clientHeight + 2,
      'Expected Basic tab in HostModal to render without vertical clipping at 1280x820, got ' + JSON.stringify(basicMetrics),
    );
    assert.equal(basicMetrics.solidTiles, 10, 'Expected 10 solid Termius-style OS tiles in HostModal icon picker, got ' + JSON.stringify(basicMetrics));
    assert.equal(basicMetrics.singleRow, true, 'Expected all OS icon choices on a single clean row, got ' + JSON.stringify(basicMetrics));
    assert.equal(basicMetrics.hasDatalist, false, 'Expected native <datalist> removed in favor of custom ComboBox');
    assert.ok(basicMetrics.pinAlignDelta <= 2, 'Expected 置顶显示 aligned vertically with .color-picker-row, got ' + JSON.stringify(basicMetrics));
    await shot('02-HostModal');
    // Verify custom frosted-glass Group ComboBox opens, lists existing groups, and closes cleanly on Escape without closing HostModal
    await click('#hostGroup');
    await until(() => evaluate("!!document.querySelector('[data-combobox-open=\"true\"]')"), 'custom group combobox dropdown');
    const groupItems = await evaluate(`[...document.querySelectorAll('[data-combobox-open="true"] .ui-combobox-item-label')].map(el => el.textContent.trim())`);
    assert.ok(groupItems.includes('生产集群') && groupItems.includes('开发测试'), 'Expected existing groups in custom ComboBox, got ' + JSON.stringify(groupItems));
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('[data-combobox-open=\"true\"]')"), 'closed group combobox on Escape');
    assert.equal(await evaluate("!!document.querySelector('#connectDialog')"), true, 'Escape on open ComboBox must keep HostModal open');
    await click('#connectDialog .modal-header .segmented button:nth-child(2)');
    await sleep(120);
    const jumpTabCheck = await evaluate(`(() => {
      const sheetH = document.querySelector('#connectDialog .modal-sheet').getBoundingClientRect().height;
      const agentRow = [...document.querySelectorAll('#connectDialog .setting-row')].find(el => el.textContent.includes('SSH Agent 转发'));
      return { height: sheetH, agentBorderBottom: agentRow ? getComputedStyle(agentRow).borderBottomWidth : '' };
    })()`);
    assert.ok(Math.abs(basicMetrics.height - jumpTabCheck.height) <= 1, 'Expected identical HostModal height across tabs: ' + basicMetrics.height + ' vs ' + jumpTabCheck.height);
    assert.equal(jumpTabCheck.agentBorderBottom, '0px', 'Expected zero stray bottom border on SSH Agent 转发 row, got ' + JSON.stringify(jumpTabCheck));
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

    // Command Palette opened from tab-strip + button (#newTabBtn): grouped sections with 服务器 first, immediately followed by 操作 -> 新建 SSH 主机
    await click('#newTabBtn');
    await until(() => evaluate("!!document.querySelector('#commandPalette')"), 'command palette via #newTabBtn');
    const paletteAudit = await evaluate(`(() => {
      const box = document.querySelector('#commandPalette .spotlight-results');
      const groups = [...(box?.querySelectorAll('.spotlight-group-title') || [])].map(el => el.textContent.trim());
      const children = [...(box?.children || [])].map(el => ({
        isGroup: el.classList.contains('spotlight-group-title'),
        text: el.textContent.trim(),
        actionId: el.getAttribute('data-action-id') || '',
      }));
      const actionsHeaderIdx = children.findIndex(c => c.isGroup && c.text === '操作');
      const firstAction = actionsHeaderIdx >= 0 ? children[actionsHeaderIdx + 1] : null;
      return {
        groups,
        firstActionId: firstAction?.actionId || '',
        firstActionText: firstAction?.text || '',
        hasKbdBadge: Boolean(box?.querySelector('.spotlight-kbd')),
      };
    })()`);
    assert.equal(paletteAudit.groups[0], '服务器', 'Expected 服务器 as first section in Command Palette, got ' + JSON.stringify(paletteAudit));
    assert.equal(paletteAudit.groups[1], '本地终端', 'Expected 本地终端 as second section in Command Palette, got ' + JSON.stringify(paletteAudit));
    assert.equal(paletteAudit.groups[2], '操作', 'Expected 操作 after 本地终端 in Command Palette, got ' + JSON.stringify(paletteAudit));
    assert.equal(paletteAudit.firstActionId, 'act-new', 'Expected 新建 SSH 主机 (act-new) first in 操作 section, got ' + JSON.stringify(paletteAudit));
    assert.ok(paletteAudit.firstActionText.includes('新建 SSH 主机'), 'Expected 新建 SSH 主机 label, got ' + JSON.stringify(paletteAudit));
    assert.equal(paletteAudit.hasKbdBadge, true, 'Expected shortcut badge (.spotlight-kbd) in Command Palette');
    await shot('03-CommandPalette');
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('#commandPalette')"), 'closed command palette');

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
      const statusLeft = document.querySelector('.statusbar-left')?.textContent || '';
      const statusRight = document.querySelector('.statusbar-right')?.textContent || '';
      const legacyOverlay = document.querySelectorAll('.pane-connecting-center, .pane-loading-track').length;
      return { buttons, statusLeft, statusHasDuration: /\\b\\d{2}:\\d{2}\\b/.test(statusLeft), statusHasGridSize: /\\d+×\\d+/.test(statusRight), legacyOverlay };
    })()`);
    assert.ok(
      chromeAudit.buttons.every(b => b.text === '' && Math.abs(b.w - 28) <= 1 && Math.abs(b.h - 28) <= 1),
      'Expected uniform 28x28 icon-only titlebar buttons, got ' + JSON.stringify(chromeAudit.buttons),
    );
    assert.equal(chromeAudit.statusHasDuration, false, 'Expected connection duration (00:xx) removed from .statusbar-left, got ' + JSON.stringify(chromeAudit.statusLeft));
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
    // Verify refactored 4-zone Telemetry Popover (3-col KPI grid, Swap bar, single-line OS, floating above statusbar)
    await until(() => evaluate("!!document.querySelector('.statusbar-right .telemetry-trigger')"), 'telemetry trigger button');
    await click('.statusbar-right .telemetry-trigger');
    await until(() => evaluate("!!document.querySelector('.telemetry-popover')"), 'telemetry popover');
    const telemetryAudit = await evaluate(`(() => {
      const pop = document.querySelector('.telemetry-popover');
      const bar = document.querySelector('.statusbar');
      const kpiGrid = pop?.querySelector('.telemetry-kpi-grid');
      const kpiCells = [...(kpiGrid?.querySelectorAll('.telemetry-kpi-cell') || [])].map(c => ({
        label: c.querySelector('.telemetry-kpi-label')?.textContent?.trim() || '',
        val: c.querySelector('.telemetry-kpi-val')?.textContent?.trim() || '',
      }));
      const osEl = pop?.querySelector('.telemetry-os');
      const osRect = osEl?.getBoundingClientRect();
      const osStyle = osEl ? getComputedStyle(osEl) : null;
      const popRect = pop?.getBoundingClientRect();
      const barRect = bar?.getBoundingClientRect();
      return {
        text: pop?.textContent || '',
        hasKpiGrid: Boolean(kpiGrid),
        kpiCells,
        osText: osEl?.textContent?.trim() || '',
        osWhiteSpace: osStyle?.whiteSpace || '',
        osHeight: osRect ? Math.round(osRect.height) : 0,
        osLineHeight: osStyle ? parseFloat(osStyle.lineHeight) || parseFloat(osStyle.fontSize) * 1.4 : 0,
        popBottom: popRect ? Math.round(popRect.bottom) : 9999,
        barTop: barRect ? Math.round(barRect.top) : 0,
      };
    })()`);
    assert.equal(telemetryAudit.hasKpiGrid, true, 'Expected .telemetry-kpi-grid in telemetry popover: ' + JSON.stringify(telemetryAudit));
    assert.ok(
      telemetryAudit.kpiCells.some(c => c.label === '进程数' && c.val === '142'),
      'Expected 进程数=142 in .telemetry-kpi-grid, got ' + JSON.stringify(telemetryAudit.kpiCells),
    );
    assert.ok(
      telemetryAudit.text.includes('Swap') && telemetryAudit.text.includes('0.5 / 2.0 GB (25%)'),
      'Expected Swap bar 0.5 / 2.0 GB (25%) in telemetry popover, got ' + telemetryAudit.text,
    );
    assert.ok(
      telemetryAudit.osWhiteSpace === 'nowrap' && telemetryAudit.osHeight > 0 && telemetryAudit.osHeight <= Math.max(22, Math.ceil(telemetryAudit.osLineHeight * 1.45)),
      'Expected single-line .telemetry-os without multi-line wrap, got ' + JSON.stringify(telemetryAudit),
    );
    assert.ok(
      telemetryAudit.popBottom < telemetryAudit.barTop,
      'Expected telemetry popover floating above .statusbar (popoverRect.bottom < statusbarRect.top), got ' + JSON.stringify(telemetryAudit),
    );
    await evaluate("document.querySelector('.telemetry-popover')?.previousElementSibling?.click()");
    await until(() => evaluate("!document.querySelector('.telemetry-popover')"), 'closed telemetry popover');

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

    // AI Drawer (direct-execution conversational flow: zero mode tabs, target bar, quick chips, Enter-to-run collapsible step + summary)
    await click('#aiOpen');
    await until(() => evaluate("!!document.querySelector('#aiDrawer')"), 'AI drawer');
    const aiEmptyAudit = await evaluate(`(() => {
      const drawerText = document.querySelector('#aiDrawer')?.textContent || '';
      return {
        segmentedCount: document.querySelectorAll('#aiDrawer .segmented').length,
        hasLegacyModeTabs: drawerText.includes('写命令') || drawerText.includes('查报错') || drawerText.includes('自动运维'),
        targetText: document.querySelector('#aiDrawer .ai-target-bar')?.textContent?.trim() || '',
        quickChipCount: document.querySelectorAll('#aiDrawer .ai-quick-chip').length,
        hasPromptInput: Boolean(document.querySelector('#aiPromptInput')),
      };
    })()`);
    assert.equal(aiEmptyAudit.segmentedCount, 0, 'Expected legacy 3-mode .segmented tabs completely removed from AIDrawer, got ' + JSON.stringify(aiEmptyAudit));
    assert.equal(aiEmptyAudit.hasLegacyModeTabs, false, 'Expected legacy 写命令/查报错/自动运维 tabs completely removed from AIDrawer, got ' + JSON.stringify(aiEmptyAudit));
    assert.ok(aiEmptyAudit.targetText.includes('Tokyo Production'), 'Expected .ai-target-bar showing active session Tokyo Production, got ' + JSON.stringify(aiEmptyAudit));
    assert.equal(aiEmptyAudit.quickChipCount, 4, 'Expected 4 contextual quick chips in AIDrawer, got ' + JSON.stringify(aiEmptyAudit));
    assert.equal(aiEmptyAudit.hasPromptInput, true, 'Expected #aiPromptInput composer textarea');
    await click('#aiPromptInput');
    await evaluate(`(() => {
      const ta = document.querySelector('#aiPromptInput');
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(ta, '排查 8080 端口占用并给出重载命令');
      ta.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    await until(
      () =>
        evaluate(
          "document.querySelectorAll('#aiDrawer .ai-step-row').length === 1 && !document.querySelector('#aiDrawer .ai-running-row') && (document.querySelector('#aiDrawer .ai-md')?.textContent || '').includes('nginx')",
        ),
      'AI drawer direct execution result via Enter',
    );
    const aiResultAudit = await evaluate(`(() => {
      const stepLabel = document.querySelector('#aiDrawer .ai-step-label')?.textContent?.trim() || '';
      const stepBadge = document.querySelector('#aiDrawer .ai-step-badge')?.textContent?.trim() || '';
      const hasDoneIcon = Boolean(document.querySelector('#aiDrawer .ai-step-icon.is-done'));
      const drawerText = document.querySelector('#aiDrawer')?.textContent || '';
      const codeBlocks = document.querySelectorAll('#aiDrawer .ai-code-block').length;
      const lastCall = window.__lastAiAgentCall || null;
      return {
        stepLabel,
        stepBadge,
        hasDoneIcon,
        hasRawJson: drawerText.includes('{"command"') || drawerText.includes('{"stdout"') || drawerText.includes('{"exitCode"'),
        codeBlocks,
        calledWithHistoryArray: Array.isArray(lastCall?.history),
      };
    })()`);
    assert.equal(aiResultAudit.stepLabel, '$ ss -tlnp | grep 8080', 'Expected compact human-readable step row label, got ' + JSON.stringify(aiResultAudit));
    assert.equal(aiResultAudit.stepBadge, '脱敏 1', 'Expected redaction badge on AI step row, got ' + JSON.stringify(aiResultAudit));
    assert.equal(aiResultAudit.hasDoneIcon, true, 'Expected success icon (.ai-step-icon.is-done) on AI step row, got ' + JSON.stringify(aiResultAudit));
    assert.equal(aiResultAudit.hasRawJson, false, 'Expected zero raw JSON exposed in AIDrawer, got ' + JSON.stringify(aiResultAudit));
    assert.equal(aiResultAudit.codeBlocks, 1, 'Expected lightweight Markdown code block rendered in summary, got ' + JSON.stringify(aiResultAudit));
    assert.equal(aiResultAudit.calledWithHistoryArray, true, 'Expected RunAIAgent called with conversation history array, got ' + JSON.stringify(aiResultAudit));
    // Verify expanding collapsible step row shows friendly formatted stdout (no raw JSON), then collapse for crisp screenshot
    await click('#aiDrawer .ai-step-row');
    await until(() => evaluate("(document.querySelector('#aiDrawer .ai-step-body')?.textContent || '').includes('LISTEN 0 4096') && (document.querySelector('#aiDrawer .ai-step-body')?.textContent || '').includes('nginx')"), 'expanded AI step output');
    assert.equal(await evaluate("(document.querySelector('#aiDrawer .ai-step-body')?.textContent || '').includes('\"stdout\"')"), false, 'Expanded shell_exec step body must render clean stdout without raw JSON');
    await click('#aiDrawer .ai-step-row');
    await until(() => evaluate("!document.querySelector('#aiDrawer .ai-step-body')"), 'collapsed AI step output');
    await sleep(150);
    await shot('06-Terminal-AI');
    await click('#aiOpen');

    // Universal Importer Modal (accessed via split New Host dropdown menu)
    await click('#addHostMenuBtn');
    await until(() => evaluate("!!document.querySelector('#addHostMenu #importSshBtn')"), 'add-host split menu for import');
    await click('#importSshBtn');
    await until(() => evaluate("!!document.querySelector('#importDialog')"), 'universal import modal');
    await click('#pickImportFilesBtn');
    await until(() => evaluate("document.querySelectorAll('#importDialog .import-host-row').length === 2"), 'imported hosts list');
    const importAlign = await evaluate(`(() => {
      const master = document.querySelector('#importDialog .select-list-header input[type="checkbox"]');
      const firstRow = document.querySelector('#importDialog .import-host-row input[type="checkbox"]');
      return {
        checked: Boolean(master?.checked),
        masterLeft: master ? master.getBoundingClientRect().left : -1,
        firstRowLeft: firstRow ? firstRow.getBoundingClientRect().left : -1,
      };
    })()`);
    assert.ok(
      importAlign.checked && importAlign.masterLeft > 0 && Math.abs(importAlign.masterLeft - importAlign.firstRowLeft) <= 0.5,
      'Expected import modal master checkbox aligned pixel-for-pixel with rows: ' + JSON.stringify(importAlign),
    );
    await sleep(150);
    await shot('10-Universal-Import');
    await click('#importDialog button[aria-label="关闭"]');

    // Settings Sheet Light + SSH/KnownHosts + Agent Governance + About + Dark
    // Regression check: hovering #settingsBtn then opening #settingsDialog must leave zero phantom tooltip above the modal backdrop
    const settingsBtnPt = await evaluate(`(() => { const r = document.querySelector('#settingsBtn')?.getBoundingClientRect(); return r ? { x: r.x + r.width / 2, y: r.y + r.height / 2 } : null; })()`);
    assert.ok(settingsBtnPt, 'Expected #settingsBtn bounding rect for tooltip regression check');
    await call('Input.dispatchMouseEvent', { type: 'mouseMoved', ...settingsBtnPt });
    await sleep(360);
    await click('#settingsBtn');
    await until(() => evaluate("!!document.querySelector('#settingsDialog')"), 'settings modal');
    await sleep(350);
    const phantomTooltipCount = await evaluate("document.querySelectorAll('.ui-tooltip-content').length");
    assert.equal(phantomTooltipCount, 0, 'Expected zero phantom .ui-tooltip-content floating above #settingsDialog backdrop, got ' + phantomTooltipCount);
    await shot('07-Settings-Light');

    await evaluate(`document.querySelector('.settings-nav-btn:nth-child(2)').click()`);
    await until(() => evaluate("!!document.querySelector('#settingsDialog .pref-group > .pref-select-header input[aria-label=\"全选主机指纹\"]')"), 'known_hosts select header');
    const sshSessionToggles = await evaluate(`(() => {
      const findSwitch = (label) => {
        const row = [...document.querySelectorAll('#settingsDialog .pref-row')].find(r => r.querySelector('.pref-row-label')?.textContent?.trim() === label);
        const sw = row?.querySelector('button[role="switch"]');
        return { found: Boolean(row && sw), checked: sw?.getAttribute('aria-checked') ?? null };
      };
      return {
        autoReconnect: findSwitch('网络波动断线后自动重连'),
        multilinePaste: findSwitch('终端多行粘贴确认'),
      };
    })()`);
    assert.deepEqual(
      sshSessionToggles,
      {
        autoReconnect: { found: true, checked: 'true' },
        multilinePaste: { found: true, checked: 'true' },
      },
      'Expected autoReconnect & confirmMultilinePaste toggles in 连接与指纹 settings tab, got ' + JSON.stringify(sshSessionToggles),
    );
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
    const knownHostsDangerBgs = await evaluate(`[...document.querySelectorAll('#settingsDialog .btn-danger')].map(el => getComputedStyle(el).backgroundColor)`);
    assert.ok(
      knownHostsDangerBgs.length >= 2 && knownHostsDangerBgs.every(bg => /^rgb\(\d+,\s*\d+,\s*\d+\)$/.test(bg)),
      'Expected solid opaque rgb(...) background on known_hosts .btn-danger buttons (no translucent rgba wash), got ' + JSON.stringify(knownHostsDangerBgs),
    );
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
    const aiInputAudit = await evaluate(`(() => {
      const modelEl = document.querySelector('#model');
      const apiKeyEl = document.querySelector('#apiKey');
      return {
        modelPaddingRight: modelEl ? parseFloat(getComputedStyle(modelEl).paddingRight) : 0,
        apiKeyTypeInitial: apiKeyEl?.type || '',
        hasEyeInitially: Boolean(document.querySelector('#apiKey ~ .input-eye-btn')),
      };
    })()`);
    assert.ok(aiInputAudit.modelPaddingRight >= 30, 'Expected #model paddingRight >= 30px, got ' + JSON.stringify(aiInputAudit));
    assert.equal(aiInputAudit.apiKeyTypeInitial, 'password', 'Expected #apiKey password initially');
    assert.equal(aiInputAudit.hasEyeInitially, false, 'Expected no eye toggle on empty #apiKey');
    await evaluate(`(() => {
      const input = document.querySelector('#apiKey');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, 'sk-test-demo-key');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    await until(() => evaluate("!!document.querySelector('#apiKey ~ .input-eye-btn')"), '#apiKey eye toggle appears on input');
    assert.ok(await evaluate("parseFloat(getComputedStyle(document.querySelector('#apiKey')).paddingRight) >= 30"), 'Expected #apiKey paddingRight >= 30 when eye button is visible');
    await click('#apiKey ~ .input-eye-btn');
    assert.equal(await evaluate("document.querySelector('#apiKey')?.type"), 'text', 'Clicking #apiKey eye toggle should reveal input');
    await evaluate(`(() => {
      const input = document.querySelector('#apiKey');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, '');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    await until(() => evaluate("!document.querySelector('#apiKey ~ .input-eye-btn') && document.querySelector('#apiKey')?.type === 'password'"), 'clearing #apiKey hides eye toggle');
    await click('#fetchModelsBtn');
    await until(() => evaluate("document.querySelectorAll('#aiModelMenu [role=\"option\"]').length === 4 && (document.querySelector('#aiModelMenu')?.textContent || '').includes('已获取 4 个在线模型')"), 'fetched AI models dropdown');
    await sleep(150);
    await shot('17-Settings-AI-API');
    await click('#aiModelMenu [role="option"]:nth-child(3)');
    assert.equal(await evaluate("document.querySelector('#model')?.value"), 'claude-opus-5-5', 'Selecting a refreshed model option must update #model');
    assert.equal(await evaluate("!!document.querySelector('#aiModelMenu')"), false, 'Selecting a model option must close #aiModelMenu');
    await evaluate(`document.querySelector('#settingsDialog [data-ai-mode="cli"]').click()`);
    await sleep(200);
    const hasCliEphemeralNotice = await evaluate("(document.querySelector('#settingsDialog')?.textContent || '').includes('已启用无痕调用，不会向 Codex、Claude Code 等工具写入会话历史')");
    assert.equal(hasCliEphemeralNotice, true, 'Expected CLI zero-history ephemeral notice visible in Local CLI settings');
    await shot('15-Settings-AI-CLI');

    // Regression guard: selecting a model in Antigravity CLI (agy) must NEVER bleed into Codex CLI when switching cards, and switching back must restore agy's selection
    await until(() => evaluate("document.querySelector('#cliCommand')?.value === 'agy'"), 'initial Antigravity CLI selected');
    await click('#cliModel');
    await until(() => evaluate("!!document.querySelector('#cliModelMenu')"), 'opened Antigravity CLI models menu');
    await evaluate(`[...document.querySelectorAll('#cliModelMenu [role="option"]')].find(el => el.textContent.trim() === 'gemini-4-neon-medium')?.click()`);
    await until(() => evaluate("document.querySelector('#cliModel')?.value === 'gemini-4-neon-medium' && !document.querySelector('#cliModelMenu')"), 'selected gemini-4-neon-medium for Antigravity CLI');
    await evaluate(`[...document.querySelectorAll('#settingsDialog .provider-card')].find(el => el.textContent.includes('Codex CLI'))?.click()`);
    await until(() => evaluate("document.querySelector('#cliCommand')?.value === 'codex'"), 'selected Codex CLI card');
    const codexModelAfterSwitch = await evaluate("document.querySelector('#cliModel')?.value ?? ''");
    assert.equal(codexModelAfterSwitch, '', 'Switching from Antigravity CLI to Codex CLI must clear #cliModel instead of bleeding gemini-4-neon-medium, got ' + JSON.stringify(codexModelAfterSwitch));
    await evaluate(`[...document.querySelectorAll('#settingsDialog .provider-card')].find(el => el.textContent.includes('Antigravity CLI'))?.click()`);
    await until(() => evaluate("document.querySelector('#cliCommand')?.value === 'agy'"), 'switched back to Antigravity CLI card');
    assert.equal(await evaluate("document.querySelector('#cliModel')?.value"), 'gemini-4-neon-medium', 'Switching back to Antigravity CLI must restore its remembered model');

    // Exercise Local AI CLI live model refresh & searchable dropdown (Codex CLI -> #fetchCliModelsBtn -> #cliModelMenu)
    await evaluate(`[...document.querySelectorAll('#settingsDialog .provider-card')].find(el => el.textContent.includes('Codex CLI'))?.click()`);
    await until(() => evaluate("document.querySelector('#cliCommand')?.value === 'codex'"), 're-selected Codex CLI card for live refresh');
    assert.equal(await evaluate("document.querySelector('#cliModel')?.value"), '', 'Codex CLI #cliModel must remain clean before selecting a Codex model');
    await click('#fetchCliModelsBtn');
    await until(
      () =>
        evaluate(
          "!!document.querySelector('#cliModelMenu') && (document.querySelector('#cliModelMenu')?.textContent || '').includes('已获取 4 个可用模型') && (document.querySelector('#cliModelMenu')?.textContent || '').includes('使用 CLI 默认模型（留空）') && document.querySelectorAll('#cliModelMenu [role=\"option\"]').length === 5",
        ),
      'fetched Codex CLI live models dropdown',
    );
    await sleep(150);
    await shot('40-Settings-AI-CLI-Models');
    await evaluate(`[...document.querySelectorAll('#cliModelMenu [role="option"]')].find(el => el.textContent.trim() === 'gpt-6.2-sol')?.click()`);
    await until(() => evaluate("document.querySelector('#cliModel')?.value === 'gpt-6.2-sol' && !document.querySelector('#cliModelMenu')"), 'selected live CLI model from dropdown');

    // Verify refreshed API & CLI model lists persist in localStorage across closing & reopening SettingsModal (without clicking refresh again)
    const persistedAiCache = await evaluate("JSON.parse(localStorage.getItem('shell:ai-models-cache:v1') || 'null')");
    assert.ok(
      persistedAiCache &&
        Array.isArray(persistedAiCache.api?.['https://api.anthropic.com/v1']) &&
        persistedAiCache.api['https://api.anthropic.com/v1'].length === 4 &&
        Array.isArray(persistedAiCache.cli?.codex) &&
        persistedAiCache.cli.codex.includes('gpt-6.2-sol'),
      'Expected refreshed API & CLI model lists persisted in shell:ai-models-cache:v1, got ' + JSON.stringify(persistedAiCache),
    );
    await click('#settingsDialog button[aria-label="关闭设置"]');
    await until(() => evaluate("!document.querySelector('#settingsDialog')"), 'closed settings to verify AI model cache persistence');
    await click('#settingsBtn');
    await until(() => evaluate("!!document.querySelector('#settingsDialog')"), 'reopened settings to verify AI model cache persistence');
    await evaluate(`document.querySelector('.settings-nav-btn:nth-child(4)').click()`);
    await until(() => evaluate("!!document.querySelector('#model')"), 'reopened AI settings tab');
    await click('#model');
    await until(
      () =>
        evaluate(
          "!!document.querySelector('#aiModelMenu') && (document.querySelector('#aiModelMenu')?.textContent || '').includes('已获取 4 个在线模型') && document.querySelectorAll('#aiModelMenu [role=\"option\"]').length === 4",
        ),
      'persisted API models dropdown without re-fetching',
    );
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('#aiModelMenu')"), 'closed persisted API models dropdown');
    await evaluate(`document.querySelector('#settingsDialog [data-ai-mode="cli"]').click()`);
    await evaluate(`[...document.querySelectorAll('#settingsDialog .provider-card')].find(el => el.textContent.includes('Codex CLI'))?.click()`);
    await until(() => evaluate("document.querySelector('#cliCommand')?.value === 'codex'"), 'reselected Codex CLI card');
    await click('#cliModel');
    await until(
      () =>
        evaluate(
          "!!document.querySelector('#cliModelMenu') && (document.querySelector('#cliModelMenu')?.textContent || '').includes('已获取 4 个可用模型') && (document.querySelector('#cliModelMenu')?.textContent || '').includes('gpt-6.2-sol') && document.querySelectorAll('#cliModelMenu [role=\"option\"]').length === 5",
        ),
      'persisted Codex CLI models dropdown without re-fetching',
    );
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('#cliModelMenu')"), 'closed persisted CLI models dropdown');

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
      aboutAlign.length >= 4 &&
        aboutAlign.every(b => Math.abs(b.width - aboutAlign[0].width) <= 1.5 && Math.abs(b.right - aboutAlign[0].right) <= 1.5),
      'Expected About tab vertical action buttons aligned in width and right edge, got ' + JSON.stringify(aboutAlign),
    );
    await shot('14-Settings-About');

    // Verify smooth check-for-updates animation: permanent .update-check-icon, zero width jump, 正在检查… pill, and >= 600ms display duration
    const beforeCheckMeta = await evaluate(`(() => {
      const btn = document.querySelector('#checkUpdateBtn');
      const icon = btn?.querySelector('.update-check-icon');
      return {
        hasIcon: Boolean(icon),
        width: btn ? btn.getBoundingClientRect().width : 0,
      };
    })()`);
    assert.equal(beforeCheckMeta.hasIcon, true, 'Expected permanent .update-check-icon inside #checkUpdateBtn');
    assert.ok(beforeCheckMeta.width >= 97, 'Expected fixed min-width on #checkUpdateBtn, got ' + JSON.stringify(beforeCheckMeta));
    const checkStartedAt = Date.now();
    await evaluate("document.querySelector('#checkUpdateBtn')?.click()");
    await until(
      () =>
        evaluate(
          "Boolean(document.querySelector('#checkUpdateBtn.is-checking .update-check-icon.is-spinning')) && (document.querySelector('#settingsDialog .pref-row-stack')?.textContent || '').includes('正在检查…')",
        ),
      'smooth update checking active state',
    );
    const duringCheckWidth = await evaluate("document.querySelector('#checkUpdateBtn')?.getBoundingClientRect().width ?? 0");
    assert.ok(
      Math.abs(duringCheckWidth - beforeCheckMeta.width) <= 0.5,
      `Expected zero horizontal jump on #checkUpdateBtn during check: before=${beforeCheckMeta.width}, during=${duringCheckWidth}`,
    );
    await until(
      () =>
        evaluate(
          "!document.querySelector('#checkUpdateBtn.is-checking') && (document.querySelector('#settingsDialog .pref-row-stack')?.textContent || '').includes('已是最新')",
        ),
      'update check settled back to 已是最新',
    );
    const checkElapsedMs = Date.now() - checkStartedAt;
    assert.ok(checkElapsedMs >= 600, `Expected smooth >= 600ms minimum check-update animation duration, got ${checkElapsedMs}ms`);

    // Verify About tab symmetrical 5x2 shortcuts grid & portable cleanup row wording, plus cleanup confirmation dialog (no trailing exe-delete hint)
    const aboutTextAudit = await evaluate(`(() => ({
      shortcutCount: document.querySelectorAll('#settingsDialog .shortcut-cell').length,
      hasCleanupLabel: (document.querySelector('#settingsDialog')?.textContent || '').includes('清理本机残留'),
      hasUninstallWord: (document.querySelector('#settingsDialog')?.textContent || '').includes('卸载'),
    }))()`);
    assert.deepEqual(
      aboutTextAudit,
      { shortcutCount: 10, hasCleanupLabel: true, hasUninstallWord: false },
      'Expected symmetrical 10-item shortcuts grid and portable 清理本机残留 wording without 卸载, got ' + JSON.stringify(aboutTextAudit),
    );
    await click('#openUninstallBtn');
    await until(
      () =>
        evaluate(
          "!!document.querySelector('#uninstallConfirmDialog') && Boolean(document.querySelector('#uninstallConfirmDialog input[type=\"checkbox\"]')?.checked) && (document.querySelector('#uninstallConfirmDialog h2')?.textContent || '').trim() === '清理本机残留并退出' && document.querySelectorAll('#uninstallConfirmDialog .modal-body p').length === 0 && (document.querySelector('#uninstallConfirmDialog')?.textContent || '').includes('确认清理并退出')",
        ),
      'portable cleanup confirm modal',
    );
    await sleep(160);
    const uninstallAlignAudit = await evaluate(`(() => {
      const sheet = document.querySelector('#uninstallConfirmDialog .modal-sheet');
      const dot = document.querySelector('#uninstallConfirmDialog .cleanup-checklist-dot');
      const cb = document.querySelector('#uninstallConfirmDialog .cleanup-checkbox-row input[type="checkbox"]');
      const labelSpan = document.querySelector('#uninstallConfirmDialog .cleanup-checkbox-row span');
      const dotR = dot?.getBoundingClientRect();
      const cbR = cb?.getBoundingClientRect();
      const spanR = labelSpan?.getBoundingClientRect();
      return {
        role: sheet?.getAttribute('role') || '',
        ariaModal: sheet?.getAttribute('aria-modal') || '',
        sheetWidth: sheet ? Math.round(sheet.getBoundingClientRect().width) : 0,
        checklistCount: document.querySelectorAll('#uninstallConfirmDialog .cleanup-checklist li').length,
        labelText: labelSpan?.textContent?.trim() || '',
        labelSpanHeight: spanR ? spanR.height : 999,
        cbTextCenterDeltaY: cbR && spanR ? Math.abs((cbR.top + cbR.height / 2) - (spanR.top + spanR.height / 2)) : 999,
        dotCbCenterDeltaX: dotR && cbR ? Math.abs((dotR.left + dotR.width / 2) - (cbR.left + cbR.width / 2)) : 999,
      };
    })()`);
    assert.equal(uninstallAlignAudit.role, 'dialog', 'Expected role="dialog" on #uninstallConfirmDialog .modal-sheet');
    assert.equal(uninstallAlignAudit.ariaModal, 'true', 'Expected aria-modal="true" on #uninstallConfirmDialog .modal-sheet');
    assert.equal(uninstallAlignAudit.checklistCount, 2, 'Expected 2 .cleanup-checklist items');
    assert.ok(uninstallAlignAudit.labelSpanHeight <= 24, 'Expected single-line checkbox label (height <= 24px), got ' + JSON.stringify(uninstallAlignAudit));
    assert.ok(uninstallAlignAudit.cbTextCenterDeltaY <= 2, 'Expected checkbox & label vertical center delta <= 2px, got ' + JSON.stringify(uninstallAlignAudit));
    assert.ok(uninstallAlignAudit.dotCbCenterDeltaX <= 2, 'Expected checklist dot & checkbox horizontal center delta <= 2px, got ' + JSON.stringify(uninstallAlignAudit));
    await shot('43-Settings-Uninstall-Dialog');
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('#uninstallConfirmDialog')"), 'closed uninstall confirm modal on Escape');
    assert.equal(await evaluate("!!document.querySelector('#settingsDialog')"), true, 'Escape on #uninstallConfirmDialog must keep #settingsDialog open');
    assert.deepEqual(await evaluate('window.__uninstallCalls'), [], 'Canceling uninstall dialog must never invoke UninstallAndQuit');

    // Verify seamless in-app update progress bar inside About tab (no stray horizontal divider above bar, no duplicate status text)
    await evaluate(`window.runtime.EventsEmit('update:progress', { phase: 'downloading', message: '正在下载新版本…', percent: 68, downloaded: 10800000, total: 15850000 })`);
    await until(() => evaluate("document.querySelector('#settingsDialog .update-reveal.is-open .update-progress-track') !== null"), 'smooth update progress reveal');
    await sleep(280);
    const updateProgressAudit = await evaluate(`(() => {
      const stack = document.querySelector('#settingsDialog .pref-row-stack');
      const main = stack?.querySelector('.pref-row-main');
      const reveal = stack?.querySelector('.update-reveal.is-open');
      const msgHits = (stack?.textContent || '').split('正在下载新版本…').length - 1;
      const borderCard = getComputedStyle(document.documentElement).getPropertyValue('--border-card').trim();
      return {
        hasStack: !!stack,
        mainHasBorderBottom: main ? getComputedStyle(main).borderBottomWidth : '',
        revealVisible: !!reveal && reveal.getBoundingClientRect().height > 15,
        msgHits,
        borderCard,
      };
    })()`);
    assert.deepEqual(
      { hasStack: updateProgressAudit.hasStack, mainHasBorderBottom: updateProgressAudit.mainHasBorderBottom, revealVisible: updateProgressAudit.revealVisible, msgHits: updateProgressAudit.msgHits },
      { hasStack: true, mainHasBorderBottom: '0px', revealVisible: true, msgHits: 1 },
      'Expected update progress inside unified pref-row-stack with 0px border above progress bar and single status message, got ' + JSON.stringify(updateProgressAudit),
    );
    assert.ok(updateProgressAudit.borderCard, 'Expected --border-card CSS token defined');
    await shot('34-Settings-About-Updating');
    await evaluate(`window.runtime.EventsEmit('update:progress', null)`);
    await sleep(160);

    // Open Backup & Migration tab (Tab 6) -> verify passphrase eye toggle & capture screenshot
    await evaluate(`document.querySelector('.settings-nav-btn:nth-child(6)').click()`);
    await until(() => evaluate("!!document.querySelector('#vaultPass')"), 'backup tab');
    await sleep(180);
    await shot('44-Settings-Backup');
    await evaluate(`(() => {
      const input = document.querySelector('#vaultPass');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, 'vault-secret-123');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    await until(() => evaluate("!!document.querySelector('#vaultPass ~ .input-eye-btn')"), 'vaultPass eye toggle appears');
    assert.ok(await evaluate("parseFloat(getComputedStyle(document.querySelector('#vaultPass')).paddingRight) >= 30"), 'Expected #vaultPass paddingRight >= 30 with eye button');
    await click('#vaultPass ~ .input-eye-btn');
    assert.equal(await evaluate("document.querySelector('#vaultPass')?.type"), 'text', 'Clicking #vaultPass eye toggle should reveal passphrase');
    await evaluate(`(() => {
      const input = document.querySelector('#vaultPass');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, '');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    await until(() => evaluate("!document.querySelector('#vaultPass ~ .input-eye-btn') && document.querySelector('#vaultPass')?.type === 'password'"), 'clearing #vaultPass hides eye toggle');

    // Open Agent & Security tab -> verify plaintext MCP token, accent switch, code-block wrapping & capture MCP + Guard screenshots
    await evaluate(`document.querySelector('.settings-nav-btn:nth-child(5)').click()`);
    await until(() => evaluate("!!document.querySelector('#mcpTokenInput')"), 'agent MCP tab');
    await sleep(250);
    const mcpAudit = await evaluate(`(() => {
      const tokInput = document.querySelector('#mcpTokenInput');
      const sw = document.querySelector('#mcpEnableSwitch');
      const codeBlock = document.querySelector('#settingsDialog .code-block');
      const codeStyle = codeBlock ? getComputedStyle(codeBlock) : null;
      const contentText = document.querySelector('#settingsDialog .settings-content')?.textContent || '';
      const hideBtnCount = document.querySelectorAll('#settingsDialog button[aria-label="隐藏令牌"], #settingsDialog button[aria-label="显示令牌"]').length;
      return {
        inputType: tokInput?.type || '',
        inputValue: tokInput?.value || '',
        switchHasAccent: Boolean(sw?.classList.contains('accent')),
        hasMaskDots: contentText.includes('••••'),
        hideBtnCount,
        codeContainsFullToken: Boolean(codeBlock?.textContent?.includes('sh_demo_token_9f8e7d6c5b4a32109876543210abcdef')),
        wordBreak: codeStyle?.wordBreak || '',
        overflowWrap: codeStyle?.overflowWrap || '',
      };
    })()`);
    assert.equal(mcpAudit.inputType, 'text', 'Expected #mcpTokenInput type="text" plaintext display, got ' + JSON.stringify(mcpAudit));
    assert.equal(mcpAudit.inputValue, 'sh_demo_token_9f8e7d6c5b4a32109876543210abcdef', 'Expected full real token in #mcpTokenInput');
    assert.equal(mcpAudit.switchHasAccent, true, 'Expected #mcpEnableSwitch to carry .accent class, got ' + JSON.stringify(mcpAudit));
    assert.equal(mcpAudit.hasMaskDots, false, 'Expected zero •••• masked token characters in Agent MCP tab');
    assert.equal(mcpAudit.hideBtnCount, 0, 'Expected zero hide/show token buttons in Agent MCP tab');
    assert.equal(mcpAudit.codeContainsFullToken, true, 'Expected MCP JSON config preview to show full plaintext token');
    assert.equal(mcpAudit.wordBreak, 'normal', 'Expected .code-block word-break: normal, got ' + JSON.stringify(mcpAudit));
    assert.equal(mcpAudit.overflowWrap, 'anywhere', 'Expected .code-block overflow-wrap: anywhere, got ' + JSON.stringify(mcpAudit));
    await shot('11-Agent-MCP-Settings');
    // Also verify Claude Code CLI snippet shows full token cleanly without breaking URL ports
    await evaluate(`[...document.querySelectorAll('#settingsDialog .settings-content .segmented')][2]?.querySelector('button:nth-child(2)')?.click()`);
    await sleep(160);
    const claudeSnippetText = await evaluate("document.querySelector('#settingsDialog .code-block')?.textContent || ''");
    assert.ok(
      claudeSnippetText.includes('http://127.0.0.1:17822/mcp') &&
        claudeSnippetText.includes('sh_demo_token_9f8e7d6c5b4a32109876543210abcdef') &&
        !claudeSnippetText.includes('••••'),
      'Expected Claude Code MCP snippet to show full URL and plaintext token, got ' + claudeSnippetText,
    );
    await shot('45-Agent-MCP-Claude');
    await evaluate(`[...document.querySelectorAll('#settingsDialog .settings-content .segmented')][2]?.querySelector('button:nth-child(1)')?.click()`);
    await sleep(100);
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
    // Verify Windows auto-start switch (#autoStartSwitch) defaults to true, has zero subtitle noise, and toggles true -> false -> true cleanly
    const autoStartRowAudit = await evaluate(`(() => {
      const sw = document.querySelector('#autoStartSwitch');
      const row = sw?.closest('.pref-row');
      return {
        checked: sw?.getAttribute('aria-checked') ?? null,
        label: row?.querySelector('.pref-row-label')?.textContent?.trim() || '',
        subCount: row ? row.querySelectorAll('.pref-row-sub').length : -1,
      };
    })()`);
    assert.deepEqual(
      autoStartRowAudit,
      { checked: 'true', label: '开机自动启动', subCount: 0 },
      'Expected #autoStartSwitch initially true on a clean single-line row with zero .pref-row-sub subtitle, got ' + JSON.stringify(autoStartRowAudit),
    );
    await click('#autoStartSwitch');
    await until(() => evaluate("document.querySelector('#autoStartSwitch')?.getAttribute('aria-checked') === 'false'"), 'disabled #autoStartSwitch');
    await click('#autoStartSwitch');
    await until(() => evaluate("document.querySelector('#autoStartSwitch')?.getAttribute('aria-checked') === 'true'"), 're-enabled #autoStartSwitch');

    await evaluate(`document.querySelector('#settingsDialog .segmented button:nth-child(2)').click()`);
    await sleep(250);
    const darkRedSolid = await evaluate("getComputedStyle(document.documentElement).getPropertyValue('--red-solid').trim().toLowerCase()");
    assert.equal(darkRedSolid, '#b62324', 'Expected calmed deep-crimson --red-solid #b62324 in dark mode, got ' + darkRedSolid);

    // Verify Default Local Shell selector (#defaultLocalShellSelect) shows concise 自动（PowerShell 7） and opens cleanly with detected shells
    await until(() => evaluate("!!document.querySelector('#defaultLocalShellSelect') && (document.querySelector('#defaultLocalShellSelect')?.textContent || '').includes('自动（PowerShell 7）')"), 'default local shell selector with 自动（PowerShell 7）');
    await evaluate(`document.querySelector('#defaultLocalShellSelect')?.scrollIntoView({ block: 'center' })`);
    await sleep(120);
    await click('#defaultLocalShellSelect');
    await until(
      () =>
        evaluate(
          "document.querySelectorAll('[data-radix-select-viewport] [role=\"option\"]').length >= 6 && (document.querySelector('[data-radix-select-viewport]')?.textContent || '').includes('自动（PowerShell 7）') && (document.querySelector('[data-radix-select-viewport]')?.textContent || '').includes('WSL · Ubuntu')",
        ),
      'default local shell select options',
    );
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('[data-radix-select-viewport]')"), 'closed default local shell selector');
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
    // Verify Settings: default 13 px font size, concise 隐藏主机 IP label, and 全局配色 3x2 grid
    const settingsV157 = await evaluate(`(() => {
      const dialogText = document.querySelector('#settingsDialog')?.textContent || '';
      const fontRow = [...document.querySelectorAll('#settingsDialog .pref-row')].find(r => r.querySelector('.pref-row-label')?.textContent?.trim() === '字号');
      const fontText = fontRow?.querySelector('.stepper span')?.textContent?.trim() || '';
      const hideIpLabel = [...document.querySelectorAll('#settingsDialog .pref-row-label')].map(el => el.textContent.trim()).find(t => t.includes('隐藏主机 IP')) || '';
      const sectionTitles = [...document.querySelectorAll('#settingsDialog .pref-section-title')].map(el => el.textContent.trim());
      const themeTiles = [...document.querySelectorAll('#settingsDialog .theme-tile')].map(el => el.textContent.trim());
      const sidebarEyeLabel = document.querySelector('#toggleHideIpBtn')?.getAttribute('aria-label') || '';
      const legacySuffix = '\\u9690\\u79c1\\u6a21\\u5f0f';
      return { fontText, hideIpLabel, hasDisallowedPrivacyTerm: dialogText.includes(legacySuffix) || sidebarEyeLabel.includes(legacySuffix), sectionTitles, themeTiles };
    })()`);
    assert.equal(settingsV157.fontText, '13 px', 'Expected default font size 13 px in Settings, got ' + JSON.stringify(settingsV157));
    assert.equal(settingsV157.hideIpLabel, '隐藏主机 IP', 'Expected concise 隐藏主机 IP label, got ' + JSON.stringify(settingsV157));
    assert.equal(settingsV157.hasDisallowedPrivacyTerm, false, 'Expected zero disallowed privacy suffix in Settings/Sidebar: ' + JSON.stringify(settingsV157));
    assert.ok(settingsV157.sectionTitles.includes('全局配色'), 'Expected 全局配色 section title, got ' + JSON.stringify(settingsV157.sectionTitles));
    const expectedPalettes = ['跟随界面外观', '曜石深灰', '东京夜色', '摩卡暮紫', '极简浅白', '暖纸米白'];
    assert.equal(settingsV157.themeTiles.length, 6, 'Expected 6 tiles (auto + 5 curated palettes) in 全局配色 grid, got ' + JSON.stringify(settingsV157.themeTiles));
    for (const label of expectedPalettes) {
      assert.ok(
        settingsV157.themeTiles.some(t => t.includes(label)),
        `Expected palette tile "${label}" in 全局配色 grid, got ${JSON.stringify(settingsV157.themeTiles)}`,
      );
    }
    assert.equal(settingsV157.themeTiles.some(t => t.includes('Termius')), false, 'Legacy Termius palette name must not appear');
    await shot('08-Settings-Dark');

    // Global Theme live switching: Tokyo Night -> Warm Paper -> Obsidian Dark + localStorage persistence
    const selectGlobalPalette = async (name) => {
      await evaluate(`[...document.querySelectorAll('#settingsDialog .theme-tile')].find(el => el.textContent.includes(${JSON.stringify(name)}))?.click()`);
    };

    await selectGlobalPalette('东京夜色');
    await until(() => evaluate("document.documentElement.dataset.palette === 'tokyo-night' && document.documentElement.dataset.theme === 'dark'"), 'Tokyo Night global palette');
    const tokyoState = await evaluate(`(() => ({
      palette: document.documentElement.dataset.palette,
      theme: document.documentElement.dataset.theme,
      sidebarVar: getComputedStyle(document.documentElement).getPropertyValue('--bg-sidebar').trim(),
      sidebarBg: getComputedStyle(document.querySelector('.sidebar')).backgroundColor,
      cached: JSON.parse(localStorage.getItem('shell:appearance:v2') || 'null'),
    }))()`);
    assert.equal(tokyoState.palette, 'tokyo-night');
    assert.equal(tokyoState.theme, 'dark');
    assert.equal(tokyoState.sidebarBg, 'rgb(31, 35, 53)', 'Expected Tokyo Night Storm sidebar bg #1F2335, got ' + JSON.stringify(tokyoState));
    assert.deepEqual(tokyoState.cached, { theme: 'dark', terminalTheme: 'tokyo-night' }, 'Expected localStorage shell:appearance:v2 persisted for Tokyo Night');
    await click('#settingsDialog button[aria-label="关闭设置"]');
    await until(() => evaluate("!document.querySelector('#settingsDialog')"), 'closed settings for Tokyo Night screenshot');
    await sleep(250);
    await shot('31-GlobalTheme-TokyoNight');

    await click('#settingsBtn');
    await until(() => evaluate("document.querySelectorAll('#settingsDialog .theme-tile').length === 6"), 'reopened settings for Warm Paper');
    await selectGlobalPalette('暖纸米白');
    await until(() => evaluate("document.documentElement.dataset.palette === 'warm-paper' && document.documentElement.dataset.theme === 'light'"), 'Warm Paper global palette');
    const warmState = await evaluate(`(() => ({
      palette: document.documentElement.dataset.palette,
      theme: document.documentElement.dataset.theme,
      sidebarVar: getComputedStyle(document.documentElement).getPropertyValue('--bg-sidebar').trim(),
      sidebarBg: getComputedStyle(document.querySelector('.sidebar')).backgroundColor,
      cached: JSON.parse(localStorage.getItem('shell:appearance:v2') || 'null'),
    }))()`);
    assert.equal(warmState.palette, 'warm-paper');
    assert.equal(warmState.theme, 'light');
    assert.equal(warmState.sidebarBg, 'rgb(242, 233, 225)', 'Expected Rosé Pine Dawn Warm Paper sidebar bg #F2E9E1, got ' + JSON.stringify(warmState));
    assert.deepEqual(warmState.cached, { theme: 'light', terminalTheme: 'warm-paper' }, 'Expected localStorage shell:appearance:v2 persisted for Warm Paper');
    await click('#settingsDialog button[aria-label="关闭设置"]');
    await until(() => evaluate("!document.querySelector('#settingsDialog')"), 'closed settings for Warm Paper screenshot');
    await sleep(250);
    await shot('32-GlobalTheme-WarmPaper');

    await click('#settingsBtn');
    await until(() => evaluate("document.querySelectorAll('#settingsDialog .theme-tile').length === 6"), 'reopened settings to restore Obsidian Graphite');
    await selectGlobalPalette('曜石深灰');
    await until(() => evaluate("document.documentElement.dataset.palette === 'shell-dark' && document.documentElement.dataset.theme === 'dark'"), 'restored Obsidian Graphite global palette');
    const obsidianCache = await evaluate("JSON.parse(localStorage.getItem('shell:appearance:v2') || 'null')");
    assert.deepEqual(obsidianCache, { theme: 'dark', terminalTheme: 'shell-dark' }, 'Expected localStorage shell:appearance:v2 persisted for Obsidian Graphite');
    await click('#settingsDialog button[aria-label="关闭设置"]');
    await until(() => evaluate("!document.querySelector('#settingsDialog')"), 'closed settings after restoring Obsidian Graphite');
    await sleep(250);
    // Verify 曜石深灰 (GitHub Dark #161B22) terminal background, crisp cursor (#58A6FF), vivid ANSI green (#56D364), CJK sans-serif font stack, no blue active accent bars & full tab title visibility
    const darkPaletteAudit = await evaluate(`(() => {
      const wrap = document.querySelector('.pane-xterm-wrap');
      const bg = wrap ? getComputedStyle(wrap).backgroundColor : '';
      const rows = document.querySelector('.pane-xterm-wrap .xterm-rows');
      const xtermFontFamily = rows ? getComputedStyle(rows).fontFamily : '';
      const greenSpan = document.querySelector('.pane-xterm-wrap .xterm-fg-2');
      const greenColor = greenSpan ? getComputedStyle(greenSpan).color : '';
      const styleText = [...document.querySelectorAll('.pane-xterm-wrap style')].map(s => s.textContent || '').join('\\n');
      const cursorEl = document.querySelector('.pane-xterm-wrap .xterm-cursor');
      const cursorStyle = cursorEl ? getComputedStyle(cursorEl) : null;
      const cursorBoxShadow = cursorStyle ? cursorStyle.boxShadow : '';
      const hasCrispCursor =
        /#58a6ff/i.test(styleText) ||
        styleText.includes('88, 166, 255') ||
        cursorBoxShadow.includes('88, 166, 255') ||
        cursorStyle?.backgroundColor === 'rgb(88, 166, 255)' ||
        cursorStyle?.borderLeftColor === 'rgb(88, 166, 255)';
      const hasCrispGreen = /#56d364/i.test(styleText) || greenColor === 'rgb(86, 211, 100)';
      const activeTab = document.querySelector('.tab-item.active');
      const activeHost = document.querySelector('.host-row.active');
      const tabAfterContent = activeTab ? getComputedStyle(activeTab, '::after').content : null;
      const hostBeforeContent = activeHost ? getComputedStyle(activeHost, '::before').content : null;
      const firstTabLabel = activeTab?.querySelector('.tab-label') ?? document.querySelector('.tab-item .tab-label');
      const tabFullVisible = firstTabLabel ? firstTabLabel.scrollWidth <= firstTabLabel.clientWidth + 1 : false;
      return { bg, xtermFontFamily, greenColor, cursorBoxShadow, hasCrispCursor, hasCrispGreen, tabAfterContent, hostBeforeContent, tabFullVisible };
    })()`);
    assert.equal(darkPaletteAudit.bg, 'rgb(22, 27, 34)', 'Expected GitHub Dark canvas background #161B22, got ' + JSON.stringify(darkPaletteAudit));
    assert.equal(darkPaletteAudit.greenColor, 'rgb(86, 211, 100)', 'Expected crisp GitHub Dark ANSI green #56D364, got ' + JSON.stringify(darkPaletteAudit));
    assert.equal(darkPaletteAudit.hasCrispCursor, true, 'Expected crisp GitHub Dark cursor #58A6FF in xterm renderer, got ' + JSON.stringify(darkPaletteAudit));
    assert.equal(darkPaletteAudit.hasCrispGreen, true, 'Expected crisp green #56D364 in xterm renderer, got ' + JSON.stringify(darkPaletteAudit));
    assert.ok(darkPaletteAudit.xtermFontFamily.includes('Microsoft YaHei'), 'Expected .xterm-rows computed fontFamily to include Microsoft YaHei CJK sans-serif fallback, got ' + JSON.stringify(darkPaletteAudit.xtermFontFamily));
    assert.equal(darkPaletteAudit.tabAfterContent, 'none', 'Expected no blue ::after accent bar on .tab-item.active, got ' + JSON.stringify(darkPaletteAudit));
    assert.equal(darkPaletteAudit.hostBeforeContent, 'none', 'Expected no blue ::before accent bar on .host-row.active, got ' + JSON.stringify(darkPaletteAudit));
    assert.equal(darkPaletteAudit.tabFullVisible, true, 'Expected full host name visible on tab without premature truncation, got ' + JSON.stringify(darkPaletteAudit));
    await shot('09-Terminal-Dark');

    // Capture Dark Mode Direct-Execution AI Drawer (50-Terminal-AI-Dark)
    await click('#aiOpen');
    await until(() => evaluate("!!document.querySelector('#aiDrawer #aiPromptInput')"), 'dark AI drawer');
    await click('#aiPromptInput');
    await evaluate(`(() => {
      const ta = document.querySelector('#aiPromptInput');
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(ta, '排查 8080 端口占用并给出重载命令');
      ta.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    await until(
      () =>
        evaluate(
          "document.querySelectorAll('#aiDrawer .ai-step-row').length === 1 && !!document.querySelector('#aiDrawer .ai-step-icon.is-done') && !document.querySelector('#aiDrawer .ai-running-row') && (document.querySelector('#aiDrawer .ai-md')?.textContent || '').includes('nginx')",
        ),
      'dark AI drawer execution result',
    );
    await sleep(160);
    await shot('50-Terminal-AI-Dark');
    await click('#aiOpen');
    await until(() => evaluate("!document.querySelector('#aiDrawer')"), 'closed dark AI drawer');

    // Capture Dark Mode 4-zone Telemetry Popover (35-Telemetry-Popover-Dark)
    await click('.statusbar-right .telemetry-trigger');
    await until(() => evaluate("!!document.querySelector('.telemetry-popover')"), 'dark telemetry popover');
    await sleep(160);
    const darkTelemetryAudit = await evaluate(`(() => {
      const pop = document.querySelector('.telemetry-popover');
      const bar = document.querySelector('.statusbar');
      const osEl = pop?.querySelector('.telemetry-os');
      const osRect = osEl?.getBoundingClientRect();
      const popRect = pop?.getBoundingClientRect();
      const barRect = bar?.getBoundingClientRect();
      return {
        hasKpiGrid: Boolean(pop?.querySelector('.telemetry-kpi-grid')),
        osHeight: osRect ? Math.round(osRect.height) : 0,
        popBottom: popRect ? Math.round(popRect.bottom) : 9999,
        barTop: barRect ? Math.round(barRect.top) : 0,
      };
    })()`);
    assert.ok(
      darkTelemetryAudit.hasKpiGrid && darkTelemetryAudit.osHeight > 0 && darkTelemetryAudit.osHeight <= 24 && darkTelemetryAudit.popBottom < darkTelemetryAudit.barTop,
      'Expected dark telemetry popover floating cleanly above statusbar with single-line OS, got ' + JSON.stringify(darkTelemetryAudit),
    );
    await shot('35-Telemetry-Popover-Dark');
    await evaluate("document.querySelector('.telemetry-popover')?.previousElementSibling?.click()");
    await until(() => evaluate("!document.querySelector('.telemetry-popover')"), 'closed dark telemetry popover');

    // Capture Dark Mode mixed CJK + English + ANSI bold/italic/inverted badges (36-Terminal-CJK-Dark)
    await evaluate(`(() => {
      const sid = window.__lastConnectedSessionId;
      if (!sid) throw new Error('Missing active session id for CJK terminal capture');
      const report = [
        '\\x1b[2J\\x1b[H',
        '\\x1b[1;36m########################################################################\\x1b[0m',
        '\\x1b[1;97m                   IP质量体检报告：203.0.113.* (Tokyo)                   \\x1b[0m',
        '\\x1b[1;36m########################################################################\\x1b[0m',
        '\\x1b[1;34m一、基础信息 (Basic Info)\\x1b[0m',
        '  自治系统号：        \\x1b[1;37mAS15169\\x1b[0m   组织：\\x1b[36mGoogle Cloud (ap-northeast-1)\\x1b[0m',
        '  注册地 / 使用地：   \\x1b[32m[JP] 日本 东京 (Tokyo)\\x1b[0m  时区：Asia/Tokyo (UTC+9)',
        '  IP 类型属性：       \\x1b[1;30;42m 原生IP \\x1b[0m  \\x1b[1;30;41m 广播IP \\x1b[0m  \\x1b[1;30;42m 家宽 \\x1b[0m  \\x1b[1;30;43m 商业 \\x1b[0m  \\x1b[1;30;46m 机房 \\x1b[0m  \\x1b[3;90m(商业数据中心 / 非住宅)\\x1b[0m',
        '',
        '\\x1b[1;34m二、风险评分与因子 (Risk & Reputation)\\x1b[0m',
        '  欺诈风险等级：      \\x1b[1;30;42m 极低 \\x1b[0m | \\x1b[32m低\\x1b[0m | \\x1b[33m中等\\x1b[0m | \\x1b[31m高\\x1b[0m | \\x1b[1;97;41m 极高 \\x1b[0m   \\x1b[1;32m[极低 | 低 | 中等 | 高 | 极高]\\x1b[0m',
        '  安全因子校验：      代理：\\x1b[1;32m否\\x1b[0m  VPN：\\x1b[1;32m否\\x1b[0m  Tor：\\x1b[1;32m否\\x1b[0m  黑名单率：\\x1b[1;37m0.00%\\x1b[0m',
        '  数据库校验：        Scamalytics \\x1b[32m0/100 (低风险)\\x1b[0m  IPQS \\x1b[32m12/100\\x1b[0m  AbuseIPDB \\x1b[32m0%\\x1b[0m',
        '  字体混排校验：      \\x1b[1m粗体中文 Bold English 1234\\x1b[0m  \\x1b[3m斜体中文 Italic Latin 5678\\x1b[0m  常规中文 Regular',
        '',
        '\\x1b[1;34m三、流媒体及 AI 服务解锁状态 (Streaming & AI Unlock)\\x1b[0m',
        '  服务商        状态             地区    方式',
        '  Netflix       \\x1b[1;30;42m 完整解锁 \\x1b[0m      JP      \\x1b[32m原生\\x1b[0m',
        '  OpenAI / API  \\x1b[1;30;42m 原生支持 \\x1b[0m      JP      \\x1b[32m直连\\x1b[0m',
        '  Claude Web    \\x1b[1;30;43m 仅部分区 \\x1b[0m      US      \\x1b[33mDNS\\x1b[0m',
        '  TikTok        \\x1b[1;97;41m 限制访问 \\x1b[0m      --      \\x1b[31m屏蔽\\x1b[0m',
        '',
        'deploy@tokyo-production:~/app$ ',
      ].join('\\r\\n');
      window.runtime.EventsEmit('ssh:data', sid, btoa(unescape(encodeURIComponent(report))));
    })()`);
    await until(() => evaluate("(document.querySelector('.pane-xterm-wrap .xterm-rows')?.textContent || '').includes('原生IP') && (document.querySelector('.pane-xterm-wrap .xterm-rows')?.textContent || '').includes('极低')"), 'CJK IPQuality terminal payload rendered');
    await sleep(220);
    await shot('36-Terminal-CJK-Dark');

    // Emit realistic RegionRestrictionCheck + IPQuality mixed CJK/English ANSI output & verify ALL remaining palettes end-to-end (37 Light ANSI, 38 Warm Paper, 39 Catppuccin Mocha)
    await evaluate(`(() => {
      const sid = window.__lastConnectedSessionId;
      if (!sid) throw new Error('Missing active session id for multi-palette ANSI capture');
      const payload = [
        '\\x1b[2J\\x1b[H',
        '\\x1b[1;36m** 正在测试IPv6解锁情况 — RegionRestrictionCheck + IPQuality 综合检测 **\\x1b[0m',
        '\\x1b[1;34m============[ Multination / 流媒体与 AI 区域解锁检测 ]============\\x1b[0m',
        '  Dazn:                           \\x1b[32mYes (Region: SG)\\x1b[0m',
        '  Disney+:                        \\x1b[1;32mYes (Region: SG)\\x1b[0m  \\x1b[36m[4K HDR / 原生解锁]\\x1b[0m',
        '  Netflix:                        \\x1b[1;32mYes (Region: SG)\\x1b[0m  \\x1b[32m完整解锁 (Originals & Licensed)\\x1b[0m',
        '  YouTube Premium:                \\x1b[32mYes (Region: SG)\\x1b[0m',
        '  ChatGPT / OpenAI API:           \\x1b[1;32mYes (Region: SG)\\x1b[0m  \\x1b[35mWeb + API 直连\\x1b[0m',
        '  Claude / Anthropic:             \\x1b[33mMaybe (WAF Blocked)\\x1b[0m  \\x1b[1;33m需验证 ASN 路由\\x1b[0m',
        '  Bahamut Anime (动画疯):         \\x1b[31mNo (Region: CN)\\x1b[0m',
        '  Bilibili 港澳台限定:            \\x1b[1;31mNo (Region: CN)\\x1b[0m',
        '  IPv6 Connectivity:              \\x1b[1;33mIPv6 Not Supported\\x1b[0m',
        '',
        '\\x1b[1;36m** 正在测试IPv6解锁情况 — IPQuality 风险与属性摘要 **\\x1b[0m',
        '  \\x1b[1mASN / 归属地:\\x1b[0m                  \\x1b[34mAS16509 Amazon AWS\\x1b[0m  \\x1b[32m[SG] 新加坡 (Singapore)\\x1b[0m',
        '  \\x1b[1m风险评级 (IPQS / Scamalytics):\\x1b[0m  \\x1b[1;32m低风险 (0/100)\\x1b[0m | \\x1b[33m中等警告 (35/100)\\x1b[0m | \\x1b[31m高危拦截 (92/100)\\x1b[0m',
        '  \\x1b[1m常规与加粗对比校验:\\x1b[0m            \\x1b[32mNormal Green\\x1b[0m / \\x1b[1;32mBold Green\\x1b[0m  \\x1b[33mNormal Yellow\\x1b[0m / \\x1b[1;33mBold Yellow\\x1b[0m  \\x1b[31mNormal Red\\x1b[0m / \\x1b[1;31mBold Red\\x1b[0m  \\x1b[36mNormal Cyan\\x1b[0m / \\x1b[1;36mBold Cyan\\x1b[0m',
        '',
        'deploy@tokyo-production:~/app$ ',
      ].join('\\r\\n');
      window.runtime.EventsEmit('ssh:data', sid, btoa(unescape(encodeURIComponent(payload))));
    })()`);
    await until(
      () =>
        evaluate(
          "(document.querySelector('.pane-xterm-wrap .xterm-rows')?.textContent || '').includes('Yes (Region: SG)') && (document.querySelector('.pane-xterm-wrap .xterm-rows')?.textContent || '').includes('正在测试IPv6解锁情况')",
        ),
      'RegionRestrictionCheck + IPQuality ANSI payload rendered',
    );

    const switchGlobalPaletteAndClose = async (label, expectedPalette, expectedTheme) => {
      await click('#settingsBtn');
      await until(() => evaluate("!!document.querySelector('#settingsDialog')"), `reopened settings for ${expectedPalette}`);
      await evaluate(`document.querySelector('.settings-nav-btn:nth-child(1)')?.click()`);
      await until(() => evaluate("document.querySelectorAll('#settingsDialog .theme-tile').length === 6"), `appearance palette grid for ${expectedPalette}`);
      await selectGlobalPalette(label);
      await until(
        () =>
          evaluate(
            `document.documentElement.dataset.palette === ${JSON.stringify(expectedPalette)} && document.documentElement.dataset.theme === ${JSON.stringify(expectedTheme)}`,
          ),
        `switched global palette to ${expectedPalette}`,
      );
      await click('#settingsDialog button[aria-label="关闭设置"]');
      await until(() => evaluate("!document.querySelector('#settingsDialog')"), `closed settings for ${expectedPalette}`);
      await sleep(250);
    };

    // 37: Light Mode (shell-light / GitHub Light) crisp ANSI contrast
    await switchGlobalPaletteAndClose('极简浅白', 'shell-light', 'light');
    const lightAnsiAudit = await evaluate(`(() => {
      const wrap = document.querySelector('.pane-xterm-wrap');
      const greenSpan = document.querySelector('.pane-xterm-wrap .xterm-fg-2');
      const split = document.querySelector('.sidebar-new-split');
      return {
        canvasBg: wrap ? getComputedStyle(wrap).backgroundColor : '',
        greenColor: greenSpan ? getComputedStyle(greenSpan).color : '',
        splitBg: split ? getComputedStyle(split).backgroundColor : '',
      };
    })()`);
    assert.equal(lightAnsiAudit.canvasBg, 'rgb(255, 255, 255)', 'Expected GitHub Light terminal canvas #FFFFFF, got ' + JSON.stringify(lightAnsiAudit));
    assert.equal(lightAnsiAudit.greenColor, 'rgb(17, 99, 41)', 'Expected high-contrast GitHub Light ANSI green #116329, got ' + JSON.stringify(lightAnsiAudit));
    assert.equal(lightAnsiAudit.splitBg, 'rgb(9, 105, 218)', 'Expected GitHub Light accent #0969DA on .sidebar-new-split, got ' + JSON.stringify(lightAnsiAudit));
    await shot('37-Terminal-Light-ANSI');

    // 38: Warm Paper (warm-paper / Rosé Pine Dawn) terminal + pine-teal harmonized chrome
    await switchGlobalPaletteAndClose('暖纸米白', 'warm-paper', 'light');
    const warmTermAudit = await evaluate(`(() => {
      const wrap = document.querySelector('.pane-xterm-wrap');
      const sidebar = document.querySelector('.sidebar');
      const split = document.querySelector('.sidebar-new-split');
      return {
        canvasBg: wrap ? getComputedStyle(wrap).backgroundColor : '',
        sidebarBg: sidebar ? getComputedStyle(sidebar).backgroundColor : '',
        splitBg: split ? getComputedStyle(split).backgroundColor : '',
      };
    })()`);
    assert.equal(warmTermAudit.canvasBg, 'rgb(250, 244, 237)', 'Expected Rosé Pine Dawn terminal canvas #FAF4ED, got ' + JSON.stringify(warmTermAudit));
    assert.equal(warmTermAudit.sidebarBg, 'rgb(242, 233, 225)', 'Expected Rosé Pine Dawn sidebar #F2E9E1, got ' + JSON.stringify(warmTermAudit));
    assert.equal(warmTermAudit.splitBg, 'rgb(40, 105, 131)', 'Expected Rosé Pine Dawn pine-teal accent #286983 on .sidebar-new-split, got ' + JSON.stringify(warmTermAudit));
    await shot('38-Palette-WarmPaper-Terminal');

    // 39: Catppuccin Mocha (catppuccin-mocha) terminal + mauve harmonized chrome
    await switchGlobalPaletteAndClose('摩卡暮紫', 'catppuccin-mocha', 'dark');
    const mochaTermAudit = await evaluate(`(() => {
      const wrap = document.querySelector('.pane-xterm-wrap');
      const sidebar = document.querySelector('.sidebar');
      const split = document.querySelector('.sidebar-new-split');
      return {
        canvasBg: wrap ? getComputedStyle(wrap).backgroundColor : '',
        sidebarBg: sidebar ? getComputedStyle(sidebar).backgroundColor : '',
        splitBg: split ? getComputedStyle(split).backgroundColor : '',
      };
    })()`);
    assert.equal(mochaTermAudit.canvasBg, 'rgb(30, 30, 46)', 'Expected Catppuccin Mocha terminal canvas #1E1E2E, got ' + JSON.stringify(mochaTermAudit));
    assert.equal(mochaTermAudit.sidebarBg, 'rgb(24, 24, 37)', 'Expected Catppuccin Mocha sidebar #181825, got ' + JSON.stringify(mochaTermAudit));
    assert.equal(mochaTermAudit.splitBg, 'rgb(139, 108, 232)', 'Expected Catppuccin Mocha mauve-indigo accent #8B6CE8 on .sidebar-new-split, got ' + JSON.stringify(mochaTermAudit));
    await shot('39-Palette-CatppuccinMocha-Terminal');

    // Restore shell-dark cleanly before downstream dark-mode checks
    await switchGlobalPaletteAndClose('曜石深灰', 'shell-dark', 'dark');

    // Open SFTP + CodeMirror 6 Remote Editor in Dark theme (Nginx config highlighting + #161B22 canvas)
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
    assert.equal(cmDarkCheck.bg, 'rgb(22, 27, 34)', 'Expected Dark CodeMirror 6 editor canvas #161B22, got ' + cmDarkCheck.bg);
    await shot('25-Remote-Editor-Dark');
    await click('.modal-sheet.editor button[aria-label="关闭"]');
    await until(() => evaluate("!document.querySelector('.modal-sheet.editor')"), 'closed dark editor');
    await click('#sftpBtn');
    await until(() => evaluate("!document.querySelector('#filesPane')"), 'closed dark SFTP drawer');

    // Verify Dark Mode 2-pane horizontal split: zero broken blue top outline, distinct focused mini-bar, and subtle unfocused dimming
    await click('button[aria-label="左右分屏"]');
    await until(() => evaluate("!!document.querySelector('[role=\"dialog\"][aria-label=\"选择分屏主机\"]')"), 'dark split host picker');
    await click('#commandPalette [data-action-id="host-p3"]');
    await until(
      () =>
        evaluate(
          "document.querySelectorAll('.pane-box.multi-pane').length === 2 && document.querySelectorAll('.pane-box.multi-pane .xterm-screen').length === 2 && !document.querySelector('.pane-connect-trace') && document.querySelectorAll('.pane-box.multi-pane.focused').length === 1",
        ),
      'two connected dark split panes',
    );
    await sleep(280);
    const splitDarkAudit = await evaluate(`(() => {
      const focused = document.querySelector('.pane-box.multi-pane.focused');
      const unfocused = document.querySelector('.pane-box.multi-pane:not(.focused)');
      const fBar = focused?.querySelector('.pane-mini-bar');
      const uBar = unfocused?.querySelector('.pane-mini-bar');
      const fHost = focused?.querySelector('.pane-xterm-host');
      const uHost = unfocused?.querySelector('.pane-xterm-host');
      return {
        paneCount: document.querySelectorAll('.pane-box.multi-pane').length,
        focusedOutlineStyle: focused ? getComputedStyle(focused).outlineStyle : null,
        focusedBarBg: fBar ? getComputedStyle(fBar).backgroundColor : null,
        unfocusedBarBg: uBar ? getComputedStyle(uBar).backgroundColor : null,
        focusedHostOpacity: fHost ? getComputedStyle(fHost).opacity : null,
        unfocusedHostOpacity: uHost ? parseFloat(getComputedStyle(uHost).opacity) : null,
      };
    })()`);
    assert.equal(splitDarkAudit.paneCount, 2, 'Expected 2 split panes in Dark Mode, got ' + JSON.stringify(splitDarkAudit));
    assert.equal(splitDarkAudit.focusedOutlineStyle, 'none', 'Expected zero outline on .pane-box.multi-pane.focused, got ' + JSON.stringify(splitDarkAudit));
    assert.ok(
      splitDarkAudit.focusedBarBg && splitDarkAudit.unfocusedBarBg && splitDarkAudit.focusedBarBg !== splitDarkAudit.unfocusedBarBg,
      'Expected focused .pane-mini-bar background to differ cleanly from unfocused .pane-mini-bar, got ' + JSON.stringify(splitDarkAudit),
    );
    assert.equal(splitDarkAudit.focusedHostOpacity, '1', 'Expected focused .pane-xterm-host opacity === 1, got ' + JSON.stringify(splitDarkAudit));
    assert.ok(
      typeof splitDarkAudit.unfocusedHostOpacity === 'number' && splitDarkAudit.unfocusedHostOpacity < 0.85,
      'Expected unfocused .pane-xterm-host opacity < 0.85, got ' + JSON.stringify(splitDarkAudit),
    );
    await shot('33-Split-Dark');
    await click('.pane-box.multi-pane.focused .pane-mini-bar button[aria-label="关闭此分屏"]');
    await until(() => evaluate("document.querySelectorAll('.pane-box').length === 1 && !document.querySelector('.pane-box.multi-pane')"), 'restored single dark pane');

    // Verify centered SSH connection progress card (jump hop + elapsed timer + vertical milestone rail)
    await evaluate(`(() => {
      const api = window.go.main.App;
      const origProfiles = api.Profiles;
      window.restoreProgressProfile = () => { api.Profiles = origProfiles; };
      api.Profiles = async () => [
        ...(await origProfiles()),
        { id: 'progress-test', name: 'Prod Core API', host: '10.24.0.42', port: 22, user: 'deploy', icon: 'ubuntu', jumpHostId: 'p1', auth: 'agent', keepAliveSec: 30 }
      ];
      window.runtime.EventsEmit('agent:connect_profile', 'progress-test');
    })()`);
    await until(
      () =>
        evaluate(
          "!!document.querySelector('.pane-connect-card:not(.is-error) .pane-connect-step.is-active') && document.querySelectorAll('.pane-connect-card:not(.is-error) .pane-connect-step.is-done').length === 2",
        ),
      'active connect progress card with jump hop milestones',
    );
    await sleep(180);
    const progressAudit = await evaluate(`(() => {
      const overlay = document.querySelector('.pane-connect-trace')?.getBoundingClientRect();
      const card = document.querySelector('.pane-connect-card:not(.is-error)')?.getBoundingClientRect();
      const host = document.querySelector('.pane-box.focused .pane-xterm-host');
      const hop = document.querySelector('.pane-connect-card .pane-connect-hop')?.textContent?.trim() || '';
      const elapsed = document.querySelector('.pane-connect-card .pane-connect-elapsed')?.textContent?.trim() || '';
      const doneSteps = document.querySelectorAll('.pane-connect-card .pane-connect-step.is-done').length;
      const activeSteps = document.querySelectorAll('.pane-connect-card .pane-connect-step.is-active').length;
      const pendingSteps = document.querySelectorAll('.pane-connect-card .pane-connect-step.is-pending').length;
      const centerDelta = overlay && card ? Math.abs((card.left + card.width / 2) - (overlay.left + overlay.width / 2)) : 999;
      return { xtermHidden: Boolean(host?.classList.contains('is-hidden')), hop, elapsed, doneSteps, activeSteps, pendingSteps, centerDelta };
    })()`);
    assert.equal(progressAudit.xtermHidden, true, 'Expected xterm hidden while connecting');
    assert.ok(progressAudit.hop.includes('Tokyo Production'), 'Expected jump hop badge on connect progress card, got ' + JSON.stringify(progressAudit));
    assert.match(progressAudit.elapsed, /^\d+\.\ds$/, 'Expected live elapsed timer on connect progress card, got ' + JSON.stringify(progressAudit));
    assert.deepEqual(
      { done: progressAudit.doneSteps, active: progressAudit.activeSteps, pending: progressAudit.pendingSteps },
      { done: 2, active: 1, pending: 1 },
      'Expected vertical milestone rail (jump/dial done, auth active, shell pending): ' + JSON.stringify(progressAudit),
    );
    assert.ok(progressAudit.centerDelta <= 2, 'Expected centered .pane-connect-card, centerDelta=' + progressAudit.centerDelta);
    await shot('27-Connect-Progress-Card');
    await evaluate(`(() => { window.finishProgressConnect?.(); window.restoreProgressProfile?.(); })()`);
    await click('.tab-item.active .tab-close');
    await until(() => evaluate("document.querySelectorAll('.tab-item').length === 1"), 'closed progress test tab');

    // Verify centered SSH connection error diagnosis card (vertical milestone rail + copy error + edit/reconnect)
    await evaluate(`(() => {
      const api = window.go.main.App;
      const origProfiles = api.Profiles;
      window.restoreFailProfile = () => { api.Profiles = origProfiles; };
      api.Profiles = async () => [
        ...(await origProfiles()),
        { id: 'fail-test', name: 'Staging DB', host: '10.24.0.99', port: 22, user: 'ops', icon: 'debian', jumpHostId: 'p1', auth: 'agent', keepAliveSec: 30 }
      ];
      window.runtime.EventsEmit('agent:connect_profile', 'fail-test');
    })()`);
    await until(() => evaluate("!!document.querySelector('.pane-connect-card.is-error .pane-connect-error-title')"), 'connect error trace overlay');
    const traceAudit = await evaluate(`(() => {
      const overlay = document.querySelector('.pane-connect-trace')?.getBoundingClientRect();
      const card = document.querySelector('.pane-connect-card.is-error')?.getBoundingClientRect();
      const host = document.querySelector('.pane-box.focused .pane-xterm-host');
      const hop = document.querySelector('.pane-connect-card.is-error .pane-connect-hop')?.textContent?.trim() || '';
      const pill = document.querySelector('.pane-connect-card.is-error .pane-connect-pill.error')?.textContent?.trim() || '';
      const title = document.querySelector('.pane-connect-error-title')?.textContent?.trim() || '';
      const raw = document.querySelector('.pane-connect-raw')?.textContent?.trim() || '';
      const hasCopyBtn = !!document.querySelector('.pane-connect-card.is-error .pane-connect-copy');
      const btns = [...document.querySelectorAll('.pane-connect-actions button')].map(b => b.textContent.trim());
      const doneSteps = document.querySelectorAll('.pane-connect-card.is-error .pane-connect-step.is-done').length;
      const errSteps = document.querySelectorAll('.pane-connect-card.is-error .pane-connect-step.is-error').length;
      const centerDelta = overlay && card ? Math.abs((card.left + card.width / 2) - (overlay.left + overlay.width / 2)) : 999;
      return {
        xtermHidden: Boolean(host?.classList.contains('is-hidden')),
        hop,
        pill,
        title,
        hasRaw: raw.includes('10.24.0.99:22'),
        hasCopyBtn,
        btns,
        doneSteps,
        errSteps,
        centerDelta,
      };
    })()`);
    assert.equal(traceAudit.xtermHidden, true, 'Expected xterm host hidden before first connection (no corner cursor)');
    assert.ok(traceAudit.hop.includes('Tokyo Production'), 'Expected jump hop badge on error card, got ' + JSON.stringify(traceAudit));
    assert.equal(traceAudit.pill, '连接失败', 'Expected error status pill, got ' + JSON.stringify(traceAudit));
    assert.ok(traceAudit.title.includes('连接被拒绝'), 'Expected clear Chinese error diagnosis, got ' + JSON.stringify(traceAudit));
    assert.equal(traceAudit.hasRaw, true, 'Expected raw error detail shown');
    assert.equal(traceAudit.hasCopyBtn, true, 'Expected copy-error button on error card');
    assert.ok(traceAudit.btns.includes('重新连接') && traceAudit.btns.includes('编辑配置'), 'Expected Reconnect + Edit Profile buttons, got ' + JSON.stringify(traceAudit.btns));
    assert.ok(traceAudit.doneSteps >= 1 && traceAudit.errSteps === 1, 'Expected completed jump milestone + failed dial milestone, got ' + JSON.stringify(traceAudit));
    assert.ok(traceAudit.centerDelta <= 2, 'Expected centered error card, centerDelta=' + traceAudit.centerDelta);
    await shot('26-Connect-Error-Trace');
    await click('.pane-connect-card.is-error .pane-connect-copy');
    await until(() => evaluate("document.querySelector('.pane-connect-card.is-error .pane-connect-copy')?.title === '已复制错误详情'"), 'copy error detail feedback');
    await evaluate('window.restoreFailProfile()');
    await click('.tab-item.active .tab-close');
    await until(() => evaluate("document.querySelectorAll('.tab-item').length === 1"), 'closed error test tab');

    // Verify Multi-Tab chip separation, 20x20 close hitboxes, searchable switcher popover (26x26 close + 关闭全部), and tab context menu 关闭所有标签
    await click('.host-row[data-profile-id="p2"]');
    await until(() => evaluate("document.querySelectorAll('.tab-item').length === 2"), 'second tab opened');
    await click('.host-row[data-profile-id="p3"]');
    await until(() => evaluate("document.querySelectorAll('.tab-item').length === 3"), 'third tab opened');
    for (const expectedCount of [4, 5]) {
      await evaluate(`(() => {
        const tab = document.querySelectorAll('.tab-item')[${expectedCount - 4}];
        const r = tab.getBoundingClientRect();
        tab.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: r.x + 12, clientY: r.y + 12 }));
      })()`);
      await until(() => evaluate("[...document.querySelectorAll('.context-menu-item')].some(el => el.textContent.trim() === '复制新会话标签')"), 'tab context menu for clone');
      await evaluate(`[...document.querySelectorAll('.context-menu-item')].find(el => el.textContent.trim() === '复制新会话标签')?.click()`);
      await until(() => evaluate(`document.querySelectorAll('.tab-item').length === ${expectedCount}`), `cloned tab #${expectedCount}`);
    }
    await until(() => evaluate("!document.querySelector('.tab-item.state-connecting')"), 'all 5 tabs connected');
    await sleep(180);
    const multiTabMetrics = await evaluate(`(() => {
      const items = [...document.querySelectorAll('.tab-item')];
      const rects = items.map(el => el.getBoundingClientRect());
      const gaps = [];
      for (let i = 1; i < rects.length; i++) gaps.push(rects[i].left - rects[i - 1].right);
      const closeRect = items[0]?.querySelector('.tab-close')?.getBoundingClientRect();
      const allBtn = document.querySelector('#allTabsBtn');
      const allBtnRect = allBtn?.getBoundingClientRect();
      return {
        count: items.length,
        minGap: gaps.length ? Math.min(...gaps) : 0,
        closeW: closeRect ? Math.round(closeRect.width) : 0,
        closeH: closeRect ? Math.round(closeRect.height) : 0,
        allBtnText: allBtn?.textContent?.trim() || '',
        allBtnVisible: Boolean(allBtnRect && allBtnRect.width > 0 && allBtnRect.height > 0),
      };
    })()`);
    assert.equal(multiTabMetrics.count, 5, 'Expected 5 open tabs, got ' + JSON.stringify(multiTabMetrics));
    assert.ok(multiTabMetrics.minGap >= 3, 'Expected separated .tab-item chips (gap >= 3px), got ' + JSON.stringify(multiTabMetrics));
    assert.ok(
      Math.abs(multiTabMetrics.closeW - 20) <= 1 && Math.abs(multiTabMetrics.closeH - 20) <= 1,
      'Expected larger 20x20 .tab-close hit target, got ' + JSON.stringify(multiTabMetrics),
    );
    assert.equal(multiTabMetrics.allBtnVisible, true, 'Expected #allTabsBtn visible with 5 tabs');
    assert.equal(multiTabMetrics.allBtnText, '5', 'Expected #allTabsBtn to display tab count 5, got ' + multiTabMetrics.allBtnText);

    await click('#allTabsBtn');
    await until(() => evaluate("!!document.querySelector('.tabs-switcher-popover')"), 'multi-tab switcher popover');
    await sleep(150);
    const switcherMetrics = await evaluate(`(() => {
      const pop = document.querySelector('.tabs-switcher-popover');
      const search = pop?.querySelector('.tabs-switcher-search input');
      const closeAllBtn = pop?.querySelector('.tabs-switcher-action.danger');
      const closeRowBtn = pop?.querySelector('.tabs-switcher-close');
      const closeR = closeRowBtn?.getBoundingClientRect();
      return {
        rowCount: pop?.querySelectorAll('.tabs-switcher-row').length ?? 0,
        hasSearch: Boolean(search && search.getBoundingClientRect().width > 80),
        closeAllText: closeAllBtn?.textContent?.trim() || '',
        closeW: closeR ? Math.round(closeR.width) : 0,
        closeH: closeR ? Math.round(closeR.height) : 0,
      };
    })()`);
    assert.equal(switcherMetrics.rowCount, 5, 'Expected 5 rows in tab switcher popover, got ' + JSON.stringify(switcherMetrics));
    assert.equal(switcherMetrics.hasSearch, true, 'Expected search input in tab switcher popover');
    assert.equal(switcherMetrics.closeAllText, '关闭全部', 'Expected 关闭全部 button in tab switcher popover');
    assert.ok(
      Math.abs(switcherMetrics.closeW - 26) <= 1 && Math.abs(switcherMetrics.closeH - 26) <= 1,
      'Expected 26x26 .tabs-switcher-close hit target, got ' + JSON.stringify(switcherMetrics),
    );
    await shot('28-MultiTab-Switcher');

    // Exercise live search filter in switcher, then dismiss & verify 关闭所有标签 via tab right-click menu
    await evaluate(`(() => {
      const input = document.querySelector('.tabs-switcher-search input');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, 'GPU');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    await until(
      () =>
        evaluate(
          "document.querySelectorAll('.tabs-switcher-row').length === 1 && (document.querySelector('.tabs-switcher-row')?.textContent || '').includes('Local GPU Box')",
        ),
      'filtered tab switcher results',
    );
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('.tabs-switcher-popover')"), 'closed tab switcher popover');

    await evaluate(`(() => {
      const tab = document.querySelector('.tab-item');
      const r = tab.getBoundingClientRect();
      tab.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: r.x + 12, clientY: r.y + 12 }));
    })()`);
    await until(() => evaluate("[...document.querySelectorAll('.context-menu-item')].some(el => el.textContent.trim() === '关闭所有标签')"), 'tab context menu 关闭所有标签');
    await evaluate(`[...document.querySelectorAll('.context-menu-item')].find(el => el.textContent.trim() === '关闭所有标签')?.click()`);
    await until(() => evaluate("document.querySelectorAll('.tab-item').length === 0"), 'all tabs closed via context menu');

    // Exercise Native Local Terminal opened via + Command Palette (Dark + Light + mixed Local/SSH split)
    await click('#newTabBtn');
    await until(() => evaluate("!!document.querySelector('#commandPalette [data-action-id=\"local-pwsh\"]')"), 'command palette local shell entry');
    await sleep(180);
    await click('#commandPalette [data-action-id="local-pwsh"]');
    await until(() => evaluate("!document.querySelector('#commandPalette')"), 'closed command palette after launching local terminal');
    await until(
      () =>
        evaluate(
          "!!document.querySelector('.pane-xterm-wrap .xterm-screen') && (document.querySelector('.pane-xterm-wrap .xterm-rows')?.textContent || '').includes('PowerShell 7.5.1') && (document.querySelector('.statusbar-left')?.textContent || '').includes('C:\\\\Users\\\\haochi\\\\Projects\\\\Shell')",
        ),
      'native local terminal session & OSC 9;9 cwd',
    );
    await sleep(220);
    const localTermAudit = await evaluate(`(() => ({
      hasSftpBtn: Boolean(document.querySelector('#sftpBtn')),
      hasTunnelsBtn: Boolean(document.querySelector('#tunnelsBtn')),
      hasTelemetry: Boolean(document.querySelector('.statusbar-right .telemetry-trigger')),
      statusLeft: document.querySelector('.statusbar-left')?.textContent?.trim() || '',
      statusRight: document.querySelector('.statusbar-right')?.textContent?.trim() || '',
      tabLabel: document.querySelector('.tab-item.active .tab-label')?.textContent?.trim() || '',
    }))()`);
    assert.equal(localTermAudit.hasSftpBtn, false, 'Local terminal must hide #sftpBtn, got ' + JSON.stringify(localTermAudit));
    assert.equal(localTermAudit.hasTunnelsBtn, false, 'Local terminal must hide #tunnelsBtn, got ' + JSON.stringify(localTermAudit));
    assert.equal(localTermAudit.hasTelemetry, false, 'Local terminal must not show remote server telemetry, got ' + JSON.stringify(localTermAudit));
    assert.ok(localTermAudit.statusLeft.includes('PowerShell 7') && localTermAudit.statusLeft.includes('C:\\Users\\haochi\\Projects\\Shell'), 'Expected shell name + OSC 9;9 cwd in local statusbar-left, got ' + JSON.stringify(localTermAudit));
    assert.ok(localTermAudit.statusRight.includes('本机终端') && /\d+\s*×\s*\d+/.test(localTermAudit.statusRight), 'Expected 本机终端 badge + cols × rows in local statusbar-right, got ' + JSON.stringify(localTermAudit));
    await shot('41-Local-Terminal-Dark');

    // Verify mixed Local Terminal + SSH horizontal split coexists cleanly
    await click('button[aria-label="左右分屏"]');
    await until(() => evaluate("!!document.querySelector('[role=\"dialog\"][aria-label=\"选择分屏主机\"]')"), 'mixed local+SSH split picker');
    await click('#commandPalette [data-action-id="host-p1"]');
    await until(
      () =>
        evaluate(
          "document.querySelectorAll('.pane-box.multi-pane').length === 2 && document.querySelectorAll('.pane-box.multi-pane .xterm-screen').length === 2 && !document.querySelector('.pane-connect-trace')",
        ),
      'mixed local terminal + SSH split panes connected',
    );
    await click('.pane-box.multi-pane.focused .pane-mini-bar button[aria-label="关闭此分屏"]');
    await until(() => evaluate("document.querySelectorAll('.pane-box').length === 1 && !document.querySelector('.pane-box.multi-pane')"), 'closed SSH split pane leaving local terminal');

    // Capture Light Mode Native Local Terminal screenshot, then restore Dark Mode & close local tab
    await click('button[aria-label="切换深浅色外观"]');
    await until(() => evaluate("document.documentElement.dataset.theme === 'light'"), 'switched local terminal to light theme');
    await sleep(220);
    await shot('42-Local-Terminal-Light');
    await click('button[aria-label="切换深浅色外观"]');
    await until(() => evaluate("document.documentElement.dataset.theme === 'dark'"), 'restored dark theme after local terminal capture');
    await click('.tab-item.active .tab-close');
    await until(() => evaluate("document.querySelectorAll('.tab-item').length === 0"), 'closed local terminal tab');

    // Verify Dark Mode split New Host dropdown menu + HostModal solid OS icons + frosted Group ComboBox
    const darkNewHostBg = await evaluate(`(() => {
      const el = document.querySelector('.sidebar-new-split');
      el?.getAnimations?.().forEach(a => a.finish());
      return el ? getComputedStyle(el).backgroundColor : '';
    })()`);
    assert.equal(darkNewHostBg, 'rgb(47, 129, 247)', 'Expected GitHub Dark accent #2F81F7 .sidebar-new-split in dark mode, got ' + darkNewHostBg);
    await click('#addHostMenuBtn');
    await until(() => evaluate("!!document.querySelector('#addHostMenu #importSshBtn') && !!document.querySelector('#addHostMenu #exportHostsBtn') && !document.querySelector('#addHostMenu #sidebarLocalTermBtn')"), 'dark add-host split menu (import/export only)');
    await sleep(160);
    await shot('29-AddHost-SplitMenu-Dark');
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('#addHostMenu')"), 'closed add-host split menu');

    await click('#addHost');
    await until(() => evaluate("!!document.querySelector('#connectDialog')"), 'dark host modal');
    await click('#hostGroup');
    await until(() => evaluate("!!document.querySelector('[data-combobox-open=\"true\"]')"), 'dark group combobox dropdown');
    await sleep(160);
    await shot('30-HostModal-Dark-ComboBox');
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('[data-combobox-open=\"true\"]')"), 'closed dark group combobox');
    await click('#connectDialog [data-close]');
    await until(() => evaluate("!document.querySelector('#connectDialog')"), 'closed dark host modal');

    // Verify Dark Mode Delete Host confirmation dialog (close X button, role="dialog", Escape dismissal)
    await evaluate(`document.querySelector('.host-row[data-profile-id="p3"] button[title="编辑主机"]')?.click()`);
    await until(() => evaluate("!!document.querySelector('#connectDialog #deleteProfile')"), 'edit host modal with delete button');
    await sleep(140);
    await shot('46-HostModal-Dark-Edit');
    await click('#deleteProfile');
    await until(() => evaluate("!!document.querySelector('#confirmDeleteDialog')"), 'delete host confirmation dialog');
    const delAudit = await evaluate(`(() => {
      const sheet = document.querySelector('#confirmDeleteDialog .modal-sheet');
      const closeBtn = document.querySelector('#confirmDeleteDialog .modal-header button[aria-label="关闭"]');
      return {
        role: sheet?.getAttribute('role') || '',
        ariaModal: sheet?.getAttribute('aria-modal') || '',
        hasCloseBtn: Boolean(closeBtn),
      };
    })()`);
    assert.deepEqual(delAudit, { role: 'dialog', ariaModal: 'true', hasCloseBtn: true }, 'Expected role="dialog", aria-modal="true", and close X button on delete host confirm dialog');
    await sleep(140);
    await shot('47-DeleteHost-Confirm-Dark');
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await until(() => evaluate("!document.querySelector('#confirmDeleteDialog')"), 'Escape closes delete host confirmation dialog');
    assert.equal(await evaluate("!!document.querySelector('#connectDialog')"), true, 'Escape on delete confirm must keep underlying HostModal open');
    await click('#connectDialog [data-close]');
    await until(() => evaluate("!document.querySelector('#connectDialog')"), 'closed edit host modal');

    // Verify High-Risk Agent Approval Dialog button hierarchy (neutral 拒绝 .btn + solid danger 允许一次 .btn-danger-solid)
    await evaluate(`window.runtime.EventsEmit('agent:approval', {
      id: 'req-danger-1',
      source: 'mcp',
      clientName: 'Claude Code',
      host: 'Tokyo Production',
      tool: 'shell_exec',
      summary: 'rm -rf /var/tmp/old-release && systemctl restart nginx',
      reason: '包含不可逆删除与服务重启操作，请人工核对后放行',
      risk: 'danger',
      canRememberSession: false,
      segments: [
        { raw: 'rm -rf /var/tmp/old-release', program: 'rm', args: ['-rf', '/var/tmp/old-release'], wrapped: false, hasRedir: false, risk: 'danger', reason: '递归删除' },
        { raw: 'systemctl restart nginx', program: 'systemctl', args: ['restart', 'nginx'], wrapped: false, hasRedir: false, risk: 'caution', reason: '服务重启' }
      ]
    })`);
    await until(() => evaluate("!!document.querySelector('#agentApprovalDialog')"), 'high-risk agent approval dialog');
    const approvalAudit = await evaluate(`(() => {
      const sheet = document.querySelector('#agentApprovalDialog .modal-sheet');
      const btns = [...document.querySelectorAll('#agentApprovalDialog .modal-footer button')];
      const denyBtn = btns.find(b => b.textContent.includes('拒绝'));
      const allowBtn = btns.find(b => b.textContent.includes('允许一次'));
      return {
        role: sheet?.getAttribute('role') || '',
        ariaModal: sheet?.getAttribute('aria-modal') || '',
        denyClasses: denyBtn ? [...denyBtn.classList] : [],
        allowClasses: allowBtn ? [...allowBtn.classList] : [],
      };
    })()`);
    assert.equal(approvalAudit.role, 'dialog', 'Expected role="dialog" on #agentApprovalDialog');
    assert.equal(approvalAudit.ariaModal, 'true', 'Expected aria-modal="true" on #agentApprovalDialog');
    assert.equal(approvalAudit.denyClasses.includes('btn-danger'), false, 'Expected neutral .btn (not .btn-danger) on 拒绝 button, got ' + JSON.stringify(approvalAudit));
    assert.equal(approvalAudit.allowClasses.includes('btn-danger-solid'), true, 'Expected .btn-danger-solid on high-risk 允许一次 button, got ' + JSON.stringify(approvalAudit));
    await sleep(160);
    await shot('48-Agent-Approval-Danger-Dark');
    await evaluate(`[...document.querySelectorAll('#agentApprovalDialog .modal-footer button')].find(b => b.textContent.includes('拒绝'))?.click()`);
    await until(() => evaluate("!document.querySelector('#agentApprovalDialog')"), 'dismissed agent approval dialog');

    // Verify Snippets Drawer uses unified search-box-sm
    await click('#snippetsBtn');
    await until(() => evaluate("!!document.querySelector('.right-drawer .search-box.search-box-sm')"), 'snippets drawer with search-box-sm');
    await sleep(160);
    await shot('49-Snippets-Drawer-Dark');
    await click('#snippetsBtn');
    await until(() => evaluate("!document.querySelector('.right-drawer')"), 'closed snippets drawer');

    const { verifyAIState } = await import('./ai-state.mjs');
    await verifyAIState({ call, evaluate, until, click });
    const { verifySplitWorkspace } = await import('./split-workspace.mjs');
    await verifySplitWorkspace({ call, evaluate, until, click });
    const exportRect = await verifyHostExport({ call, evaluate, until, click, shot });
    await verifyEmptyHostExport({ call, evaluate, until, click, expectedRect: exportRect });

    assert.deepEqual(exceptions, [], 'Expected zero uncaught runtime exceptions');
    assert.deepEqual(consoleErrors, [], 'Expected zero console.error messages');
    console.log(`[Stage 2] Verified all interactive UI views, zero exceptions/console errors, and captured ${shotCount} Retina screenshots.`);
  } finally {
    socket?.close();
    if (edge.exitCode === null) edge.kill();
    server.close();
  }
}

await verifyNativeBinary();
await verifyFrontendUI();
