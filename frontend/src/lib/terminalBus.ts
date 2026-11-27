import type { SearchAddon } from '@xterm/addon-search';
import type { Terminal } from '@xterm/xterm';

export interface TerminalController {
  term: Terminal;
  search: SearchAddon;
  fit: () => void;
  focus: () => void;
  writeData: (bytes: Uint8Array) => void;
  writeln: (text: string) => void;
  getSelection: () => string;
  getTranscript: () => string;
  clear: () => void;
}

class TerminalBus {
  private controllers = new Map<string, TerminalController>();
  private pendingOutput = new Map<string, Uint8Array[]>();

  register(sessionId: string, ctrl: TerminalController): void {
    this.controllers.set(sessionId, ctrl);
    const queued = this.pendingOutput.get(sessionId);
    if (queued?.length) {
      this.pendingOutput.delete(sessionId);
      for (const chunk of queued) {
        ctrl.writeData(chunk);
      }
    }
  }

  unregister(sessionId: string): void {
    this.controllers.delete(sessionId);
    this.pendingOutput.delete(sessionId);
  }

  get(sessionId: string): TerminalController | undefined {
    return this.controllers.get(sessionId);
  }

  writeBytes(sessionId: string, bytes: Uint8Array): void {
    const ctrl = this.controllers.get(sessionId);
    if (ctrl) {
      ctrl.writeData(bytes);
      return;
    }
    let q = this.pendingOutput.get(sessionId);
    if (!q) {
      q = [];
      this.pendingOutput.set(sessionId, q);
    }
    if (q.length < 256) {
      q.push(bytes);
    }
  }

  writeln(sessionId: string, line: string): void {
    const ctrl = this.controllers.get(sessionId);
    if (ctrl) {
      ctrl.writeln(line);
    }
  }

  fitAll(): void {
    for (const ctrl of this.controllers.values()) {
      ctrl.fit();
    }
  }
}

export const terminalBus = new TerminalBus();

export function encodeBase64(data: string | Uint8Array): string {
  const bytes = typeof data === 'string' ? new TextEncoder().encode(data) : data;
  let raw = '';
  for (let i = 0; i < bytes.length; i += 8192) {
    raw += String.fromCharCode(...bytes.subarray(i, i + 8192));
  }
  return btoa(raw);
}

export function decodeBase64(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    out[i] = bin.charCodeAt(i);
  }
  return out;
}
