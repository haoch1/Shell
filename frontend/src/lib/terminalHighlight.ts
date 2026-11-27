/**
 * Stateful VT100/ANSI-aware terminal syntax highlighter.
 *
 * Highlights plain, uncolored terminal output (shell prompts, IPv4 addresses, URLs,
 * JSON keys, log severity/status keywords, ISO timestamps, file paths, and metrics)
 * using standard 16 ANSI SGR codes so colors automatically harmonize with every
 * light and dark terminal theme.
 *
 * Safety invariants:
 * - Passes alternate-screen apps (`vim`, `htop`, `nano`, `less`, `tmux`) through untouched.
 * - Never overrides text already styled by active SGR sequences (`ls --color`, `git diff`, `bat`).
 * - Cleanly buffers incomplete trailing escape sequences across chunk boundaries.
 * - Fast-paths tiny interactive keystroke echo chunks for zero-latency typing.
 */

const ANSI_RESET = '\x1b[0m';
const ANSI_BOLD = '\x1b[1m';
const ANSI_RED = '\x1b[31m';
const ANSI_GREEN = '\x1b[32m';
const ANSI_YELLOW = '\x1b[33m';
const ANSI_BLUE = '\x1b[34m';
const ANSI_MAGENTA = '\x1b[35m';
const ANSI_CYAN = '\x1b[36m';
const ANSI_MUTED = '\x1b[90m';

const MAX_PENDING_CSI = 256;
const MAX_PENDING_STR = 4096;

