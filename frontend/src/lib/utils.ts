import type { SplitNode } from '../types/shell';

export function randomId(): string {
  const bytes = new Uint32Array(3);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (n) => n.toString(16).padStart(8, '0')).join('');
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '0 B';
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = bytes / 1024;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  return `${v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)} ${units[u]}`;
}

export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '00:00';
  const s = Math.floor(seconds);
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  if (d > 0) return `${d}d ${pad(h)}h`;
  if (h > 0) return `${pad(h)}:${pad(m)}:${pad(sec)}`;
  return `${pad(m)}:${pad(sec)}`;
}

export function formatModTime(unixSec: number): string {
  if (!unixSec) return '—';
  const d = new Date(unixSec * 1000);
  const now = new Date();
  const sameYear = d.getFullYear() === now.getFullYear();
  const pad = (n: number) => String(n).padStart(2, '0');
  if (sameYear) {
    return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export function formatRelativeTime(unixSec?: number): string {
  if (!unixSec) return '';
  const diff = Math.max(0, Math.floor(Date.now() / 1000) - unixSec);
  if (diff < 60) return '刚刚';
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)} 天前`;
  const d = new Date(unixSec * 1000);
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`;
}

/**
 * Parses quick-connect inputs such as:
 * - "root@10.0.0.5"
 * - "ubuntu@server.example.com:2222"
 * - "ssh -p 2222 deploy@192.168.1.10"
 * - "192.168.1.10"
 */
export function parseQuickConnect(raw: string): { user: string; host: string; port: number } | null {
  let s = raw.trim();
  if (!s) return null;
  if (s.toLowerCase().startsWith('ssh ')) {
    s = s.slice(4).trim();
  }
  let port = 22;
  const pMatch = s.match(/(?:^|\s)-p\s*(\d{1,5})(?:\s|$)/);
  if (pMatch) {
    port = Number(pMatch[1]);
    s = s.replace(pMatch[0], ' ').trim();
  }
  // Take last token (e.g. after flags)
  const tokens = s.split(/\s+/).filter((t) => !t.startsWith('-'));
  const target = tokens[tokens.length - 1];
  if (!target) return null;

  let user = 'root';
  let hostPart = target;
  if (target.includes('@')) {
    const idx = target.lastIndexOf('@');
    user = target.slice(0, idx).trim() || 'root';
    hostPart = target.slice(idx + 1).trim();
  }
  if (!hostPart) return null;

  // Handle [ipv6]:port or host:port
  if (hostPart.startsWith('[')) {
    const closeIdx = hostPart.indexOf(']');
    if (closeIdx > 1) {
      const rest = hostPart.slice(closeIdx + 1);
      if (rest.startsWith(':')) {
        const parsed = Number(rest.slice(1));
        if (Number.isInteger(parsed) && parsed >= 1 && parsed <= 65535) port = parsed;
      }
      hostPart = hostPart.slice(1, closeIdx);
    }
  } else {
    const colonCount = (hostPart.match(/:/g) || []).length;
    if (colonCount === 1) {
      const [h, p] = hostPart.split(':');
      const parsed = Number(p);
      if (h && Number.isInteger(parsed) && parsed >= 1 && parsed <= 65535) {
        hostPart = h;
        port = parsed;
      }
    }
  }
  if (!hostPart || hostPart.includes(' ')) return null;
  if (!Number.isInteger(port) || port < 1 || port > 65535) return null;
  return { user, host: hostPart, port };
}

/** Extracts {{variable}} placeholders from a snippet command (ignoring Go template {{.Field}} syntax). */
export function extractSnippetVars(command: string): string[] {
  const vars: string[] = [];
  const seen = new Set<string>();
  const re = /\{\{([a-zA-Z_][a-zA-Z0-9_-]*)\}\}/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(command)) !== null) {
    if (!seen.has(m[1])) {
      seen.add(m[1]);
      vars.push(m[1]);
    }
  }
  return vars;
}

export function applySnippetVars(command: string, values: Record<string, string>): string {
  return command.replace(/\{\{([a-zA-Z_][a-zA-Z0-9_-]*)\}\}/g, (_, k) => values[k] ?? '');
}

// --- Split Pane Tree Utilities ---

export function collectLeaves(node: SplitNode): { paneId: string; sessionId: string }[] {
  if (node.type === 'leaf') return [{ paneId: node.paneId, sessionId: node.sessionId }];
  return [...collectLeaves(node.first), ...collectLeaves(node.second)];
}

export function splitPaneNode(
  node: SplitNode,
  targetPaneId: string,
  direction: 'horizontal' | 'vertical',
  newPaneId: string,
  newSessionId: string,
): SplitNode {
  if (node.type === 'leaf') {
    if (node.paneId !== targetPaneId) return node;
    return {
      type: 'split',
      id: randomId(),
      direction,
      ratio: 0.5,
      first: node,
      second: { type: 'leaf', paneId: newPaneId, sessionId: newSessionId },
    };
  }
  return {
    ...node,
    first: splitPaneNode(node.first, targetPaneId, direction, newPaneId, newSessionId),
    second: splitPaneNode(node.second, targetPaneId, direction, newPaneId, newSessionId),
  };
}

export function removePaneNode(node: SplitNode, targetPaneId: string): SplitNode | null {
  if (node.type === 'leaf') {
    return node.paneId === targetPaneId ? null : node;
  }
  const first = removePaneNode(node.first, targetPaneId);
  const second = removePaneNode(node.second, targetPaneId);
  if (!first && !second) return null;
  if (!first) return second;
  if (!second) return first;
  return { ...node, first, second };
}

export function updateSplitRatio(node: SplitNode, splitId: string, ratio: number): SplitNode {
  if (node.type === 'leaf') return node;
  const clamped = Math.max(0.18, Math.min(0.82, ratio));
  if (node.id === splitId) {
    return { ...node, ratio: clamped };
  }
  return {
    ...node,
    first: updateSplitRatio(node.first, splitId, clamped),
    second: updateSplitRatio(node.second, splitId, clamped),
  };
}

export function replacePaneSession(node: SplitNode, targetPaneId: string, newSessionId: string): SplitNode {
  if (node.type === 'leaf') {
    return node.paneId === targetPaneId ? { ...node, sessionId: newSessionId } : node;
  }
  return {
    ...node,
    first: replacePaneSession(node.first, targetPaneId, newSessionId),
    second: replacePaneSession(node.second, targetPaneId, newSessionId),
  };
}
