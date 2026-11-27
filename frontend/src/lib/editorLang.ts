import type { Extension } from '@codemirror/state';
import { StreamLanguage } from '@codemirror/language';
import { json } from '@codemirror/lang-json';
import { yaml } from '@codemirror/lang-yaml';
import { javascript } from '@codemirror/lang-javascript';
import { python } from '@codemirror/lang-python';
import { html } from '@codemirror/lang-html';
import { css } from '@codemirror/lang-css';
import { sql } from '@codemirror/lang-sql';
import { xml } from '@codemirror/lang-xml';
import { markdown } from '@codemirror/lang-markdown';
import { go } from '@codemirror/lang-go';
import { shell } from '@codemirror/legacy-modes/mode/shell';
import { nginx } from '@codemirror/legacy-modes/mode/nginx';
import { dockerFile } from '@codemirror/legacy-modes/mode/dockerfile';
import { toml } from '@codemirror/legacy-modes/mode/toml';
import { properties } from '@codemirror/legacy-modes/mode/properties';
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
  switch (langId) {
    case 'shell':
      return StreamLanguage.define(shell);
    case 'nginx':
      return StreamLanguage.define(nginx);
    case 'dockerfile':
      return StreamLanguage.define(dockerFile);
    case 'toml':
      return StreamLanguage.define(toml);
    case 'ini':
      return StreamLanguage.define(properties);
    case 'diff':
      return StreamLanguage.define(diff);
    case 'yaml':
      return yaml();
    case 'json':
      return json();
    case 'javascript':
      return javascript({ jsx: true });
    case 'typescript':
      return javascript({ typescript: true, jsx: true });
    case 'python':
      return python();
    case 'go':
      return go();
    case 'sql':
      return sql();
    case 'html':
      return html();
    case 'css':
      return css();
    case 'xml':
      return xml();
    case 'markdown':
      return markdown();
    case 'plain':
    default:
      return [];
  }
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