// Classic POSIX/Debian/Ubuntu shell prompt at line start: `user@host:path$ `
const PROMPT_STD_RE = /^([a-zA-Z0-9._-]+@[a-zA-Z0-9._-]+)(:)([^\s#$>]+)([#$>])(\s|$)/;
// Bracketed RHEL/CentOS/Rocky/Fedora/Arch prompt at line start: `[user@host path]# `
const PROMPT_BRACKET_RE = /^(\[)([a-zA-Z0-9._-]+@[a-zA-Z0-9._-]+)(\s+)([^\]]+)(\])([#$>])(\s|$)/;

// Single combined non-overlapping regex pass so matches never nest or double-wrap.
const TOKEN_RE = new RegExp(
  [
    // 1. URLs (cyan)
    '(?<url>https?:\\/\\/[^\\s"\'<>]+)',
    // 2. JSON object keys (blue)
    '(?<jsonKey>"[^"\\r\\n]{1,48}"(?=\\s*:))',
    // 3. ISO 8601 dates & timestamps (muted gray)
    '(?<isoDate>\\b\\d{4}-\\d{2}-\\d{2}(?:[T ]\\d{2}:\\d{2}:\\d{2}(?:\\.\\d+)?(?:Z|[+-]\\d{2}:?\\d{2})?)?\\b)',
    // 4. IPv4 addresses with optional :port or /cidr (magenta)
    '(?<ipv4>(?<![a-zA-Z0-9_])(?<!\\d\\.)(?:(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)\\.){3}(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)(?::\\d{1,5}|\\/(?:3[0-2]|[12]?\\d))?(?!\\d)(?!\\.\\d))',
    // 5. Absolute file paths with >= 2 segments (cyan)
    '(?<path>(?<![a-zA-Z0-9:/._-])\\/(?:[a-zA-Z0-9._-]+\\/)+[a-zA-Z0-9._-]*)',
    // 6. Error / failure keywords (red)
    '(?<err>\\b(?:ERROR|FATAL|CRITICAL|PANIC|FAILED|FAIL|Exception|denied|refused)\\b)',
    // 7. Warning keywords (yellow)
    '(?<warn>\\b(?:WARN|WARNING|DEPRECATED|TIMEOUT)\\b)',
    // 8. Healthy / success status keywords (green)
    '(?<ok>\\b(?:OK|SUCCESS|SUCCEEDED|ACTIVE|RUNNING|CONNECTED|LISTEN|ENABLED)\\b|\\bUp \\d+(?:\\s+(?:seconds?|minutes?|hours?|days?|weeks?|months?|secs?|mins?|hrs?))?\\b)',
    // 9. Numeric metrics & units (yellow)
    '(?<metric>(?<![\\w.])\\d+(?:\\.\\d+)?(?:%(?![a-zA-Z0-9_])|(?:KiB|MiB|GiB|TiB|kB\\/s|KB\\/s|MB\\/s|GB\\/s|B\\/s|kB|KB|MB|GB|TB|ms|us|ns|s|G|M|K|B)\\b))',
  ].join('|'),
  'g',
);

const ALT_SCREEN_MODES = new Set(['47', '1047', '1049']);

// Bit flags for non-color SGR styles so partial resets (22/23/24/...) clear accurately.
const STYLE_BOLD_DIM = 1 << 0;
const STYLE_ITALIC = 1 << 1;
const STYLE_UNDERLINE = 1 << 2;
const STYLE_BLINK = 1 << 3;
const STYLE_INVERSE = 1 << 4;
const STYLE_HIDDEN = 1 << 5;
const STYLE_STRIKE = 1 << 6;

function highlightTokens(text: string): string {
  if (!text) return text;
  TOKEN_RE.lastIndex = 0;
  return text.replace(TOKEN_RE, (match, ...args) => {
    const groups = args[args.length - 1] as Record<string, string | undefined>;
    if (groups.url !== undefined) return `${ANSI_CYAN}${match}${ANSI_RESET}`;
    if (groups.jsonKey !== undefined) return `${ANSI_BLUE}${match}${ANSI_RESET}`;
    if (groups.isoDate !== undefined) return `${ANSI_MUTED}${match}${ANSI_RESET}`;
    if (groups.ipv4 !== undefined) return `${ANSI_MAGENTA}${match}${ANSI_RESET}`;
    if (groups.path !== undefined) return `${ANSI_CYAN}${match}${ANSI_RESET}`;
    if (groups.err !== undefined) return `${ANSI_RED}${match}${ANSI_RESET}`;
    if (groups.warn !== undefined) return `${ANSI_YELLOW}${match}${ANSI_RESET}`;
    if (groups.ok !== undefined) return `${ANSI_GREEN}${match}${ANSI_RESET}`;
    if (groups.metric !== undefined) return `${ANSI_YELLOW}${match}${ANSI_RESET}`;
    return match;
  });
}

function highlightLineSegment(segment: string, atLineStart: boolean): string {
  if (!segment) return segment;
  if (atLineStart) {
    const stdMatch = PROMPT_STD_RE.exec(segment);
    if (stdMatch) {
      const [full, userHost, colon, path, sigil, trail] = stdMatch;
      const styledPrompt =
        `${ANSI_GREEN}${userHost}${ANSI_RESET}${colon}` +
        `${ANSI_CYAN}${path}${ANSI_RESET}${ANSI_BOLD}${sigil}${ANSI_RESET}${trail}`;
      const rest = segment.slice(full.length);
      return rest ? styledPrompt + highlightTokens(rest) : styledPrompt;
    }
    const brkMatch = PROMPT_BRACKET_RE.exec(segment);
    if (brkMatch) {
      const [full, open, userHost, space, path, close, sigil, trail] = brkMatch;
      const styledPrompt =
        `${open}${ANSI_GREEN}${userHost}${ANSI_RESET}${space}` +
        `${ANSI_CYAN}${path}${ANSI_RESET}${close}${ANSI_BOLD}${sigil}${ANSI_RESET}${trail}`;
      const rest = segment.slice(full.length);
      return rest ? styledPrompt + highlightTokens(rest) : styledPrompt;
    }
  }
  return highlightTokens(segment);
}

function hasVisibleChar(text: string, start: number, end: number): boolean {
  for (let i = start; i < end; i++) {
    if (text.charCodeAt(i) !== 0x07) return true;
  }
  return false;
}

export class TerminalHighlighter {
  private altScreen = false;
  private fgActive = false;
  private bgActive = false;
  private styleMask = 0;
  private atLineStart = true;
  private pendingEsc = '';
  private inRawString: 'osc' | 'st' | null = null;

  private get sgrActive(): boolean {
    return this.fgActive || this.bgActive || this.styleMask !== 0;
  }

  reset(): void {
    this.altScreen = false;
    this.fgActive = false;
    this.bgActive = false;
    this.styleMask = 0;
    this.atLineStart = true;
    this.pendingEsc = '';
    this.inRawString = null;
  }

  /** Flushes any buffered incomplete escape sequence without altering it. */
  flush(): string {
    const tail = this.pendingEsc;
    this.pendingEsc = '';
    return tail;
  }

  process(chunk: string): string {
    if (!chunk) return '';

    // Fast-path tiny interactive keystroke echo for zero-latency typing
    if (
      !this.pendingEsc &&
      !this.inRawString &&
      chunk.length <= 2 &&
      !chunk.includes('\x1b') &&
      !chunk.includes('\n') &&
      !chunk.includes('\r')
    ) {
      if (!this.altScreen && chunk !== '\x07') {
        this.atLineStart = false;
      }
      return chunk;
    }

    const input = this.pendingEsc ? this.pendingEsc + chunk : chunk;
    this.pendingEsc = '';

    let out = '';
    let i = 0;

    // Continue pass-through of an oversized OSC / DCS / APC string sequence
    if (this.inRawString !== null) {
      const doneIdx = this.scanRawStringEnd(input, 0, this.inRawString === 'osc');
      if (doneIdx === -1) {
        if (input.endsWith('\x1b')) {
          this.pendingEsc = '\x1b';
          return input.slice(0, -1);
        }
        return input;
      }
      out += input.slice(0, doneIdx);
      this.inRawString = null;
      i = doneIdx;
    }

    while (i < input.length) {
      const escIdx = input.indexOf('\x1b', i);
      if (escIdx === -1) {
        out += this.emitPlain(input.slice(i));
        break;
      }
      if (escIdx > i) {
        out += this.emitPlain(input.slice(i, escIdx));
      }

      i = escIdx;
      if (i + 1 >= input.length) {
        this.pendingEsc = '\x1b';
        break;
      }

      const next = input.charCodeAt(i + 1);

      // CSI: ESC [ <params 0x30..0x3F>* <intermediates 0x20..0x2F>* <final 0x40..0x7E>
      if (next === 0x5b) {
        let j = i + 2;
        while (j < input.length && input.charCodeAt(j) >= 0x30 && input.charCodeAt(j) <= 0x3f) {
          j++;
        }
        const paramEnd = j;
        while (j < input.length && input.charCodeAt(j) >= 0x20 && input.charCodeAt(j) <= 0x2f) {
          j++;
        }
        if (j >= input.length) {
          if (j - i > MAX_PENDING_CSI) {
            out += input.slice(i);
          } else {
            this.pendingEsc = input.slice(i);
          }
          break;
        }
        const finalCode = input.charCodeAt(j);
        const seq = input.slice(i, j + 1);
        if (finalCode >= 0x40 && finalCode <= 0x7e) {
          const hasIntermediates = j > paramEnd;
          this.handleCsi(input.slice(i + 2, paramEnd), hasIntermediates, finalCode);
        }
        out += seq;
        i = j + 1;
        continue;
      }

      // OSC: ESC ] ... (BEL | ESC \)
      if (next === 0x5d) {
        const endIdx = this.scanRawStringEnd(input, i + 2, true);
        if (endIdx === -1) {
          if (input.length - i > MAX_PENDING_STR) {
            if (input.endsWith('\x1b')) {
              out += input.slice(i, -1);
              this.pendingEsc = '\x1b';
            } else {
              out += input.slice(i);
            }
            this.inRawString = 'osc';
          } else {
            this.pendingEsc = input.slice(i);
          }
          break;
        }
        out += input.slice(i, endIdx);
        i = endIdx;
        continue;
      }

      // DCS (P), SOS (X), PM (^), APC (_): terminated by ST (ESC \)
      if (next === 0x50 || next === 0x58 || next === 0x5e || next === 0x5f) {
        const endIdx = this.scanRawStringEnd(input, i + 2, false);
        if (endIdx === -1) {
          if (input.length - i > MAX_PENDING_STR) {
            if (input.endsWith('\x1b')) {
              out += input.slice(i, -1);
              this.pendingEsc = '\x1b';
            } else {
              out += input.slice(i);
            }
            this.inRawString = 'st';
          } else {
            this.pendingEsc = input.slice(i);
          }
          break;
        }
        out += input.slice(i, endIdx);
        i = endIdx;
        continue;
      }

      // nF ESC sequences with intermediate bytes (e.g. ESC ( B, ESC ) 0, ESC # 8)
      if (next >= 0x20 && next <= 0x2f) {
        let j = i + 1;
        while (j < input.length && input.charCodeAt(j) >= 0x20 && input.charCodeAt(j) <= 0x2f) {
          j++;
        }
        if (j >= input.length) {
          if (j - i > 16) {
            out += input.slice(i);
          } else {
            this.pendingEsc = input.slice(i);
          }
          break;
        }
        out += input.slice(i, j + 1);
        i = j + 1;
        continue;
      }

      // Standard 2-byte ESC sequence (e.g. ESC c, ESC M, ESC =, ESC >)
      if (next >= 0x30 && next <= 0x7e) {
        if (next === 0x63) {
          // RIS: full terminal reset
          this.altScreen = false;
          this.fgActive = false;
          this.bgActive = false;
          this.styleMask = 0;
          this.atLineStart = true;
        }
        out += input.slice(i, i + 2);
        i += 2;
        continue;
      }

      // Unexpected control byte after ESC; emit ESC verbatim and continue
      out += '\x1b';
      i += 1;
    }

    return out;
  }

  private scanRawStringEnd(input: string, start: number, allowBel: boolean): number {
    for (let j = start; j < input.length; j++) {
      const c = input.charCodeAt(j);
      if (allowBel && c === 0x07) {
        return j + 1;
      }
      if (c === 0x1b) {
        if (j + 1 >= input.length) {
          return -1;
        }
        if (input.charCodeAt(j + 1) === 0x5c) {
          return j + 2;
        }
      }
    }
    return -1;
  }

  private handleCsi(params: string, hasIntermediates: boolean, finalCode: number): void {
    if (hasIntermediates) return;

    // SGR: ESC [ <params> m
    if (finalCode === 0x6d) {
      if (!params) {
        this.resetSgr();
        return;
      }
      // Private SGR prefixes (?, >, <, =) are not standard styling
      const first = params.charCodeAt(0);
      if (first >= 0x3c && first <= 0x3f) return;
      this.applySgrParams(params);
      return;
    }

    // DECSET / DECRST: ESC [ ? <modes> h / l
    if ((finalCode === 0x68 || finalCode === 0x6c) && params.startsWith('?')) {
      const enable = finalCode === 0x68;
      const modes = params.slice(1).split(';');
      for (const m of modes) {
        if (ALT_SCREEN_MODES.has(m)) {
          this.altScreen = enable;
        }
      }
      return;
    }

    // Cursor positioning updates line-start tracking for main-screen prompt detection
    if (!this.altScreen && (!params || (params.charCodeAt(0) >= 0x30 && params.charCodeAt(0) <= 0x3b))) {
      // CUP (H) / HVP (f)
      if (finalCode === 0x48 || finalCode === 0x66) {
        const parts = params.split(';');
        const col = parts[1];
        this.atLineStart = !col || col === '1';
      } else if (finalCode === 0x47 || finalCode === 0x60) {
        // CHA (G) / HPA (`)
        this.atLineStart = !params || params === '1';
      } else if (finalCode === 0x45 || finalCode === 0x46) {
        // CNL (E) / CPL (F)
        this.atLineStart = true;
      } else if (finalCode === 0x43) {
        // CUF (C): cursor forward
        this.atLineStart = false;
      }
    }
  }

  private resetSgr(): void {
    this.fgActive = false;
    this.bgActive = false;
    this.styleMask = 0;
  }

  private applySgrParams(params: string): void {
    const parts = params.split(';');
    for (let idx = 0; idx < parts.length; idx++) {
      const raw = parts[idx];
      if (!raw) {
        this.resetSgr();
        continue;
      }
      // Handle colon subparameters like `38:2::255:0:0` or `4:3`
      const colonIdx = raw.indexOf(':');
      const code = Number(colonIdx === -1 ? raw : raw.slice(0, colonIdx));
      if (Number.isNaN(code)) continue;

      if (code === 0) {
        this.resetSgr();
      } else if (code === 1 || code === 2) {
        this.styleMask |= STYLE_BOLD_DIM;
      } else if (code === 3) {
        this.styleMask |= STYLE_ITALIC;
      } else if (code === 4) {
        this.styleMask |= STYLE_UNDERLINE;
      } else if (code === 5 || code === 6) {
        this.styleMask |= STYLE_BLINK;
      } else if (code === 7) {
        this.styleMask |= STYLE_INVERSE;
      } else if (code === 8) {
        this.styleMask |= STYLE_HIDDEN;
      } else if (code === 9) {
        this.styleMask |= STYLE_STRIKE;
      } else if (code === 22) {
        this.styleMask &= ~STYLE_BOLD_DIM;
      } else if (code === 23) {
        this.styleMask &= ~STYLE_ITALIC;
      } else if (code === 24) {
        this.styleMask &= ~STYLE_UNDERLINE;
      } else if (code === 25) {
        this.styleMask &= ~STYLE_BLINK;
      } else if (code === 27) {
        this.styleMask &= ~STYLE_INVERSE;
      } else if (code === 28) {
        this.styleMask &= ~STYLE_HIDDEN;
      } else if (code === 29) {
        this.styleMask &= ~STYLE_STRIKE;
      } else if ((code >= 30 && code <= 37) || (code >= 90 && code <= 97)) {
        this.fgActive = true;
      } else if (code === 39) {
        this.fgActive = false;
      } else if ((code >= 40 && code <= 47) || (code >= 100 && code <= 107)) {
        this.bgActive = true;
      } else if (code === 49) {
        this.bgActive = false;
      } else if (code === 38 || code === 48 || code === 58) {
        if (code === 38) this.fgActive = true;
        else if (code === 48) this.bgActive = true;
        else this.styleMask |= STYLE_UNDERLINE;
        // Skip semicolon-delimited extended color payload so `0` RGB channel isn't treated as reset
        if (colonIdx === -1 && idx + 1 < parts.length) {
          const mode = Number(parts[idx + 1]);
          if (mode === 5) idx += 2;
          else if (mode === 2) idx += 4;
        }
      }
    }
  }

  private emitPlain(text: string): string {
    if (!text) return '';
    if (this.altScreen) {
      return text;
    }
    if (this.sgrActive) {
      this.updateLineStartFromPlain(text);
      return text;
    }
    return this.highlightPlain(text);
  }

  private updateLineStartFromPlain(text: string): void {
    const lastNl = Math.max(text.lastIndexOf('\n'), text.lastIndexOf('\r'));
    if (lastNl === -1) {
      if (hasVisibleChar(text, 0, text.length)) {
        this.atLineStart = false;
      }
      return;
    }
    this.atLineStart = !hasVisibleChar(text, lastNl + 1, text.length);
  }

  private highlightPlain(text: string): string {
    let out = '';
    let start = 0;

    for (let i = 0; i < text.length; i++) {
      const c = text.charCodeAt(i);
      if (c === 0x0a || c === 0x0d) {
        if (i > start) {
          out += highlightLineSegment(text.slice(start, i), this.atLineStart);
        }
        // Preserve exact \r\n, \n, or \r delimiter
        if (c === 0x0d && i + 1 < text.length && text.charCodeAt(i + 1) === 0x0a) {
          out += '\r\n';
          i++;
        } else {
          out += text[i];
        }
        this.atLineStart = true;
        start = i + 1;
      }
    }

    if (start < text.length) {
      const tail = text.slice(start);
      out += highlightLineSegment(tail, this.atLineStart);
      if (hasVisibleChar(text, start, text.length)) {
        this.atLineStart = false;
      }
    }

    return out;
  }
}
