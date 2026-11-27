import type { Profile, SessionRecord } from '../types/shell';

// Host metadata may change between the request and the tab's handshake.
export function sameSessionTarget(a: Profile, b: Profile): boolean {
  return !!a.id && a.id === b.id && a.host.toLowerCase() === b.host.toLowerCase()
    && a.user === b.user && a.port === b.port && a.auth === b.auth
    && (a.keyPath || '') === (b.keyPath || '') && (a.jumpHostId || '') === (b.jumpHostId || '')
    && (a.proxyType || '') === (b.proxyType || '') && (a.proxyHost || '') === (b.proxyHost || '')
    && (a.proxyUser || '') === (b.proxyUser || '') && (a.proxyPort || 0) === (b.proxyPort || 0)
    && (a.agentAccess || '') === (b.agentAccess || '')
    && Boolean(a.forwardAgent) === Boolean(b.forwardAgent) && (a.startupCommand || '') === (b.startupCommand || '');
}

export function reconnectSecret(previous: Pick<SessionRecord, 'profile' | 'secret' | 'remember'>, target: Profile): string | null {
  if (target.auth === 'agent' || target.remembered) return '';
  if (previous.remember || !sameSessionTarget(previous.profile, target)) return null;
  return previous.secret ?? '';
}

// A Wails Disconnect can reach Go before a pending Connect has registered its session.
// In that case the successful late reply owns the responsibility for closing it.
export async function connectIfOpen(
  signal: AbortSignal,
  prepare: () => Promise<void>,
  connect: () => Promise<void>,
  disconnect: () => Promise<void>,
): Promise<boolean> {
  if (signal.aborted) return false;
  await prepare();
  if (signal.aborted) return false;
  await connect();
  if (!signal.aborted) return true;
  await disconnect();
  return false;
}
