import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Radio, X } from 'lucide-react';
import type {
  AgentApprovalRequest,
  AgentStatusView,
  AppSettings,
  ChallengeRequest,
  ConnectStage,
  ConnectStep,
  DrawerMode,
  HostVerifyRequest,
  LocalShellProfile,
  Profile,
  ServerStats,
  SessionRecord,
  Snippet,
  TransferProgress,
  TunnelRule,
  UpdateInfo,
  UpdateProgress,
  WorkspaceTab,
} from './types/shell';
import { errorMessage, getApi, hasDesktopBridge, onDesktopEvent } from './lib/bridge';
import { resolveTerminalTheme, syncTerminalThemeForAppearance } from './lib/themes';
import { decodeBase64, encodeBase64, terminalBus } from './lib/terminalBus';
import { connectIfOpen, reconnectSecret, sameSessionTarget } from './lib/sessionAttempt';
import {
  collectLeaves,
  randomId,
  removePaneNode,
  replacePaneSession,
  splitPaneNode,
  updateSplitRatio,
} from './lib/utils';
import { Titlebar } from './components/Titlebar';
import { Sidebar } from './components/Sidebar';
import { WelcomeView } from './components/WelcomeView';
import { SplitWorkspace } from './components/SplitWorkspace';
import { detachPaneToTab, movePane } from './lib/paneLayout';
import { usePaneDrag } from './lib/usePaneDrag';
import { StatusBar } from './components/StatusBar';
import { SftpDrawer } from './components/SftpDrawer';
import { TunnelsDrawer } from './components/TunnelsDrawer';
import { SnippetsDrawer } from './components/SnippetsDrawer';
import { AIDrawer } from './components/AIDrawer';
import { HostModal } from './components/HostModal';
import { CommandPalette } from './components/CommandPalette';
import { SSHConfigImportModal } from './components/SSHConfigImportModal';
import { HostExportModal } from './components/HostExportModal';
import { SettingsModal, type SettingsTab } from './components/SettingsModal';
import { RemoteEditorModal } from './components/RemoteEditorModal';
import { TerminalSearchBar } from './components/TerminalSearchBar';
import { ChallengeDialog, VerifyHostDialog } from './components/SecurityDialogs';
import { AgentApprovalDialog } from './components/AgentApprovalDialog';
import { DEFAULT_TERMINAL_FONT } from './lib/terminalFont';
import { TooltipProvider } from './components/ui/Tooltip';
import { detectOSIconFromText } from './components/HostIcon';

interface SplitTarget {
  tabId: string;
  paneId: string;
  direction: 'horizontal' | 'vertical';
}

const APPEARANCE_STORAGE_KEY = 'shell:appearance:v2';

const DEFAULT_PREVIEW_LOCAL_SHELLS: LocalShellProfile[] = [
  { id: 'pwsh', name: 'PowerShell 7', command: 'pwsh.exe', args: ['-NoLogo'], icon: 'powershell', category: 'shell', isDefault: true },
  { id: 'powershell', name: 'Windows PowerShell', command: 'powershell.exe', args: ['-NoLogo'], icon: 'powershell', category: 'shell' },
  { id: 'cmd', name: '命令提示符', command: 'cmd.exe', icon: 'cmd', category: 'shell' },
  { id: 'git-bash', name: 'Git Bash', command: 'bash.exe', args: ['--login', '-i'], icon: 'git', category: 'shell' },
  { id: 'wsl:Ubuntu', name: 'WSL · Ubuntu', command: 'wsl.exe', args: ['-d', 'Ubuntu', '--cd', '~'], icon: 'ubuntu', category: 'wsl' },
];

const DEFAULT_SETTINGS: AppSettings = {
  theme: 'light',
  terminalTheme: 'auto',
  fontFamily: DEFAULT_TERMINAL_FONT,
  fontSize: 13,
  lineHeight: 1.28,
  cursorStyle: 'bar',
  cursorBlink: true,
  scrollback: 10000,
  copyOnSelect: false,
  rightClickPaste: false,
  showTelemetry: true,
  hideHostIp: false,
  syntaxHighlight: true,
  autoReconnect: true,
  confirmMultilinePaste: true,
  defaultLocalShell: '',
  groupOrder: [],
  webglEnabled: false,
  defaultKeepAlive: 30,
  confirmOnClose: true,
  closeToTray: true,
  autoCheckUpdate: true,
};

