import assert from 'node:assert/strict';
import test from 'node:test';
import { formatConnectError } from '../src/lib/connectError.ts';

test('formatConnectError classifies authentication failures and trims redundant SSH wrappers', () => {
  const cases = [
    'SSH 握手失败：ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain',
    'ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain',
    'Permission denied (publickey,password).',
    'x509: decryption password incorrect / wrong passphrase',
    '此私钥需要口令',
    '打开 SSH 私钥失败：open /bad/key: The system cannot find the file specified.',
    '读取 SSH 私钥失败，请检查格式和口令：ssh: cannot decode encrypted private keys',
    '密钥格式不受支持',
    '密码不能为空',
  ];

  for (const raw of cases) {
    const res = formatConnectError(raw);
    assert.equal(res.title, '身份认证失败：密码或密钥不正确', `raw=${raw}`);
    assert.equal(res.canEditProfile, true, `raw=${raw}`);
    assert.ok(res.detail && res.detail.length > 0, `expected detail for raw=${raw}`);
  }

  const trimmed = formatConnectError(
    'SSH 握手失败：ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain',
  );
  assert.equal(
    trimmed.detail,
    'ssh: unable to authenticate, attempted methods [none password], no supported methods remain',
  );
});

test('formatConnectError classifies timeout errors', () => {
  for (const raw of [
    '连接 10.0.0.1:22 失败：dial tcp 10.0.0.1:22: i/o timeout',
    'SSH 握手失败：context deadline exceeded',
    'dial tcp 203.0.113.10:22: connectex: A connection attempt failed because the connected party timed out.',
  ]) {
    const res = formatConnectError(raw);
    assert.equal(res.title, '连接超时：目标主机无响应', `raw=${raw}`);
    assert.equal(res.canEditProfile, true);
    assert.ok(res.detail);
  }
});

test('formatConnectError classifies connection refused errors', () => {
  for (const raw of [
    '连接 127.0.0.1:2222 失败：dial tcp 127.0.0.1:2222: connect: connection refused',
    '连接 192.168.1.5:22 失败：dial tcp 192.168.1.5:22: connectex: No connection could be made because the target machine actively refused it.',
  ]) {
    const res = formatConnectError(raw);
    assert.equal(res.title, '连接被拒绝：请检查 SSH 端口或防火墙', `raw=${raw}`);
    assert.equal(res.canEditProfile, true);
    assert.ok(res.detail);
  }
});

test('formatConnectError classifies DNS and host lookup errors', () => {
  for (const raw of [
    '连接 bad.invalid:22 失败：dial tcp: lookup bad.invalid: no such host',
    'dial tcp: lookup prod-db.internal on 127.0.0.53:53: server misbehaving',
  ]) {
    const res = formatConnectError(raw);
    assert.equal(res.title, '域名解析失败：无法解析主机地址', `raw=${raw}`);
    assert.equal(res.canEditProfile, true);
    assert.ok(res.detail);
  }
});

test('formatConnectError classifies unreachable network / route errors', () => {
  for (const raw of [
    'dial tcp 198.51.100.2:22: connect: network is unreachable',
    'dial tcp 10.99.0.1:22: connect: no route to host',
  ]) {
    const res = formatConnectError(raw);
    assert.equal(res.title, '网络不可达：请检查网络或代理设置', `raw=${raw}`);
    assert.equal(res.canEditProfile, true);
    assert.ok(res.detail);
  }
});

test('formatConnectError classifies host key verification errors with canEditProfile=false', () => {
  for (const raw of [
    'SSH 握手失败：ssh: handshake failed: knownhosts: key mismatch',
    'SSH 握手失败：主机指纹已变更或已撤销，连接已停止。请核实服务器身份后检查 C:\\Users\\ops\\known_hosts：knownhosts: key mismatch',
    'SSH 握手失败：已拒绝此主机指纹',
    'host key verification failed',
    '用户拒绝信任该服务器',
  ]) {
    const res = formatConnectError(raw);
    assert.equal(res.title, '主机指纹校验未通过', `raw=${raw}`);
    assert.equal(res.canEditProfile, false, `raw=${raw}`);
    assert.ok(res.detail);
  }
});

test('formatConnectError classifies jump host errors ahead of nested network/auth errors', () => {
  for (const raw of [
    '连接跳板机 10.0.0.1 失败：dial tcp 10.0.0.1:22: i/o timeout',
    '通过跳板机连接 10.0.0.2:22 失败：ssh: rejected: connect failed (Connection refused)',
    '未找到所配置的跳板机主机资料',
    'failed to establish jump tunnel: connection refused',
  ]) {
    const res = formatConnectError(raw);
    assert.equal(res.title, '跳板机连接失败', `raw=${raw}`);
    assert.equal(res.canEditProfile, true);
    assert.ok(res.detail);
  }
});

test('formatConnectError classifies proxy errors ahead of underlying dial refused/timeout', () => {
  for (const raw of [
    '连接 10.0.0.1:22 失败：socks connect tcp 127.0.0.1:1080->10.0.0.1:22: dial tcp 127.0.0.1:1080: connectex: No connection could be made because the target machine actively refused it.',
    '连接 10.0.0.1:22 失败：连接 HTTP 代理 127.0.0.1:7890 失败：dial tcp 127.0.0.1:7890: i/o timeout',
    'proxy authentication required (407)',
  ]) {
    const res = formatConnectError(raw);
    assert.equal(res.title, '代理连接失败：请检查代理配置', `raw=${raw}`);
    assert.equal(res.canEditProfile, true);
    assert.ok(res.detail);
  }
});

test('formatConnectError handles fallback, empty input, whitespace normalization, and 240-char truncation', () => {
  assert.deepEqual(formatConnectError(''), {
    title: '连接失败',
    canEditProfile: true,
  });

  assert.deepEqual(formatConnectError('   '), {
    title: '连接失败',
    canEditProfile: true,
  });

  assert.deepEqual(formatConnectError('连接失败'), {
    title: '连接失败',
    canEditProfile: true,
  });

  const generic = formatConnectError('Error:   unexpected EOF\n\tfrom remote peer  ');
  assert.deepEqual(generic, {
    title: '连接失败',
    detail: 'unexpected EOF from remote peer',
    canEditProfile: true,
  });

  const huge = 'SSH 握手失败：' + 'a'.repeat(400);
  const truncated = formatConnectError(huge);
  assert.equal(truncated.title, '连接失败');
  assert.equal(truncated.canEditProfile, true);
  assert.ok(truncated.detail);
  assert.ok(truncated.detail.length <= 240, `expected <= 240 chars, got ${truncated.detail.length}`);
  assert.ok(truncated.detail.endsWith('…'));
});
