import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Radio } from 'lucide-react';
import type {
  AgentApprovalRequest,
  AgentStatusView,
  AppSettings,
  ChallengeRequest,
  DrawerMode,
  HostVerifyRequest,
  Profile,
  ServerStats,
  SessionRecord,
  Snippet,
  TransferProgress,
  TunnelRule,
  WorkspaceTab,
} from './types/shell';
import { errorMessage, getApi, hasDesktopBridge, onDesktopEvent } from './lib/bridge';
import { decodeBase64, encodeBase64, terminalBus } from './lib/terminalBus';
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
import { StatusBar } from './components/StatusBar';
import { SftpDrawer } from './components/SftpDrawer';
import { TunnelsDrawer } from './components/TunnelsDrawer';
import { SnippetsDrawer } from './components/SnippetsDrawer';
import { AIDrawer } from './components/AIDrawer';
import { HostModal } from './components/HostModal';
import { CommandPalette } from './components/CommandPalette';
import { SSHConfigImportModal } from './components/SSHConfigImportModal';
import { SettingsModal, type SettingsTab } from './components/SettingsModal';
import { RemoteEditorModal } from './components/RemoteEditorModal';
import { TerminalSearchBar } from './components/TerminalSearchBar';
import { ChallengeDialog, VerifyHostDialog } from './components/SecurityDialogs';
import { AgentApprovalDialog } from './components/AgentApprovalDialog';
import { detectOSIconFromText } from './components/HostIcon';

const DEFAULT_SETTINGS: AppSettings = {
  theme: 'light',
  terminalTheme: 'apple-dark',
  fontFamily: '"SF Mono", "JetBrains Mono", "Cascadia Mono", Consolas, monospace',
  fontSize: 14,
  lineHeight: 1.28,
  cursorStyle: 'bar',
  cursorBlink: true,
  scrollback: 10000,
  copyOnSelect: false,
  rightClickPaste: false,
  showTelemetry: true,
  webglEnabled: false,
  defaultKeepAlive: 30,
  confirmOnClose: true,
  closeToTray: true,
};