function readCachedAppearance(): Pick<AppSettings, 'theme' | 'terminalTheme'> | null {
  try {
    const raw = window.localStorage?.getItem(APPEARANCE_STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<Pick<AppSettings, 'theme' | 'terminalTheme'>> | null;
    if (!parsed || typeof parsed !== 'object') return null;
    const theme: AppSettings['theme'] =
      parsed.theme === 'dark' || parsed.theme === 'light' || parsed.theme === 'system'
        ? parsed.theme
        : DEFAULT_SETTINGS.theme;
    const terminalTheme =
      typeof parsed.terminalTheme === 'string' && parsed.terminalTheme.trim()
        ? parsed.terminalTheme.trim()
        : DEFAULT_SETTINGS.terminalTheme;
    return { theme, terminalTheme };
  } catch {
    return null;
  }
}

function writeCachedAppearance(theme: AppSettings['theme'], terminalTheme: string): void {
  try {
    const sysDark = window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false;
    const baseDark = theme === 'dark' || (theme === 'system' && sysDark);
    const normalizedTerm =
      !terminalTheme || terminalTheme === 'auto'
        ? 'auto'
        : resolveTerminalTheme(terminalTheme, baseDark).id;
    window.localStorage?.setItem(
      APPEARANCE_STORAGE_KEY,
      JSON.stringify({ theme, terminalTheme: normalizedTerm }),
    );
  } catch {
    // ignore storage restrictions
  }
}

function getInitialSettings(): AppSettings {
  const cached = readCachedAppearance();
  return cached ? { ...DEFAULT_SETTINGS, ...cached } : DEFAULT_SETTINGS;
}

export const App: React.FC = () => {
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [snippets, setSnippets] = useState<Snippet[]>([]);
  const [localShells, setLocalShells] = useState<LocalShellProfile[]>(() =>
    hasDesktopBridge() ? [] : DEFAULT_PREVIEW_LOCAL_SHELLS,
  );
  const [settings, setSettings] = useState<AppSettings>(getInitialSettings);
  const [settingsLoaded, setSettingsLoaded] = useState(false);
  const [agentStatus, setAgentStatus] = useState<AgentStatusView | null>(null);
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [tabs, setTabs] = useState<WorkspaceTab[]>([]);
  const [activeTabId, setActiveTabId] = useState<string | null>(null);
  const [sessions, setSessions] = useState<Record<string, SessionRecord>>({});
  const [drawer, setDrawer] = useState<DrawerMode>('none');
  const [searchOpen, setSearchOpen] = useState(false);

  // Modals
  const [hostModal, updateHostModal] = useState<{ open: boolean; profile: Profile | null; split?: SplitTarget }>({
    open: false,
    profile: null,
  });
  const promptForVisibleConnect = useRef('');
  const hostModalGeneration = useRef(0);
  const setHostModal = useCallback((next: typeof hostModal, requestedProfile = '') => {
    hostModalGeneration.current++;
    const previous = promptForVisibleConnect.current;
    promptForVisibleConnect.current = requestedProfile;
    if (previous && previous !== requestedProfile) {
      void getApi().CancelVisibleConnect(previous).catch(() => {});
    }
    updateHostModal(next);
  }, []);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [splitPicker, setSplitPicker] = useState<{
    target: SplitTarget;
    source: Profile;
    sourceLocalShell?: LocalShellProfile;
  } | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [exportIds, setExportIds] = useState<string[] | null>(null);
  const [settingsModal, setSettingsModal] = useState<{ open: boolean; tab: SettingsTab }>({
    open: false,
    tab: 'appearance',
  });
  const [aiConfigVersion, setAiConfigVersion] = useState(0);
  const [editorTarget, setEditorTarget] = useState<{ sessionId: string; remotePath: string } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<Profile | null>(null);

  // Security & Agent Approval dialogs
  const [verifyQueue, setVerifyQueue] = useState<HostVerifyRequest[]>([]);
  const [challengeQueue, setChallengeQueue] = useState<ChallengeRequest[]>([]);
  const [approvalQueue, setApprovalQueue] = useState<AgentApprovalRequest[]>([]);

  // Telemetry & transfers
  const [statsBySession, setStatsBySession] = useState<Record<string, ServerStats>>({});
  const [netRateBySession, setNetRateBySession] = useState<Record<string, { rxPerSec: number; txPerSec: number }>>({});
  const [tunnelsCountBySession, setTunnelsCountBySession] = useState<Record<string, number>>({});
  const [transfers, setTransfers] = useState<TransferProgress[]>([]);
  const [explainText, setExplainText] = useState('');
  const [toasts, setToasts] = useState<{ id: number; text: string }[]>([]);
  const [updateInfo, setUpdateInfo] = useState<UpdateInfo | null>(null);
  const [updateProgress, setUpdateProgress] = useState<UpdateProgress | null>(null);
  const [checkingUpdate, setCheckingUpdate] = useState(false);
  const [updatingApp, setUpdatingApp] = useState(false);
  const [updateError, setUpdateError] = useState('');

  const writeChainsRef = useRef<Map<string, Promise<void>>>(new Map());
  const settingsWrite = useRef(Promise.resolve());
  const settingsRevisionRef = useRef(0);
  const wsMapRef = useRef<Map<string, WebSocket>>(new Map());
  const connectingRef = useRef(new Map<string, { controller: AbortController; requestedProfile: string }>());
  const reconnectTimersRef = useRef<Map<string, number>>(new Map());
  const reconnectPaneRef = useRef<(tabId: string, paneId: string, oldSessionId: string, attempt?: number) => void>(() => {});
  const lastSizeRef = useRef<Map<string, string>>(new Map());
  const lastConnectAtRef = useRef<Map<string, number>>(new Map());
  const autoCheckedRef = useRef(false);
  const checkingUpdateRef = useRef(false);
  const textEncoderRef = useRef(new TextEncoder());
  const sessionsRef = useRef(sessions);
  sessionsRef.current = sessions;
  const tabsRef = useRef(tabs);
  tabsRef.current = tabs;
  const profilesRef = useRef(profiles);
  profilesRef.current = profiles;
  const localShellsRef = useRef(localShells);
  localShellsRef.current = localShells;
  const settingsRef = useRef(settings);
  settingsRef.current = settings;

  const toast = useCallback((text: string) => {
    const id = Date.now() + Math.random();
    setToasts((prev) => [...prev.slice(-2), { id, text }]);
    window.setTimeout(() => {
      setToasts((prev) => prev.filter((t) => t.id !== id));
    }, 2800);
  }, []);

  // Resolve effective light/dark appearance
  const [sysDark, setSysDark] = useState(() => window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false);
  useEffect(() => {
    const mq = window.matchMedia?.('(prefers-color-scheme: dark)');
    if (!mq) return;
    const handler = (e: MediaQueryListEvent) => setSysDark(e.matches);
    mq.addEventListener('change', handler);
    return () => mq.removeEventListener('change', handler);
  }, []);

  const baseDark = settings.theme === 'dark' || (settings.theme === 'system' && sysDark);
  const activePalette = useMemo(
    () => resolveTerminalTheme(settings.terminalTheme, baseDark),
    [settings.terminalTheme, baseDark],
  );
  const effectiveDark = activePalette.isDark;
  const workspaceSettings = useMemo(
    () => (settings.theme === 'system' ? { ...settings } : settings),
    [settings, effectiveDark],
  );
  useEffect(() => {
    document.documentElement.dataset.theme = effectiveDark ? 'dark' : 'light';
    document.documentElement.dataset.palette = activePalette.id;
  }, [effectiveDark, activePalette.id]);

  // Initial load from Go backend
  const reloadData = useCallback(async () => {
    if (!hasDesktopBridge()) {
      setLocalShells((cur) => (cur.length > 0 ? cur : DEFAULT_PREVIEW_LOCAL_SHELLS));
      return;
    }
    const revBefore = settingsRevisionRef.current;
    const api = getApi();
    const [profs, snips, cfg, agSt, shells] = await Promise.all([
      api.Profiles().catch(() => []),
      api.Snippets().catch(() => []),
      api.GetSettings().catch(() => null),
      api.GetAgentStatus().catch(() => null),
      typeof api.LocalShells === 'function' ? api.LocalShells().catch(() => null) : Promise.resolve(null),
    ]);
    setProfiles(profs || []);
    setSnippets(snips || []);
    if (Array.isArray(shells) && shells.length > 0) {
      setLocalShells(shells);
    } else if (typeof api.LocalShells !== 'function') {
      setLocalShells((cur) => (cur.length > 0 ? cur : DEFAULT_PREVIEW_LOCAL_SHELLS));
    }
    if (cfg) {
      if (settingsRevisionRef.current === revBefore) {
        const merged = { ...DEFAULT_SETTINGS, ...cfg };
        setSettings(merged);
        writeCachedAppearance(merged.theme, merged.terminalTheme);
      }
      setSettingsLoaded(true);
    }
    if (agSt) setAgentStatus(agSt);
  }, []);

  useEffect(() => {
    void reloadData();
  }, [reloadData]);

  // Attach a low-latency binary WebSocket stream for a session (falls back cleanly to Wails IPC in mock mode).
  const attachSessionWS = useCallback(async (sessionId: string, signal: AbortSignal) => {
    try {
      const api = getApi();
      if (typeof api.TermWSURL !== 'function') return;
      const url = await api.TermWSURL(sessionId);
      if (!url || signal.aborted) return;
      const existing = wsMapRef.current.get(sessionId);
      if (existing) {
        existing.close();
        wsMapRef.current.delete(sessionId);
      }
      await new Promise<void>((resolve) => {
        const ws = new WebSocket(url);
        wsMapRef.current.set(sessionId, ws);
        ws.binaryType = 'arraybuffer';
        let settled = false;
        const done = () => {
          if (!settled) {
            settled = true;
            resolve();
          }
        };
        const timer = window.setTimeout(done, 120);
        ws.onopen = () => {
          window.clearTimeout(timer);
          done();
        };
        ws.onmessage = (ev) => {
          if (!signal.aborted && wsMapRef.current.get(sessionId) === ws && ev.data instanceof ArrayBuffer) {
            terminalBus.writeBytes(sessionId, new Uint8Array(ev.data));
          }
        };
        ws.onerror = () => {
          window.clearTimeout(timer);
          done();
        };
        ws.onclose = () => {
          window.clearTimeout(timer);
          done();
          if (wsMapRef.current.get(sessionId) === ws) {
            wsMapRef.current.delete(sessionId);
          }
        };
      });
    } catch {
      // Fall back to Wails EventsEmit/Input seamlessly
    }
  }, []);

  const cancelAutoReconnect = useCallback((sessionId: string) => {
    const timer = reconnectTimersRef.current.get(sessionId);
    if (timer !== undefined) {
      window.clearTimeout(timer);
      reconnectTimersRef.current.delete(sessionId);
    }
    setSessions((prev) => {
      const cur = prev[sessionId];
      if (!cur || !cur.reconnectAt) return prev;
      return { ...prev, [sessionId]: { ...cur, reconnectAt: undefined } };
    });
  }, []);

  const scheduleAfterPaint = useCallback((fn: () => void) => {
    let ran = false;
    const run = () => {
      if (ran) return;
      ran = true;
      window.clearTimeout(timer);
      window.cancelAnimationFrame(frame);
      fn();
    };
    const frame = window.requestAnimationFrame(run);
    const timer = window.setTimeout(run, 48);
  }, []);

  // Start an SSH connection for a newly allocated session ID
  const startSSHConnection = useCallback(
    (sessionId: string, profile: Profile, secret: string, remember: boolean, requestedProfile = '') => {
      const attempt = new AbortController();
      connectingRef.current.set(sessionId, { controller: attempt, requestedProfile });
      scheduleAfterPaint(async () => {
        const api = getApi();
        const ctrl = terminalBus.get(sessionId);
        ctrl?.fit();
        const cols = ctrl?.term.cols || 100;
        const rows = ctrl?.term.rows || 32;

        try {
          const opened = await connectIfOpen(
            attempt.signal,
            () => attachSessionWS(sessionId, attempt.signal),
            () => api.Connect(sessionId, profile, secret, remember, cols, rows),
            () => api.Disconnect(sessionId),
          );
          if (!opened) return;
          setSessions((prev) => {
            const cur = prev[sessionId];
            if (!cur || cur.state !== 'connecting') return prev;
            const doneSteps = cur.connectSteps?.map((step) =>
              step.status === 'active' ? { ...step, status: 'done' as const } : step,
            );
            return {
              ...prev,
              [sessionId]: {
                ...cur,
                state: 'connected',
                hasConnected: true,
                reconnectAt: undefined,
                reconnectAttempt: 0,
                connectSteps: doneSteps,
                startedAt: Date.now(),
              },
            };
          });
          const latestCtrl = terminalBus.get(sessionId);
          if (latestCtrl) {
            latestCtrl.fit();
            const nextSizeKey = `${latestCtrl.term.cols}x${latestCtrl.term.rows}`;
            lastSizeRef.current.set(sessionId, nextSizeKey);
            if (latestCtrl.term.cols !== cols || latestCtrl.term.rows !== rows) {
              void api.Resize(sessionId, latestCtrl.term.cols, latestCtrl.term.rows).catch(() => {});
            }
          }
          void api
            .ListTunnels(sessionId)
            .then((ts) =>
              setTunnelsCountBySession((prev) => ({
                ...prev,
                [sessionId]: (ts || []).filter((t) => t.active).length,
              })),
            )
            .catch(() => {});
          void api.Profiles().then(setProfiles).catch(() => {});
        } catch (err) {
          if (attempt.signal.aborted) return;
          setVerifyQueue((q) => q.filter((r) => r.id !== sessionId));
          setChallengeQueue((q) => q.filter((r) => r.id !== sessionId));
          const msg = errorMessage(err);
          const ws = wsMapRef.current.get(sessionId);
          if (ws) {
            ws.close();
            wsMapRef.current.delete(sessionId);
          }
          setSessions((prev) => {
            const cur = prev[sessionId];
            if (!cur) return prev;
            const prevSteps = cur.connectSteps ?? [];
            let marked = false;
            const failedSteps: ConnectStep[] = prevSteps.map((step) => {
              if (step.status === 'active') {
                marked = true;
                return { ...step, status: 'error' as const };
              }
              return step;
            });
            if (!marked) {
              if (failedSteps.length > 0) {
                const lastIdx = failedSteps.length - 1;
                failedSteps[lastIdx] = { ...failedSteps[lastIdx], status: 'error' };
              } else {
                failedSteps.push({ stage: 'error', text: '建立连接失败', status: 'error' });
              }
            }
            return {
              ...prev,
              [sessionId]: {
                ...cur,
                state: 'closed',
                endMessage: msg,
                connectSteps: failedSteps,
              },
            };
          });
          if (sessionsRef.current[sessionId]?.hasConnected) {
            terminalBus.writeln(sessionId, `\r\n\x1b[31m连接失败：${msg}\x1b[0m`);
          }
        } finally {
          connectingRef.current.delete(sessionId);
        }
      });
    },
    [attachSessionWS],
  );

  const openSession = useCallback(
    (profile: Profile, secret = '', remember = Boolean(profile.remembered), requestedProfile = '', split?: SplitTarget) => {
      if (split) {
        const tab = tabsRef.current.find(t => t.id === split.tabId);
        const leaves = tab ? collectLeaves(tab.root) : [];
        if (!leaves.some(l => l.paneId === split.paneId)) {
          toast('原窗格已关闭');
          return;
        }
        if (leaves.length >= 6) {
          toast('单个标签最多支持 6 个分屏');
          return;
        }
      }
      const sessionId = randomId();
      const paneId = randomId();
      const tabId = split?.tabId ?? randomId();
      const rec: SessionRecord = {
        id: sessionId,
        kind: 'ssh',
        profile,
        state: 'connecting',
        startedAt: Date.now(),
        dir: '.',
        cols: 100,
        rows: 32,
        hasConnected: false,
        connectSteps: [{ stage: 'dial', text: '正在初始化连接...', status: 'active' }],
        secret,
        remember,
      };
      setSessions((prev) => ({ ...prev, [sessionId]: rec }));
      if (split) {
        setTabs(prev => prev.map(t => t.id === tabId ? {
          ...t,
          root: splitPaneNode(t.root, split.paneId, split.direction, paneId, sessionId),
          activePaneId: paneId,
        } : t));
      } else {
        setTabs(prev => [...prev, {
          id: tabId,
          title: profile.name || profile.host,
          color: profile.color,
          root: { type: 'leaf', paneId, sessionId },
          activePaneId: paneId,
          broadcast: false,
        }]);
      }
      setActiveTabId(tabId);
      startSSHConnection(sessionId, profile, secret, remember, requestedProfile);
    },
    [startSSHConnection, toast],
  );

  const resolveTargetLocalShell = useCallback((shellId?: string): LocalShellProfile => {
    const shells = localShellsRef.current;
    if (shellId) {
      const explicit = shells.find((s) => s.id === shellId);
      if (explicit) return explicit;
    }
    const pref = settingsRef.current.defaultLocalShell;
    if (pref) {
      const preferred = shells.find((s) => s.id === pref);
      if (preferred) return preferred;
    }
    return (
      shells.find((s) => s.isDefault) ??
      shells[0] ?? {
        id: shellId || 'powershell',
        name: 'PowerShell',
        command: 'powershell.exe',
        args: ['-NoLogo'],
        icon: 'powershell',
        category: 'shell',
        isDefault: true,
      }
    );
  }, []);

  const startLocalConnection = useCallback(
    (sessionId: string, shell: LocalShellProfile, cwd = '') => {
      const attempt = new AbortController();
      connectingRef.current.set(sessionId, { controller: attempt, requestedProfile: '' });
      scheduleAfterPaint(async () => {
        const ctrl = terminalBus.get(sessionId);
        ctrl?.fit();
        const cols = ctrl?.term.cols || 100;
        const rows = ctrl?.term.rows || 32;

        const writePreviewBanner = (sh: LocalShellProfile) => {
          const banner =
            sh.category === 'wsl'
              ? `\x1b[32muser@wsl\x1b[0m:\x1b[34m~\x1b[0m$ `
              : sh.id === 'cmd'
                ? `Microsoft Windows [Version 10.0]\r\n(c) Microsoft Corporation.\r\n\r\nC:\\Users\\user> `
                : sh.id === 'git-bash'
                  ? `\x1b[32muser@desktop\x1b[0m \x1b[35mMINGW64\x1b[0m \x1b[33m~\x1b[0m\r\n$ `
                  : `${sh.name}\r\n\x1b[36mPS C:\\Users\\user>\x1b[0m `;
          terminalBus.writeBytes(sessionId, textEncoderRef.current.encode(banner));
        };

        if (!hasDesktopBridge()) {
          connectingRef.current.delete(sessionId);
          if (!attempt.signal.aborted) {
            writePreviewBanner(shell);
            ctrl?.focus();
          }
          return;
        }

        const api = getApi();
        if (typeof api.StartLocalTerminal !== 'function') {
          connectingRef.current.delete(sessionId);
          if (!attempt.signal.aborted) {
            writePreviewBanner(shell);
            ctrl?.focus();
          }
          return;
        }

        try {
          let actualShell: LocalShellProfile | null = null;
          const opened = await connectIfOpen(
            attempt.signal,
            () => attachSessionWS(sessionId, attempt.signal),
            async () => {
              actualShell = await api.StartLocalTerminal!(sessionId, shell.id, cwd, cols, rows);
            },
            () => api.Disconnect(sessionId),
          );
          if (!opened) return;
          if (actualShell) {
            const resolvedShell = actualShell as LocalShellProfile;
            setSessions((prev) => {
              const cur = prev[sessionId];
              if (!cur) return prev;
              return {
                ...prev,
                [sessionId]: {
                  ...cur,
                  localShell: resolvedShell,
                  profile: {
                    ...cur.profile,
                    name: resolvedShell.name,
                    icon: resolvedShell.icon || cur.profile.icon,
                  },
                },
              };
            });
            setTabs((prev) =>
              prev.map((t) =>
                t.root.type === 'leaf' && t.root.sessionId === sessionId
                  ? { ...t, title: resolvedShell.name }
                  : t,
              ),
            );
          }
          const latestCtrl = terminalBus.get(sessionId);
          if (latestCtrl) {
            latestCtrl.fit();
            const nextSizeKey = `${latestCtrl.term.cols}x${latestCtrl.term.rows}`;
            lastSizeRef.current.set(sessionId, nextSizeKey);
            if (latestCtrl.term.cols !== cols || latestCtrl.term.rows !== rows) {
              void api.Resize(sessionId, latestCtrl.term.cols, latestCtrl.term.rows).catch(() => {});
            }
            latestCtrl.focus();
          }
        } catch (err) {
          if (attempt.signal.aborted) return;
          const msg = errorMessage(err);
          const ws = wsMapRef.current.get(sessionId);
          if (ws) {
            ws.close();
            wsMapRef.current.delete(sessionId);
          }
          setSessions((prev) => {
            const cur = prev[sessionId];
            if (!cur) return prev;
            return {
              ...prev,
              [sessionId]: {
                ...cur,
                state: 'closed',
                endMessage: msg,
              },
            };
          });
          terminalBus.writeln(sessionId, `\r\n\x1b[31m本地终端启动失败：${msg}\x1b[0m`);
          toast(msg);
        } finally {
          connectingRef.current.delete(sessionId);
        }
      });
    },
    [attachSessionWS, toast],
  );

  const openLocalTerminal = useCallback(
    (shellId?: string, split?: SplitTarget, cwd = '') => {
      if (split) {
        const tab = tabsRef.current.find((t) => t.id === split.tabId);
        const leaves = tab ? collectLeaves(tab.root) : [];
        if (!leaves.some((l) => l.paneId === split.paneId)) {
          toast('原窗格已关闭');
          return;
        }
        if (leaves.length >= 6) {
          toast('单个标签最多支持 6 个分屏');
          return;
        }
      }
      const shell = resolveTargetLocalShell(shellId);
      const sessionId = randomId();
      const paneId = randomId();
      const tabId = split?.tabId ?? randomId();
      const placeholderProfile: Profile = {
        id: '',
        name: shell.name,
        host: 'local',
        port: 0,
        user: '',
        auth: 'agent',
        icon: shell.icon || 'terminal',
      };
      const rec: SessionRecord = {
        id: sessionId,
        kind: 'local',
        localShell: shell,
        profile: placeholderProfile,
        state: 'connected',
        startedAt: Date.now(),
        dir: cwd || '',
        cols: 100,
        rows: 32,
        hasConnected: true,
      };
      setSessions((prev) => ({ ...prev, [sessionId]: rec }));
      if (split) {
        setTabs((prev) =>
          prev.map((t) =>
            t.id === tabId
              ? {
                  ...t,
                  root: splitPaneNode(t.root, split.paneId, split.direction, paneId, sessionId),
                  activePaneId: paneId,
                }
              : t,
          ),
        );
      } else {
        setTabs((prev) => [
          ...prev,
          {
            id: tabId,
            title: shell.name,
            root: { type: 'leaf', paneId, sessionId },
            activePaneId: paneId,
            broadcast: false,
          },
        ]);
      }
      setActiveTabId(tabId);
      startLocalConnection(sessionId, shell, cwd);
    },
    [resolveTargetLocalShell, startLocalConnection, toast],
  );

  const connectOrPromptProfile = useCallback(
    (profile: Profile, forceNew = false, requestedProfile = '') => {
      if (!forceNew && profile.id) {
        // Focus existing live or connecting tab for this host instead of spawning duplicates on double-click.
        for (const tab of tabsRef.current) {
          const leaves = collectLeaves(tab.root);
          const match = leaves.find((l) => {
            const s = sessionsRef.current[l.sessionId];
            if (!s || !sameSessionTarget(s.profile, profile)) return false;
            return s.state === 'connecting' || (!requestedProfile && s.state === 'connected');
          });
          if (match) {
            const attempt = connectingRef.current.get(match.sessionId);
            if (attempt && requestedProfile) attempt.requestedProfile = requestedProfile;
            setActiveTabId(tab.id);
            setTabs((prev) =>
              prev.map((t) => (t.id === tab.id ? { ...t, activePaneId: match.paneId } : t)),
            );
            setSearchOpen(false);
            return;
          }
        }
        // Anti-double-click guard before React commits the new tab state.
        const now = Date.now();
        const prev = lastConnectAtRef.current.get(profile.id) ?? 0;
        if (!requestedProfile && now - prev < 750 && (profile.auth === 'agent' || profile.remembered)) {
          return;
        }
        lastConnectAtRef.current.set(profile.id, now);
      }

      if (profile.auth === 'agent' || profile.remembered) {
        openSession(profile, '', Boolean(profile.remembered), requestedProfile);
      } else {
        setHostModal({ open: true, profile }, requestedProfile);
      }
    },
    [openSession, setHostModal],
  );

  const handleClearRecent = useCallback(async () => {
    try {
      await getApi().ClearRecentHosts();
      setProfiles((prev) => prev.map((p) => ({ ...p, lastUsedAt: 0 })));
      toast('已清空最近使用记录');
    } catch (ex) {
      toast(errorMessage(ex));
    }
  }, [toast]);

  const handleCheckUpdate = useCallback(
    async (silent = false) => {
      if (!hasDesktopBridge()) return;
      const api = getApi();
      if (typeof api.CheckForUpdate !== 'function') return;

      if (silent) {
        if (checkingUpdateRef.current) return;
        try {
          const info = await api.CheckForUpdate();
          setUpdateInfo(info);
          if (info.hasUpdate) {
            toast(`发现新版本 v${info.latestVersion}，可在「关于 Shell」中一键更新`);
          }
        } catch {
          // Silent background check failures should not surface errors or flash UI.
        }
        return;
      }

      if (checkingUpdateRef.current) return;
      checkingUpdateRef.current = true;
      setCheckingUpdate(true);
      setUpdateError('');
      const startedAt = Date.now();
      let failed = false;
      let nextInfo: UpdateInfo | null = null;
      let nextError = '';
      try {
        nextInfo = await api.CheckForUpdate();
      } catch (ex) {
        failed = true;
        nextError = errorMessage(ex);
      }

      const minMs = failed ? 480 : 720;
      const waitMs = Math.max(0, minMs - (Date.now() - startedAt));
      if (waitMs > 0) {
        await new Promise<void>((resolve) => window.setTimeout(resolve, waitMs));
      }

      try {
        if (failed) {
          setUpdateError(nextError);
        } else if (nextInfo) {
          setUpdateInfo(nextInfo);
          toast(
            nextInfo.hasUpdate
              ? `发现新版本 v${nextInfo.latestVersion}`
              : `已是最新版本 v${nextInfo.currentVersion}`,
          );
        }
      } finally {
        checkingUpdateRef.current = false;
        setCheckingUpdate(false);
      }
    },
    [toast],
  );

  const handleApplyUpdate = useCallback(async () => {
    if (!hasDesktopBridge()) return;
    const api = getApi();
    if (typeof api.DownloadAndApplyUpdate !== 'function') return;
    setUpdatingApp(true);
    setUpdateError('');
    setUpdateProgress({ phase: 'checking', downloaded: 0, total: 0, percent: 0, message: '准备下载更新…' });
    try {
      await api.DownloadAndApplyUpdate();
    } catch (ex) {
      const msg = errorMessage(ex);
      setUpdateError(msg);
      setUpdateProgress(null);
      setUpdatingApp(false);
      toast(msg);
    }
  }, [toast]);

  // Automatic background update check once after startup when enabled.
  useEffect(() => {
    if (!settingsLoaded || autoCheckedRef.current || settings.autoCheckUpdate === false || !hasDesktopBridge()) return;
    const timer = window.setTimeout(() => {
      autoCheckedRef.current = true;
      void handleCheckUpdate(true);
    }, 3200);
    return () => window.clearTimeout(timer);
  }, [settingsLoaded, settings.autoCheckUpdate, handleCheckUpdate]);

  // Subscribe to in-app update download/verify progress.
  useEffect(() => {
    if (!hasDesktopBridge()) return;
    return onDesktopEvent<[UpdateProgress]>('update:progress', (prog) => {
      if (!prog) return;
      setUpdateProgress(prog);
      if (prog.phase === 'error') {
        setUpdateError(prog.message || '更新失败');
        setUpdatingApp(false);
      }
    });
  }, []);

  // Desktop event subscriptions
  useEffect(() => {
    if (!hasDesktopBridge()) return;
    const offs = [
      onDesktopEvent<[string, string]>('ssh:data', (id, b64) => {
        terminalBus.writeBytes(id, decodeBase64(b64));
      }),
      onDesktopEvent<[string, string]>('ssh:end', (id, message) => {
        const ws = wsMapRef.current.get(id);
        if (ws) {
          ws.close();
          wsMapRef.current.delete(id);
        }
        setVerifyQueue((q) => q.filter((r) => r.id !== id));
        setChallengeQueue((q) => q.filter((r) => r.id !== id));
        const curSnap = sessionsRef.current[id];
        const cleanMsg = String(message || '连接已关闭').replace(/[\x00-\x1f\x7f-\x9f]/g, ' ');
        const isCleanExit = !message || cleanMsg === '连接已关闭' || cleanMsg.includes('exit status 0');
        const isAuthOrTrustFail = /认证|密码|私钥|指纹|口令|permission denied|unable to authenticate/i.test(cleanMsg);
        const nextAttempt = (curSnap?.reconnectAttempt ?? 0) + 1;
        const shouldAutoReconnect = Boolean(
          curSnap &&
            curSnap.kind !== 'local' &&
            curSnap.state !== 'closed' &&
            curSnap.hasConnected &&
            settingsRef.current.autoReconnect !== false &&
            !isCleanExit &&
            !isAuthOrTrustFail &&
            nextAttempt <= 3,
        );
        const delayMs = nextAttempt === 1 ? 2000 : nextAttempt === 2 ? 4000 : 8000;
        const reconnectAt = shouldAutoReconnect ? Date.now() + delayMs : undefined;

        setSessions((prev) => {
          const cur = prev[id];
          if (!cur || cur.state === 'closed') return prev;
          return {
            ...prev,
            [id]: {
              ...cur,
              state: 'closed',
              endMessage: cleanMsg,
              reconnectAt,
              reconnectAttempt: shouldAutoReconnect ? nextAttempt : cur.reconnectAttempt,
            },
          };
        });
        terminalBus.writeln(id, `\r\n\x1b[90m[${cleanMsg}]\x1b[0m`);
        if (shouldAutoReconnect) {
          const existing = reconnectTimersRef.current.get(id);
          if (existing !== undefined) window.clearTimeout(existing);
          const timer = window.setTimeout(() => {
            reconnectTimersRef.current.delete(id);
            const liveCur = sessionsRef.current[id];
            if (!liveCur || liveCur.state !== 'closed' || !liveCur.reconnectAt) return;
            for (const tab of tabsRef.current) {
              const leaf = collectLeaves(tab.root).find((l) => l.sessionId === id);
              if (leaf) {
                reconnectPaneRef.current(tab.id, leaf.paneId, id, nextAttempt);
                return;
              }
            }
          }, delayMs);
          reconnectTimersRef.current.set(id, timer);
        }
      }),
      onDesktopEvent<[string, string, string]>('ssh:status', (id, stage, message) => {
        const validStages: ConnectStage[] = ['jump', 'dial', 'auth', 'shell', 'error'];
        const normStage: ConnectStage = (validStages as string[]).includes(stage)
          ? (stage as ConnectStage)
          : 'dial';
        const rawText = String(message || '').trim();
        const text =
          settingsRef.current.hideHostIp && normStage === 'dial' && rawText !== '正在经跳板机连接目标主机...'
            ? '正在建立网络连接...'
            : rawText || '正在建立连接...';
        setSessions((prev) => {
          const cur = prev[id];
          if (!cur || cur.state !== 'connecting') return prev;
          const prevSteps = cur.connectSteps ?? [];
          const isInitialPlaceholder =
            prevSteps.length === 1 &&
            prevSteps[0].stage === 'dial' &&
            prevSteps[0].status === 'active' &&
            (prevSteps[0].text === '正在初始化连接...' || prevSteps[0].text === '正在重新连接...');
          let nextSteps: ConnectStep[];
          if (isInitialPlaceholder) {
            nextSteps = [{ stage: normStage, text, status: 'active' }];
          } else if (prevSteps.length > 0 && prevSteps[prevSteps.length - 1].stage === normStage) {
            nextSteps = prevSteps.map((step, idx) =>
              idx === prevSteps.length - 1
                ? { ...step, text, status: 'active' as const }
                : step.status === 'active'
                  ? { ...step, status: 'done' as const }
                  : step,
            );
          } else {
            nextSteps = [
              ...prevSteps.map((step) => (step.status === 'active' ? { ...step, status: 'done' as const } : step)),
              { stage: normStage, text, status: 'active' as const },
            ];
          }
          return { ...prev, [id]: { ...cur, connectSteps: nextSteps } };
        });
      }),
      onDesktopEvent<[string, number]>('ssh:latency', (id, ms) => {
        setSessions((prev) => {
          const cur = prev[id];
          if (!cur) return prev;
          return { ...prev, [id]: { ...cur, latencyMs: ms } };
        });
      }),
      onDesktopEvent<[HostVerifyRequest]>('ssh:verify', (req) => {
        setVerifyQueue((q) => [...q, req]);
      }),
      onDesktopEvent<[ChallengeRequest]>('ssh:challenge', (req) => {
        setChallengeQueue((q) => [...q, req]);
      }),
      onDesktopEvent<[AgentApprovalRequest]>('agent:approval', (req) => {
        setApprovalQueue((q) => [...q, req]);
      }),
      onDesktopEvent<[string, boolean?]>('agent:connect_profile', async (profileId, fromMcp) => {
        let target = profilesRef.current.find((p) => p.id === profileId);
        if (!target) {
          const saved = await getApi().Profiles().catch(() => []);
          target = saved?.find((p) => p.id === profileId);
        }
        if (target) {
          connectOrPromptProfile(target, false, fromMcp ? profileId : '');
        } else if (fromMcp) {
          void getApi().CancelVisibleConnect(profileId).catch(() => {});
        }
      }),
      onDesktopEvent<[string]>('agent:focus_session', (sessionId) => {
        for (const tab of tabsRef.current) {
          const leaf = collectLeaves(tab.root).find((l) => l.sessionId === sessionId);
          if (leaf) {
            setActiveTabId(tab.id);
            setTabs((prev) =>
              prev.map((t) => (t.id === tab.id ? { ...t, activePaneId: leaf.paneId } : t)),
            );
            setSearchOpen(false);
            break;
          }
        }
      }),
      onDesktopEvent<[TransferProgress]>('sftp:progress', (prog) => {
        setTransfers((prev) => {
          const idx = prev.findIndex((p) => p.transferId === prog.transferId);
          if (idx >= 0) {
            const next = [...prev];
            next[idx] = prog;
            return next;
          }
          return [...prev.slice(-15), prog];
        });
      }),
      onDesktopEvent<[string]>('shell:notice', (msg) => {
        if (msg) toast(msg);
      }),
      onDesktopEvent<[AppSettings]>('shell:settings_updated', (next) => {
        if (next) {
          const merged = { ...DEFAULT_SETTINGS, ...next };
          setSettings(merged);
          writeCachedAppearance(merged.theme, merged.terminalTheme);
        }
      }),
      onDesktopEvent<[AgentStatusView]>('agent:status', (st) => {
        if (st) setAgentStatus(st);
      }),
      onDesktopEvent<[string]>('shell:tray_action', (action) => {
        if (action === 'new_host') {
          setHostModal({ open: true, profile: null });
        } else if (action === 'open_settings') {
          setSettingsModal({ open: true, tab: 'appearance' });
        } else if (action === 'open_agent_settings') {
          setSettingsModal({ open: true, tab: 'agent' });
        }
      }),
    ];
    return () => offs.forEach((off) => off());
  }, [connectOrPromptProfile, toast]);

  // Immediately probe live SSH sessions when OS regains network connectivity (e.g. wake from sleep / Wi-Fi switch).
  useEffect(() => {
    if (!hasDesktopBridge()) return;
    const onOnline = () => {
      const api = getApi();
      if (typeof api.ProbeSession !== 'function') return;
      for (const sess of Object.values(sessionsRef.current)) {
        if (sess.state === 'connected' && sess.kind !== 'local') {
          void api.ProbeSession(sess.id).catch(() => {});
        }
      }
    };
    window.addEventListener('online', onOnline);
    return () => window.removeEventListener('online', onOnline);
  }, []);

  // Active tab & session derivation
  const activeTab = useMemo(() => tabs.find((t) => t.id === activeTabId) ?? null, [tabs, activeTabId]);
  const activeLeaves = useMemo(() => (activeTab ? collectLeaves(activeTab.root) : []), [activeTab]);
  const activeLeaf = useMemo(
    () => activeLeaves.find((l) => l.paneId === activeTab?.activePaneId) ?? activeLeaves[0] ?? null,
    [activeLeaves, activeTab?.activePaneId],
  );
  const activeSession = activeLeaf ? sessions[activeLeaf.sessionId] : undefined;
  const activeProfile = useMemo(
    () => profiles.find((p) => p.id === activeSession?.profile.id),
    [profiles, activeSession?.profile.id],
  );
  const jumpHostName = useMemo(() => {
    const jid = activeSession?.profile.jumpHostId;
    if (!jid) return undefined;
    return profiles.find((p) => p.id === jid)?.name;
  }, [profiles, activeSession?.profile.jumpHostId]);

  const activeOsHint = useMemo(() => {
    if (!activeSession) return undefined;
    if (activeSession.kind === 'local') {
      const sh = activeSession.localShell;
      if (sh?.category === 'wsl') {
        const distro = sh.name.replace(/^WSL\s*·\s*/i, '').trim();
        return distro ? `Linux (WSL ${distro})` : 'Linux (WSL)';
      }
      return sh?.name ? `Windows (${sh.name})` : 'Windows (PowerShell)';
    }
    return statsBySession[activeSession.id]?.os;
  }, [activeSession, statsBySession]);

  // Automatically close SSH-only drawers when switching to a local terminal session.
  useEffect(() => {
    if (activeSession?.kind === 'local' && (drawer === 'sftp' || drawer === 'tunnels')) {
      setDrawer('none');
    }
  }, [activeSession?.id, activeSession?.kind, drawer]);

  // Non-overlapping, zero-sleep live server telemetry polling + automatic OS distro icon detection
  useEffect(() => {
    if (
      !hasDesktopBridge() ||
      !settings.showTelemetry ||
      !activeSession ||
      activeSession.kind === 'local' ||
      activeSession.state !== 'connected'
    ) {
      return;
    }
    const sid = activeSession.id;
    const profId = activeSession.profile.id;
    let canceled = false;
    let timerId: number | null = null;
    let prevNet: { rx: number; tx: number; ts: number } | null = null;
    let osSynced = false;

    const scheduleNext = (delayMs: number) => {
      if (canceled) return;
      timerId = window.setTimeout(() => void poll(), delayMs);
    };

    const poll = async () => {
      if (canceled) return;
      if (document.hidden) {
        scheduleNext(6000);
        return;
      }
      try {
        const st = await getApi().FetchStats(sid);
        if (canceled) return;
        const now = Date.now();
        if (prevNet && st.netRxBytes >= prevNet.rx && st.netTxBytes >= prevNet.tx) {
          const dt = Math.max(0.5, (now - prevNet.ts) / 1000);
          const rxPerSec = Math.round((st.netRxBytes - prevNet.rx) / dt);
          const txPerSec = Math.round((st.netTxBytes - prevNet.tx) / dt);
          setNetRateBySession((prev) => ({
            ...prev,
            [sid]: { rxPerSec, txPerSec },
          }));
        }
        prevNet = { rx: st.netRxBytes, tx: st.netTxBytes, ts: now };
        setStatsBySession((prev) => ({ ...prev, [sid]: st }));

        // Auto-detect authentic OS icon (Ubuntu/Debian/CentOS/Rocky/Alpine/Arch/Fedora...) when profile icon is Auto
        if (!osSynced && st.os && profId) {
          osSynced = true;
          const detected = detectOSIconFromText(st.os);
          const curProf = profilesRef.current.find((p) => p.id === profId);
          if (detected && curProf && !curProf.icon) {
            const updated = { ...curProf, icon: detected };
            setProfiles((prev) => prev.map((p) => (p.id === profId ? updated : p)));
            void getApi().SaveProfile(updated).catch(() => {});
          }
        }
      } catch {
        // Ignore if host doesn't support /proc or busy
      }
      scheduleNext(6000);
    };

    scheduleNext(2200);
    return () => {
      canceled = true;
      if (timerId !== null) window.clearTimeout(timerId);
    };
  }, [activeSession?.id, activeSession?.kind, activeSession?.state, settings.showTelemetry]);

  useEffect(() => {
    const t = window.setTimeout(() => terminalBus.fitAll(), 210);
    return () => window.clearTimeout(t);
  }, [sidebarOpen, drawer, activeTabId]);

  const handleQuickConnect = useCallback((target: { user: string; host: string; port: number }, split?: SplitTarget) => {
    setHostModal({
      open: true,
      split,
      profile: {
        id: '',
        name: target.host,
        host: target.host,
        port: target.port,
        user: target.user,
        auth: 'password',
      },
    });
  }, [setHostModal]);

  const writeSessionInput = useCallback((sessionId: string, data: string | Uint8Array) => {
    const s = sessionsRef.current[sessionId];
    if (!s || s.state === 'closed') return;
    const bytes = typeof data === 'string' ? textEncoderRef.current.encode(data) : data;
    // Fast path: send raw binary frame directly over loopback WebSocket (<0.1ms, zero Base64, zero Promise queue!).
    const ws = wsMapRef.current.get(sessionId);
    if (ws && ws.readyState === WebSocket.OPEN) {
      for (let start = 0; start < bytes.length; start += 32768) {
        ws.send(bytes.subarray(start, start + 32768));
      }
      return;
    }
    if (s.state !== 'connected' || !hasDesktopBridge()) return;
    // Fallback path (mock / non-WS environments)
    let chain = writeChainsRef.current.get(sessionId) ?? Promise.resolve();
    for (let start = 0; start < bytes.length; start += 32768) {
      const b64 = encodeBase64(bytes.subarray(start, start + 32768));
      chain = chain.then(async () => {
        if (sessionsRef.current[sessionId]?.state === 'connected') {
          await getApi().Input(sessionId, b64).catch(() => {});
        }
      });
    }
    writeChainsRef.current.set(sessionId, chain);
  }, []);

  const handlePaneInput = useCallback(
    (tabId: string, _paneId: string, sessionId: string, data: string | Uint8Array) => {
      const tab = tabsRef.current.find((t) => t.id === tabId);
      if (tab?.broadcast) {
        for (const leaf of collectLeaves(tab.root)) {
          writeSessionInput(leaf.sessionId, data);
        }
      } else {
        writeSessionInput(sessionId, data);
      }
    },
    [writeSessionInput],
  );

  const handlePaneResize = useCallback((sessionId: string, cols: number, rows: number) => {
    const sizeKey = `${cols}x${rows}`;
    setSessions((prev) => {
      const cur = prev[sessionId];
      if (!cur || (cur.cols === cols && cur.rows === rows)) return prev;
      return { ...prev, [sessionId]: { ...cur, cols, rows } };
    });
    if (hasDesktopBridge() && sessionsRef.current[sessionId]?.state === 'connected' && lastSizeRef.current.get(sessionId) !== sizeKey) {
      lastSizeRef.current.set(sessionId, sizeKey);
      void getApi().Resize(sessionId, cols, rows).catch(() => {});
    }
  }, []);

  const handlePaneCwdChange = useCallback((sessionId: string, dir: string) => {
    setSessions((prev) => {
      const cur = prev[sessionId];
      if (!cur || cur.dir === dir) return prev;
      return { ...prev, [sessionId]: { ...cur, dir } };
    });
  }, []);

  const closeSessionBackend = useCallback((sessionId: string) => {
    const rTimer = reconnectTimersRef.current.get(sessionId);
    if (rTimer !== undefined) {
      window.clearTimeout(rTimer);
      reconnectTimersRef.current.delete(sessionId);
    }
    const attempt = connectingRef.current.get(sessionId);
    attempt?.controller.abort();
    if (attempt?.requestedProfile) void getApi().CancelVisibleConnect(attempt.requestedProfile).catch(() => {});
    connectingRef.current.delete(sessionId);
    const ws = wsMapRef.current.get(sessionId);
    if (ws) {
      ws.close();
      wsMapRef.current.delete(sessionId);
    }
    if (hasDesktopBridge()) {
      void getApi().Disconnect(sessionId).catch(() => {});
    }
    writeChainsRef.current.delete(sessionId);
    lastSizeRef.current.delete(sessionId);
    setVerifyQueue((q) => q.filter((r) => r.id !== sessionId));
    setChallengeQueue((q) => q.filter((r) => r.id !== sessionId));
    setSessions((prev) => {
      const next = { ...prev };
      delete next[sessionId];
      return next;
    });
  }, []);

  const handleCloseTab = useCallback(
    (tabId: string) => {
      const tab = tabsRef.current.find((t) => t.id === tabId);
      if (!tab) return;
      for (const leaf of collectLeaves(tab.root)) {
        closeSessionBackend(leaf.sessionId);
      }
      setTabs((prev) => {
        const idx = prev.findIndex((t) => t.id === tabId);
        const next = prev.filter((t) => t.id !== tabId);
        if (activeTabId === tabId) {
          const fallback = next[Math.min(idx, next.length - 1)]?.id ?? null;
          setActiveTabId(fallback);
          if (!fallback && (drawer === 'sftp' || drawer === 'tunnels')) {
            setDrawer('none');
          }
        }
        return next;
      });
    },
    [activeTabId, closeSessionBackend, drawer],
  );

  const handleCloseOtherTabs = useCallback(
    (keepTabId: string) => {
      for (const tab of tabsRef.current) {
        if (tab.id !== keepTabId) {
          for (const leaf of collectLeaves(tab.root)) {
            closeSessionBackend(leaf.sessionId);
          }
        }
      }
      setTabs((prev) => prev.filter((t) => t.id === keepTabId));
      setActiveTabId(keepTabId);
    },
    [closeSessionBackend],
  );

  const handleCloseAllTabs = useCallback(() => {
    for (const tab of tabsRef.current) {
      for (const leaf of collectLeaves(tab.root)) {
        closeSessionBackend(leaf.sessionId);
      }
    }
    setTabs([]);
    setActiveTabId(null);
    setSearchOpen(false);
    setDrawer((cur) => (cur === 'sftp' || cur === 'tunnels' ? 'none' : cur));
  }, [closeSessionBackend]);

  const handleCloneTab = useCallback(
    (tabId: string) => {
      const tab = tabsRef.current.find((t) => t.id === tabId);
      if (!tab) return;
      const leaves = collectLeaves(tab.root);
      const srcLeaf = leaves.find((l) => l.paneId === tab.activePaneId) ?? leaves[0];
      const src = srcLeaf ? sessionsRef.current[srcLeaf.sessionId] : undefined;
      if (!src) return;
      if (src.kind === 'local') {
        openLocalTerminal(src.localShell?.id, undefined, src.dir && src.dir !== '.' ? src.dir : '');
        return;
      }
      openSession(src.profile, src.secret ?? '', Boolean(src.remember ?? src.profile.remembered));
    },
    [openLocalTerminal, openSession],
  );

  const handleSplitPane = useCallback(
    (tabId: string, paneId: string, direction: 'horizontal' | 'vertical') => {
      const tab = tabsRef.current.find((t) => t.id === tabId);
      if (!tab) return;
      const leaves = collectLeaves(tab.root);
      if (leaves.length >= 6) {
        toast('单个标签最多支持 6 个分屏');
        return;
      }
      const sourceLeaf = leaves.find((l) => l.paneId === paneId);
      const sourceSession = sourceLeaf ? sessionsRef.current[sourceLeaf.sessionId] : undefined;
      if (!sourceSession) return;

      setPaletteOpen(false);
      setSplitPicker({
        target: { tabId, paneId, direction },
        source: sourceSession.profile,
        sourceLocalShell: sourceSession.kind === 'local' ? sourceSession.localShell : undefined,
      });
    },
    [toast],
  );

  const chooseSplitProfile = (profile: Profile) => {
    if (!splitPicker) return;
    const {target} = splitPicker;
    const tab = tabsRef.current.find(t => t.id === target.tabId);
    const leaf = tab && collectLeaves(tab.root).find(l => l.paneId === target.paneId);
    const source = leaf && sessionsRef.current[leaf.sessionId];
    if (!source) return;
    if (profile.auth === 'agent' || profile.remembered) {
      openSession(profile, '', Boolean(profile.remembered), '', target);
    } else if ((profile === source.profile || sameSessionTarget(profile, source.profile)) && source.secret) {
      openSession(profile, source.secret, false, '', target);
    } else {
      setHostModal({open: true, profile, split: target});
    }
  };

  const handleClosePane = useCallback(
    (tabId: string, paneId: string) => {
      const tab = tabsRef.current.find((t) => t.id === tabId);
      if (!tab) return;
      const leaves = collectLeaves(tab.root);
      if (leaves.length <= 1) {
        handleCloseTab(tabId);
        return;
      }
      const target = leaves.find((l) => l.paneId === paneId);
      if (target) closeSessionBackend(target.sessionId);
      const nextRoot = removePaneNode(tab.root, paneId);
      if (!nextRoot) {
        handleCloseTab(tabId);
        return;
      }
      const nextLeaves = collectLeaves(nextRoot);
      setTabs((prev) =>
        prev.map((t) =>
          t.id === tabId
            ? {
                ...t,
                root: nextRoot,
                activePaneId: t.activePaneId === paneId ? nextLeaves[0].paneId : t.activePaneId,
                broadcast: nextLeaves.length > 1 ? t.broadcast : false,
              }
            : t,
        ),
      );
    },
    [closeSessionBackend, handleCloseTab],
  );

  const handleDetachPane = useCallback((tabId: string, paneId: string) => {
    const tab = tabsRef.current.find((t) => t.id === tabId);
    if (!tab) return;
    const leaves = collectLeaves(tab.root);
    if (leaves.length <= 1) return;
    const target = leaves.find((l) => l.paneId === paneId);
    if (!target) return;
    const sess = sessionsRef.current[target.sessionId];
    const title = sess ? sess.profile.name || sess.profile.host : tab.title;
    const color = sess?.profile.color;
    const newTabId = randomId();
    setTabs((prev) => detachPaneToTab(prev, tabId, paneId, newTabId, title, color));
    setActiveTabId(newTabId);
    setSearchOpen(false);
    requestAnimationFrame(() => terminalBus.get(target.sessionId)?.focus());
  }, []);

  const handleReconnectPane = useCallback(
    (tabId: string, paneId: string, oldSessionId: string, attemptCount = 0) => {
      const old = sessionsRef.current[oldSessionId];
      if (!old) return;
      if (old.kind === 'local') {
        const shell = old.localShell ?? resolveTargetLocalShell();
        const cwd = old.dir && old.dir !== '.' ? old.dir : '';
        const newSessionId = randomId();
        closeSessionBackend(oldSessionId);
        const rec: SessionRecord = {
          id: newSessionId,
          kind: 'local',
          localShell: shell,
          profile: {
            id: '',
            name: shell.name,
            host: 'local',
            port: 0,
            user: '',
            auth: 'agent',
            icon: shell.icon || 'terminal',
          },
          state: 'connected',
          startedAt: Date.now(),
          dir: cwd,
          cols: old.cols,
          rows: old.rows,
          hasConnected: true,
        };
        setSessions((prev) => ({ ...prev, [newSessionId]: rec }));
        setTabs((prev) =>
          prev.map((t) => (t.id === tabId ? { ...t, root: replacePaneSession(t.root, paneId, newSessionId) } : t)),
        );
        startLocalConnection(newSessionId, shell, cwd);
        return;
      }
      const latestProfile = profilesRef.current.find((p) => p.id === old.profile.id) ?? old.profile;
      const secret = reconnectSecret(old, latestProfile);
      if (secret === null) {
        cancelAutoReconnect(oldSessionId);
        if (attemptCount === 0) {
          setHostModal({ open: true, profile: latestProfile });
        }
        return;
      }
      const remember = Boolean(latestProfile.remembered);
      const newSessionId = randomId();
      closeSessionBackend(oldSessionId);
      const rec: SessionRecord = {
        id: newSessionId,
        kind: 'ssh',
        profile: latestProfile,
        state: 'connecting',
        startedAt: Date.now(),
        dir: sameSessionTarget(old.profile, latestProfile) ? old.dir : '.',
        cols: old.cols,
        rows: old.rows,
        endMessage: undefined,
        hasConnected: false,
        reconnectAttempt: attemptCount,
        connectSteps: [
          {
            stage: 'dial',
            text: attemptCount > 0 ? `正在自动重连 (${attemptCount}/3)...` : '正在重新连接...',
            status: 'active',
          },
        ],
        secret,
        remember,
      };
      setSessions((prev) => ({ ...prev, [newSessionId]: rec }));
      setTabs((prev) =>
        prev.map((t) => (t.id === tabId ? { ...t, root: replacePaneSession(t.root, paneId, newSessionId) } : t)),
      );
      startSSHConnection(newSessionId, latestProfile, secret, remember);
    },
    [cancelAutoReconnect, closeSessionBackend, resolveTargetLocalShell, setHostModal, startLocalConnection, startSSHConnection],
  );
  reconnectPaneRef.current = handleReconnectPane;

  const sendCommandToActive = useCallback(
    (cmd: string, execute: boolean) => {
      if (!activeTab || !activeLeaf) return;
      const activeSess = sessionsRef.current[activeLeaf.sessionId];
      const eol = activeSess?.kind === 'local' ? '\r' : '\n';
      const payload = execute ? cmd + eol : cmd;
      handlePaneInput(activeTab.id, activeLeaf.paneId, activeLeaf.sessionId, payload);
      terminalBus.get(activeLeaf.sessionId)?.focus();
    },
    [activeLeaf, activeTab, handlePaneInput],
  );

  const updateSettings = useCallback(async (changes: Partial<AppSettings>) => {
    settingsRevisionRef.current += 1;
    const touchesAppearance = 'theme' in changes || 'terminalTheme' in changes;
    if (touchesAppearance) {
      const nextTheme = changes.theme ?? settingsRef.current.theme;
      const nextTerm = changes.terminalTheme ?? settingsRef.current.terminalTheme;
      writeCachedAppearance(nextTheme, nextTerm);
    }
    setSettings((current) => ({ ...current, ...changes }));
    if (!hasDesktopBridge()) return;
    settingsWrite.current = settingsWrite.current.then(async () => {
      const saved = await getApi().UpdatePreferences(changes);
      setSettings((current) => {
        const keys = Object.keys(changes) as (keyof AppSettings)[];
        const confirmed = keys.filter((key) => current[key] === changes[key]);
        const next = { ...current, ...Object.fromEntries(confirmed.map((key) => [key, saved[key]])) };
        if (touchesAppearance) {
          writeCachedAppearance(next.theme, next.terminalTheme);
        }
        return next;
      });
    }).catch((error) => toast(`保存设置失败：${errorMessage(error)}`));
    await settingsWrite.current;
  }, [toast]);

  const handleFontZoom = useCallback(
    (delta: number) => {
      const nextSize = delta === 0 ? DEFAULT_SETTINGS.fontSize : Math.max(10, Math.min(28, settings.fontSize + delta));
      void updateSettings({ fontSize: nextSize });
      toast(`终端字号 ${nextSize}px`);
    },
    [settings, toast, updateSettings],
  );

  // Global keyboard shortcuts (including Ctrl+1..9 & Ctrl+Tab & Ctrl+W)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      if (!mod) return;
      const key = e.key.toLowerCase();
      if (!e.shiftKey && key === 'k') {
        e.preventDefault();
        setPaletteOpen((v) => !v);
      } else if (!e.shiftKey && key === 'n') {
        e.preventDefault();
        setHostModal({ open: true, profile: null });
      } else if (!e.shiftKey && key === 'b') {
        e.preventDefault();
        setSidebarOpen((v) => !v);
      } else if (e.key === ',') {
        e.preventDefault();
        setSettingsModal({ open: true, tab: 'appearance' });
      } else if (e.shiftKey && key === 'a') {
        e.preventDefault();
        setDrawer((d) => (d === 'ai' ? 'none' : 'ai'));
      } else if (e.shiftKey && key === 't') {
        e.preventDefault();
        openLocalTerminal();
      } else if (e.shiftKey && key === 'f' && activeSession) {
        e.preventDefault();
        setSearchOpen(true);
      } else if (!e.shiftKey && e.key >= '1' && e.key <= '9') {
        const idx = Number(e.key) - 1;
        if (tabsRef.current[idx]) {
          e.preventDefault();
          setActiveTabId(tabsRef.current[idx].id);
        }
      } else if (e.key === 'Tab' && tabsRef.current.length > 1) {
        e.preventDefault();
        const list = tabsRef.current;
        const curIdx = list.findIndex((t) => t.id === activeTabId);
        const nextIdx = (curIdx + (e.shiftKey ? -1 : 1) + list.length) % list.length;
        setActiveTabId(list[nextIdx].id);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [activeSession, activeTabId, openLocalTerminal]);

  useEffect(() => {
    if (!confirmDelete) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        setConfirmDelete(null);
      }
    };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  }, [confirmDelete]);

  const existingGroups = useMemo(
    () => [...new Set(profiles.map((p) => p.group?.trim()).filter((g): g is string => Boolean(g)))],
    [profiles],
  );

  const handleSaveHost = async (
    p: Profile,
    secret: string,
    remember: boolean,
    keepExisting: boolean,
    andConnect: boolean,
  ) => {
    const generation = hostModalGeneration.current;
    const api = getApi();
    const stored =
      typeof api.SaveProfileWithSecret === 'function'
        ? await api.SaveProfileWithSecret(p, secret, remember, keepExisting)
        : await api.SaveProfile(p);
    if (hostModalGeneration.current !== generation) {
      await reloadData();
      return;
    }
    let requestedProfile = '';
    if (andConnect && promptForVisibleConnect.current === stored.id && hostModal.profile && sameSessionTarget(stored, hostModal.profile)) {
      requestedProfile = stored.id;
      promptForVisibleConnect.current = '';
    }
    setHostModal({ open: false, profile: null });
    if (andConnect) {
      openSession(stored, secret, remember, requestedProfile, hostModal.split);
    } else {
      toast(`已保存主机「${stored.name}」`);
    }
    await reloadData();
  };

  const handleDeleteHost = async (p: Profile) => {
    await getApi().DeleteProfile(p.id);
    setConfirmDelete(null);
    setHostModal({ open: false, profile: null });
    await reloadData();
    toast(`已删除主机「${p.name}」`);
  };

  const handleBatchDeleteHosts = async (ids: string[]) => {
    if (!ids.length) return;
    await getApi().DeleteProfiles(ids);
    await reloadData();
    toast(`已批量删除 ${ids.length} 台主机`);
  };

  const handleBatchGroupHosts = async (ids: string[], group: string) => {
    if (!ids.length) return;
    const clean = group.trim();
    const api = getApi();
    if (typeof api.SetProfilesGroup === 'function') {
      await api.SetProfilesGroup(ids, clean);
    } else {
      const idSet = new Set(ids);
      for (const p of profilesRef.current) {
        if (idSet.has(p.id)) {
          await api.SaveProfile({ ...p, group: clean });
        }
      }
    }
    await reloadData();
    toast(clean ? `已将 ${ids.length} 台主机移至分组「${clean}」` : `已将 ${ids.length} 台主机移出分组`);
  };

  const paneDrag = usePaneDrag(tabs, (source, target, edge) => {
    const splitId = randomId();
    setTabs(prev => movePane(prev, source, target, edge, splitId));
    setActiveTabId(target.tabId);
    const from = tabs.find(tab => tab.id === source.tabId);
    const moved = from && collectLeaves(from.root).find(leaf => leaf.paneId === source.paneId);
    if (moved) requestAnimationFrame(() => terminalBus.get(moved.sessionId)?.term.focus());
  });

  const handleSaveProfileTunnels = async (p: Profile, tunnels: TunnelRule[]) => {
    await getApi().SaveProfile({ ...p, tunnels });
    await reloadData();
  };

  const toggleDrawer = (mode: DrawerMode) => {
    if (activeSession?.kind === 'local' && (mode === 'sftp' || mode === 'tunnels')) {
      return;
    }
    setDrawer((prev) => (prev === mode ? 'none' : mode));
  };

  const activeVerify = verifyQueue[0] ?? null;
  const activeChallenge = !activeVerify ? challengeQueue[0] ?? null : null;
  const activeApproval = !activeVerify && !activeChallenge ? approvalQueue[0] ?? null : null;

  return (
    <TooltipProvider>
    <div className="app-frame">
      <Titlebar
        paneDrag={paneDrag}
        sidebarOpen={sidebarOpen}
        onToggleSidebar={() => setSidebarOpen((v) => !v)}
        profiles={profiles}
        tabs={tabs}
        activeTabId={activeTabId}
        sessions={sessions}
        drawer={drawer}
        hideHostIp={Boolean(settings.hideHostIp)}
        onSelectTab={(id) => {
          setActiveTabId(id);
          setSearchOpen(false);
        }}
        onCloseTab={handleCloseTab}
        onCloseOtherTabs={handleCloseOtherTabs}
        onCloseAllTabs={handleCloseAllTabs}
        onCloneTab={handleCloneTab}
        onDetachPane={handleDetachPane}
        onNewConnection={() => setHostModal({ open: true, profile: null })}
        onSplit={(dir, tabId) => {
          const tab = tabId ? tabs.find(t => t.id === tabId) : activeTab;
          if (tab) handleSplitPane(tab.id, tab.activePaneId, dir);
        }}
        onToggleBroadcast={() =>
          activeTab &&
          setTabs((prev) => prev.map((t) => (t.id === activeTab.id ? { ...t, broadcast: !t.broadcast } : t)))
        }
        onOpenSearch={() => setSearchOpen(true)}
        onToggleDrawer={toggleDrawer}
        onOpenPalette={() => setPaletteOpen(true)}
      />

      <div className="app-body">
        <Sidebar
          open={sidebarOpen}
          tabs={tabs}
          activeTabId={activeTabId}
          onDragPane={paneDrag.start}
          profiles={profiles}
          groupOrder={settings.groupOrder ?? []}
          onReorderGroups={(next) => void updateSettings({ groupOrder: next })}
          sessions={sessions}
          activeProfileId={activeSession?.profile.id}
          hasUpdate={Boolean(updateInfo?.hasUpdate)}
          onConnect={(p) => connectOrPromptProfile(p, false)}
          onConnectNewSession={(p) => connectOrPromptProfile(p, true)}
          onQuickConnect={handleQuickConnect}
          onAddHost={() => setHostModal({ open: true, profile: null })}
          onEditHost={(p) => setHostModal({ open: true, profile: p })}
          onDuplicateHost={async (p) => {
            await getApi().DuplicateProfile(p.id);
            await reloadData();
            toast(`已复制「${p.name}」`);
          }}
          onDeleteHost={(p) => setConfirmDelete(p)}
          onBatchDeleteHosts={handleBatchDeleteHosts}
          onBatchGroupHosts={handleBatchGroupHosts}
          onTogglePin={async (p) => {
            await getApi().SaveProfile({ ...p, pinned: !p.pinned });
            await reloadData();
          }}
          onOpenImportSSH={() => setImportOpen(true)}
          onOpenExportHosts={setExportIds}
          onOpenSettings={(tab = 'appearance') => setSettingsModal({ open: true, tab })}
          isDark={effectiveDark}
          hideHostIp={Boolean(settings.hideHostIp)}
          onToggleHideIp={() => void updateSettings({ hideHostIp: !settings.hideHostIp })}
          onToggleTheme={() => {
            const nextDark = !effectiveDark;
            void updateSettings({
              theme: nextDark ? 'dark' : 'light',
              terminalTheme: syncTerminalThemeForAppearance(settings.terminalTheme, nextDark),
            });
          }}
          onToast={toast}
        />

        <main className="workspace-col">
          {activeTab?.broadcast && (
            <div className="broadcast-banner">
              <Radio size={13} />
              <span>广播输入已开启：您的按键将同步发送到当前标签的 {activeLeaves.length} 个分屏</span>
              <button
                type="button"
                className="btn btn-sm"
                style={{ height: 20, padding: '0 8px', fontSize: 11 }}
                onClick={() =>
                  setTabs((prev) => prev.map((t) => (t.id === activeTab.id ? { ...t, broadcast: false } : t)))
                }
              >
                关闭广播
              </button>
            </div>
          )}

          <div className="stage-wrap">
            <div className="terminal-stage">
              {tabs.length === 0 ? (
                <WelcomeView
                  profiles={profiles}
                  hideHostIp={Boolean(settings.hideHostIp)}
                  onConnect={(p) => connectOrPromptProfile(p, false)}
                  onQuickConnect={handleQuickConnect}
                  onNewConnection={() => setHostModal({ open: true, profile: null })}
                  onImportSSH={() => setImportOpen(true)}
                  onClearRecent={() => void handleClearRecent()}
                  onOpenKeys={() => setSettingsModal({ open: true, tab: 'keys' })}
                  onOpenSettings={() => setSettingsModal({ open: true, tab: 'appearance' })}
                  onOpenAI={() => setDrawer('ai')}
                  onOpenPalette={() => setPaletteOpen(true)}
                />
              ) : (
                <SplitWorkspace
                  tabs={tabs}
                  activeTabId={activeTabId}
                  paneDrag={paneDrag}
                  profiles={profiles}
                  sessions={sessions}
                  settings={workspaceSettings}
                  onFocusPane={(tabId, paneId) =>
                    setTabs(prev => prev.map(t => t.id === tabId && t.activePaneId !== paneId ? {...t, activePaneId: paneId} : t))
                  }
                  onRatioChange={(tabId, splitId, ratio) =>
                    setTabs(prev => prev.map(t => t.id === tabId ? {...t, root: updateSplitRatio(t.root, splitId, ratio)} : t))
                  }
                  onPaneInput={handlePaneInput}
                  onPaneResize={handlePaneResize}
                  onPaneCwdChange={handlePaneCwdChange}
                  onReconnectPane={handleReconnectPane}
                  onCancelAutoReconnect={cancelAutoReconnect}
                  onEditProfile={(profile) => {
                    const latest = profilesRef.current.find((p) => p.id === profile.id) ?? profile;
                    setHostModal({ open: true, profile: latest });
                  }}
                  onClosePane={handleClosePane}
                  onDetachPane={handleDetachPane}
                  onSplitPane={handleSplitPane}
                  onOpenSearch={() => setSearchOpen(true)}
                  onOpenSFTP={() => setDrawer('sftp')}
                  onExplainSelection={(text) => {
                    setExplainText(text);
                    setDrawer('ai');
                  }}
                  onExportLog={async (sess, content) => {
                    try {
                      const dest = await getApi().SaveTranscript(`${sess.profile.name}.log`, content);
                      if (dest) toast('终端日志已导出');
                    } catch (ex) {
                      toast(errorMessage(ex));
                    }
                  }}
                  onFontZoom={handleFontZoom}
                  onToast={toast}
                />
              )}

              {searchOpen && activeSession && (
                <TerminalSearchBar sessionId={activeSession.id} onClose={() => setSearchOpen(false)} />
              )}
            </div>

            {drawer === 'sftp' && activeSession?.state === 'connected' && activeSession.kind !== 'local' && (
              <SftpDrawer
                key={activeSession.id}
                session={activeSession}
                transfers={transfers}
                onClose={() => setDrawer('none')}
                onUpdateDir={(dir) => handlePaneCwdChange(activeSession.id, dir)}
                onCdInTerminal={(dir) => {
                  const quoted = `'${dir.replace(/'/g, `'\\''`)}'`;
                  sendCommandToActive(`cd ${quoted}`, true);
                }}
                onEditFile={(remotePath) => setEditorTarget({ sessionId: activeSession.id, remotePath })}
                onToast={toast}
              />
            )}

            {drawer === 'tunnels' && activeSession?.state === 'connected' && activeSession.kind !== 'local' && (
              <TunnelsDrawer
                key={activeSession.id}
                session={activeSession}
                profile={activeProfile}
                onClose={() => setDrawer('none')}
                onSaveProfileTunnels={handleSaveProfileTunnels}
                onTunnelsChanged={(count) =>
                  setTunnelsCountBySession((prev) => ({ ...prev, [activeSession.id]: count }))
                }
                onToast={toast}
              />
            )}

            {drawer === 'snippets' && (
              <SnippetsDrawer
                snippets={snippets}
                hasActiveSession={activeSession?.state === 'connected'}
                onClose={() => setDrawer('none')}
                onInsertCommand={sendCommandToActive}
                onSaveSnippet={async (s) => {
                  await getApi().SaveSnippet(s);
                  setSnippets(await getApi().Snippets());
                  toast('命令片段已保存');
                }}
                onDeleteSnippet={async (id) => {
                  await getApi().DeleteSnippet(id);
                  setSnippets(await getApi().Snippets());
                  toast('已删除命令片段');
                }}
              />
            )}

            {drawer === 'ai' && (
              <AIDrawer
                configVersion={aiConfigVersion}
                activeSessionId={activeSession?.state === 'connected' ? activeSession.id : undefined}
                activeSessionName={
                  activeSession?.state === 'connected'
                    ? activeSession.profile.name || activeSession.profile.host
                    : undefined
                }
                isLocalSession={activeSession?.state === 'connected' && activeSession.kind === 'local'}
                hasActiveSession={activeSession?.state === 'connected'}
                permissionMode={agentStatus?.policy?.mode}
                osHint={activeOsHint}
                initialExplainText={explainText}
                onClearInitialExplain={() => setExplainText('')}
                onClose={() => setDrawer('none')}
                onOpenAISettings={() => setSettingsModal({ open: true, tab: 'ai' })}
                onOpenAgentSecurity={() => setSettingsModal({ open: true, tab: 'agent' })}
                onInsertCommand={sendCommandToActive}
                onToast={toast}
              />
            )}
          </div>

          <StatusBar
            session={activeSession}
            jumpHostName={jumpHostName}
            stats={activeSession ? statsBySession[activeSession.id] : undefined}
            netRate={activeSession ? netRateBySession[activeSession.id] : undefined}
            showTelemetry={settings.showTelemetry}
            hideHostIp={Boolean(settings.hideHostIp)}
            activeTunnelsCount={activeSession ? tunnelsCountBySession[activeSession.id] ?? 0 : 0}
            agentStatus={agentStatus}
            onOpenTunnels={() => setDrawer('tunnels')}
            onOpenAgentSettings={() => setSettingsModal({ open: true, tab: 'agent' })}
          />
        </main>
      </div>

      {hostModal.open && (
        <HostModal
          key={hostModal.profile?.id || 'new-host'}
          profile={hostModal.profile}
          allProfiles={profiles}
          existingGroups={existingGroups}
          onClose={() => setHostModal({ open: false, profile: null })}
          onSave={handleSaveHost}
          onDelete={(p) => setConfirmDelete(p)}
        />
      )}

      {(paletteOpen || splitPicker) && (
        <CommandPalette
          key={splitPicker ? `split-${splitPicker.target.paneId}` : 'commands'}
          splitSource={splitPicker?.source}
          splitSourceLocalShell={splitPicker?.sourceLocalShell}
          profiles={profiles}
          localShells={localShells}
          defaultLocalShellId={settings.defaultLocalShell}
          snippets={snippets}
          hasActiveSession={activeSession?.state === 'connected'}
          isActiveLocalSession={activeSession?.kind === 'local'}
          isDark={effectiveDark}
          hideHostIp={Boolean(settings.hideHostIp)}
          onClose={() => { setPaletteOpen(false); setSplitPicker(null); }}
          onConnectProfile={splitPicker ? chooseSplitProfile : connectOrPromptProfile}
          onQuickConnect={(target) => handleQuickConnect(target, splitPicker?.target)}
          onOpenLocalTerminal={(shellId) => openLocalTerminal(shellId, splitPicker?.target)}
          onRunSnippet={(s) => sendCommandToActive(s.command, false)}
          onNewHost={() => setHostModal({ open: true, profile: null, split: splitPicker?.target })}
          onImportSSH={() => setImportOpen(true)}
          onSplit={(dir) => activeTab && activeLeaf && handleSplitPane(activeTab.id, activeLeaf.paneId, dir)}
          onToggleSFTP={() => toggleDrawer('sftp')}
          onToggleTunnels={() => toggleDrawer('tunnels')}
          onToggleAI={() => toggleDrawer('ai')}
          onToggleTheme={() => {
            const nextDark = !effectiveDark;
            void updateSettings({
              theme: nextDark ? 'dark' : 'light',
              terminalTheme: syncTerminalThemeForAppearance(settings.terminalTheme, nextDark),
            });
          }}
          onOpenSettings={(tab = 'appearance') => setSettingsModal({ open: true, tab })}
        />
      )}

      {importOpen && (
        <SSHConfigImportModal
          onClose={() => setImportOpen(false)}
          onImported={(count) => {
            void reloadData();
            toast(`成功导入 ${count} 台主机`);
          }}
        />
      )}

      {exportIds !== null && (
        <HostExportModal
          profiles={profiles}
          initialIds={exportIds}
          hideHostIp={Boolean(settings.hideHostIp)}
          onClose={() => setExportIds(null)}
        />
      )}

      {settingsModal.open && (
        <SettingsModal
          initialTab={settingsModal.tab}
          settings={settings}
          localShells={localShells}
          activeSessionId={activeSession?.state === 'connected' && activeSession.kind !== 'local' ? activeSession.id : undefined}
          updateInfo={updateInfo}
          updateProgress={updateProgress}
          checkingUpdate={checkingUpdate}
          updatingApp={updatingApp}
          updateError={updateError}
          onCheckUpdate={() => handleCheckUpdate(false)}
          onApplyUpdate={handleApplyUpdate}
          onUpdateSettings={updateSettings}
          onAgentStatusChanged={setAgentStatus}
          onOpenImportSSH={() => setImportOpen(true)}
          onClose={() => {
            setSettingsModal({ open: false, tab: 'appearance' });
            setAiConfigVersion((v) => v + 1);
          }}
          onProfilesImported={reloadData}
          onToast={toast}
        />
      )}

      {editorTarget && (
        <RemoteEditorModal
          key={JSON.stringify([editorTarget.sessionId, editorTarget.remotePath])}
          sessionId={editorTarget.sessionId}
          remotePath={editorTarget.remotePath}
          isDark={effectiveDark}
          onClose={() => setEditorTarget(null)}
          onSaved={() => {}}
          onToast={toast}
        />
      )}

      {confirmDelete && (
        <div id="confirmDeleteDialog" className="modal-backdrop" onClick={() => setConfirmDelete(null)}>
          <div className="modal-sheet" role="dialog" aria-modal="true" style={{ maxWidth: 400 }} onClick={(e) => e.stopPropagation()}>
            <div className="modal-header">
              <h2>删除主机</h2>
              <button type="button" className="icon-btn" aria-label="关闭" onClick={() => setConfirmDelete(null)}>
                <X size={15} />
              </button>
            </div>
            <div className="modal-body">
              <p style={{ margin: 0 }}>
                确定删除主机「<b>{confirmDelete.name}</b>」及其保存的加密凭据？
              </p>
            </div>
            <div className="modal-footer">
              <button type="button" className="btn" onClick={() => setConfirmDelete(null)}>
                取消
              </button>
              <button type="button" className="btn btn-danger-solid" onClick={() => void handleDeleteHost(confirmDelete)}>
                确认删除
              </button>
            </div>
          </div>
        </div>
      )}

      {activeVerify && (
        <VerifyHostDialog
          request={activeVerify}
          onDecision={(accept) => {
            const cur = activeVerify;
            setVerifyQueue((q) => q.slice(1));
            void getApi().ConfirmHost(cur.id, accept).catch(() => {});
          }}
        />
      )}

      {activeChallenge && (
        <ChallengeDialog
          request={activeChallenge}
          onSubmit={(answers, accept) => {
            const cur = activeChallenge;
            setChallengeQueue((q) => q.slice(1));
            void getApi().AnswerChallenge(cur.id, answers, accept).catch(() => {});
          }}
        />
      )}

      {activeApproval && (
        <AgentApprovalDialog
          request={activeApproval}
          onResolve={(decision) => {
            const cur = activeApproval;
            setApprovalQueue((q) => q.slice(1));
            void getApi().ResolveAgentApproval(cur.id, decision).catch(() => {});
          }}
        />
      )}

      {toasts.length > 0 && (
        <div className="toast-stack" aria-live="polite">
          {toasts.map((t) => (
            <div key={t.id} className="toast-pill">
              {t.text}
            </div>
          ))}
        </div>
      )}
    </div>
    </TooltipProvider>
  );
};
