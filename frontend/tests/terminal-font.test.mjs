import test from 'node:test';
import assert from 'node:assert/strict';
import {
  DEFAULT_TERMINAL_FONT,
  TERMINAL_FONT_OPTIONS,
  matchFontOptionValue,
  resolveTerminalFontFamily,
} from '../src/lib/terminalFont.ts';

test('resolveTerminalFontFamily injects CJK sans-serif fallback before monospace for custom Latin stacks', () => {
  const customLatin = '"IBM Plex Mono", Consolas, monospace';
  const resolved = resolveTerminalFontFamily(customLatin);
  const cjkIdx = resolved.indexOf('Microsoft YaHei UI');
  const monoIdx = resolved.lastIndexOf('monospace');
  assert.ok(cjkIdx > 0, 'Expected Microsoft YaHei UI injected');
  assert.ok(cjkIdx < monoIdx, 'Expected CJK fallback placed before generic monospace');
});

test('resolveTerminalFontFamily preserves stacks that already include a CJK sans-serif font', () => {
  const custom = '"JetBrains Mono", "Sarasa Mono SC", monospace';
  assert.equal(resolveTerminalFontFamily(custom), custom);
});

test('resolveTerminalFontFamily appends fallback and monospace when missing', () => {
  const bare = '"IBM Plex Mono"';
  const resolved = resolveTerminalFontFamily(bare);
  assert.ok(resolved.startsWith('"IBM Plex Mono", '));
  assert.ok(resolved.includes('Microsoft YaHei UI'));
  assert.ok(resolved.endsWith(', monospace'));
});

test('resolveTerminalFontFamily falls back to DEFAULT_TERMINAL_FONT on empty input', () => {
  assert.equal(resolveTerminalFontFamily(''), DEFAULT_TERMINAL_FONT);
  assert.equal(resolveTerminalFontFamily('   '), DEFAULT_TERMINAL_FONT);
});

test('matchFontOptionValue matches curated preset options directly and classifies others as custom', () => {
  assert.equal(matchFontOptionValue(''), TERMINAL_FONT_OPTIONS[0].value);
  assert.equal(matchFontOptionValue('   '), TERMINAL_FONT_OPTIONS[0].value);
  for (const option of TERMINAL_FONT_OPTIONS) {
    assert.equal(matchFontOptionValue(option.value), option.value);
  }
  assert.equal(matchFontOptionValue('"Custom Nerd Font", monospace'), '__custom__');
});
