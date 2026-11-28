import assert from 'node:assert/strict';
import test from 'node:test';
import {
  basenameRemotePath,
  bitsToMode,
  clampDrawerWidth,
  classifyFileKind,
  getFileKindLabel,
  getFileKindToneClass,
  joinRemotePath,
  modeToBits,
  modeToOctal,
  modeToSymbolic,
  normalizeRemotePath,
  octalToMode,
  parentRemotePath,
  parseStoredBookmarks,
  pushNavHistory,
  resolveDestinationPath,
  SFTP_WIDTH_DEFAULT,
  SFTP_WIDTH_MAX,
  SFTP_WIDTH_MIN,
  sortRemoteEntries,
  stepNavHistory,
  suggestCopyName,
} from '../src/lib/sftpUtils.ts';

test('classifyFileKind classifies directories and all specialized file kinds accurately', () => {
  assert.equal(classifyFileKind('src', true, false), 'dir');
  assert.equal(classifyFileKind('data.json', true, true), 'dir');

  // Code
  for (const name of ['main.go', 'app.tsx', 'script.py', 'index.js', 'schema.sql', 'style.scss', 'fix.patch']) {
    assert.equal(classifyFileKind(name, false, false), 'code', name);
  }

  // Config
  for (const name of [
    'nginx.conf',
    'docker-compose.yml',
    'docker-compose.prod.yaml',
    'Dockerfile',
    'Dockerfile.dev',
    'Makefile',
    '.env',
    '.env.production',
    'sshd_config',
    'app.service',
    'config.toml',
    'package.json',
    '.gitignore',
  ]) {
    assert.equal(classifyFileKind(name, false, false), 'config', name);
  }

  // Shell
  for (const name of ['deploy.sh', '.bashrc', '.zshrc', 'init.bash', 'run.ps1', 'profile.zsh']) {
    assert.equal(classifyFileKind(name, false, false), 'shell', name);
  }

  // Archive
  for (const name of ['backup.tar.gz', 'release.tar.zst', 'bundle.zip', 'app.tgz', 'pkg.deb', 'lib.7z']) {
    assert.equal(classifyFileKind(name, false, false), 'archive', name);
  }

  // Image
  for (const name of ['logo.png', 'hero.webp', 'icon.svg', 'photo.JPG']) {
    assert.equal(classifyFileKind(name, false, false), 'image', name);
  }

  // Log
  for (const name of ['access.log', 'error.log.1', 'syslog', 'server.out', 'worker.err']) {
    assert.equal(classifyFileKind(name, false, false), 'log', name);
  }

  // Key
  for (const name of ['id_ed25519', 'id_rsa.pub', 'authorized_keys', 'server.pem', 'tls.key', 'ca.crt']) {
    assert.equal(classifyFileKind(name, false, false), 'key', name);
  }

  // Doc
  for (const name of ['README.md', 'LICENSE', 'CHANGELOG.txt', 'notes.pdf', 'report.csv']) {
    assert.equal(classifyFileKind(name, false, false), 'doc', name);
  }

  // Generic fallback
  assert.equal(classifyFileKind('binary_blob', false, false), 'file');
  assert.equal(classifyFileKind('unknown.xyz', false, true), 'file');

  // Tone class & label mapping
  assert.equal(getFileKindToneClass('dir'), 'sftp-kind-dir');
  assert.equal(getFileKindToneClass('key'), 'sftp-kind-key');
  assert.equal(getFileKindLabel('archive'), '压缩包');
});

