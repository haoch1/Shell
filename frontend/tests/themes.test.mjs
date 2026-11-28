import test from 'node:test';
import assert from 'node:assert/strict';
import {
  TERMINAL_THEMES,
  resolveTerminalTheme,
  syncTerminalThemeForAppearance,
} from '../src/lib/themes.ts';

const HEX_RE = /^#[0-9A-Fa-f]{6}$/;

const ANSI_KEYS = [
  'black',
  'red',
  'green',
  'yellow',
  'blue',
  'magenta',
  'cyan',
  'white',
  'brightBlack',
  'brightRed',
  'brightGreen',
  'brightYellow',
  'brightBlue',
  'brightMagenta',
  'brightCyan',
  'brightWhite',
];

const LIGHT_BRIGHT_CHROMATIC_KEYS = [
  'brightRed',
  'brightGreen',
  'brightYellow',
  'brightBlue',
  'brightMagenta',
  'brightCyan',
];

const DARK_BADGE_BG_KEYS = ['red', 'green', 'yellow', 'cyan'];

function relativeLuminance(hex) {
  assert.match(hex, HEX_RE, `Expected #RRGGBB hex color, got ${hex}`);
  const r = parseInt(hex.slice(1, 3), 16) / 255;
  const g = parseInt(hex.slice(3, 5), 16) / 255;
  const b = parseInt(hex.slice(5, 7), 16) / 255;
  const linearize = (c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return 0.2126 * linearize(r) + 0.7152 * linearize(g) + 0.0722 * linearize(b);
}

function contrastRatio(hexA, hexB) {
  const lumA = relativeLuminance(hexA);
  const lumB = relativeLuminance(hexB);
  const lighter = Math.max(lumA, lumB);
  const darker = Math.min(lumA, lumB);
  return (lighter + 0.05) / (darker + 0.05);
}

test('TERMINAL_THEMES exposes all 5 curated presets with distinct backgrounds and valid #RRGGBB tokens', () => {
  const expectedIds = ['shell-dark', 'tokyo-night', 'catppuccin-mocha', 'shell-light', 'warm-paper'];
  assert.deepEqual(
    TERMINAL_THEMES.map((t) => t.id),
    expectedIds,
  );

  const backgrounds = new Set();
  for (const preset of TERMINAL_THEMES) {
    assert.equal(preset.preview.length, 4, `${preset.id} preview should have 4 hex colors`);
    for (const swatch of preset.preview) {
      assert.match(swatch, HEX_RE, `${preset.id} preview swatch ${swatch} must be #RRGGBB`);
    }

    const { theme } = preset;
    for (const key of ['background', 'foreground', 'cursor', 'cursorAccent', ...ANSI_KEYS]) {
      const value = theme[key];
      assert.equal(typeof value, 'string', `${preset.id}.theme.${key} must be defined`);
      assert.match(value, HEX_RE, `${preset.id}.theme.${key} (${value}) must be valid #RRGGBB`);
    }

    assert.equal(
      preset.preview[0].toUpperCase(),
      theme.background.toUpperCase(),
      `${preset.id} preview[0] must match terminal background`,
    );
    assert.equal(
      preset.preview[1].toUpperCase(),
      theme.foreground.toUpperCase(),
      `${preset.id} preview[1] must match terminal foreground`,
    );
    backgrounds.add(theme.background.toUpperCase());
  }

  assert.equal(backgrounds.size, expectedIds.length, 'Every global palette must have a distinct terminal background');
});

test('Every preset achieves >= 5.5:1 WCAG contrast on foreground and ANSI colors (with >= 6.5:1 dark badge ink)', () => {
  for (const preset of TERMINAL_THEMES) {
    const bg = preset.theme.background;
    const fgRatio = contrastRatio(preset.theme.foreground, bg);
    assert.ok(
      fgRatio >= 5.5,
      `${preset.id} foreground ${preset.theme.foreground} vs ${bg} contrast ${fgRatio.toFixed(2)} < 5.5`,
    );

    for (const key of ANSI_KEYS) {
      const color = preset.theme[key];
      if (preset.isDark && key === 'black') {
        for (const badgeKey of DARK_BADGE_BG_KEYS) {
          const badgeBg = preset.theme[badgeKey];
          const badgeRatio = contrastRatio(color, badgeBg);
          assert.ok(
            badgeRatio >= 6.5,
            `${preset.id} dark badge ink black (${color}) vs ${badgeKey} (${badgeBg}) contrast ${badgeRatio.toFixed(2)} < 6.5`,
          );
        }
        continue;
      }

      const ratio = contrastRatio(color, bg);
      assert.ok(
        ratio >= 5.5,
        `${preset.id}.${key} (${color}) vs background (${bg}) contrast ${ratio.toFixed(2)} < 5.5`,
      );
    }
  }
});

test('Light presets keep all bright chromatic ANSI colors >= 6.5:1 contrast against background', () => {
  const lightPresets = TERMINAL_THEMES.filter((preset) => !preset.isDark);
  assert.deepEqual(
    lightPresets.map((p) => p.id),
    ['shell-light', 'warm-paper'],
  );

  for (const preset of lightPresets) {
    const bg = preset.theme.background;
    for (const key of LIGHT_BRIGHT_CHROMATIC_KEYS) {
      const color = preset.theme[key];
      const ratio = contrastRatio(color, bg);
      assert.ok(
        ratio >= 6.5,
        `${preset.id}.${key} (${color}) vs background (${bg}) contrast ${ratio.toFixed(2)} < 6.5`,
      );
    }
  }
});

test('resolveTerminalTheme handles auto, legacy IDs, unknown IDs, and direct presets', () => {
  assert.equal(resolveTerminalTheme('auto', true).id, 'shell-dark');
  assert.equal(resolveTerminalTheme('auto', false).id, 'shell-light');
  assert.equal(resolveTerminalTheme('', true).id, 'shell-dark');
  assert.equal(resolveTerminalTheme('', false).id, 'shell-light');

  assert.equal(resolveTerminalTheme('one-dark', false).id, 'shell-dark');
  assert.equal(resolveTerminalTheme('dracula', false).id, 'tokyo-night');
  assert.equal(resolveTerminalTheme('rose-pine', false).id, 'catppuccin-mocha');
  assert.equal(resolveTerminalTheme('github-light', true).id, 'shell-light');
  assert.equal(resolveTerminalTheme('solarized-light', true).id, 'warm-paper');
  assert.equal(resolveTerminalTheme('rose-pine-dawn', true).id, 'warm-paper');
  assert.equal(resolveTerminalTheme('catppuccin-latte', true).id, 'warm-paper');

  assert.equal(resolveTerminalTheme('nonexistent-theme', true).id, 'shell-dark');
  assert.equal(resolveTerminalTheme('nonexistent-theme', false).id, 'shell-light');
});

test('syncTerminalThemeForAppearance preserves auto, keeps same-polarity choices, and pairs across light/dark', () => {
  assert.equal(syncTerminalThemeForAppearance('auto', true), 'auto');
  assert.equal(syncTerminalThemeForAppearance('auto', false), 'auto');
  assert.equal(syncTerminalThemeForAppearance('', true), 'auto');

  // Same polarity stays unchanged
  assert.equal(syncTerminalThemeForAppearance('tokyo-night', true), 'tokyo-night');
  assert.equal(syncTerminalThemeForAppearance('catppuccin-mocha', true), 'catppuccin-mocha');
  assert.equal(syncTerminalThemeForAppearance('warm-paper', false), 'warm-paper');

  // Cross-polarity switches to sibling pairId
  assert.equal(syncTerminalThemeForAppearance('shell-dark', false), 'shell-light');
  assert.equal(syncTerminalThemeForAppearance('shell-light', true), 'shell-dark');
  assert.equal(syncTerminalThemeForAppearance('tokyo-night', false), 'shell-light');
  assert.equal(syncTerminalThemeForAppearance('catppuccin-mocha', false), 'warm-paper');
  assert.equal(syncTerminalThemeForAppearance('warm-paper', true), 'catppuccin-mocha');

  // Legacy IDs normalize before pairing
  assert.equal(syncTerminalThemeForAppearance('rose-pine-dawn', false), 'warm-paper');
  assert.equal(syncTerminalThemeForAppearance('rose-pine-dawn', true), 'catppuccin-mocha');
  assert.equal(syncTerminalThemeForAppearance('dracula', true), 'tokyo-night');
  assert.equal(syncTerminalThemeForAppearance('dracula', false), 'shell-light');
});
