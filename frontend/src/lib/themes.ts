import type { ITheme } from '@xterm/xterm';
import type { ProfileColor } from '../types/shell';

export interface TerminalThemePreset {
  id: string;
  name: string;
  isDark: boolean;
  /** Sibling preset ID used when switching between Light and Dark app appearance */
  pairId?: string;
  preview: [string, string, string, string]; // bg, fg, accent1, accent2
  theme: ITheme;
}

export const TERMINAL_THEMES: TerminalThemePreset[] = [
  {
    id: 'shell-dark',
    name: '曜石深灰',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#161B22', '#F0F6FC', '#58A6FF', '#56D364'],
    theme: {
      // GitHub Dark Default / Primer Dark — all non-black ANSI colors >= 5.5:1 vs background
      background: '#161B22',
      foreground: '#F0F6FC',
      cursor: '#58A6FF',
      cursorAccent: '#161B22',
      selectionBackground: 'rgba(88, 166, 255, 0.30)',
      black: '#0D1117',
      red: '#FF7B72',
      green: '#56D364',
      yellow: '#E3B341',
      blue: '#58A6FF',
      magenta: '#D2A8FF',
      cyan: '#39C5CF',
      white: '#E6EDF3',
      brightBlack: '#919BA5',
      brightRed: '#FFA198',
      brightGreen: '#7EE787',
      brightYellow: '#F2CC60',
      brightBlue: '#79C0FF',
      brightMagenta: '#E2C5FF',
      brightCyan: '#56D4DD',
      brightWhite: '#FFFFFF',
    },
  },
  {
    id: 'tokyo-night',
    name: '东京夜色',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#1D2030', '#E6ECFF', '#7AA2F7', '#9ECE6A'],
    theme: {
      // Official Tokyo Night Storm — all non-black ANSI colors >= 5.5:1 vs background
      background: '#1D2030',
      foreground: '#E6ECFF',
      cursor: '#7AA2F7',
      cursorAccent: '#1D2030',
      selectionBackground: 'rgba(122, 162, 247, 0.30)',
      black: '#11131F',
      red: '#F7768E',
      green: '#9ECE6A',
      yellow: '#E0AF68',
      blue: '#7AA2F7',
      magenta: '#BB9AF7',
      cyan: '#7DCFFF',
      white: '#D5DEFF',
      brightBlack: '#8C97C4',
      brightRed: '#FF8FA3',
      brightGreen: '#B5E384',
      brightYellow: '#F2C67E',
      brightBlue: '#93B6FF',
      brightMagenta: '#CEB3FA',
      brightCyan: '#9EE0FF',
      brightWhite: '#FFFFFF',
    },
  },
  {
    id: 'catppuccin-mocha',
    name: '摩卡暮紫',
    isDark: true,
    pairId: 'warm-paper',
    preview: ['#1E1E2E', '#E6ECFA', '#89B4FA', '#A6E3A1'],
    theme: {
      // Official Catppuccin Mocha — all non-black ANSI colors >= 5.5:1 vs background
      background: '#1E1E2E',
      foreground: '#E6ECFA',
      cursor: '#F5E0DC',
      cursorAccent: '#1E1E2E',
      selectionBackground: 'rgba(137, 180, 250, 0.30)',
      black: '#11111B',
      red: '#F38BA8',
      green: '#A6E3A1',
      yellow: '#F9E2AF',
      blue: '#89B4FA',
      magenta: '#CBA6F7',
      cyan: '#94E2D5',
      white: '#DCE3F8',
      brightBlack: '#9399B2',
      brightRed: '#F7A3BC',
      brightGreen: '#BBEFB6',
      brightYellow: '#FCECC6',
      brightBlue: '#A8C8FC',
      brightMagenta: '#DCBEFA',
      brightCyan: '#AEECE2',
      brightWhite: '#FFFFFF',
    },
  },
  {
    id: 'shell-light',
    name: '极简浅白',
    isDark: false,
    pairId: 'shell-dark',
    preview: ['#FFFFFF', '#1F2328', '#0550AE', '#116329'],
    theme: {
      // Official GitHub Light Default / Primer Light — all 16 ANSI colors >= 5.5:1, bright* >= 6.5:1
      background: '#FFFFFF',
      foreground: '#1F2328',
      cursor: '#0969DA',
      cursorAccent: '#FFFFFF',
      selectionBackground: 'rgba(9, 105, 218, 0.18)',
      black: '#1F2328',
      red: '#C21825',
      green: '#116329',
      yellow: '#8A4F00',
      blue: '#0550AE',
      magenta: '#6639BA',
      cyan: '#096678',
      white: '#424A53',
      brightBlack: '#57606A',
      brightRed: '#A40E26',
      brightGreen: '#0B5322',
      brightYellow: '#754000',
      brightBlue: '#0757BA',
      brightMagenta: '#5128A1',
      brightCyan: '#065263',
      brightWhite: '#1F2328',
    },
  },
  {
    id: 'warm-paper',
    name: '暖纸米白',
    isDark: false,
    pairId: 'catppuccin-mocha',
    preview: ['#FAF4ED', '#2B2638', '#286983', '#1D6B46'],
    theme: {
      // Official Rosé Pine Dawn Warm Editorial — all 16 ANSI colors >= 5.5:1, bright* >= 6.5:1
      background: '#FAF4ED',
      foreground: '#2B2638',
      cursor: '#286983',
      cursorAccent: '#FAF4ED',
      selectionBackground: 'rgba(40, 105, 131, 0.20)',
      black: '#2B2638',
      red: '#B4284B',
      green: '#1D6B46',
      yellow: '#8D4F06',
      blue: '#245E78',
      magenta: '#744496',
      cyan: '#176773',
      white: '#524B64',
      brightBlack: '#655D77',
      brightRed: '#961C3C',
      brightGreen: '#145536',
      brightYellow: '#743F02',
      brightBlue: '#1A4B63',
      brightMagenta: '#5E327E',
      brightCyan: '#0E525D',
      brightWhite: '#2B2638',
    },
  },
];