test('chmod helpers convert symmetrically between numeric mode, bits, octal, and symbolic', () => {
  const b755 = modeToBits(0o755);
  assert.deepEqual(b755, {
    owner: { r: true, w: true, x: true },
    group: { r: true, w: false, x: true },
    other: { r: true, w: false, x: true },
  });
  assert.equal(bitsToMode(b755), 0o755);
  assert.equal(modeToOctal(0o755), '755');
  assert.equal(modeToSymbolic(0o755), 'rwxr-xr-x');

  const b644 = modeToBits(0o644);
  assert.deepEqual(b644, {
    owner: { r: true, w: true, x: false },
    group: { r: true, w: false, x: false },
    other: { r: true, w: false, x: false },
  });
  assert.equal(bitsToMode(b644), 0o644);
  assert.equal(modeToOctal(0o644), '644');
  assert.equal(modeToSymbolic(0o644), 'rw-r--r--');

  // Masks high POSIX file-type bits cleanly
  assert.equal(modeToOctal(0o100600), '600');
  assert.equal(modeToSymbolic(0o100600), 'rw-------');

  // Preserves special permission bits (setuid / setgid / sticky)
  assert.equal(modeToOctal(0o4755), '4755');
  assert.equal(modeToSymbolic(0o4755), 'rwsr-xr-x');
  assert.equal(modeToSymbolic(0o2750), 'rwxr-s---');
  assert.equal(modeToSymbolic(0o1777), 'rwxrwxrwt');
  assert.equal(modeToSymbolic(0o4644), 'rwSr--r--');
  assert.equal(modeToOctal(0), '000');
  assert.equal(modeToSymbolic(0), '---------');

  // octalToMode validates strictly ^[0-7]{3,4}$
  assert.equal(octalToMode('755'), 0o755);
  assert.equal(octalToMode('0644'), 0o644);
  assert.equal(octalToMode(' 4755 '), 0o4755);
  assert.equal(octalToMode('75'), null);
  assert.equal(octalToMode('75555'), null);
  assert.equal(octalToMode('789'), null);
  assert.equal(octalToMode('abc'), null);
  assert.equal(octalToMode(''), null);
});

test('path helpers normalize, join, parent, basename, and resolve destinations cleanly', () => {
  assert.equal(normalizeRemotePath('/etc//nginx///'), '/etc/nginx');
  assert.equal(normalizeRemotePath('/'), '/');
  assert.equal(normalizeRemotePath(''), '/');
  assert.equal(normalizeRemotePath('.'), '.');
  assert.equal(normalizeRemotePath('/tmp//folder /'), '/tmp/folder ');

  assert.equal(joinRemotePath('/', 'etc'), '/etc');
  assert.equal(joinRemotePath('/etc/nginx/', 'nginx.conf'), '/etc/nginx/nginx.conf');
  assert.equal(joinRemotePath('/etc/nginx', '/var/log/app.log'), '/var/log/app.log');
  assert.equal(joinRemotePath('.', 'hello.txt'), './hello.txt');
  assert.equal(joinRemotePath('/tmp/folder ', ' name '), '/tmp/folder / name ');

  assert.equal(parentRemotePath('/etc/nginx/nginx.conf'), '/etc/nginx');
  assert.equal(parentRemotePath('/etc/nginx/'), '/etc');
  assert.equal(parentRemotePath('/etc'), '/');
  assert.equal(parentRemotePath('/'), '/');
  assert.equal(parentRemotePath(''), '/');

  assert.equal(basenameRemotePath('/etc/nginx/nginx.conf'), 'nginx.conf');
  assert.equal(basenameRemotePath('/'), '/');
  assert.equal(basenameRemotePath('/tmp/file '), 'file ');

  assert.equal(resolveDestinationPath('/etc/nginx/site.conf', 'api.conf'), '/etc/nginx/api.conf');
  assert.equal(resolveDestinationPath('/etc/nginx/site.conf', '/tmp/site.conf'), '/tmp/site.conf');
  assert.equal(resolveDestinationPath('/etc/nginx/site.conf', 'sites-enabled/site.conf'), '/etc/nginx/sites-enabled/site.conf');
  assert.equal(resolveDestinationPath('/tmp/folder /file', 'file '), '/tmp/folder /file ');
});

