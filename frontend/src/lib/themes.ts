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
    id: 'termius-dark',
    name: 'Termius Dark',
    isDark: true,
    pairId: 'termius-light',
    preview: ['#1C2028', '#EDF2FA', '#60A5FA', '#34D399'],
    theme: {
      background: '#1C2028',
      foreground: '#EDF2FA',
      cursor: '#34D399',
      cursorAccent: '#1C2028',
      selectionBackground: 'rgba(96, 165, 250, 0.30)',
      black: '#252B37',
      red: '#FF758F',
      green: '#34D399',
      yellow: '#FCD34D',
      blue: '#60A5FA',
      magenta: '#C084FC',
      cyan: '#38BDF8',
      white: '#DCE4F2',
      brightBlack: '#808FA6',
      brightRed: '#FF94A8',
      brightGreen: '#4ADE80',
      brightYellow: '#FDE68A',
      brightBlue: '#93C5FD',
      brightMagenta: '#D8B4FE',
      brightCyan: '#67E8F9',
      brightWhite: '#F8FAFC',
    },
  },
  {
    id: 'tokyo-night',
    name: 'Tokyo Night',
    isDark: true,
    pairId: 'termius-light',
    preview: ['#1A1B26', '#C0CAF5', '#7AA2F7', '#9ECE6A'],
    theme: {
      background: '#1A1B26',
      foreground: '#C0CAF5',
      cursor: '#C0CAF5',
      cursorAccent: '#1A1B26',
      selectionBackground: 'rgba(122, 162, 247, 0.25)',
      black: '#15161E',
      red: '#F7768E',
      green: '#9ECE6A',
      yellow: '#E0AF68',
      blue: '#7AA2F7',
      magenta: '#BB9AF7',
      cyan: '#7DCFFF',
      white: '#A9B1D6',
      brightBlack: '#565F89',
      brightRed: '#F7768E',
      brightGreen: '#9ECE6A',
      brightYellow: '#E0AF68',
      brightBlue: '#7AA2F7',
      brightMagenta: '#BB9AF7',
      brightCyan: '#7DCFFF',
      brightWhite: '#C0CAF5',
    },
  },
  {
    id: 'catppuccin-mocha',
    name: 'Catppuccin',
    isDark: true,
    pairId: 'termius-light',
    preview: ['#1E1E2E', '#CDD6F4', '#89B4FA', '#A6E3A1'],
    theme: {
      background: '#1E1E2E',
      foreground: '#CDD6F4',
      cursor: '#F5E0DC',
      cursorAccent: '#1E1E2E',
      selectionBackground: 'rgba(137, 180, 250, 0.25)',
      black: '#45475A',
      red: '#F38BA8',
      green: '#A6E3A1',
      yellow: '#F9E2AF',
      blue: '#89B4FA',
      magenta: '#F5C2E7',
      cyan: '#94E2D5',
      white: '#BAC2DE',
      brightBlack: '#6C7086',
      brightRed: '#F38BA8',
      brightGreen: '#A6E3A1',
      brightYellow: '#F9E2AF',
      brightBlue: '#89B4FA',
      brightMagenta: '#F5C2E7',
      brightCyan: '#94E2D5',
      brightWhite: '#A6ADC8',
    },
  },
  {
    id: 'dracula',
    name: 'Dracula',
    isDark: true,
    pairId: 'termius-light',
    preview: ['#282A36', '#F8F8F2', '#BD93F9', '#50FA7B'],
    theme: {
      background: '#282A36',
      foreground: '#F8F8F2',
      cursor: '#F8F8F2',
      cursorAccent: '#282A36',
      selectionBackground: 'rgba(189, 147, 249, 0.28)',
      black: '#21222C',
      red: '#FF5555',
      green: '#50FA7B',
      yellow: '#F1FA8C',
      blue: '#BD93F9',
      magenta: '#FF79C6',
      cyan: '#8BE9FD',
      white: '#F8F8F2',
      brightBlack: '#6272A4',
      brightRed: '#FF6E6E',
      brightGreen: '#69FF94',
      brightYellow: '#FFFFA5',
      brightBlue: '#D6ACFF',
      brightMagenta: '#FF92DF',
      brightCyan: '#A4FFFF',
      brightWhite: '#FFFFFF',
    },
  },
  {
    id: 'termius-light',
    name: 'Termius Light',
    isDark: false,
    pairId: 'termius-dark',
    preview: ['#F8FAFC', '#1E293B', '#2563EB', '#0D9488'],
    theme: {
      background: '#F8FAFC',
      foreground: '#1E293B',
      cursor: '#2563EB',
      cursorAccent: '#F8FAFC',
      selectionBackground: 'rgba(37, 99, 235, 0.16)',
      black: '#1E293B',
      red: '#DC2626',
      green: '#0F9D6E',
      yellow: '#B45309',
      blue: '#2563EB',
      magenta: '#7C3AED',
      cyan: '#0284C7',
      white: '#64748B',
      brightBlack: '#475569',
      brightRed: '#EF4444',
      brightGreen: '#10B981',
      brightYellow: '#D97706',
      brightBlue: '#3B82F6',
      brightMagenta: '#8B5CF6',
      brightCyan: '#0EA5E9',
      brightWhite: '#0F172A',
    },
  },
];

const LEGACY_THEME_MAP: Record<string, string> = {
  'apple-dark': 'termius-dark',
  'one-half-dark': 'termius-dark',
  nord: 'termius-dark',
  'rose-pine': 'catppuccin-mocha',
  'apple-light': 'termius-light',
  'github-light': 'termius-light',
  'tokyo-day': 'termius-light',
  'catppuccin-latte': 'termius-light',
  'rose-pine-dawn': 'termius-light',
  'nord-light': 'termius-light',
  'solarized-light': 'termius-light',
};

/** Resolves a theme ID (including "auto" and legacy IDs) against the current Light/Dark app appearance. */
export function resolveTerminalTheme(id: string, isDark: boolean): TerminalThemePreset {
  const lightFallback = TERMINAL_THEMES.find((t) => t.id === 'termius-light') ?? TERMINAL_THEMES[0];
  const darkFallback = TERMINAL_THEMES.find((t) => t.id === 'termius-dark') ?? TERMINAL_THEMES[0];
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
  return cur.pairId ?? (nextIsDark ? 'termius-dark' : 'termius-light');
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
