export type FileKind =
  | 'dir'
  | 'code'
  | 'config'
  | 'shell'
  | 'archive'
  | 'image'
  | 'log'
  | 'key'
  | 'doc'
  | 'file';

export type SftpSortKey = 'name' | 'size' | 'modTime' | 'type';
export type SftpSortDir = 'asc' | 'desc';

export interface ChmodTriad {
  r: boolean;
  w: boolean;
  x: boolean;
}

export interface ChmodBits {
  owner: ChmodTriad;
  group: ChmodTriad;
  other: ChmodTriad;
}

export interface SftpBookmark {
  label: string;
  path: string;
}

export interface SftpNavHistory {
  stack: string[];
  index: number;
}

export const SFTP_WIDTH_STORAGE_KEY = 'shell.sftp.width';
export const SFTP_WIDTH_MIN = 360;
export const SFTP_WIDTH_MAX = 780;
export const SFTP_WIDTH_DEFAULT = 440;

export const DEFAULT_SFTP_BOOKMARKS: readonly SftpBookmark[] = [
  { label: '主目录 (~)', path: '.' },
  { label: '根目录 (/)', path: '/' },
  { label: '/etc', path: '/etc' },
  { label: '/var/log', path: '/var/log' },
  { label: '/opt', path: '/opt' },
  { label: '/tmp', path: '/tmp' },
  { label: '/root', path: '/root' },
];

export const CHMOD_PRESETS: readonly { octal: string; label: string }[] = [
  { octal: '644', label: '标准文件' },
  { octal: '755', label: '目录 / 脚本' },
  { octal: '600', label: '私钥 / 机密' },
  { octal: '777', label: '完全开放' },
];

const KEY_BASENAMES = new Set([
  'id_rsa',
  'id_ed25519',
  'id_ecdsa',
  'id_dsa',
  'authorized_keys',
  'authorized_keys2',
  'known_hosts',
]);

const KEY_EXTS = new Set([
  'pem',
  'key',
  'pub',
  'crt',
  'cer',
  'der',
  'p12',
  'pfx',
  'jks',
  'ppk',
  'asc',
  'gpg',
  'kdbx',
  'secret',
]);

const SHELL_BASENAMES = new Set([
  '.bashrc',
  '.bash_profile',
  '.bash_logout',
  '.bash_aliases',
  '.zshrc',
  '.zprofile',
  '.zshenv',
  '.profile',
  '.kshrc',
]);

const SHELL_EXTS = new Set([
  'sh',
  'bash',
  'zsh',
  'fish',
  'ksh',
  'csh',
  'tcsh',
  'ps1',
  'bat',
  'cmd',
  'awk',
]);

const ARCHIVE_DOUBLE_EXTS = [
  '.tar.gz',
  '.tar.bz2',
  '.tar.xz',
  '.tar.zst',
  '.tar.lz',
  '.tar.lzma',
];

const ARCHIVE_EXTS = new Set([
  'zip',
  'tar',
  'gz',
  'tgz',
  'bz2',
  'tbz2',
  'xz',
  'txz',
  'zst',
  '7z',
  'rar',
  'deb',
  'rpm',
  'apk',
  'iso',
  'dmg',
  'jar',
  'war',
  'ear',
  'whl',
]);

const IMAGE_EXTS = new Set([
  'png',
  'jpg',
  'jpeg',
  'gif',
  'webp',
  'svg',
  'ico',
  'bmp',
  'avif',
  'tif',
  'tiff',
  'psd',
  'heic',
]);

const LOG_BASENAMES = new Set([
  'syslog',
  'dmesg',
  'messages',
  'kern.log',
  'auth.log',
  'daemon.log',
  'wtmp',
  'btmp',
]);

const CONFIG_BASENAMES = new Set([
  'dockerfile',
  'makefile',
  'gnumakefile',
  'cmakelists.txt',
  'vagrantfile',
  'procfile',
  'gemfile',
  'hosts',
  'fstab',
  'crontab',
  'resolv.conf',
  'sshd_config',
  'ssh_config',
  'nginx.conf',
  '.gitignore',
  '.gitattributes',
  '.gitmodules',
  '.dockerignore',
  '.editorconfig',
  '.npmrc',
  '.yarnrc',
  '.prettierrc',
  '.eslintrc',
  '.env',
]);

const CONFIG_EXTS = new Set([
  'conf',
  'cfg',
  'cnf',
  'ini',
  'toml',
  'yaml',
  'yml',
  'json',
  'jsonc',
  'json5',
  'xml',
  'plist',
  'properties',
  'service',
  'timer',
  'socket',
  'target',
  'mount',
  'env',
  'hcl',
  'tf',
  'tfvars',
  'lock',
]);

