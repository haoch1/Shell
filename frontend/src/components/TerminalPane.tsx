import React, { useEffect, useRef, useState } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SearchAddon } from '@xterm/addon-search';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { Unicode11Addon } from '@xterm/addon-unicode11';
import { WebglAddon } from '@xterm/addon-webgl';
import '@xterm/xterm/css/xterm.css';
import {
  ArrowUpRight,
  Clipboard,
  Copy,
  Download,
  Eraser,
  FolderOpen,
  Loader2,
  Search,
  Sparkles,
  SplitSquareHorizontal,
  SplitSquareVertical,
  X,
} from 'lucide-react';
import type { AppSettings, SessionRecord } from '../types/shell';
import { getTerminalTheme } from '../lib/themes';
import { terminalBus } from '../lib/terminalBus';
import { copyToClipboard, openExternalURL, readFromClipboard } from '../lib/bridge';
import { formatHostDisplay } from '../lib/utils';

interface TerminalPaneProps {
  paneId: string;
  onDragPane: (e: React.DragEvent) => void;
  session: SessionRecord;
  settings: AppSettings;
  focused: boolean;
  multiPane: boolean;
  onFocus: () => void;
  onInput: (data: string | Uint8Array) => void;
  onResize: (cols: number, rows: number) => void;
  onCwdChange?: (dir: string) => void;
  onReconnect: () => void;
  onClosePane: () => void;
  onDetachPane?: () => void;
  onSplit: (dir: 'horizontal' | 'vertical') => void;
  onOpenSearch: () => void;
  onOpenSFTP: () => void;
  onExplainSelection: (text: string) => void;
  onExportLog: (content: string) => void;
  onFontZoom: (delta: number) => void;
  onToast: (msg: string) => void;
}