const LEGACY_THEME_MAP: Record<string, string> = {
  'one-dark': 'shell-dark',
  'one-half-dark': 'shell-dark',
  'one-dark-pro': 'shell-dark',
  'termius-dark': 'shell-dark',
  'apple-dark': 'shell-dark',
  nord: 'shell-dark',
  dracula: 'tokyo-night',
  'rose-pine': 'catppuccin-mocha',
  'termius-light': 'shell-light',
  'apple-light': 'shell-light',
  'github-light': 'shell-light',
  'tokyo-day': 'shell-light',
  'nord-light': 'shell-light',
  'solarized-light': 'warm-paper',
  'catppuccin-latte': 'warm-paper',
  'rose-pine-dawn': 'warm-paper',
};

/** Resolves a theme ID (including "auto" and legacy IDs) against the current Light/Dark app appearance. */
export function resolveTerminalTheme(id: string, isDark: boolean): TerminalThemePreset {
  const lightFallback = TERMINAL_THEMES.find((t) => t.id === 'shell-light') ?? TERMINAL_THEMES[0];
  const darkFallback = TERMINAL_THEMES.find((t) => t.id === 'shell-dark') ?? TERMINAL_THEMES[0];
  if (!id || id === 'auto') {
    return isDark ? darkFallback : lightFallback;
  }
  const normalized = LEGACY_THEME_MAP[id] ?? id;
  return TERMINAL_THEMES.find((t) => t.id === normalized) ?? (isDark ? darkFallback : lightFallback);
}

/** Returns a matching Light/Dark sibling when switching app appearance so terminal & UI always stay harmonious. */
export function syncTerminalThemeForAppearance(currentId: string, nextIsDark: boolean): string {
  if (!currentId || currentId === 'auto') return 'auto';
  const normalized = LEGACY_THEME_MAP[currentId] ?? currentId;
  const cur = TERMINAL_THEMES.find((t) => t.id === normalized);
  if (!cur || cur.isDark === nextIsDark) return normalized;
  return cur.pairId ?? (nextIsDark ? 'shell-dark' : 'shell-light');
}

export function getTerminalTheme(id: string, isDark = true): TerminalThemePreset {
  return resolveTerminalTheme(id, isDark);
}

export const PROFILE_COLORS: { id: ProfileColor; label: string; hex: string }[] = [
  { id: '', label: '默认', hex: '#8E8E93' },
  { id: 'blue', label: '蓝', hex: '#3B82F6' },
  { id: 'green', label: '绿', hex: '#10B981' },
  { id: 'orange', label: '橙', hex: '#F59E0B' },
  { id: 'red', label: '红', hex: '#EF4444' },
  { id: 'purple', label: '紫', hex: '#A855F7' },
  { id: 'teal', label: '青', hex: '#06B6D4' },
];

export function profileColorHex(color?: ProfileColor): string {
  return PROFILE_COLORS.find((c) => c.id === (color ?? ''))?.hex ?? '#8E8E93';
}
