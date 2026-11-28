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
    preview: ['#1B1C20', '#F3F6FA', '#60A5FA', '#4ADE80'],
    theme: {
      // Pure-neutral elevated graphite canvas + daylight-legible high-contrast ANSI palette
      background: '#1B1C20',
      foreground: '#F3F6FA',
      cursor: '#F3F6FA',
      cursorAccent: '#1B1C20',
      selectionBackground: 'rgba(96, 165, 250, 0.30)',
      black: '#2A2C32',
      red: '#F87171',
      green: '#4ADE80',
      yellow: '#FACC15',
      blue: '#60A5FA',
      magenta: '#C084FC',
      cyan: '#38BDF8',
      white: '#E2E8F0',
      brightBlack: '#8B949E',
      brightRed: '#FCA5A5',
      brightGreen: '#6EE7B7',
      brightYellow: '#FDE047',
      brightBlue: '#93C5FD',
      brightMagenta: '#D8B4FE',
      brightCyan: '#7DD3FC',
      brightWhite: '#FFFFFF',
    },
  },
  {
    id: 'tokyo-night',
    name: '东京夜色',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#1E202E', '#E8EEFF', '#7AA2F7', '#9ECE6A'],
    theme: {
      // Daylight-elevated midnight canvas + vivid Tokyo Night ANSI palette
      background: '#1E202E',
      foreground: '#E8EEFF',
      cursor: '#E8EEFF',
      cursorAccent: '#1E202E',
      selectionBackground: 'rgba(122, 162, 247, 0.30)',
      black: '#2B2F45',
      red: '#F7768E',
      green: '#9ECE6A',
      yellow: '#E0AF68',
      blue: '#7AA2F7',
      magenta: '#BB9AF7',
      cyan: '#7DCFFF',
      white: '#D5DEFF',
      brightBlack: '#7F8AB5',
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
    preview: ['#212131', '#E6EBFA', '#89B4FA', '#A6E3A1'],
    theme: {
      // Daylight-elevated charcoal canvas + vivid Catppuccin ANSI palette
      background: '#212131',
      foreground: '#E6EBFA',
      cursor: '#F5E0DC',
      cursorAccent: '#212131',
      selectionBackground: 'rgba(137, 180, 250, 0.30)',
      black: '#36364F',
      red: '#F38BA8',
      green: '#A6E3A1',
      yellow: '#F9E2AF',
      blue: '#89B4FA',
      magenta: '#CBA6F7',
      cyan: '#94E2D5',
      white: '#CDD6F4',
      brightBlack: '#8F96B2',
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
    preview: ['#FFFFFF', '#0B0F19', '#1D4ED8', '#15803D'],
    theme: {
      // Daylight high-legibility alabaster canvas + deep WCAG-AA+ ANSI palette
      background: '#FFFFFF',
      foreground: '#0B0F19',
      cursor: '#0071E3',
      cursorAccent: '#FFFFFF',
      selectionBackground: 'rgba(0, 113, 227, 0.20)',
      black: '#0B0F19',
      red: '#B91C1C',
      green: '#15803D',
      yellow: '#A16207',
      blue: '#1D4ED8',
      magenta: '#7E22CE',
      cyan: '#0E7490',
      white: '#374151',
      brightBlack: '#4B5563',
      brightRed: '#DC2626',
      brightGreen: '#047857',
      brightYellow: '#B45309',
      brightBlue: '#2563EB',
      brightMagenta: '#9333EA',
      brightCyan: '#027A94',
      brightWhite: '#0B0F19',
    },
  },
  {
    id: 'warm-paper',
    name: '暖纸米白',
    isDark: false,
    pairId: 'catppuccin-mocha',
    preview: ['#FAF7F0', '#1A1714', '#1D4ED8', '#15803D'],
    theme: {
      // Daylight high-contrast editorial paper canvas + deep ANSI palette
      background: '#FAF7F0',
      foreground: '#1A1714',
      cursor: '#2563EB',
      cursorAccent: '#FAF7F0',
      selectionBackground: 'rgba(37, 99, 235, 0.20)',
      black: '#1A1714',
      red: '#B91C1C',
      green: '#15803D',
      yellow: '#A16207',
      blue: '#1D4ED8',
      magenta: '#7E22CE',
      cyan: '#0E7490',
      white: '#433E38',
      brightBlack: '#575047',
      brightRed: '#DC2626',
      brightGreen: '#047857',
      brightYellow: '#B45309',
      brightBlue: '#2563EB',
      brightMagenta: '#9333EA',
      brightCyan: '#027A94',
      brightWhite: '#1A1714',
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