export const TerminalPane: React.FC<TerminalPaneProps> = React.memo(({
  paneId,
  onDragPane,
  session,
  settings,
  focused,
  multiPane,
  onFocus,
  onInput,
  onResize,
  onCwdChange,
  onReconnect,
  onClosePane,
  onDetachPane,
  onSplit,
  onOpenSearch,
  onOpenSFTP,
  onExplainSelection,
  onExportLog,
  onFontZoom,
  onToast,
}) => {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef(onInput);
  const resizeRef = useRef(onResize);
  const cwdRef = useRef(onCwdChange);
  const settingsRef = useRef(settings);
  const zoomRef = useRef(onFontZoom);
  const toastRef = useRef(onToast);
  const searchOpenRef = useRef(onOpenSearch);
  const reconnectRef = useRef(onReconnect);
  const stateRef = useRef(session.state);
  inputRef.current = onInput;
  resizeRef.current = onResize;
  cwdRef.current = onCwdChange;
  settingsRef.current = settings;
  zoomRef.current = onFontZoom;
  toastRef.current = onToast;
  searchOpenRef.current = onOpenSearch;
  reconnectRef.current = onReconnect;
  stateRef.current = session.state;

  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; selection: string } | null>(null);

  const isAppDark =
    settings.theme === 'dark' ||
    (settings.theme === 'system' && (window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false));
  const preset = getTerminalTheme(settings.terminalTheme, isAppDark);

  useEffect(() => {
    const container = hostRef.current;
    if (!container) return;

    const initDark =
      settingsRef.current.theme === 'dark' ||
      (settingsRef.current.theme === 'system' &&
        (window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false));
    const term = new Terminal({
      fontFamily: settingsRef.current.fontFamily,
      fontSize: settingsRef.current.fontSize,
      lineHeight: settingsRef.current.lineHeight,
      cursorStyle: settingsRef.current.cursorStyle,
      cursorBlink: settingsRef.current.cursorBlink,
      scrollback: settingsRef.current.scrollback,
      theme: getTerminalTheme(settingsRef.current.terminalTheme, initDark).theme,
      allowProposedApi: true,
      macOptionIsMeta: true,
    });

    const fit = new FitAddon();
    const search = new SearchAddon();
    const links = new WebLinksAddon((_ev, uri) => openExternalURL(uri));
    const unicode11 = new Unicode11Addon();

    term.loadAddon(fit);
    term.loadAddon(search);
    term.loadAddon(links);
    term.loadAddon(unicode11);
    term.unicode.activeVersion = '11';

    term.open(container);

    let webgl: WebglAddon | null = null;
    if (settingsRef.current.webglEnabled) {
      try {
        webgl = new WebglAddon();
        webgl.onContextLoss(() => {
          webgl?.dispose();
          webgl = null;
        });
        term.loadAddon(webgl);
      } catch {
        webgl = null;
      }
    }

    let resizeTimer: number | null = null;
    let lastCols = 0;
    let lastRows = 0;
    const doFit = () => {
      if (container.clientWidth < 40 || container.clientHeight < 30) return;
      try {
        fit.fit();
      } catch {
        // Ignore transient layout measurements while hidden
      }
    };

    const debouncedFit = () => {
      if (resizeTimer !== null) window.clearTimeout(resizeTimer);
      resizeTimer = window.setTimeout(() => {
        resizeTimer = null;
        doFit();
      }, 80);
    };

    const observer = new ResizeObserver(debouncedFit);
    observer.observe(container);

    const dataDisp = term.onData((d) => inputRef.current(d));
    const binDisp = term.onBinary((d) => inputRef.current(Uint8Array.from(d, (c) => c.charCodeAt(0))));
    const resizeDisp = term.onResize(({ cols, rows }) => {
      if (cols === lastCols && rows === lastRows) return;
      lastCols = cols;
      lastRows = rows;
      resizeRef.current(cols, rows);
    });
    // Track OSC 7 working directory updates emitted by modern shells (bash/zsh/fish) so SFTP opens directly in CWD
    const osc7Disp = term.parser.registerOscHandler(7, (data) => {
      try {
        const u = new URL(data);
        if (u.protocol === 'file:' && u.pathname) {
          cwdRef.current?.(decodeURIComponent(u.pathname));
        }
      } catch {
        // Ignore non-URI OSC 7 payloads
      }
      return false;
    });
    const selDisp = term.onSelectionChange(() => {
      if (settingsRef.current.copyOnSelect && term.hasSelection()) {
        const text = term.getSelection();
        if (text) {
          void copyToClipboard(text);
        }
      }
    });

    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== 'keydown') return true;
      if (stateRef.current === 'closed' && e.key === 'Enter' && !e.ctrlKey && !e.altKey && !e.metaKey) {
        e.preventDefault();
        reconnectRef.current();
        return false;
      }
      if (e.ctrlKey && e.shiftKey && e.code === 'KeyC') {
        e.preventDefault();
        if (term.hasSelection()) {
          void copyToClipboard(term.getSelection());
          toastRef.current('已复制选区');
        }
        return false;
      }
      if (e.ctrlKey && e.shiftKey && e.code === 'KeyV') {
        e.preventDefault();
        void readFromClipboard().then((t) => {
          if (t) term.paste(t);
        });
        return false;
      }
      if (e.ctrlKey && e.shiftKey && e.code === 'KeyF') {
        e.preventDefault();
        searchOpenRef.current();
        return false;
      }
      if (e.ctrlKey && !e.shiftKey && (e.key === '=' || e.key === '+')) {
        e.preventDefault();
        zoomRef.current(1);
        return false;
      }
      if (e.ctrlKey && !e.shiftKey && e.key === '-') {
        e.preventDefault();
        zoomRef.current(-1);
        return false;
      }
      if (e.ctrlKey && !e.shiftKey && e.key === '0') {
        e.preventDefault();
        zoomRef.current(0);
        return false;
      }
      return true;
    });

    const getTranscript = (): string => {
      const buf = term.buffer.active;
      const lines: string[] = [];
      for (let i = 0; i < buf.length; i++) {
        const line = buf.getLine(i);
        if (line) lines.push(line.translateToString(true));
      }
      while (lines.length && !lines[lines.length - 1].trim()) lines.pop();
      return lines.join('\n') + '\n';
    };

    terminalBus.register(session.id, {
      term,
      search,
      fit: doFit,
      focus: () => term.focus(),
      writeData: (bytes) => term.write(bytes),
      writeln: (line) => term.writeln(line),
      getSelection: () => term.getSelection(),
      getTranscript,
      clear: () => term.clear(),
    });

    const initialFrame = requestAnimationFrame(() => {
      doFit();
      resizeRef.current(term.cols, term.rows);
      term.focus();
    });

    return () => {
      cancelAnimationFrame(initialFrame);
      if (resizeTimer !== null) window.clearTimeout(resizeTimer);
      observer.disconnect();
      dataDisp.dispose();
      binDisp.dispose();
      resizeDisp.dispose();
      osc7Disp.dispose();
      selDisp.dispose();
      webgl?.dispose();
      terminalBus.unregister(session.id);
      term.dispose();
    };
  }, [session.id]);

  // Live-update xterm options when user changes appearance settings.
  useEffect(() => {
    const ctrl = terminalBus.get(session.id);
    if (!ctrl) return;
    ctrl.term.options.fontFamily = settings.fontFamily;
    ctrl.term.options.fontSize = settings.fontSize;
    ctrl.term.options.lineHeight = settings.lineHeight;
    ctrl.term.options.cursorStyle = settings.cursorStyle;
    ctrl.term.options.cursorBlink = settings.cursorBlink;
    ctrl.term.options.scrollback = settings.scrollback;
    ctrl.term.options.theme = getTerminalTheme(settings.terminalTheme, isAppDark).theme;
    requestAnimationFrame(() => ctrl.fit());
  }, [
    session.id,
    settings.theme,
    settings.fontFamily,
    settings.fontSize,
    settings.lineHeight,
    settings.cursorStyle,
    settings.cursorBlink,
    settings.scrollback,
    settings.terminalTheme,
    isAppDark,
  ]);

  const handleContextMenu = (e: React.MouseEvent) => {
    e.preventDefault();
    onFocus();
    const ctrl = terminalBus.get(session.id);
    const sel = ctrl?.getSelection() ?? '';
    if (settings.rightClickPaste) {
      if (sel) {
        void copyToClipboard(sel);
        ctrl?.term.clearSelection();
        onToast('已复制选中文本');
      } else {
        void readFromClipboard().then((t) => {
          if (t && session.state === 'connected') ctrl?.term.paste(t);
        });
      }
      return;
    }
    setCtxMenu({ x: e.clientX, y: e.clientY, selection: sel });
  };

  const hideHostIp = Boolean(settings.hideHostIp);
  const paneDisplayName =
    session.profile.name && (!hideHostIp || session.profile.name !== session.profile.host)
      ? session.profile.name
      : hideHostIp
        ? session.profile.user || 'SSH 主机'
        : formatHostDisplay(session.profile.host, session.profile.port);

  return (
    <div
      data-pane-id={paneId}
      data-session-id={session.id}
      className={`pane-box ${multiPane ? 'multi-pane' : ''} ${focused ? 'focused' : ''}`}
      onMouseDown={onFocus}
      onContextMenu={handleContextMenu}
    >
      {multiPane && (
        <div className="pane-mini-bar" draggable onDragStart={onDragPane} title="拖动此会话到终端边缘分屏">
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
            <span className={`tab-dot ${session.state}`} />
            <span className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'inline-flex', alignItems: 'center', minWidth: 0 }}>
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{paneDisplayName}</span>
              <span
                className={`ip-inline-collapsible ${hideHostIp ? 'is-hidden' : ''}`}
                data-ip-hidden={hideHostIp ? 'true' : undefined}
              >
                {hideHostIp ? '' : ` (${session.profile.user}@${formatHostDisplay(session.profile.host, session.profile.port)})`}
              </span>
            </span>
          </div>
          <div className="pane-mini-actions">
            {onDetachPane && (
              <button
                type="button"
                className="host-action-btn"
                style={{ width: 20, height: 20 }}
                title="移出为独立标签页"
                aria-label="移出为独立标签页"
                onClick={(e) => {
                  e.stopPropagation();
                  onDetachPane();
                }}
              >
                <ArrowUpRight size={11.5} />
              </button>
            )}
            <button
              type="button"
              className="host-action-btn"
              style={{ width: 20, height: 20 }}
              title="关闭此分屏"
              aria-label="关闭此分屏"
              onClick={(e) => {
                e.stopPropagation();
                onClosePane();
              }}
            >
              <X size={11} />
            </button>
          </div>
        </div>
      )}

      <div
        className="pane-xterm-wrap"
        style={{ background: preset.theme.background ?? '#141720' }}
      >
        <div ref={hostRef} className="pane-xterm-host" />
      </div>

      {session.state === 'connecting' && (
        <div className="pane-connecting-bar">
          <Loader2 size={13} className="spin" />
          <span className="mono">
            {hideHostIp
              ? `正在连接 ${paneDisplayName} …`
              : `正在连接 ${session.profile.user}@${formatHostDisplay(session.profile.host, session.profile.port)} …`}
          </span>
        </div>
      )}

      {session.state === 'closed' && (
        <div className="pane-closed-bar">
          <span>{session.endMessage || '会话已断开'}</span>
          <button type="button" className="btn btn-primary btn-sm" onClick={onReconnect}>
            重新连接
          </button>
        </div>
      )}

      {ctxMenu && (
        <>
          <div
            style={{ position: 'fixed', inset: 0, zIndex: 139 }}
            onClick={() => setCtxMenu(null)}
            onContextMenu={(e) => {
              e.preventDefault();
              setCtxMenu(null);
            }}
          />
          <div
            className="context-menu"
            style={{
              left: Math.min(ctxMenu.x, window.innerWidth - 200),
              top: Math.min(ctxMenu.y, window.innerHeight - 310),
            }}
          >
            <button
              type="button"
              className="context-menu-item"
              disabled={!ctxMenu.selection}
              onClick={() => {
                if (ctxMenu.selection) {
                  void copyToClipboard(ctxMenu.selection);
                  onToast('已复制');
                }
                setCtxMenu(null);
              }}
            >
              <Copy size={13} />
              复制选区
            </button>
            <button
              type="button"
              className="context-menu-item"
              disabled={session.state !== 'connected'}
              onClick={() => {
                void readFromClipboard().then((t) => {
                  if (t) terminalBus.get(session.id)?.term.paste(t);
                });
                setCtxMenu(null);
              }}
            >
              <Clipboard size={13} />
              粘贴
            </button>
            {ctxMenu.selection && (
              <button
                type="button"
                className="context-menu-item"
                onClick={() => {
                  onExplainSelection(ctxMenu.selection);
                  setCtxMenu(null);
                }}
              >
                <Sparkles size={13} />
                用 AI 解释选中内容
              </button>
            )}
            <div className="context-menu-sep" />
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onOpenSearch();
                setCtxMenu(null);
              }}
            >
              <Search size={13} />
              搜索输出
            </button>
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                terminalBus.get(session.id)?.clear();
                setCtxMenu(null);
              }}
            >
              <Eraser size={13} />
              清屏
            </button>
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                const text = terminalBus.get(session.id)?.getTranscript() ?? '';
                onExportLog(text);
                setCtxMenu(null);
              }}
            >
              <Download size={13} />
              导出终端日志
            </button>
            <div className="context-menu-sep" />
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onSplit('horizontal');
                setCtxMenu(null);
              }}
            >
              <SplitSquareHorizontal size={13} />
              向右分屏
            </button>
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onSplit('vertical');
                setCtxMenu(null);
              }}
            >
              <SplitSquareVertical size={13} />
              向下分屏
            </button>
            {multiPane && onDetachPane && (
              <button
                type="button"
                className="context-menu-item"
                onClick={() => {
                  onDetachPane();
                  setCtxMenu(null);
                }}
              >
                <ArrowUpRight size={13} />
                移出为独立标签页
              </button>
            )}
            {session.state === 'connected' && (
              <button
                type="button"
                className="context-menu-item"
                onClick={() => {
                  onOpenSFTP();
                  setCtxMenu(null);
                }}
              >
                <FolderOpen size={13} />
                打开 SFTP 文件
              </button>
            )}
          </div>
        </>
      )}
    </div>
  );
});
