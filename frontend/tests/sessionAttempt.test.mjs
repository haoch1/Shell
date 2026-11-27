import assert from 'node:assert/strict';
import test from 'node:test';
import { connectIfOpen, reconnectSecret, sameSessionTarget } from '../src/lib/sessionAttempt.ts';

test('remembered tabs can match display edits but cannot cross connection targets', () => {
  const profile = { id: 'one', name: 'host', host: 'app.example', user: 'admin', port: 22, auth: 'key' };
  assert.equal(sameSessionTarget(profile, { ...profile, name: 'New label', host: 'APP.example', proxyPort: 0 }), true);
  for (const [field, value] of Object.entries({
    id: 'two', host: 'other.example', user: 'guest', port: 2222, auth: 'agent',
    keyPath: 'other.key', jumpHostId: 'jump', proxyType: 'system', proxyHost: 'proxy.example',
    proxyPort: 1080, proxyUser: 'proxy-user', agentAccess: 'deny', forwardAgent: true, startupCommand: 'pwd',
  })) {
    assert.equal(sameSessionTarget(profile, { ...profile, [field]: value }), false, field);
  }
});

test('a pane closed before its first frame never opens a connection', async () => {
  const unexpected = () => assert.fail('closed pane made a bridge call');
  assert.equal(await connectIfOpen(AbortSignal.abort(), unexpected, unexpected, unexpected), false);
});

test('reconnect never reuses a typed password across accounts or after forgetting it', () => {
  const profile = { id: 'one', host: 'app.example', port: 22, user: 'alice', auth: 'password' };
  const previous = { profile, secret: 'test-fixture', remember: false };
  assert.equal(reconnectSecret(previous, profile), previous.secret);
  assert.equal(reconnectSecret(previous, { ...profile, user: 'bob' }), null);
  assert.equal(reconnectSecret({ ...previous, remember: true }, profile), null);
  assert.equal(reconnectSecret(previous, { ...profile, user: 'bob', remembered: true }), '');
  assert.equal(reconnectSecret(previous, { ...profile, auth: 'agent' }), '');
});

test('closing a pane during WebSocket setup skips SSH', async () => {
  const controller = new AbortController();
  const ready = Promise.withResolvers();
  const unexpected = () => assert.fail('unexpected SSH bridge call');
  const pending = connectIfOpen(controller.signal, () => ready.promise, unexpected, unexpected);
  controller.abort();
  ready.resolve();
  assert.equal(await pending, false);
});

test('a backend session accepted after its pane closes is released', async () => {
  const controller = new AbortController();
  const connecting = Promise.withResolvers();
  const connected = Promise.withResolvers();
  let disconnects = 0;
  const pending = connectIfOpen(controller.signal, async () => {}, () => {
    connecting.resolve();
    return connected.promise;
  }, async () => { disconnects++; });
  await connecting.promise;
  controller.abort();
  connected.resolve();
  assert.equal(await pending, false);
  assert.equal(disconnects, 1);
});

test('an open pane retains its session and receives handshake errors', async () => {
  const { signal } = new AbortController();
  const prepare = async () => {};
  const disconnect = () => assert.fail('unexpected disconnect');
  assert.equal(await connectIfOpen(signal, prepare, async () => {}, disconnect), true);
  const failure = new Error('handshake failed');
  await assert.rejects(connectIfOpen(signal, prepare, async () => { throw failure; }, disconnect), failure);
});
