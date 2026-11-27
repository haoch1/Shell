import assert from 'node:assert/strict';
import test from 'node:test';
import { TerminalHighlighter } from '../src/lib/terminalHighlight.ts';

function stripAnsi(text) {
  return text.replace(/\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, '');
}

test('highlights plain Debian shell prompt at line start (including after OSC title)', () => {
  const h = new TerminalHighlighter();
  const raw = '\x1b[?2004h\x1b]0;root@debian: ~\x07root@debian:~# ';
  const out = h.process(raw);

  assert.equal(stripAnsi(out), 'root@debian:~# ');
  assert.ok(out.includes('\x1b[?2004h\x1b]0;root@debian: ~\x07'), 'preserves bracketed paste and OSC title');
  assert.ok(out.includes('\x1b[32mroot@debian\x1b[0m:'), 'highlights user@host green');
  assert.ok(out.includes('\x1b[36m~\x1b[0m'), 'highlights path cyan');
  assert.ok(out.includes('\x1b[1m#\x1b[0m '), 'bolds prompt sigil');

  // Bracketed RHEL/Rocky prompt after newline
  const out2 = h.process('\r\n[admin@prod-01 /etc/nginx]$ ');
  assert.equal(stripAnsi(out2), '\r\n[admin@prod-01 /etc/nginx]$ ');
  assert.ok(out2.includes('[\x1b[32madmin@prod-01\x1b[0m \x1b[36m/etc/nginx\x1b[0m]\x1b[1m$\x1b[0m '));
});

test('highlights Last login IPv4, ports, CIDR, and URLs without corrupting text', () => {
  const h = new TerminalHighlighter();
  const login = 'Last login: Fri Nov 27 14:20:01 2026 from 8.217.10.13\r\n';
  const outLogin = h.process(login);
  assert.equal(stripAnsi(outLogin), login);
  assert.ok(outLogin.includes('\x1b[35m8.217.10.13\x1b[0m'));

  const net = 'Proxy 10.0.1.8:8080 subnet 192.168.0.0/24 docs https://docs.example.com/v2/api\r\n';
  const outNet = h.process(net);
  assert.equal(stripAnsi(outNet), net);
  assert.ok(outNet.includes('\x1b[35m10.0.1.8:8080\x1b[0m'));
  assert.ok(outNet.includes('\x1b[35m192.168.0.0/24\x1b[0m'));
  assert.ok(outNet.includes('\x1b[36mhttps://docs.example.com/v2/api\x1b[0m'));

  // Invalid IPv4 / version numbers are not highlighted as IPs
  const ver = 'Linux kernel 6.1.0.999 version 1.2.3.4.5\r\n';
  const outVer = h.process(ver);
  assert.equal(outVer, ver);
});

test('highlights docker stats JSON keys, metrics, and status keywords', () => {
  const h = new TerminalHighlighter();
  const jsonLine = '{"Container":"api-prod","CPUPerc":"12.45%","MemUsage":"256MiB / 3.84GiB","Status":"RUNNING"}\r\n';
  const out = h.process(jsonLine);

  assert.equal(stripAnsi(out), jsonLine);
  assert.ok(out.includes('\x1b[34m"Container"\x1b[0m:'));
  assert.ok(out.includes('\x1b[34m"CPUPerc"\x1b[0m:'));
  assert.ok(out.includes('\x1b[33m12.45%\x1b[0m'));
  assert.ok(out.includes('\x1b[33m256MiB\x1b[0m'));
  assert.ok(out.includes('\x1b[33m3.84GiB\x1b[0m'));
  assert.ok(out.includes('\x1b[32mRUNNING\x1b[0m'));
});

