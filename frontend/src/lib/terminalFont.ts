// Terminal font stack helpers ensuring weight-matched CJK sans-serif fallbacks on Windows.

export const CJK_SANS_FALLBACK =
  '"Sarasa Mono SC", "Maple Mono SC NF", "Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", "Noto Sans SC", "Noto Sans CJK SC"';

export const DEFAULT_TERMINAL_FONT =
  '"Cascadia Mono", "JetBrains Mono", "SF Mono", Consolas, "Sarasa Mono SC", "Maple Mono SC NF", "Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", "Noto Sans SC", monospace';

export interface FontOption {
  label: string;
  value: string;
}

export const TERMINAL_FONT_OPTIONS: FontOption[] = [
  {
    label: '系统推荐等宽',
    value: DEFAULT_TERMINAL_FONT,
  },
  {
    label: 'Cascadia Mono',
    value: '"Cascadia Mono", Consolas, "Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", monospace',
  },
  {
    label: 'JetBrains Mono',
    value: '"JetBrains Mono", "Cascadia Mono", Consolas, "Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", monospace',
  },
  {
    label: 'Fira Code',
    value: '"Fira Code", "Cascadia Mono", Consolas, "Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", monospace',
  },
  {
    label: 'Sarasa Mono SC',
    value: '"Sarasa Mono SC", "Cascadia Mono", Consolas, "Microsoft YaHei UI", "Microsoft YaHei", monospace',
  },
  {
    label: 'Consolas',
    value: 'Consolas, "Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", monospace',
  },
];

const LEGACY_FONT_MAP: Record<string, string> = {
  '"SF Mono", "JetBrains Mono", "Cascadia Mono", Consolas, monospace': TERMINAL_FONT_OPTIONS[0].value,
  '"Cascadia Mono", Consolas, monospace': TERMINAL_FONT_OPTIONS[1].value,
  '"JetBrains Mono", "Cascadia Mono", Consolas, monospace': TERMINAL_FONT_OPTIONS[2].value,
  '"Fira Code", "Cascadia Mono", Consolas, monospace': TERMINAL_FONT_OPTIONS[3].value,
  'Consolas, monospace': TERMINAL_FONT_OPTIONS[5].value,
};

const HAS_CJK_FONT_RE =
  /(yahei|pingfang|sarasa|maple\s*mono.*sc|noto\s*sans\s*(cjk|sc)|source\s*han|hiragino|harmonyos|misans|heiti|wenquanyi|微软雅黑|思源黑体)/i;

/**
 * Ensures any terminal font stack includes a modern sans-serif CJK fallback before generic `monospace`,
 * preventing Windows WebView2/DirectWrite from falling back to thin serif SimSun/NSimSun.
 */
export function resolveTerminalFontFamily(raw?: string): string {
  const trimmed = (raw ?? '').trim();
  if (!trimmed) return DEFAULT_TERMINAL_FONT;
  if (HAS_CJK_FONT_RE.test(trimmed)) return trimmed;

  const parts = trimmed
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
  const monoIdx = parts.findIndex((p) => p.toLowerCase() === 'monospace');
  if (monoIdx >= 0) {
    parts.splice(monoIdx, 0, CJK_SANS_FALLBACK);
    return parts.join(', ');
  }
  return `${parts.join(', ')}, ${CJK_SANS_FALLBACK}, monospace`;
}

/** Matches a saved fontFamily (including legacy stacks without CJK fallback) to a curated preset option. */
export function matchFontOptionValue(raw?: string): string {
  const trimmed = (raw ?? '').trim();
  if (!trimmed) return TERMINAL_FONT_OPTIONS[0].value;
  if (TERMINAL_FONT_OPTIONS.some((o) => o.value === trimmed)) return trimmed;
  if (LEGACY_FONT_MAP[trimmed]) return LEGACY_FONT_MAP[trimmed];
  return '__custom__';
}
