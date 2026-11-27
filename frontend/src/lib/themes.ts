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
    name: '石墨深色',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#1C2028', '#D8DEE9', '#7AAEF8', '#7FD29A'],
    theme: {
      // Eye-comfort calibrated graphite palette: uniform OKLCH lightness & restrained chroma
      background: '#1C2028',
      foreground: '#D8DEE9',
      cursor: '#93B4F5',
      cursorAccent: '#1C2028',
      selectionBackground: 'rgba(122, 174, 248, 0.26)',
      black: '#252B37',
      red: '#E57E8A',
      green: '#7FD29A',
      yellow: '#E2C07D',
      blue: '#7AAEF8',
      magenta: '#C0A4E8',
      cyan: '#74C7D8',
      white: '#C4CDD9',
      brightBlack: '#6E7A8E',
      brightRed: '#EE9AA4',
      brightGreen: '#9AE0B0',
      brightYellow: '#ECD19A',
      brightBlue: '#9BC3FA',
      brightMagenta: '#D4BEF2',
      brightCyan: '#95D7E4',
      brightWhite: '#EDF2F7',
    },
  },
  {
    id: 'one-dark',
    name: 'One Dark',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#21252B', '#ABB2BF', '#61AFEF', '#98C379'],
    theme: {
      background: '#21252B',
      foreground: '#ABB2BF',
      cursor: '#93B4F5',
      cursorAccent: '#21252B',
      selectionBackground: 'rgba(97, 175, 239, 0.24)',
      black: '#282C34',
      red: '#E06C75',
      green: '#98C379',
      yellow: '#E5C07B',
      blue: '#61AFEF',
      magenta: '#C678DD',
      cyan: '#56B6C2',
      white: '#ABB2BF',
      brightBlack: '#636D83',
      brightRed: '#E88388',
      brightGreen: '#A8CC8C',
      brightYellow: '#DBAB79',
      brightBlue: '#71BEF2',
      brightMagenta: '#D291E4',
      brightCyan: '#66C2CD',
      brightWhite: '#D7DAE0',
    },
  },
  {
    id: 'tokyo-night',
    name: 'Tokyo Night',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#1A1B26', '#C0CAF5', '#7AA2F7', '#9ECE6A'],
    theme: {
      background: '#1A1B26',
      foreground: '#C0CAF5',
      cursor: '#A9B7E8',
      cursorAccent: '#1A1B26',
      selectionBackground: 'rgba(122, 162, 247, 0.25)',
      black: '#15161E',
      red: '#E87E94',
      green: '#9ECE6A',
      yellow: '#E0AF68',
      blue: '#7AA2F7',
      magenta: '#BB9AF7',
      cyan: '#7DCFFF',
      white: '#A9B1D6',
      brightBlack: '#636DA6',
      brightRed: '#F296A9',
      brightGreen: '#B0DB82',
      brightYellow: '#E8C082',
      brightBlue: '#94B6FA',
      brightMagenta: '#CBB1FA',
      brightCyan: '#9EDBFA',
      brightWhite: '#D5DEFF',
    },
  },
  {
    id: 'catppuccin-mocha',
    name: 'Catppuccin',
    isDark: true,
    pairId: 'shell-light',
    preview: ['#1E1E2E', '#CDD6F4', '#89B4FA', '#A6E3A1'],
    theme: {
      background: '#1E1E2E',
      foreground: '#CDD6F4',
      cursor: '#B4BEFE',
      cursorAccent: '#1E1E2E',
      selectionBackground: 'rgba(137, 180, 250, 0.24)',
      black: '#45475A',
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
      brightWhite: '#DCE0F8',
    },
  },
  {
    id: 'shell-light',
    name: '清雅浅色',
    isDark: false,
    pairId: 'shell-dark',
    preview: ['#F8FAFC', '#1E293B', '#2563EB', '#15805D'],
    theme: {
      background: '#F8FAFC',
      foreground: '#1E293B',
      cursor: '#2563EB',
      cursorAccent: '#F8FAFC',
      selectionBackground: 'rgba(37, 99, 235, 0.16)',
      black: '#1E293B',
      red: '#C83E4D',
      green: '#15805D',
      yellow: '#A16207',
      blue: '#2563EB',
      magenta: '#7E4CC9',
      cyan: '#0E7490',
      white: '#475569',
      brightBlack: '#64748B',
      brightRed: '#DC4F5E',
      brightGreen: '#1E966F',
      brightYellow: '#B47214',
      brightBlue: '#3B76F2',
      brightMagenta: '#9061D9',
      brightCyan: '#0891B2',
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