test('highlights journalctl logs, df -h output, and error/warn/ok keywords', () => {
  const h = new TerminalHighlighter();
  const journal = [
    '2026-11-27T14:22:01Z host nginx[421]: ERROR upstream 10.0.0.5:8080 Connection refused',
    '2026-11-27 14:22:02 host worker[88]: WARNING retry after TIMEOUT (250ms) at /var/log/worker/app.log',
    '2026-11-27 14:22:05 host systemd[1]: status OK service ACTIVE container Up 3 days',
    '/dev/vda1        40G   12G   26G  32% /',
  ].join('\r\n') + '\r\n';

  const out = h.process(journal);
  assert.equal(stripAnsi(out), journal);
  assert.ok(out.includes('\x1b[90m2026-11-27T14:22:01Z\x1b[0m'));
  assert.ok(out.includes('\x1b[90m2026-11-27 14:22:02\x1b[0m'));
  assert.ok(out.includes('\x1b[31mERROR\x1b[0m'));
  assert.ok(out.includes('\x1b[31mrefused\x1b[0m'));
  assert.ok(out.includes('\x1b[33mWARNING\x1b[0m'));
  assert.ok(out.includes('\x1b[33mTIMEOUT\x1b[0m'));
  assert.ok(out.includes('\x1b[33m250ms\x1b[0m'));
  assert.ok(out.includes('\x1b[36m/var/log/worker/app.log\x1b[0m'));
  assert.ok(out.includes('\x1b[32mOK\x1b[0m'));
  assert.ok(out.includes('\x1b[32mACTIVE\x1b[0m'));
  assert.ok(out.includes('\x1b[32mUp 3 days\x1b[0m'));
  assert.ok(out.includes('\x1b[36m/dev/vda1\x1b[0m'));
  assert.ok(out.includes('\x1b[33m40G\x1b[0m'));
  assert.ok(out.includes('\x1b[33m32%\x1b[0m'));
});

test('preserves already-colored ANSI output untouched (including truecolor with 0 channels)', () => {
  const h = new TerminalHighlighter();
  const coloredPrompt = '\x1b[01;32mroot@ubuntu\x1b[00m:\x1b[01;34m/etc/nginx\x1b[00m# ';
  assert.equal(h.process(coloredPrompt), coloredPrompt);

  const lsAndGit =
    '\x1b[31m- ERROR 10.0.0.1 /etc/hosts\x1b[0m\r\n' +
    '\x1b[38;2;255;128;0mWARNING 99% https://example.com\x1b[0m\r\n' +
    '\x1b[32mOK\x1b[39m plain 8.8.8.8\r\n';
  const out = h.process(lsAndGit);
  assert.ok(out.startsWith('\x1b[31m- ERROR 10.0.0.1 /etc/hosts\x1b[0m\r\n'));
  assert.ok(out.includes('\x1b[38;2;255;128;0mWARNING 99% https://example.com\x1b[0m\r\n'));
  // After \x1b[39m resets foreground, subsequent plain text resumes highlighting
  assert.ok(out.includes('\x1b[32mOK\x1b[39m plain \x1b[35m8.8.8.8\x1b[0m\r\n'));
});

test('passes alternate-screen TUI sessions (?1049h .. ?1049l) through byte-for-byte untouched', () => {
  const h = new TerminalHighlighter();
  const enterAndBody =
    '\x1b[?1049h\x1b[2J\x1b[Hroot@host:~# ERROR FATAL 10.0.0.1 https://a.com /etc/passwd 99%\r\n';
  assert.equal(h.process(enterAndBody), enterAndBody);

  const midChunk = 'htop CPU 88.5% Mem 4.2GiB WARN OK 192.168.1.1\r\n';
  assert.equal(h.process(midChunk), midChunk);

  const exitAndPlain = 'vim footer\x1b[?1049l\r\nroot@host:~# ERROR 10.0.0.1\r\n';
  const outExit = h.process(exitAndPlain);
  assert.ok(outExit.startsWith('vim footer\x1b[?1049l\r\n'));
  assert.ok(outExit.includes('\x1b[32mroot@host\x1b[0m:'));
  assert.ok(outExit.includes('\x1b[31mERROR\x1b[0m'));
  assert.ok(outExit.includes('\x1b[35m10.0.0.1\x1b[0m'));
});