const CODE_EXTS = new Set([
  'ts',
  'tsx',
  'mts',
  'cts',
  'js',
  'jsx',
  'mjs',
  'cjs',
  'py',
  'pyi',
  'go',
  'rs',
  'c',
  'h',
  'cc',
  'cpp',
  'cxx',
  'hpp',
  'java',
  'kt',
  'kts',
  'cs',
  'php',
  'rb',
  'lua',
  'pl',
  'pm',
  'swift',
  'scala',
  'dart',
  'zig',
  'ex',
  'exs',
  'erl',
  'hs',
  'vue',
  'svelte',
  'astro',
  'html',
  'htm',
  'css',
  'scss',
  'sass',
  'less',
  'sql',
  'graphql',
  'gql',
  'proto',
  'r',
  'jl',
  'nim',
  'diff',
  'patch',
]);

const DOC_BASENAMES = new Set([
  'readme',
  'license',
  'licence',
  'changelog',
  'authors',
  'contributing',
  'notice',
  'todo',
]);

const DOC_EXTS = new Set([
  'md',
  'mdx',
  'markdown',
  'txt',
  'rst',
  'adoc',
  'pdf',
  'doc',
  'docx',
  'xls',
  'xlsx',
  'ppt',
  'pptx',
  'csv',
  'tsv',
  'rtf',
  'tex',
]);

const FILE_KIND_LABELS: Record<FileKind, string> = {
  dir: '目录',
  code: '代码',
  config: '配置',
  shell: '脚本',
  archive: '压缩包',
  image: '图片',
  log: '日志',
  key: '密钥',
  doc: '文档',
  file: '文件',
};

const FILE_KIND_TONE_CLASS: Record<FileKind, string> = {
  dir: 'sftp-kind-dir',
  code: 'sftp-kind-code',
  config: 'sftp-kind-config',
  shell: 'sftp-kind-shell',
  archive: 'sftp-kind-archive',
  image: 'sftp-kind-image',
  log: 'sftp-kind-log',
  key: 'sftp-kind-key',
  doc: 'sftp-kind-doc',
  file: 'sftp-kind-file',
};

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

export function classifyFileKind(name: string, isDir = false, _isSymlink = false): FileKind {
  if (isDir) return 'dir';

  const base = basenameRemotePath(name).trim().toLowerCase();
  if (!base) return 'file';

  if (KEY_BASENAMES.has(base) || /^id_(rsa|ed25519|ecdsa|dsa)(\.pub)?$/.test(base)) {
    return 'key';
  }
  if (SHELL_BASENAMES.has(base)) {
    return 'shell';
  }
  for (const suffix of ARCHIVE_DOUBLE_EXTS) {
    if (base.endsWith(suffix)) return 'archive';
  }
  if (LOG_BASENAMES.has(base) || /\.log(\.\d+)?$/.test(base) || base.endsWith('.out') || base.endsWith('.err')) {
    return 'log';
  }
  if (
    CONFIG_BASENAMES.has(base) ||
    base.startsWith('dockerfile.') ||
    base.startsWith('.env.') ||
    /^docker-compose(\..+)?\.ya?ml$/.test(base)
  ) {
    return 'config';
  }
  if (DOC_BASENAMES.has(base) || /^(readme|license|licence|changelog)\./.test(base)) {
    return 'doc';
  }

  const dotIdx = base.lastIndexOf('.');
  if (dotIdx > 0 && dotIdx < base.length - 1) {
    const ext = base.slice(dotIdx + 1);
    if (KEY_EXTS.has(ext)) return 'key';
    if (SHELL_EXTS.has(ext)) return 'shell';
    if (ARCHIVE_EXTS.has(ext)) return 'archive';
    if (IMAGE_EXTS.has(ext)) return 'image';
    if (CONFIG_EXTS.has(ext)) return 'config';
    if (CODE_EXTS.has(ext)) return 'code';
    if (DOC_EXTS.has(ext)) return 'doc';
  }

  return 'file';
}

export function getFileKindToneClass(kind: FileKind): string {
  return FILE_KIND_TONE_CLASS[kind];
}

export function getFileKindLabel(kind: FileKind): string {
  return FILE_KIND_LABELS[kind];
}

