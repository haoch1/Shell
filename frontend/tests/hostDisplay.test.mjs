import assert from 'node:assert/strict';
import test from 'node:test';
import { formatHostDisplay, maskHostAddress } from '../src/lib/utils.ts';

test('maskHostAddress returns empty string with no dot placeholders', () => {
  assert.equal(maskHostAddress(''), '');
  assert.equal(maskHostAddress('   '), '');
  assert.equal(maskHostAddress('56.10.113.238'), '');
  assert.equal(maskHostAddress(' 10.0.0.1 '), '');
  assert.equal(maskHostAddress('2406:da18:b15:8000:4839:f12a:9c3b:4e21'), '');
  assert.equal(maskHostAddress('[2406:da18::1]'), '');
  assert.equal(maskHostAddress('::1'), '');
  assert.equal(maskHostAddress('prod.example.com'), '');
  assert.equal(maskHostAddress('bastion-01'), '');
});

test('formatHostDisplay preserves standard formatting when hideIp is omitted or false', () => {
  assert.equal(formatHostDisplay(''), '');
  assert.equal(formatHostDisplay('   ', 2222, false), '');

  // IPv4
  assert.equal(formatHostDisplay('56.10.113.238'), '56.10.113.238');
  assert.equal(formatHostDisplay('56.10.113.238', 22), '56.10.113.238');
  assert.equal(formatHostDisplay('56.10.113.238', 22, false), '56.10.113.238');
  assert.equal(formatHostDisplay('56.10.113.238', 58222), '56.10.113.238:58222');
  assert.equal(formatHostDisplay('56.10.113.238', 58222, false), '56.10.113.238:58222');

  // IPv6
  assert.equal(formatHostDisplay('::1', 22, false), '::1');
  assert.equal(formatHostDisplay('[::1]', 2222, false), '::1 :2222');
  assert.equal(formatHostDisplay('2406:da18:b15:8000:4839:f12a:9c3b:4e21', 22), '2406:da18…4e21');
  assert.equal(formatHostDisplay('2406:da18:b15:8000:4839:f12a:9c3b:4e21', 58222, false), '2406:da18…4e21 :58222');

  // Domains
  assert.equal(formatHostDisplay('prod.example.com', 22, false), 'prod.example.com');
  assert.equal(formatHostDisplay('prod.example.com', 2222, false), 'prod.example.com:2222');
  assert.equal(
    formatHostDisplay('very-long-production-host.internal.example.com', 22, false),
    'very-long-…xample.com',
  );
  assert.equal(
    formatHostDisplay('very-long-production-host.internal.example.com', 2222, false),
    'very-long-…xample.com:2222',
  );
});

test('formatHostDisplay completely omits host and port without dots when hideIp is true', () => {
  assert.equal(formatHostDisplay('', 2222, true), '');
  assert.equal(formatHostDisplay('   ', 22, true), '');

  // IPv4 default & custom port
  assert.equal(formatHostDisplay('56.10.113.238', 22, true), '');
  assert.equal(formatHostDisplay('56.10.113.238', 58222, true), '');
  assert.equal(formatHostDisplay('192.168.1.10', undefined, true), '');

  // IPv6 default & custom port
  assert.equal(formatHostDisplay('2406:da18:b15:8000:4839:f12a:9c3b:4e21', 22, true), '');
  assert.equal(formatHostDisplay('2406:da18:b15:8000:4839:f12a:9c3b:4e21', 58222, true), '');
  assert.equal(formatHostDisplay('[::1]', 2222, true), '');

  // Domains default & custom port
  assert.equal(formatHostDisplay('prod.example.com', 22, true), '');
  assert.equal(formatHostDisplay('prod.example.com', 2222, true), '');
  assert.equal(formatHostDisplay('very-long-production-host.internal.example.com', 60022, true), '');
});