test('suggestCopyName handles standard files, extensionless names, dotfiles, multi-ext archives, dirs, and collisions', () => {
  assert.equal(suggestCopyName('nginx.conf'), 'nginx.copy.conf');
  assert.equal(suggestCopyName('data'), 'data-copy');
  assert.equal(suggestCopyName('.bashrc'), '.bashrc-copy');
  assert.equal(suggestCopyName('.eslintrc.json'), '.eslintrc.copy.json');
  assert.equal(suggestCopyName('backup.tar.gz'), 'backup.copy.tar.gz');
  assert.equal(suggestCopyName('release.tar.xz'), 'release.copy.tar.xz');
  assert.equal(suggestCopyName('v1.2.0', true), 'v1.2.0-copy');

  // Collision avoidance when existing names are provided
  assert.equal(
    suggestCopyName('nginx.conf', false, ['nginx.conf', 'nginx.copy.conf']),
    'nginx.copy2.conf',
  );
  assert.equal(
    suggestCopyName('data', true, ['data', 'data-copy', 'data-copy-2']),
    'data-copy-3',
  );
});

test('sortRemoteEntries keeps directories on top and respects sort keys and direction', () => {
  const sample = [
    { name: 'b.log', isDir: false, isSymlink: false, size: 500, modTime: 200 },
    { name: 'z-dir', isDir: true, isSymlink: false, size: 4096, modTime: 100 },
    { name: 'a.ts', isDir: false, isSymlink: false, size: 100, modTime: 300 },
    { name: 'a-dir', isDir: true, isSymlink: false, size: 4096, modTime: 400 },
    { name: 'c.conf', isDir: false, isSymlink: false, size: 900, modTime: 50 },
  ];

  assert.deepEqual(
    sortRemoteEntries(sample, 'name', 'asc').map((e) => e.name),
    ['a-dir', 'z-dir', 'a.ts', 'b.log', 'c.conf'],
  );
  assert.deepEqual(
    sortRemoteEntries(sample, 'name', 'desc').map((e) => e.name),
    ['z-dir', 'a-dir', 'c.conf', 'b.log', 'a.ts'],
  );
  assert.deepEqual(
    sortRemoteEntries(sample, 'size', 'desc').map((e) => e.name),
    ['z-dir', 'a-dir', 'c.conf', 'b.log', 'a.ts'],
  );
  assert.deepEqual(
    sortRemoteEntries(sample, 'modTime', 'desc').map((e) => e.name),
    ['a-dir', 'z-dir', 'a.ts', 'b.log', 'c.conf'],
  );
  assert.deepEqual(
    sortRemoteEntries(sample, 'type', 'asc').map((e) => e.name),
    ['a-dir', 'z-dir', 'a.ts', 'c.conf', 'b.log'],
  );
});

test('navigation history, drawer width clamping, and bookmark parser behave predictably', () => {
  let hist = { stack: ['/home/ops'], index: 0 };
  hist = pushNavHistory(hist, '/etc');
  hist = pushNavHistory(hist, '/var/log');
  // Duplicate push is ignored
  hist = pushNavHistory(hist, '/var/log/');
  assert.deepEqual(hist, { stack: ['/home/ops', '/etc', '/var/log'], index: 2 });

  const back1 = stepNavHistory(hist, -1);
  assert.equal(back1.path, '/etc');
  assert.equal(back1.history.index, 1);

  // Pushing after going back truncates forward stack
  const branched = pushNavHistory(back1.history, '/opt');
  assert.deepEqual(branched, { stack: ['/home/ops', '/etc', '/opt'], index: 2 });
  assert.equal(stepNavHistory(branched, 1).path, null);

  assert.equal(clampDrawerWidth(100), SFTP_WIDTH_MIN);
  assert.equal(clampDrawerWidth(999), SFTP_WIDTH_MAX);
  assert.equal(clampDrawerWidth(520.4), 520);
  assert.equal(clampDrawerWidth(Number.NaN), SFTP_WIDTH_DEFAULT);

  assert.deepEqual(parseStoredBookmarks('["/etc/", "/etc", "/opt", 42]'), ['/etc', '/opt']);
  assert.deepEqual(parseStoredBookmarks('not-json'), []);
});