export const App: React.FC = () => {
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [snippets, setSnippets] = useState<Snippet[]>([]);
  const [settings, setSettings] = useState<AppSettings>(DEFAULT_SETTINGS);
  const [agentStatus, setAgentStatus] = useState<AgentStatusView | null>(null);
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [tabs, setTabs] = useState<WorkspaceTab[]>([]);
  const [activeTabId, setActiveTabId] = useState<string | null>(null);
  const [sessions, setSessions] = useState<Record<string, SessionRecord>>({});
  const [drawer, setDrawer] = useState<DrawerMode>('none');
  const [searchOpen, setSearchOpen] = useState(false);

  // Modals
  const [hostModal, setHostModal] = useState<{ open: boolean; profile: Profile | null }>({
    open: false,
    profile: null,
  });
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
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

  const writeChainsRef = useRef<Map<string, Promise<void>>>(new Map());
  const wsMapRef = useRef<Map<string, WebSocket>>(new Map());
  const lastSizeRef = useRef<Map<string, string>>(new Map());
  const textEncoderRef = useRef(new TextEncoder());
  const sessionsRef = useRef(sessions);
  sessionsRef.current = sessions;
  const tabsRef = useRef(tabs);
  tabsRef.current = tabs;
  const profilesRef = useRef(profiles);
  profilesRef.current = profiles;

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

  const isDark = settings.theme === 'dark' || (settings.theme === 'system' && sysDark);
  useEffect(() => {
    document.documentElement.dataset.theme = isDark ? 'dark' : 'light';
  }, [isDark]);

  // Initial load from Go backend
  const reloadData = useCallback(async () => {
    if (!hasDesktopBridge()) return;
    const api = getApi();
    const [profs, snips, cfg, agSt] = await Promise.all([
      api.Profiles().catch(() => []),
      api.Snippets().catch(() => []),
      api.GetSettings().catch(() => DEFAULT_SETTINGS),
      api.GetAgentStatus().catch(() => null),
    ]);
    setProfiles(profs || []);
    setSnippets(snips || []);
    setSettings({ ...DEFAULT_SETTINGS, ...cfg });
    if (agSt) setAgentStatus(agSt);
  }, []);

  useEffect(() => {
    void reloadData();
  }, [reloadData]);

  // Attach a low-latency binary WebSocket stream for a session (falls back cleanly to Wails IPC in mock mode).
  const attachSessionWS = useCallback(async (sessionId: string) => {
    try {
      const api = getApi();
      if (typeof api.TermWSURL !== 'function') return;
      const url = await api.TermWSURL(sessionId);
      if (!url) return;
      const existing = wsMapRef.current.get(sessionId);
      if (existing) {
        existing.close();
        wsMapRef.current.delete(sessionId);
      }
      await new Promise<void>((resolve) => {
        const ws = new WebSocket(url);
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
          wsMapRef.current.set(sessionId, ws);
          done();
        };
        ws.onmessage = (ev) => {
          if (ev.data instanceof ArrayBuffer) {
            terminalBus.writeBytes(sessionId, new Uint8Array(ev.data));
          }
        };
        ws.onerror = () => {
          window.clearTimeout(timer);
          done();
        };
        ws.onclose = () => {
          if (wsMapRef.current.get(sessionId) === ws) {
            wsMapRef.current.delete(sessionId);
          }
        };
      });
    } catch {
      // Fall back to Wails EventsEmit/Input seamlessly
    }
  }, []);

  // Start an SSH connection for a newly allocated session ID
  const startSSHConnection = useCallback(
    (sessionId: string, profile: Profile, secret: string, remember: boolean) => {
      const api = getApi();
      const ctrl = terminalBus.get(sessionId);
      const cols = ctrl?.term.cols || 100;
      const rows = ctrl?.term.rows || 32;

      void attachSessionWS(sessionId).then(() =>
        api
          .Connect(sessionId, profile, secret, remember, cols, rows)
          .then(() => {
            setSessions((prev) => {
              const cur = prev[sessionId];
              if (!cur || cur.state !== 'connecting') return prev;
              return { ...prev, [sessionId]: { ...cur, state: 'connected', startedAt: Date.now() } };
            });
            const latestCtrl = terminalBus.get(sessionId);
            if (latestCtrl) {
              latestCtrl.fit();
              latestCtrl.focus();
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
          })
          .catch((err) => {
            const msg = errorMessage(err);
            const ws = wsMapRef.current.get(sessionId);
            if (ws) {
              ws.close();
              wsMapRef.current.delete(sessionId);
            }
            setSessions((prev) => {
              const cur = prev[sessionId];
              if (!cur) return prev;
              return { ...prev, [sessionId]: { ...cur, state: 'closed', endMessage: msg } };
            });
            terminalBus.writeln(sessionId, `\r\n\x1b[31m连接失败：${msg}\x1b[0m`);
          }),
      );
    },
    [attachSessionWS],
  );

  const openSessionInNewTab = useCallback(
    (profile: Profile, secret = '', remember = Boolean(profile.remembered)) => {
      const sessionId = randomId();
      const paneId = randomId();
      const tabId = randomId();
      const rec: SessionRecord = {
        id: sessionId,
        profile,
        state: 'connecting',
        startedAt: Date.now(),
        dir: '.',
        cols: 100,
        rows: 32,
        secret,
        remember,
      };
      setSessions((prev) => ({ ...prev, [sessionId]: rec }));
      setTabs((prev) => [
        ...prev,
        {
          id: tabId,
          title: profile.name || profile.host,
          color: profile.color,
          root: { type: 'leaf', paneId, sessionId },
          activePaneId: paneId,
          broadcast: false,
        },
      ]);
      setActiveTabId(tabId);
      requestAnimationFrame(() => startSSHConnection(sessionId, profile, secret, remember));
    },
    [startSSHConnection],
  );

  const connectOrPromptProfile = useCallback(
    (profile: Profile) => {
      if (profile.auth === 'agent' || profile.remembered) {
        openSessionInNewTab(profile, '', Boolean(profile.remembered));
      } else {
        setHostModal({ open: true, profile });
      }
    },
    [openSessionInNewTab],
  );

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
        setSessions((prev) => {
          const cur = prev[id];
          if (!cur || cur.state === 'closed') return prev;
          return { ...prev, [id]: { ...cur, state: 'closed', endMessage: message || '连接已关闭' } };
        });
        const cleanMsg = String(message || '连接已关闭').replace(/[\x00-\x1f\x7f-\x9f]/g, ' ');
        terminalBus.writeln(id, `\r\n\x1b[90m[${cleanMsg}]\x1b[0m`);
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
      onDesktopEvent<[string]>('agent:connect_profile', (profileId) => {
        const target = profilesRef.current.find((p) => p.id === profileId);
        if (target) {
          connectOrPromptProfile(target);
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
        if (next) setSettings({ ...DEFAULT_SETTINGS, ...next });
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

  // Non-overlapping, zero-sleep live server telemetry polling + automatic OS distro icon detection
  useEffect(() => {
    if (!hasDesktopBridge() || !settings.showTelemetry || !activeSession || activeSession.state !== 'connected') {
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
          setNetRateBySession((prev) => ({
            ...prev,
            [sid]: {
              rxPerSec: Math.round((st.netRxBytes - prevNet!.rx) / dt),
              txPerSec: Math.round((st.netTxBytes - prevNet!.tx) / dt),
            },
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
  }, [activeSession?.id, activeSession?.state, settings.showTelemetry]);

  useEffect(() => {
    const t = window.setTimeout(() => terminalBus.fitAll(), 210);
    return () => window.clearTimeout(t);
  }, [sidebarOpen, drawer, activeTabId]);

  const handleQuickConnect = useCallback((target: { user: string; host: string; port: number }) => {
    setHostModal({
      open: true,
      profile: {
        id: '',
        name: target.host,
        host: target.host,
        port: target.port,
        user: target.user,
        auth: 'password',
      },
    });
  }, []);

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
    if (s.state !== 'connected') return;
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
    if (sessionsRef.current[sessionId]?.state === 'connected' && lastSizeRef.current.get(sessionId) !== sizeKey) {
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
    const ws = wsMapRef.current.get(sessionId);
    if (ws) {
      ws.close();
      wsMapRef.current.delete(sessionId);
    }
    void getApi().Disconnect(sessionId).catch(() => {});
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

  const handleCloneTab = useCallback(
    (tabId: string) => {
      const tab = tabsRef.current.find((t) => t.id === tabId);
      if (!tab) return;
      const leaves = collectLeaves(tab.root);
      const srcLeaf = leaves.find((l) => l.paneId === tab.activePaneId) ?? leaves[0];
      const src = srcLeaf ? sessionsRef.current[srcLeaf.sessionId] : undefined;
      if (!src) return;
      openSessionInNewTab(src.profile, src.secret ?? '', Boolean(src.remember ?? src.profile.remembered));
    },
    [openSessionInNewTab],
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
      const sourceLeaf = leaves.find((l) => l.paneId === paneId) ?? leaves[0];
      const sourceSession = sourceLeaf ? sessionsRef.current[sourceLeaf.sessionId] : undefined;
      if (!sourceSession) return;

      const newSessionId = randomId();
      const newPaneId = randomId();
      const rec: SessionRecord = {
        id: newSessionId,
        profile: sourceSession.profile,
        state: 'connecting',
        startedAt: Date.now(),
        dir: '.',
        cols: 80,
        rows: 24,
        secret: sourceSession.secret,
        remember: sourceSession.remember,
      };
      setSessions((prev) => ({ ...prev, [newSessionId]: rec }));
      setTabs((prev) =>
        prev.map((t) =>
          t.id === tabId
            ? {
                ...t,
                root: splitPaneNode(t.root, paneId, direction, newPaneId, newSessionId),
                activePaneId: newPaneId,
              }
            : t,
        ),
      );
      requestAnimationFrame(() =>
        startSSHConnection(
          newSessionId,
          sourceSession.profile,
          sourceSession.secret ?? '',
          Boolean(sourceSession.remember ?? sourceSession.profile.remembered),
        ),
      );
    },
    [startSSHConnection, toast],
  );

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

  const handleReconnectPane = useCallback(
    (tabId: string, paneId: string, oldSessionId: string) => {
      const old = sessionsRef.current[oldSessionId];
      if (!old) return;
      const latestProfile = profiles.find((p) => p.id === old.profile.id) ?? old.profile;
      const newSessionId = randomId();
      closeSessionBackend(oldSessionId);
      const rec: SessionRecord = {
        id: newSessionId,
        profile: latestProfile,
        state: 'connecting',
        startedAt: Date.now(),
        dir: old.dir,
        cols: old.cols,
        rows: old.rows,
        secret: old.secret,
        remember: Boolean(latestProfile.remembered || old.remember),
      };
      setSessions((prev) => ({ ...prev, [newSessionId]: rec }));
      setTabs((prev) =>
        prev.map((t) => (t.id === tabId ? { ...t, root: replacePaneSession(t.root, paneId, newSessionId) } : t)),
      );
      requestAnimationFrame(() =>
        startSSHConnection(
          newSessionId,
          latestProfile,
          old.secret ?? '',
          Boolean(latestProfile.remembered || old.remember),
        ),
      );
    },
    [closeSessionBackend, profiles, startSSHConnection],
  );

  const sendCommandToActive = useCallback(
    (cmd: string, execute: boolean) => {
      if (!activeTab || !activeLeaf) return;
      const payload = execute ? cmd + '\n' : cmd;
      handlePaneInput(activeTab.id, activeLeaf.paneId, activeLeaf.sessionId, payload);
      terminalBus.get(activeLeaf.sessionId)?.focus();
    },
    [activeLeaf, activeTab, handlePaneInput],
  );

  const updateSettings = useCallback(async (next: AppSettings) => {
    setSettings(next);
    if (hasDesktopBridge()) {
      const saved = await getApi().SaveSettings(next);
      setSettings(saved);
    }
  }, []);

  const handleFontZoom = useCallback(
    (delta: number) => {
      const nextSize = delta === 0 ? 14 : Math.max(10, Math.min(28, settings.fontSize + delta));
      void updateSettings({ ...settings, fontSize: nextSize });
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
  }, [activeSession, activeTabId]);

  const existingGroups = useMemo(
    () => [...new Set(profiles.map((p) => p.group?.trim()).filter((g): g is string => Boolean(g)))],
    [profiles],
  );

  const handleSaveHost = async (p: Profile, secret: string, remember: boolean, andConnect: boolean) => {
    const api = getApi();
    const stored = await api.SaveProfile(p);
    if (stored.remembered && !remember) {
      await api.ForgetSecret(stored.id);
      stored.remembered = false;
    }
    setHostModal({ open: false, profile: null });
    await reloadData();
    if (andConnect) {
      openSessionInNewTab(stored, secret, remember);
    } else {
      toast(`已保存主机「${stored.name}」`);
    }
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

  const handleSaveProfileTunnels = async (p: Profile, tunnels: TunnelRule[]) => {
    await getApi().SaveProfile({ ...p, tunnels });
    await reloadData();
  };

  const toggleDrawer = (mode: DrawerMode) => {
    setDrawer((prev) => (prev === mode ? 'none' : mode));
  };

  const activeVerify = verifyQueue[0] ?? null;
  const activeChallenge = !activeVerify ? challengeQueue[0] ?? null : null;
  const activeApproval = !activeVerify && !activeChallenge ? approvalQueue[0] ?? null : null;

  return (
    <div className="app-frame">
      <Titlebar
        sidebarOpen={sidebarOpen}
        onToggleSidebar={() => setSidebarOpen((v) => !v)}
        tabs={tabs}
        activeTabId={activeTabId}
        sessions={sessions}
        drawer={drawer}
        onSelectTab={(id) => {
          setActiveTabId(id);
          setSearchOpen(false);
        }}
        onCloseTab={handleCloseTab}
        onCloseOtherTabs={handleCloseOtherTabs}
        onCloneTab={handleCloneTab}
        onNewConnection={() => setHostModal({ open: true, profile: null })}
        onSplit={(dir) => activeTab && activeLeaf && handleSplitPane(activeTab.id, activeLeaf.paneId, dir)}
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
          profiles={profiles}
          sessions={sessions}
          activeProfileId={activeSession?.profile.id}
          onConnect={connectOrPromptProfile}
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
          onTogglePin={async (p) => {
            await getApi().SaveProfile({ ...p, pinned: !p.pinned });
            await reloadData();
          }}
          onOpenImportSSH={() => setImportOpen(true)}
          onOpenSettings={(tab = 'appearance') => setSettingsModal({ open: true, tab })}
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
                  onConnect={connectOrPromptProfile}
                  onQuickConnect={handleQuickConnect}
                  onNewConnection={() => setHostModal({ open: true, profile: null })}
                  onImportSSH={() => setImportOpen(true)}
                  onOpenKeys={() => setSettingsModal({ open: true, tab: 'keys' })}
                  onOpenSettings={() => setSettingsModal({ open: true, tab: 'appearance' })}
                  onOpenAI={() => setDrawer('ai')}
                  onOpenPalette={() => setPaletteOpen(true)}
                />
              ) : (
                tabs.map((tab) => {
                  const visible = tab.id === activeTabId;
                  const leaves = collectLeaves(tab.root);
                  return (
                    <div
                      key={tab.id}
                      style={{
                        display: visible ? 'flex' : 'none',
                        width: '100%',
                        height: '100%',
                        position: 'relative',
                      }}
                    >
                      <SplitWorkspace
                        node={tab.root}
                        activePaneId={tab.activePaneId}
                        multiPane={leaves.length > 1}
                        sessions={sessions}
                        settings={settings}
                        onFocusPane={(paneId) =>
                          setTabs((prev) => prev.map((t) => (t.id === tab.id ? { ...t, activePaneId: paneId } : t)))
                        }
                        onRatioChange={(splitId, ratio) =>
                          setTabs((prev) =>
                            prev.map((t) => (t.id === tab.id ? { ...t, root: updateSplitRatio(t.root, splitId, ratio) } : t)),
                          )
                        }
                        onPaneInput={(paneId, sid, data) => handlePaneInput(tab.id, paneId, sid, data)}
                        onPaneResize={handlePaneResize}
                        onPaneCwdChange={handlePaneCwdChange}
                        onReconnectPane={(paneId, sid) => handleReconnectPane(tab.id, paneId, sid)}
                        onClosePane={(paneId) => handleClosePane(tab.id, paneId)}
                        onSplitPane={(paneId, dir) => handleSplitPane(tab.id, paneId, dir)}
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
                    </div>
                  );
                })
              )}

              {searchOpen && activeSession && (
                <TerminalSearchBar sessionId={activeSession.id} onClose={() => setSearchOpen(false)} />
              )}
            </div>

            {drawer === 'sftp' && activeSession?.state === 'connected' && (
              <SftpDrawer
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

            {drawer === 'tunnels' && activeSession?.state === 'connected' && (
              <TunnelsDrawer
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
                hasActiveSession={activeSession?.state === 'connected'}
                osHint={activeSession ? statsBySession[activeSession.id]?.os : undefined}
                initialExplainText={explainText}
                onClearInitialExplain={() => setExplainText('')}
                onClose={() => setDrawer('none')}
                onOpenAISettings={() => setSettingsModal({ open: true, tab: 'ai' })}
                onOpenAgentSecurity={() => setSettingsModal({ open: true, tab: 'agent' })}
                onInsertCommand={sendCommandToActive}
                onSaveAsSnippet={async (cmd, desc) => {
                  await getApi().SaveSnippet({
                    id: '',
                    name: desc.slice(0, 32) || 'AI 生成命令',
                    category: 'AI',
                    command: cmd,
                    description: desc,
                  });
                  setSnippets(await getApi().Snippets());
                  toast('已保存到快捷片段');
                }}
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
            activeTunnelsCount={activeSession ? tunnelsCountBySession[activeSession.id] ?? 0 : 0}
            agentStatus={agentStatus}
            onOpenTunnels={() => setDrawer('tunnels')}
            onOpenAgentSettings={() => setSettingsModal({ open: true, tab: 'agent' })}
          />
        </main>
      </div>

      {hostModal.open && (
        <HostModal
          profile={hostModal.profile}
          allProfiles={profiles}
          existingGroups={existingGroups}
          onClose={() => setHostModal({ open: false, profile: null })}
          onSave={handleSaveHost}
          onDelete={(p) => setConfirmDelete(p)}
        />
      )}

      {paletteOpen && (
        <CommandPalette
          profiles={profiles}
          snippets={snippets}
          hasActiveSession={activeSession?.state === 'connected'}
          isDark={isDark}
          onClose={() => setPaletteOpen(false)}
          onConnectProfile={connectOrPromptProfile}
          onQuickConnect={handleQuickConnect}
          onRunSnippet={(s) => sendCommandToActive(s.command, false)}
          onNewHost={() => setHostModal({ open: true, profile: null })}
          onImportSSH={() => setImportOpen(true)}
          onSplit={(dir) => activeTab && activeLeaf && handleSplitPane(activeTab.id, activeLeaf.paneId, dir)}
          onToggleSFTP={() => toggleDrawer('sftp')}
          onToggleTunnels={() => toggleDrawer('tunnels')}
          onToggleAI={() => toggleDrawer('ai')}
          onToggleTheme={() => void updateSettings({ ...settings, theme: isDark ? 'light' : 'dark' })}
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

      {settingsModal.open && (
        <SettingsModal
          initialTab={settingsModal.tab}
          settings={settings}
          activeSessionId={activeSession?.state === 'connected' ? activeSession.id : undefined}
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
          sessionId={editorTarget.sessionId}
          remotePath={editorTarget.remotePath}
          onClose={() => setEditorTarget(null)}
          onSaved={() => {}}
          onToast={toast}
        />
      )}

      {confirmDelete && (
        <div className="modal-backdrop" onClick={() => setConfirmDelete(null)}>
          <div className="modal-sheet" style={{ maxWidth: 400 }} onClick={(e) => e.stopPropagation()}>
            <div className="modal-header">
              <h2>删除主机</h2>
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
              <button type="button" className="btn btn-danger" onClick={() => void handleDeleteHost(confirmDelete)}>
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
  );
};
