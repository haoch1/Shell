import type { Extension } from '@codemirror/state';
import { StreamLanguage, foldService, type StreamParser } from '@codemirror/language';
import { shell } from '@codemirror/legacy-modes/mode/shell';
import { nginx } from '@codemirror/legacy-modes/mode/nginx';
import { dockerFile } from '@codemirror/legacy-modes/mode/dockerfile';
import { yaml } from '@codemirror/legacy-modes/mode/yaml';
import { toml } from '@codemirror/legacy-modes/mode/toml';
import { properties } from '@codemirror/legacy-modes/mode/properties';
import { javascript, json, typescript } from '@codemirror/legacy-modes/mode/javascript';
import { python } from '@codemirror/legacy-modes/mode/python';
import { go } from '@codemirror/legacy-modes/mode/go';
import { standardSQL } from '@codemirror/legacy-modes/mode/sql';
import { html, xml } from '@codemirror/legacy-modes/mode/xml';
import { css } from '@codemirror/legacy-modes/mode/css';
import { diff } from '@codemirror/legacy-modes/mode/diff';

export type EditorLanguageId =
  | 'plain'
  | 'shell'
  | 'nginx'
  | 'yaml'
  | 'json'
  | 'toml'
  | 'ini'
  | 'dockerfile'
  | 'javascript'
  | 'typescript'
  | 'python'
  | 'go'
  | 'sql'
  | 'html'
  | 'css'
  | 'xml'
  | 'markdown'
  | 'diff';

export interface EditorLanguageOption {
  id: EditorLanguageId;
  label: string;
}

export const EDITOR_LANGUAGES: readonly EditorLanguageOption[] = [
  { id: 'plain', label: 'Plain Text' },
  { id: 'shell', label: 'Shell / Bash' },
  { id: 'nginx', label: 'Nginx' },
  { id: 'yaml', label: 'YAML' },
  { id: 'json', label: 'JSON' },
  { id: 'toml', label: 'TOML' },
  { id: 'ini', label: 'INI / Conf' },
  { id: 'dockerfile', label: 'Dockerfile' },
  { id: 'javascript', label: 'JavaScript' },
  { id: 'typescript', label: 'TypeScript' },
  { id: 'python', label: 'Python' },
  { id: 'go', label: 'Go' },
  { id: 'sql', label: 'SQL' },
  { id: 'html', label: 'HTML' },
  { id: 'css', label: 'CSS' },
  { id: 'xml', label: 'XML' },
  { id: 'markdown', label: 'Markdown' },
  { id: 'diff', label: 'Diff' },
] as const;

const SHELL_BASENAMES = new Set([
  'makefile',
  'gnumakefile',
  '.bashrc',
  '.bash_profile',
  '.bash_logout',
  '.zshrc',
  '.zshenv',
  '.zprofile',
  '.profile',
  'bashrc',
  'zshrc',
  'profile',
  'crontab',
]);

const INI_BASENAMES = new Set([
  '.env',
  'hosts',
  'fstab',
  'sshd_config',
  'ssh_config',
  '.gitconfig',
  '.npmrc',
  '.editorconfig',
]);

const EXT_TO_LANG: Readonly<Record<string, EditorLanguageId>> = {
  sh: 'shell',
  bash: 'shell',
  zsh: 'shell',
  ksh: 'shell',
  fish: 'shell',
  mk: 'shell',
  envrc: 'shell',

  json: 'json',
  jsonc: 'json',
  json5: 'json',
  jsonl: 'json',

  yml: 'yaml',
  yaml: 'yaml',

  toml: 'toml',

  ini: 'ini',
  env: 'ini',
  properties: 'ini',
  service: 'ini',
  timer: 'ini',
  socket: 'ini',
  target: 'ini',
  mount: 'ini',
  path: 'ini',
  conf: 'ini',
  cfg: 'ini',
  cnf: 'ini',

  dockerfile: 'dockerfile',

  js: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  jsx: 'javascript',

  ts: 'typescript',
  mts: 'typescript',
  cts: 'typescript',
  tsx: 'typescript',

  py: 'python',
  pyw: 'python',
  pyi: 'python',

  go: 'go',

  sql: 'sql',

  html: 'html',
  htm: 'html',
  xhtml: 'html',

  css: 'css',
  scss: 'css',
  less: 'css',

  xml: 'xml',
  svg: 'xml',
  plist: 'xml',
  xsd: 'xml',
  xsl: 'xml',

  md: 'markdown',
  markdown: 'markdown',
  mdx: 'markdown',

  diff: 'diff',
  patch: 'diff',
};

/**
 * Lightweight Markdown stream parser (replaces heavy @codemirror/lang-markdown + @lezer/markdown).
 */