export function modeToBits(mode: number): ChmodBits {
  const perm = (Number.isFinite(mode) ? mode : 0) & 0o777;
  return {
    owner: {
      r: Boolean(perm & 0o400),
      w: Boolean(perm & 0o200),
      x: Boolean(perm & 0o100),
    },
    group: {
      r: Boolean(perm & 0o040),
      w: Boolean(perm & 0o020),
      x: Boolean(perm & 0o010),
    },
    other: {
      r: Boolean(perm & 0o004),
      w: Boolean(perm & 0o002),
      x: Boolean(perm & 0o001),
    },
  };
}

export function bitsToMode(bits: ChmodBits, special = 0): number {
  const spec = (Number.isFinite(special) ? special : 0) & 0o7000;
  let mode = spec;
  if (bits.owner.r) mode |= 0o400;
  if (bits.owner.w) mode |= 0o200;
  if (bits.owner.x) mode |= 0o100;
  if (bits.group.r) mode |= 0o040;
  if (bits.group.w) mode |= 0o020;
  if (bits.group.x) mode |= 0o010;
  if (bits.other.r) mode |= 0o004;
  if (bits.other.w) mode |= 0o002;
  if (bits.other.x) mode |= 0o001;
  return mode;
}

export function modeToOctal(mode: number): string {
  const masked = (Number.isFinite(mode) ? mode : 0) & 0o7777;
  if (masked > 0o777) {
    return masked.toString(8).padStart(4, '0');
  }
  return (masked & 0o777).toString(8).padStart(3, '0');
}

export function octalToMode(str: string): number | null {
  const trimmed = str.trim();
  if (!/^[0-7]{3,4}$/.test(trimmed)) return null;
  return parseInt(trimmed, 8);
}

export function modeToSymbolic(mode: number): string {
  const masked = (Number.isFinite(mode) ? mode : 0) & 0o7777;
  const bits = modeToBits(masked);
  const setuid = Boolean(masked & 0o4000);
  const setgid = Boolean(masked & 0o2000);
  const sticky = Boolean(masked & 0o1000);

  const ux = setuid ? (bits.owner.x ? 's' : 'S') : bits.owner.x ? 'x' : '-';
  const gx = setgid ? (bits.group.x ? 's' : 'S') : bits.group.x ? 'x' : '-';
  const ox = sticky ? (bits.other.x ? 't' : 'T') : bits.other.x ? 'x' : '-';

  return (
    (bits.owner.r ? 'r' : '-') +
    (bits.owner.w ? 'w' : '-') +
    ux +
    (bits.group.r ? 'r' : '-') +
    (bits.group.w ? 'w' : '-') +
    gx +
    (bits.other.r ? 'r' : '-') +
    (bits.other.w ? 'w' : '-') +
    ox
  );
}

export function normalizeRemotePath(input: string): string {
  const raw = (input || '').trim();
  if (!raw) return '/';
  if (raw === '.') return '.';
  const collapsed = raw.replace(/\/+/g, '/');
  if (collapsed.length > 1 && collapsed.endsWith('/')) {
    return collapsed.replace(/\/+$/, '') || '/';
  }
  return collapsed;
}

export function joinRemotePath(dir: string, name: string): string {
  const cleanName = (name || '').trim().replace(/^\/+/, '').replace(/\/+$/, '');
  const rawName = (name || '').trim();
  if (rawName.startsWith('/')) {
    return normalizeRemotePath(rawName);
  }
  const cleanDir = normalizeRemotePath(dir || '.');
  if (!cleanName) return cleanDir;
  if (cleanDir === '/') return `/${cleanName}`;
  return `${cleanDir}/${cleanName}`;
}

export function parentRemotePath(p: string): string {
  const clean = normalizeRemotePath(p);
  if (clean === '/' || clean === '' || clean === '.') return '/';
  const idx = clean.lastIndexOf('/');
  if (idx <= 0) return '/';
  return clean.slice(0, idx);
}

export function basenameRemotePath(p: string): string {
  const clean = normalizeRemotePath(p);
  if (clean === '/' || clean === '.') return clean;
  const idx = clean.lastIndexOf('/');
  return idx >= 0 ? clean.slice(idx + 1) : clean;
}

export function resolveDestinationPath(entryPath: string, userInput: string): string {
  const trimmed = userInput.trim();
  if (!trimmed) return entryPath;
  if (trimmed.startsWith('/')) {
    return normalizeRemotePath(trimmed);
  }
  const parent = parentRemotePath(entryPath);
  return joinRemotePath(parent, trimmed);
}

