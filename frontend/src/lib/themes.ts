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
    name: '默认深色',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#121316', '#E6EDF3', '#58A6FF', '#3FB950'],
    theme: {
      // High-clarity pure-neutral obsidian canvas + crisp GitHub Dark ANSI palette (zero haze, zero neon glare)
      background: '#121316',
      foreground: '#E6EDF3',
      cursor: '#E6EDF3',
      cursorAccent: '#121316',
      selectionBackground: 'rgba(88, 166, 255, 0.28)',
      black: '#1E2026',
      red: '#F85149',
      green: '#3FB950',
      yellow: '#E3B341',
      blue: '#58A6FF',
      magenta: '#BC8CFF',
      cyan: '#39C5CF',
      white: '#D0D7DE',
      brightBlack: '#6E7681',
      brightRed: '#FF7B72',
      brightGreen: '#56D364',
      brightYellow: '#F2CC60',
      brightBlue: '#79C0FF',
      brightMagenta: '#D2A8FF',
      brightCyan: '#56D4DD',
      brightWhite: '#FFFFFF',
    },
  },
  {
    id: 'one-dark',
    name: 'One Dark',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#191C22', '#D7DAE0', '#61AFEF', '#98C379'],
    theme: {
      background: '#191C22',
      foreground: '#D7DAE0',
      cursor: '#D7DAE0',
      cursorAccent: '#191C22',
      selectionBackground: 'rgba(97, 175, 239, 0.26)',
      black: '#232730',
      red: '#E06C75',
      green: '#98C379',
      yellow: '#E5C07B',
      blue: '#61AFEF',
      magenta: '#C678DD',
      cyan: '#56B6C2',
      white: '#C8CCD4',
      brightBlack: '#636D83',
      brightRed: '#EE7B84',
      brightGreen: '#A9D38B',
      brightYellow: '#F0CC88',
      brightBlue: '#75BCFA',
      brightMagenta: '#D58CEB',
      brightCyan: '#67C7D3',
      brightWhite: '#FFFFFF',
    },
  },
  {
    id: 'tokyo-night',
    name: 'Tokyo Night',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#16161E', '#D5DEFF', '#7AA2F7', '#9ECE6A'],
    theme: {
      background: '#16161E',
      foreground: '#D5DEFF',
      cursor: '#C0CAF5',
      cursorAccent: '#16161E',
      selectionBackground: 'rgba(122, 162, 247, 0.28)',
      black: '#1A1B26',
      red: '#F7768E',
      green: '#9ECE6A',
      yellow: '#E0AF68',
      blue: '#7AA2F7',
      magenta: '#BB9AF7',
      cyan: '#7DCFFF',
      white: '#C0CAF5',
      brightBlack: '#636DA6',
      brightRed: '#FF899D',
      brightGreen: '#B0DB82',
      brightYellow: '#EFC07B',
      brightBlue: '#8DB0FF',
      brightMagenta: '#CBB1FA',
      brightCyan: '#9EDBFA',
      brightWhite: '#FFFFFF',
    },
  },
  {
    id: 'catppuccin-mocha',
    name: 'Catppuccin',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#181825', '#CDD6F4', '#89B4FA', '#A6E3A1'],
    theme: {
      background: '#181825',
      foreground: '#CDD6F4',
      cursor: '#F5E0DC',
      cursorAccent: '#181825',
      selectionBackground: 'rgba(137, 180, 250, 0.26)',
      black: '#313244',
      red: '#F38BA8',
      green: '#A6E3A1',
      yellow: '#F9E2AF',
      blue: '#89B4FA',
      magenta: '#CBA6F7',
      cyan: '#94E2D5',
      white: '#BAC2DE',
      brightBlack: '#7F849C',
      brightRed: '#F5A0B8',
      brightGreen: '#B8EBB3',
      brightYellow: '#FCEAC2',
      brightBlue: '#A3C5FB',
      brightMagenta: '#D8BCFA',
      brightCyan: '#ABEAE0',
      brightWhite: '#FFFFFF',
    },
  },
  {
    id: 'shell-light',
    name: '默认浅色',
    isDark: false,
    pairId: 'shell-dark',
    preview: ['#F8FAFC', '#0F172A', '#2563EB', '#16A34A'],
    theme: {
      background: '#F8FAFC',
      foreground: '#0F172A',
      cursor: '#2563EB',
      cursorAccent: '#F8FAFC',
      selectionBackground: 'rgba(37, 99, 235, 0.16)',
      black: '#0F172A',
      red: '#DC2626',
      green: '#16A34A',
      yellow: '#B45309',
      blue: '#2563EB',
      magenta: '#9333EA',
      cyan: '#0891B2',
      white: '#475569',
      brightBlack: '#64748B',
      brightRed: '#EF4444',
      brightGreen: '#22C55E',
      brightYellow: '#D97706',
      brightBlue: '#3B82F6',
      brightMagenta: '#A855F7',
      brightCyan: '#06B6D4',
      brightWhite: '#0F172A',
    },
  },
];

const LEGACY_THEME_MAP: Record<string, string> = {
  'termius-dark': 'shell-dark',
  'apple-dark': 'shell-dark',
  'one-half-dark': 'one-dark',
  'one-dark-pro': 'one-dark',
  dracula: 'tokyo-night',
  nord: 'shell-dark',
  'rose-pine': 'catppuccin-mocha',
  'termius-light': 'shell-light',
  'apple-light': 'shell-light',
  'github-light': 'shell-light',
  'tokyo-day': 'shell-light',
  'catppuccin-latte': 'shell-light',
  'rose-pine-dawn': 'shell-light',
  'nord-light': 'shell-light',
  'solarized-light': 'shell-light',
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