const markdownStreamParser: StreamParser<{ fence: string | null }> = {
  startState: () => ({ fence: null }),
  token(stream, state) {
    if (stream.sol()) {
      const fenceMatch = stream.match(/^\s*(```+|~~~+)/);
      if (fenceMatch && Array.isArray(fenceMatch)) {
        const marker = fenceMatch[1][0];
        state.fence = state.fence === marker ? null : state.fence ?? marker;
        stream.skipToEnd();
        return 'meta';
      }
      if (state.fence) {
        stream.skipToEnd();
        return 'string';
      }
      if (stream.match(/^\s*#{1,6}(?:\s|$).*/)) return 'heading';
      if (stream.match(/^\s*(?:[-*_]\s*){3,}$/)) return 'meta';
      if (stream.match(/^\s*>+/)) return 'comment';
      if (stream.match(/^\s*(?:[-*+]|\d+\.)\s+/)) return 'keyword';
    }
    if (state.fence) {
      stream.skipToEnd();
      return 'string';
    }
    if (stream.match(/`[^`]+`/)) return 'string';
    if (stream.match(/\*\*[^*]+\*\*|__[^_]+__/)) return 'strong';
    if (stream.match(/\*[^*\s][^*]*\*|_[^_\s][^_]*_/)) return 'emphasis';
    if (stream.match(/!?\[[^\]]+\](?=\()/)) return 'link';
    if (stream.match(/\([^)\s]+\)/)) return 'url';
    if (stream.match(/https?:\/\/[^\s)>\]]+/)) return 'url';
    if (!stream.eatWhile(/[^`*_![(h]+/)) stream.next();
    return null;
  },
};

const STREAM_PARSERS: Readonly<Partial<Record<EditorLanguageId, StreamParser<unknown>>>> = {
  shell,
  nginx,
  dockerfile: dockerFile,
  yaml,
  toml,
  ini: properties,
  json,
  javascript,
  typescript,
  python,
  go,
  sql: standardSQL,
  html,
  css,
  xml,
  markdown: markdownStreamParser,
  diff,
};

const extensionCache = new Map<EditorLanguageId, Extension>();

function lineIndent(text: string, tabSize: number): number {
  let cols = 0;
  for (let i = 0; i < text.length; i++) {
    const ch = text.charCodeAt(i);
    if (ch === 32) cols += 1;
    else if (ch === 9) cols += tabSize - (cols % tabSize);
    else break;
  }
  return cols;
}

/**
 * Indent-based fold service so StreamLanguage modes (YAML, Nginx, Python, JSON,
 * Shell, Go, JS/TS, CSS, etc.) provide clean gutter fold ranges without heavy LR trees.
 */
export const indentFoldService: Extension = foldService.of((state, lineStart, lineEnd) => {
  const { doc, tabSize } = state;
  const startLine = doc.lineAt(lineStart);
  if (!startLine.text.trim()) return null;

  const baseIndent = lineIndent(startLine.text, tabSize);
  let lastFoldLine = startLine;

  for (let n = startLine.number + 1; n <= doc.lines; n++) {
    const line = doc.line(n);
    if (!line.text.trim()) continue;
    const indent = lineIndent(line.text, tabSize);
    if (indent <= baseIndent) break;
    lastFoldLine = line;
  }

  return lastFoldLine.number > startLine.number ? { from: lineEnd, to: lastFoldLine.to } : null;
});

/**
 * Detects the best CodeMirror 6 language ID from a remote file path
 * (checking special Unix/DevOps basenames first, then file extensions).
 */
export function detectLanguageId(remotePath: string): EditorLanguageId {
  const normalized = (remotePath || '').trim().replace(/\\/g, '/').replace(/\/+$/, '');
  if (!normalized) return 'plain';

  const base = normalized.split('/').pop() || '';
  if (!base) return 'plain';

  const lowerBase = base.toLowerCase();
  const lowerPath = normalized.toLowerCase();

  // Dockerfile / Dockerfile.prod / app.dockerfile
  if (
    lowerBase === 'dockerfile' ||
    lowerBase.startsWith('dockerfile.') ||
    lowerBase.startsWith('dockerfile-') ||
    lowerBase.endsWith('.dockerfile')
  ) {
    return 'dockerfile';
  }

  // Nginx configs: nginx.conf anywhere, or *.conf / sites-available / sites-enabled under /nginx/
  if (
    lowerBase === 'nginx.conf' ||
    (/(^|\/)nginx(\/|$)/.test(lowerPath) &&
      (lowerBase.endsWith('.conf') ||
        lowerPath.includes('/sites-available/') ||
        lowerPath.includes('/sites-enabled/')))
  ) {
    return 'nginx';
  }

  // Docker Compose files
  if (/^(docker-)?compose([._-].+)?\.ya?ml$/.test(lowerBase)) {
    return 'yaml';
  }

  // Dotenv variants (.env, .env.local, .env.production, ...)
  if (lowerBase === '.env' || lowerBase.startsWith('.env.')) {
    return 'ini';
  }

  // Exact Unix/shell/config basenames
  if (SHELL_BASENAMES.has(lowerBase)) {
    return 'shell';
  }
  if (INI_BASENAMES.has(lowerBase)) {
    return 'ini';
  }

  // Extension lookup (require dot after first char so unknown dotfiles stay plain)
  const dotIdx = lowerBase.lastIndexOf('.');
  if (dotIdx > 0 && dotIdx < lowerBase.length - 1) {
    const ext = lowerBase.slice(dotIdx + 1);
    const mapped = EXT_TO_LANG[ext];
    if (mapped) return mapped;
  }

  return 'plain';
}

/**
 * Returns the CodeMirror 6 language extension for the given EditorLanguageId.
 */
export function loadLanguageExtension(langId: EditorLanguageId): Extension {
  if (langId === 'plain') return [];
  let cached = extensionCache.get(langId);
  if (!cached) {
    const parser = STREAM_PARSERS[langId];
    cached = parser ? StreamLanguage.define(parser) : [];
    extensionCache.set(langId, cached);
  }
  return cached;
}

export type LineEnding = 'LF' | 'CRLF';

export function detectLineEnding(text: string): LineEnding {
  return text.includes('\r\n') ? 'CRLF' : 'LF';
}

export function normalizeToLF(text: string): string {
  return text.replace(/\r\n/g, '\n');
}

export function applyLineEnding(text: string, eol: LineEnding): string {
  const lf = text.replace(/\r\n/g, '\n');
  return eol === 'CRLF' ? lf.replace(/\n/g, '\r\n') : lf;
}