export function suggestCopyName(
  name: string,
  isDir = false,
  existingNames?: Iterable<string>,
): string {
  const clean = basenameRemotePath(name).trim();
  if (!clean || clean === '/' || clean === '.') return 'copy';

  const existing = existingNames ? new Set(existingNames) : null;
  const buildCandidate = (n: number): string => {
    if (isDir) {
      return n === 1 ? `${clean}-copy` : `${clean}-copy-${n}`;
    }

    const lower = clean.toLowerCase();
    for (const suffix of ARCHIVE_DOUBLE_EXTS) {
      if (lower.endsWith(suffix) && clean.length > suffix.length) {
        const stem = clean.slice(0, clean.length - suffix.length);
        const origSuffix = clean.slice(clean.length - suffix.length);
        return n === 1 ? `${stem}.copy${origSuffix}` : `${stem}.copy${n}${origSuffix}`;
      }
    }

    const dotIdx = clean.lastIndexOf('.');
    if (dotIdx > 0 && dotIdx < clean.length - 1) {
      const stem = clean.slice(0, dotIdx);
      const ext = clean.slice(dotIdx);
      return n === 1 ? `${stem}.copy${ext}` : `${stem}.copy${n}${ext}`;
    }

    return n === 1 ? `${clean}-copy` : `${clean}-copy-${n}`;
  };

  let attempt = 1;
  let candidate = buildCandidate(attempt);
  while (existing && existing.has(candidate) && attempt < 1000) {
    attempt++;
    candidate = buildCandidate(attempt);
  }
  return candidate;
}

export function clampDrawerWidth(width: number): number {
  if (!Number.isFinite(width)) return SFTP_WIDTH_DEFAULT;
  return Math.max(SFTP_WIDTH_MIN, Math.min(SFTP_WIDTH_MAX, Math.round(width)));
}

export function pushNavHistory(history: SftpNavHistory, path: string): SftpNavHistory {
  const normalized = normalizeRemotePath(path);
  const current = history.stack[history.index];
  if (current === normalized) return history;
  const nextStack = history.stack.slice(0, history.index + 1);
  nextStack.push(normalized);
  if (nextStack.length > 64) {
    nextStack.shift();
  }
  return {
    stack: nextStack,
    index: nextStack.length - 1,
  };
}

export function stepNavHistory(
  history: SftpNavHistory,
  delta: -1 | 1,
): { history: SftpNavHistory; path: string | null } {
  const nextIndex = history.index + delta;
  if (nextIndex < 0 || nextIndex >= history.stack.length) {
    return { history, path: null };
  }
  return {
    history: { stack: history.stack, index: nextIndex },
    path: history.stack[nextIndex],
  };
}

export function sortRemoteEntries<
  T extends {
    name: string;
    isDir: boolean;
    isSymlink: boolean;
    size: number;
    modTime: number;
  },
>(entries: readonly T[], sortBy: SftpSortKey, sortDir: SftpSortDir): T[] {
  const dirMul = sortDir === 'asc' ? 1 : -1;
  return [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;

    let cmp = 0;
    switch (sortBy) {
      case 'size':
        cmp = a.size - b.size;
        break;
      case 'modTime':
        cmp = a.modTime - b.modTime;
        break;
      case 'type': {
        const kindA = classifyFileKind(a.name, a.isDir, a.isSymlink);
        const kindB = classifyFileKind(b.name, b.isDir, b.isSymlink);
        cmp = collator.compare(kindA, kindB);
        if (cmp === 0) {
          const extA = fileExtension(a.name);
          const extB = fileExtension(b.name);
          cmp = collator.compare(extA, extB);
        }
        break;
      }
      case 'name':
      default:
        cmp = collator.compare(a.name, b.name);
        break;
    }

    if (cmp !== 0) return cmp * dirMul;
    return collator.compare(a.name, b.name) * dirMul;
  });
}

function fileExtension(name: string): string {
  const idx = name.lastIndexOf('.');
  return idx > 0 ? name.slice(idx + 1).toLowerCase() : '';
}

export function formatFullTimestamp(unixSec?: number): string {
  if (!unixSec) return '—';
  const d = new Date(unixSec * 1000);
  if (Number.isNaN(d.getTime())) return '—';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export function getBookmarkStorageKey(hostKey: string): string {
  return `shell.sftp.bookmarks.${hostKey || 'default'}`;
}

export function parseStoredBookmarks(raw: string | null): string[] {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    const seen = new Set<string>();
    const out: string[] = [];
    for (const item of parsed) {
      if (typeof item !== 'string') continue;
      const clean = normalizeRemotePath(item);
      if (!clean || seen.has(clean)) continue;
      seen.add(clean);
      out.push(clean);
    }
    return out;
  } catch {
    return [];
  }
}