test('buffers split escape sequences across chunk boundaries without corruption', () => {
  const h = new TerminalHighlighter();

  // Split SGR sequence across two chunks
  const p1 = h.process('Status: \x1b[3');
  const p2 = h.process('1mERROR\x1b[0m OK 10.0.0.1\r\n');
  assert.equal(p1, 'Status: ');
  assert.equal(p2, '\x1b[31mERROR\x1b[0m \x1b[32mOK\x1b[0m \x1b[35m10.0.0.1\x1b[0m\r\n');

  // Split alt-screen DECSET across two chunks
  const a1 = h.process('\x1b[?10');
  const a2 = h.process('49hERROR 10.0.0.1\x1b[?1049l');
  assert.equal(a1, '');
  assert.equal(a2, '\x1b[?1049hERROR 10.0.0.1\x1b[?1049l');

  // Split OSC title before plain prompt
  h.reset();
  const o1 = h.process('\x1b]0;root@box');
  const o2 = h.process(': /var/log\x07root@box:/var/log# ');
  assert.equal(o1, '');
  assert.ok(o2.startsWith('\x1b]0;root@box: /var/log\x07\x1b[32mroot@box\x1b[0m:\x1b[36m/var/log\x1b[0m\x1b[1m#\x1b[0m '));
});

test('fast-paths single-char and 2-char interactive keystroke echo untouched', () => {
  const h = new TerminalHighlighter();
  assert.equal(h.process('a'), 'a');
  assert.equal(h.process('l'), 'l');
  assert.equal(h.process('s'), 's');
  assert.equal(h.process('OK'), 'OK');
  assert.equal(h.process('中'), '中');
});

test('handles apt-get output, split ST/alt-screen exit, bare trailing ESC, and zero ANSI nesting', () => {
  const h = new TerminalHighlighter();
  const apt =
    'Get:1 http://deb.debian.org/debian bookworm InRelease [151kB]\r\n' +
    'Fetched 14.2MB in 2s (7.1MB/s)\r\n';
  const outApt = h.process(apt);
  assert.equal(stripAnsi(outApt), apt);
  assert.ok(outApt.includes('\x1b[36mhttp://deb.debian.org/debian\x1b[0m'));
  assert.ok(outApt.includes('\x1b[33m151kB\x1b[0m'));
  assert.ok(outApt.includes('\x1b[33m14.2MB\x1b[0m'));
  assert.ok(outApt.includes('\x1b[33m2s\x1b[0m'));
  assert.ok(outApt.includes('\x1b[33m7.1MB/s\x1b[0m'));

  // Verify zero nested SGR opens before reset
  const sgrTokens = [...outApt.matchAll(/\x1b\[([0-9;]*)m/g)].map((m) => m[1]);
  let open = false;
  for (const tok of sgrTokens) {
    if (tok === '0') {
      assert.equal(open, true, 'reset should close an active highlight');
      open = false;
    } else {
      assert.equal(open, false, 'highlight SGR must never nest');
      open = true;
    }
  }
  assert.equal(open, false, 'all highlight SGR spans must be closed');

  // Split alt-screen exit (?47h .. ?47l) + bare trailing ESC
  h.reset();
  const s1 = h.process('\x1b[?47hERROR 10.0.0.1\x1b[?4');
  const s2 = h.process('7l\r\nFAILED 10.0.0.2\x1b');
  const s3 = h.process('[32mRAW_OK\x1b[0m\r\n');
  assert.equal(s1, '\x1b[?47hERROR 10.0.0.1');
  assert.equal(s2, '\x1b[?47l\r\n\x1b[31mFAILED\x1b[0m \x1b[35m10.0.0.2\x1b[0m');
  assert.equal(s3, '\x1b[32mRAW_OK\x1b[0m\r\n');

  // Split OSC with ST (ESC \)
  h.reset();
  const osc1 = h.process('\x1b]7;file://host/var/log\x1b');
  const osc2 = h.process('\\root@host:/var/log# ');
  assert.equal(osc1, '');
  assert.ok(osc2.startsWith('\x1b]7;file://host/var/log\x1b\\\x1b[32mroot@host\x1b[0m:\x1b[36m/var/log\x1b[0m\x1b[1m#\x1b[0m '));
});
