import React, { useEffect, useMemo, useState } from 'react';
import {
  Archive,
  Bot,
  Check,
  Copy,
  Download,
  ExternalLink,
  FolderOpen,
  Info,
  KeyRound,
  Loader2,
  Palette,
  Plus,
  RefreshCw,
  Search,
  Send,
  Shield,
  Sparkles,
  Trash2,
  X,
} from 'lucide-react';
import type {
  AgentStatusView,
  AIConfigView,
  AIProviderMode,
  AppInfoView,
  AppSettings,
  DetectedAICLI,
  KnownHostEntry,
  LocalKeyInfo,
  SystemProxyInfo,
  UpdateInfo,
  UpdateProgress,
} from '../types/shell';
import { syncTerminalThemeForAppearance, TERMINAL_THEMES } from '../lib/themes';
import { copyToClipboard, errorMessage, getApi, openExternalURL } from '../lib/bridge';
import { AgentSecurityTab } from './AgentSecurityTab';

export type SettingsTab = 'appearance' | 'ssh' | 'keys' | 'ai' | 'agent' | 'backup' | 'about';

interface SettingsModalProps {
  initialTab: SettingsTab;
  settings: AppSettings;
  activeSessionId?: string;
  updateInfo?: UpdateInfo | null;
  updateProgress?: UpdateProgress | null;
  checkingUpdate?: boolean;
  updatingApp?: boolean;
  updateError?: string;
  onCheckUpdate?: () => Promise<void>;
  onApplyUpdate?: () => Promise<void>;
  onUpdateSettings: (next: AppSettings) => Promise<void>;
  onAgentStatusChanged: (st: AgentStatusView) => void;
  onOpenImportSSH: () => void;
  onClose: () => void;
  onProfilesImported: () => Promise<void>;
  onToast: (msg: string) => void;
}

const AI_PRESETS = [
  { id: 'claude', label: 'Claude', sub: 'api.anthropic.com', endpoint: 'https://api.anthropic.com/v1', model: 'claude-sonnet-4-6' },
  { id: 'chatgpt', label: 'ChatGPT', sub: 'api.openai.com', endpoint: 'https://api.openai.com/v1', model: 'gpt-5-mini' },
  { id: 'gemini', label: 'Gemini', sub: 'generativelanguage.googleapis.com', endpoint: 'https://generativelanguage.googleapis.com/v1beta/openai', model: 'gemini-2.5-flash' },
  { id: 'deepseek', label: 'DeepSeek', sub: 'api.deepseek.com', endpoint: 'https://api.deepseek.com/v1', model: 'deepseek-chat' },
];

const AI_CLI_PRESETS = [
  { id: 'claude', label: 'Claude Code', command: 'claude', modelHint: '留空使用默认，如 sonnet / opus' },
  { id: 'codex', label: 'Codex CLI', command: 'codex', modelHint: '留空使用默认，如 o3 / gpt-5' },
  { id: 'agy', label: 'Antigravity CLI', command: 'agy', modelHint: '留空使用默认模型' },
  { id: 'custom', label: '自定义 CLI', command: '', modelHint: '可选模型参数' },
];

const FONT_OPTIONS = [
  { label: '系统推荐等宽', value: '"SF Mono", "JetBrains Mono", "Cascadia Mono", Consolas, monospace' },
  { label: 'Cascadia Mono', value: '"Cascadia Mono", Consolas, monospace' },
  { label: 'JetBrains Mono', value: '"JetBrains Mono", "Cascadia Mono", Consolas, monospace' },
  { label: 'Fira Code', value: '"Fira Code", "Cascadia Mono", Consolas, monospace' },
  { label: 'Consolas', value: 'Consolas, monospace' },
];

const SHORTCUTS: { label: string; keys: string[] }[] = [
  { label: '全局命令面板', keys: ['Ctrl', 'K'] },
  { label: '新建 SSH 主机', keys: ['Ctrl', 'N'] },
  { label: '切换主机侧栏', keys: ['Ctrl', 'B'] },
  { label: '打开偏好设置', keys: ['Ctrl', ','] },
  { label: '终端内搜索', keys: ['Ctrl', 'Shift', 'F'] },
  { label: '打开 AI 助手', keys: ['Ctrl', 'Shift', 'A'] },
  { label: '终端复制 / 粘贴', keys: ['Ctrl', 'Shift', 'C / V'] },
  { label: '调整 / 重置字号', keys: ['Ctrl', '+ / - / 0'] },
];

export const SettingsModal: React.FC<SettingsModalProps> = ({
  initialTab,
  settings,
  activeSessionId,
  updateInfo,
  updateProgress,
  checkingUpdate = false,
  updatingApp = false,
  updateError = '',
  onCheckUpdate,
  onApplyUpdate,
  onUpdateSettings,
  onAgentStatusChanged,
  onOpenImportSSH,
  onClose,
  onProfilesImported,
  onToast,
}) => {
  const [tab, setTab] = useState<SettingsTab>(initialTab);
  const [knownHosts, setKnownHosts] = useState<KnownHostEntry[]>([]);
  const [khQuery, setKhQuery] = useState('');
  const [selectedLines, setSelectedLines] = useState<Record<number, boolean>>({});
  const [keys, setKeys] = useState<LocalKeyInfo[]>([]);
  const [appInfo, setAppInfo] = useState<AppInfoView | null>(null);
  const [sysProxy, setSysProxy] = useState<SystemProxyInfo | null>(null);

  // Key generation state
  const [genOpen, setGenOpen] = useState(false);
  const [keyName, setKeyName] = useState('id_ed25519_shell');
  const [keyAlgo, setKeyAlgo] = useState<'ed25519' | 'rsa-4096'>('ed25519');
  const [keyPass, setKeyPass] = useState('');
  const [keyComment, setKeyComment] = useState('shell@windows');
  const [keyErr, setKeyErr] = useState('');
  const [generating, setGenerating] = useState(false);

  // AI settings state
  const [aiCfg, setAiCfg] = useState<AIConfigView | null>(null);
  const [aiMode, setAiMode] = useState<AIProviderMode>('api');
  const [aiEndpoint, setAiEndpoint] = useState('');
  const [aiModel, setAiModel] = useState('');
  const [aiKey, setAiKey] = useState('');
  const [clearAiKey, setClearAiKey] = useState(false);
  const [cliCommand, setCliCommand] = useState('agy');
  const [cliModel, setCliModel] = useState('');
  const [cliArgs, setCliArgs] = useState('');
  const [detectedCLIs, setDetectedCLIs] = useState<DetectedAICLI[]>([]);
  const [aiMsg, setAiMsg] = useState('');
  const [aiErr, setAiErr] = useState('');
  const [aiBusy, setAiBusy] = useState(false);

  // Backup state
  const [vaultPass, setVaultPass] = useState('');
  const [includeSecrets, setIncludeSecrets] = useState(false);
  const [backupErr, setBackupErr] = useState('');

  useEffect(() => {
    const api = getApi();
    void api.AppInfo().then(setAppInfo).catch(() => {});
    void api.DetectSystemProxy?.().then(setSysProxy).catch(() => {});
    void api.KnownHosts().then(setKnownHosts).catch(() => {});
    void api.LocalKeys().then(setKeys).catch(() => {});
    void api.DetectAICLIs?.().then((list) => setDetectedCLIs(list || [])).catch(() => {});
    void api
      .AIConfig()
      .then((c) => {
        setAiCfg(c);
        setAiMode(c.mode === 'cli' ? 'cli' : 'api');
        setAiEndpoint(c.endpoint || '');
        setAiModel(c.model || '');
        if (c.cliCommand) setCliCommand(c.cliCommand);
        setCliModel(c.cliModel || '');
        setCliArgs(c.cliArgs || '');
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const patch = (partial: Partial<AppSettings>) => {
    void onUpdateSettings({ ...settings, ...partial });
  };

  const filteredKnownHosts = useMemo(() => {
    const q = khQuery.trim().toLowerCase();
    if (!q) return knownHosts;
    return knownHosts.filter(
      (k) =>
        k.hosts.join(' ').toLowerCase().includes(q) ||
        k.fingerprint.toLowerCase().includes(q) ||
        k.keyType.toLowerCase().includes(q),
    );
  }, [knownHosts, khQuery]);

  const chosenKnownLines = useMemo(
    () => filteredKnownHosts.filter((k) => selectedLines[k.line]).map((k) => k.line),
    [filteredKnownHosts, selectedLines],
  );

  const toggleAllKnown = () => {
    if (chosenKnownLines.length === filteredKnownHosts.length && filteredKnownHosts.length > 0) {
      setSelectedLines({});
    } else {
      const next: Record<number, boolean> = {};
      for (const k of filteredKnownHosts) next[k.line] = true;
      setSelectedLines(next);
    }
  };

  const removeKnownLines = async (lines: number[]) => {
    if (!lines.length) return;
    await getApi().RemoveKnownHosts(lines);
    setSelectedLines({});
    setKnownHosts(await getApi().KnownHosts());
    onToast(lines.length === 1 ? '已移除主机指纹' : `已批量移除 ${lines.length} 条指纹`);
  };

  const clearAllKnown = async () => {
    if (!knownHosts.length) return;
    await getApi().ClearKnownHosts();
    setSelectedLines({});
    setKnownHosts([]);
    onToast('已清空所有主机指纹');
  };

  const handleGenerateKey = async (e: React.FormEvent) => {
    e.preventDefault();
    setGenerating(true);
    setKeyErr('');
    try {
      await getApi().GenerateSSHKey(keyName, keyAlgo, keyPass, keyComment);
      setKeys(await getApi().LocalKeys());
      setGenOpen(false);
      setKeyPass('');
      onToast(`已生成密钥：${keyName}`);
    } catch (ex) {
      setKeyErr(errorMessage(ex));
    } finally {
      setGenerating(false);
    }
  };

  const deployPublicKey = async (pubKey: string) => {
    if (!activeSessionId || !pubKey) return;
    try {
      await getApi().InstallPublicKey(activeSessionId, pubKey);
      onToast('公钥已部署至当前主机');
    } catch (ex) {
      onToast(errorMessage(ex));
    }
  };

  const buildAIInput = () => {
    const keep = !aiKey.trim() && !clearAiKey && Boolean(aiCfg?.hasKey && aiCfg.endpoint === aiEndpoint.trim());
    return {
      mode: aiMode,
      endpoint: aiEndpoint.trim(),
      model: aiModel.trim(),
      apiKey: aiKey.trim(),
      keepKey: keep,
      cliCommand: cliCommand.trim(),
      cliArgs: cliArgs.trim(),
      cliModel: cliModel.trim(),
    };
  };

  const saveAI = async (e: React.FormEvent) => {
    e.preventDefault();
    setAiBusy(true);
    setAiErr('');
    setAiMsg('');
    try {
      const payload = buildAIInput();
      await getApi().SaveAIConfig(payload);
      const refreshed = await getApi().AIConfig().catch(() => null);
      if (refreshed) {
        setAiCfg(refreshed);
      } else {
        setAiCfg({
          mode: aiMode,
          endpoint: payload.endpoint,
          model: payload.model,
          hasKey: Boolean(payload.apiKey || payload.keepKey),
          cliCommand: payload.cliCommand,
          cliArgs: payload.cliArgs,
          cliModel: payload.cliModel,
          ready: aiMode === 'cli' ? Boolean(payload.cliCommand) : Boolean(payload.endpoint && payload.model),
        });
      }
      setAiKey('');
      setClearAiKey(false);
      setAiMsg('配置已保存');
      onToast('AI 配置已保存');
    } catch (ex) {
      setAiErr(errorMessage(ex));
    } finally {
      setAiBusy(false);
    }
  };

  const testAI = async () => {
    setAiBusy(true);
    setAiErr('');
    setAiMsg('');
    try {
      await getApi().TestAIConfig(buildAIInput());
      setAiMsg(aiMode === 'cli' ? 'CLI 调用正常' : '连接正常');
    } catch (ex) {
      setAiErr(errorMessage(ex));
    } finally {
      setAiBusy(false);
    }
  };

  const doExport = async () => {
    setBackupErr('');
    try {
      const file = await getApi().ExportBackup(includeSecrets, vaultPass);
      if (file) onToast('备份已导出');
    } catch (ex) {
      setBackupErr(errorMessage(ex));
    }
  };

  const doImport = async () => {
    setBackupErr('');
    try {
      const sum = await getApi().ImportBackup(vaultPass);
      if (sum.profiles || sum.snippets) {
        await onProfilesImported();
        onToast(`已恢复 ${sum.profiles} 台主机、${sum.snippets} 个片段`);
      }
    } catch (ex) {
      setBackupErr(errorMessage(ex));
    }
  };

  const navItems: { id: SettingsTab; label: string; icon: React.ReactNode }[] = [
    { id: 'appearance', label: '外观与终端', icon: <Palette size={15} /> },
    { id: 'ssh', label: '连接与指纹', icon: <Shield size={15} /> },
    { id: 'keys', label: 'SSH 密钥', icon: <KeyRound size={15} /> },
    { id: 'ai', label: 'AI 模型', icon: <Sparkles size={15} /> },
    { id: 'agent', label: 'Agent 与安全', icon: <Bot size={15} /> },
    { id: 'backup', label: '备份与迁移', icon: <Archive size={15} /> },
    { id: 'about', label: '关于 Shell', icon: <Info size={15} /> },
  ];

  const matchedFont = FONT_OPTIONS.some((f) => f.value === settings.fontFamily)
    ? settings.fontFamily
    : '__custom__';

  return (
    <div id="settingsDialog" className="modal-backdrop" onClick={onClose}>
      <div className="modal-sheet wide" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <h2>偏好设置</h2>
          <button type="button" className="icon-btn" aria-label="关闭设置" onClick={onClose}>
            <X size={15} />
          </button>
        </div>

        <div className="settings-layout">
          <nav className="settings-nav" aria-label="设置分类">
            {navItems.map((n) => (
              <button
                key={n.id}
                type="button"
                className={`settings-nav-btn ${tab === n.id ? 'active' : ''}`}
                onClick={() => setTab(n.id)}
              >
                {n.icon}
                <span>{n.label}</span>
              </button>
            ))}
          </nav>

          <div className="settings-content">
            {tab === 'appearance' && (
              <>
                <div className="pref-section">
                  <div className="pref-section-title">通用</div>
                  <div className="pref-group">
                    <div className="pref-row">
                      <span className="pref-row-label">界面外观</span>
                      <div className="segmented">
                        {(['light', 'dark', 'system'] as const).map((t) => (
                          <button
                            key={t}
                            type="button"
                            className={settings.theme === t ? 'active' : ''}
                            onClick={() => {
                              const nextIsDark =
                                t === 'dark' ||
                                (t === 'system' && (window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false));
                              const syncedTerm = syncTerminalThemeForAppearance(settings.terminalTheme, nextIsDark);
                              patch({ theme: t, terminalTheme: syncedTerm });
                            }}
                          >
                            {t === 'light' ? '浅色' : t === 'dark' ? '深色' : '自动'}
                          </button>
                        ))}
                      </div>
                    </div>
                    <div className="pref-row">
                      <span className="pref-row-label">关闭窗口时最小化到托盘</span>
                      <button
                        type="button"
                        role="switch"
                        aria-checked={settings.closeToTray}
                        className="apple-switch accent"
                        onClick={() => patch({ closeToTray: !settings.closeToTray })}
                      />
                    </div>
                  </div>
                </div>

                <div className="pref-section">
                  <div className="pref-section-title">终端配色</div>
                  <div className="pref-group-pad">
                    <div className="theme-grid">
                      <button
                        type="button"
                        className={`theme-tile ${settings.terminalTheme === 'auto' || !settings.terminalTheme ? 'active' : ''}`}
                        onClick={() => patch({ terminalTheme: 'auto' })}
                      >
                        <div className="theme-swatch-box theme-swatch-auto">
                          <span className="theme-swatch-half light">
                            <span style={{ color: '#2563EB' }}>&gt;_</span>
                            <span style={{ fontSize: 9, opacity: 0.72 }}>浅色</span>
                          </span>
                          <span className="theme-swatch-half dark">
                            <span style={{ color: '#34D399' }}>&gt;_</span>
                            <span style={{ fontSize: 9, opacity: 0.78 }}>深色</span>
                          </span>
                        </div>
                        <span style={{ fontSize: 11.5, fontWeight: 500 }}>跟随界面外观</span>
                      </button>
                      {TERMINAL_THEMES.map((preset) => (
                        <button
                          key={preset.id}
                          type="button"
                          className={`theme-tile ${settings.terminalTheme === preset.id ? 'active' : ''}`}
                          onClick={() => patch({ terminalTheme: preset.id })}
                        >
                          <div
                            className="theme-swatch-box"
                            style={{ background: preset.preview[0], color: preset.preview[1] }}
                          >
                            <span>
                              <b style={{ color: preset.preview[3] }}>user@host</b>:~$
                            </span>
                            <span style={{ color: preset.preview[2] }}>ls -la</span>
                          </div>
                          <span style={{ fontSize: 11.5, fontWeight: 500 }}>{preset.name}</span>
                        </button>
                      ))}
                    </div>
                  </div>
                </div>

                <div className="pref-section">
                  <div className="pref-section-title">排版与光标</div>
                  <div className="pref-group">
                    <div className="pref-row">
                      <span className="pref-row-label">终端字体</span>
                      <select
                        className="select"
                        style={{ width: 186, height: 30 }}
                        value={matchedFont}
                        onChange={(e) => {
                          if (e.target.value !== '__custom__') patch({ fontFamily: e.target.value });
                        }}
                      >
                        {FONT_OPTIONS.map((f) => (
                          <option key={f.label} value={f.value}>
                            {f.label}
                          </option>
                        ))}
                        {matchedFont === '__custom__' && <option value="__custom__">自定义字体</option>}
                      </select>
                    </div>
                    <div className="pref-row">
                      <span className="pref-row-label">字号</span>
                      <div className="stepper">
                        <button type="button" onClick={() => patch({ fontSize: Math.max(10, settings.fontSize - 1) })}>−</button>
                        <span>{settings.fontSize} px</span>
                        <button type="button" onClick={() => patch({ fontSize: Math.min(28, settings.fontSize + 1) })}>+</button>
                      </div>
                    </div>
                    <div className="pref-row">
                      <span className="pref-row-label">行高</span>
                      <div className="stepper">
                        <button
                          type="button"
                          onClick={() => patch({ lineHeight: Math.max(1.0, Number((settings.lineHeight - 0.05).toFixed(2))) })}
                        >
                          −
                        </button>
                        <span>{settings.lineHeight.toFixed(2)}</span>
                        <button
                          type="button"
                          onClick={() => patch({ lineHeight: Math.min(2.0, Number((settings.lineHeight + 0.05).toFixed(2))) })}
                        >
                          +
                        </button>
                      </div>
                    </div>
                    <div className="pref-row">
                      <span className="pref-row-label">光标样式</span>
                      <div className="segmented">
                        {(['bar', 'block', 'underline'] as const).map((cs) => (
                          <button
                            key={cs}
                            type="button"
                            className={settings.cursorStyle === cs ? 'active' : ''}
                            onClick={() => patch({ cursorStyle: cs })}
                          >
                            {cs === 'bar' ? '竖线' : cs === 'block' ? '方块' : '下划线'}
                          </button>
                        ))}
                      </div>
                    </div>
                  </div>
                </div>

                <div className="pref-section">
                  <div className="pref-section-title">交互与渲染</div>
                  <div className="pref-group">
                    {[
                      { key: 'copyOnSelect' as const, title: '选中即复制' },
                      { key: 'rightClickPaste' as const, title: '右键快捷复制 / 粘贴' },
                      { key: 'showTelemetry' as const, title: '状态栏性能监控' },
                      { key: 'webglEnabled' as const, title: 'WebGL 渲染（实验性）' },
                    ].map((opt) => (
                      <div key={opt.key} className="pref-row">
                        <span className="pref-row-label">{opt.title}</span>
                        <button
                          type="button"
                          role="switch"
                          aria-checked={settings[opt.key]}
                          className="apple-switch accent"
                          onClick={() => patch({ [opt.key]: !settings[opt.key] })}
                        />
                      </div>
                    ))}
                  </div>
                </div>
              </>
            )}

            {tab === 'ssh' && (
              <>
                <div className="pref-section">
                  <div className="pref-section-title">会话参数</div>
                  <div className="pref-group">
                    <div className="pref-row">
                      <span className="pref-row-label">默认心跳间隔</span>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                        <input
                          className="input input-sm mono"
                          style={{ width: 96, textAlign: 'right' }}
                          type="number"
                          min={0}
                          max={3600}
                          value={settings.defaultKeepAlive}
                          onChange={(e) => patch({ defaultKeepAlive: Number(e.target.value) || 0 })}
                        />
                        <span style={{ fontSize: 12, color: 'var(--ink-tertiary)', width: 16 }}>秒</span>
                      </div>
                    </div>
                    <div className="pref-row">
                      <span className="pref-row-label">终端历史行数</span>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                        <input
                          className="input input-sm mono"
                          style={{ width: 96, textAlign: 'right' }}
                          type="number"
                          min={500}
                          max={100000}
                          step={1000}
                          value={settings.scrollback}
                          onChange={(e) => patch({ scrollback: Number(e.target.value) || 10000 })}
                        />
                        <span style={{ fontSize: 12, color: 'var(--ink-tertiary)', width: 16 }}>行</span>
                      </div>
                    </div>
                  </div>
                </div>

                <div className="pref-section">
                  <div className="pref-section-title">默认出站网络</div>
                  <div className="pref-group">
                    <div className="pref-row">
                      <span className="pref-row-label">全局连接方式</span>
                      <div className="segmented">
                        {(
                          [
                            { id: 'direct', label: '直连' },
                            { id: 'system', label: '系统代理' },
                            { id: 'socks5', label: 'SOCKS5' },
                            { id: 'http', label: 'HTTP' },
                          ] as const
                        ).map((pm) => (
                          <button
                            key={pm.id}
                            type="button"
                            className={(settings.proxyMode || 'direct') === pm.id ? 'active' : ''}
                            onClick={() =>
                              patch({
                                proxyMode: pm.id,
                                proxyHost: settings.proxyHost || (pm.id === 'socks5' || pm.id === 'http' ? '127.0.0.1' : ''),
                                proxyPort: settings.proxyPort || (pm.id === 'socks5' ? 10808 : pm.id === 'http' ? 7890 : 0),
                              })
                            }
                          >
                            {pm.label}
                          </button>
                        ))}
                      </div>
                    </div>
                    {(settings.proxyMode || 'direct') === 'system' && (
                      <div className="pref-row" style={{ minHeight: 38 }}>
                        <span className="pref-row-sub">
                          {sysProxy?.enabled
                            ? `已检测到系统代理：${sysProxy.summary}（自动遵守局域网绕过规则）`
                            : '当前未检测到系统代理，连接时将自动直连'}
                        </span>
                        <span className={`pill-badge ${sysProxy?.enabled ? 'safe' : 'neutral'}`}>
                          {sysProxy?.enabled ? '已就绪' : '直连'}
                        </span>
                      </div>
                    )}
                    {(settings.proxyMode === 'socks5' || settings.proxyMode === 'http') && (
                      <div className="pref-row" style={{ gap: 8 }}>
                        <input
                          className="input input-sm mono"
                          style={{ flex: 1 }}
                          placeholder="代理地址，如 127.0.0.1"
                          value={settings.proxyHost || ''}
                          onChange={(e) => patch({ proxyHost: e.target.value })}
                        />
                        <input
                          className="input input-sm mono"
                          style={{ width: 84 }}
                          type="number"
                          placeholder="端口"
                          value={settings.proxyPort || ''}
                          onChange={(e) => patch({ proxyPort: Number(e.target.value) || 0 })}
                        />
                        <input
                          className="input input-sm mono"
                          style={{ width: 100 }}
                          placeholder="用户名（可选）"
                          value={settings.proxyUser || ''}
                          onChange={(e) => patch({ proxyUser: e.target.value })}
                        />
                      </div>
                    )}
                  </div>
                </div>

                <div className="pref-section">
                  <div className="pref-section-head">
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                      <span className="pref-section-title">已信任主机指纹 (known_hosts)</span>
                      {knownHosts.length > 0 && <span className="pill-badge neutral mono">{knownHosts.length}</span>}
                    </div>
                    {knownHosts.length > 0 && (
                      <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                        <button type="button" className="btn btn-sm btn-ghost" onClick={toggleAllKnown}>
                          {chosenKnownLines.length === filteredKnownHosts.length && filteredKnownHosts.length > 0 ? '取消全选' : '全选'}
                        </button>
                        {chosenKnownLines.length > 0 && (
                          <button
                            type="button"
                            className="btn btn-sm btn-danger"
                            onClick={() => void removeKnownLines(chosenKnownLines)}
                          >
                            <Trash2 size={12} />
                            批量移除 ({chosenKnownLines.length})
                          </button>
                        )}
                        <button type="button" className="btn btn-sm" onClick={() => void clearAllKnown()}>
                          清空全部
                        </button>
                      </div>
                    )}
                  </div>

                  {knownHosts.length > 3 && (
                    <div className="search-box">
                      <Search size={12} />
                      <input
                        className="input input-sm"
                        style={{ height: 30, paddingLeft: 28, paddingRight: 10 }}
                        placeholder="筛选主机或指纹…"
                        value={khQuery}
                        onChange={(e) => setKhQuery(e.target.value)}
                      />
                    </div>
                  )}

                  {filteredKnownHosts.length === 0 ? (
                    <div className="pref-group">
                      <div style={{ color: 'var(--ink-tertiary)', textAlign: 'center', padding: 24, fontSize: 12.5 }}>
                        {knownHosts.length === 0 ? '暂无已信任的主机指纹' : '无匹配记录'}
                      </div>
                    </div>
                  ) : (
                    <div className="pref-group">
                      {filteredKnownHosts.map((kh) => (
                        <div key={kh.line} className="pref-row" style={{ gap: 10 }}>
                          <input
                            type="checkbox"
                            checked={Boolean(selectedLines[kh.line])}
                            onChange={(e) => setSelectedLines((prev) => ({ ...prev, [kh.line]: e.target.checked }))}
                            style={{ margin: 0, flexShrink: 0, cursor: 'pointer' }}
                          />
                          <div style={{ flex: 1, minWidth: 0 }}>
                            <div className="mono" style={{ fontWeight: 600, fontSize: 12.5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                              {kh.hosts.join(', ')}
                            </div>
                            <div
                              className="mono"
                              style={{ fontSize: 11, color: 'var(--ink-tertiary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                              title={`${kh.keyType} · ${kh.fingerprint}`}
                            >
                              {kh.keyType} · {kh.fingerprint}
                            </div>
                          </div>
                          <div style={{ display: 'flex', gap: 5, flexShrink: 0 }}>
                            <button
                              type="button"
                              className="btn btn-sm btn-ghost"
                              title="复制指纹"
                              onClick={() => {
                                void copyToClipboard(kh.fingerprint);
                                onToast('已复制指纹');
                              }}
                            >
                              <Copy size={12} />
                            </button>
                            <button
                              type="button"
                              className="btn btn-sm btn-danger"
                              onClick={() => void removeKnownLines([kh.line])}
                            >
                              <Trash2 size={12} />
                              移除
                            </button>
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              </>
            )}

            {tab === 'keys' && (
              <>
                <div className="pref-section">
                  <div className="pref-section-head">
                    <span className="pref-section-title">本地 SSH 密钥 (~/.ssh)</span>
                    <button type="button" className="btn btn-primary btn-sm" onClick={() => setGenOpen((v) => !v)}>
                      <Plus size={13} />
                      生成密钥
                    </button>
                  </div>

                  {genOpen && (
                    <form className="pref-group-pad" onSubmit={handleGenerateKey}>
                      <div className="field-row">
                        <div className="field-group grow">
                          <label className="field-label">密钥文件名</label>
                          <input
                            className="input input-sm mono"
                            required
                            value={keyName}
                            onChange={(e) => setKeyName(e.target.value)}
                          />
                        </div>
                        <div className="field-group" style={{ width: 150 }}>
                          <label className="field-label">算法</label>
                          <select
                            className="select"
                            style={{ height: 30 }}
                            value={keyAlgo}
                            onChange={(e) => setKeyAlgo(e.target.value as 'ed25519' | 'rsa-4096')}
                          >
                            <option value="ed25519">Ed25519</option>
                            <option value="rsa-4096">RSA 4096</option>
                          </select>
                        </div>
                      </div>
                      <div className="field-row">
                        <div className="field-group grow">
                          <label className="field-label">保护口令</label>
                          <input
                            type="password"
                            className="input input-sm"
                            placeholder="留空则不加密"
                            value={keyPass}
                            onChange={(e) => setKeyPass(e.target.value)}
                          />
                        </div>
                        <div className="field-group grow">
                          <label className="field-label">备注</label>
                          <input
                            className="input input-sm mono"
                            value={keyComment}
                            onChange={(e) => setKeyComment(e.target.value)}
                          />
                        </div>
                      </div>
                      {keyErr && <p className="error-text">{keyErr}</p>}
                      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6 }}>
                        <button type="button" className="btn btn-sm" onClick={() => setGenOpen(false)}>
                          取消
                        </button>
                        <button type="submit" className="btn btn-primary btn-sm" style={{ minWidth: 88 }} disabled={generating}>
                          {generating ? '生成中…' : '创建密钥'}
                        </button>
                      </div>
                    </form>
                  )}

                  {keys.length === 0 ? (
                    <div className="pref-group">
                      <div style={{ textAlign: 'center', padding: 24, color: 'var(--ink-tertiary)', fontSize: 12.5 }}>
                        未在 ~/.ssh 中发现本地密钥
                      </div>
                    </div>
                  ) : (
                    <div className="pref-group">
                      {keys.map((k) => (
                        <div key={k.path} className="pref-row">
                          <div style={{ flex: 1, minWidth: 0 }}>
                            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                              <span style={{ fontWeight: 600, fontSize: 13 }} className="mono">{k.name}</span>
                              {k.keyType && <span className="pill-badge neutral">{k.keyType}</span>}
                              {k.encrypted && <span className="pill-badge caution">已加密</span>}
                            </div>
                            <div className="pref-row-sub mono" title={k.fingerprint || k.path}>
                              {k.fingerprint || k.path}
                            </div>
                          </div>
                          {k.publicKey && (
                            <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
                              <button
                                type="button"
                                className="btn btn-sm"
                                onClick={() => {
                                  void copyToClipboard(k.publicKey);
                                  onToast('已复制公钥');
                                }}
                              >
                                <Copy size={12} />
                                复制公钥
                              </button>
                              {activeSessionId && (
                                <button
                                  type="button"
                                  className="btn btn-primary btn-sm"
                                  onClick={() => void deployPublicKey(k.publicKey)}
                                >
                                  <Send size={12} />
                                  部署公钥
                                </button>
                              )}
                            </div>
                          )}
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              </>
            )}

            {tab === 'ai' && (
              <form id="settingsForm" onSubmit={saveAI} style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
                <div className="segmented" style={{ width: '100%' }}>
                  <button
                    type="button"
                    data-ai-mode="api"
                    className={aiMode === 'api' ? 'active' : ''}
                    onClick={() => {
                      setAiMode('api');
                      setAiErr('');
                      setAiMsg('');
                    }}
                  >
                    API 接口
                  </button>
                  <button
                    type="button"
                    data-ai-mode="cli"
                    className={aiMode === 'cli' ? 'active' : ''}
                    onClick={() => {
                      setAiMode('cli');
                      setAiErr('');
                      setAiMsg('');
                    }}
                  >
                    本地 CLI
                  </button>
                </div>

                {aiMode === 'api' ? (
                  <>
                    <div className="pref-section">
                      <div className="pref-section-title">常用模型预设</div>
                      <div className="provider-grid">
                        {AI_PRESETS.map((p) => {
                          const active = aiEndpoint.trim() === p.endpoint;
                          return (
                            <button
                              key={p.id}
                              type="button"
                              className={`provider-card ${active ? 'active' : ''}`}
                              onClick={() => {
                                setAiEndpoint(p.endpoint);
                                setAiModel(p.model);
                                setAiKey('');
                              }}
                            >
                              <span style={{ fontSize: 12.5, fontWeight: 600, color: 'var(--ink-primary)' }}>{p.label}</span>
                              <span className="mono" style={{ fontSize: 10.5, color: 'var(--ink-tertiary)', width: '100%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                {p.sub}
                              </span>
                            </button>
                          );
                        })}
                      </div>
                    </div>

                    <div className="pref-section">
                      <div className="pref-section-title">接口参数</div>
                      <div className="pref-group-pad">
                        <div className="field-group">
                          <label className="field-label" htmlFor="endpoint">API 地址</label>
                          <input
                            id="endpoint"
                            className="input mono"
                            required
                            value={aiEndpoint}
                            onChange={(e) => setAiEndpoint(e.target.value)}
                            placeholder="https://api.openai.com/v1（兼容任意 OpenAI 格式接口）"
                          />
                        </div>
                        <div className="field-group">
                          <label className="field-label" htmlFor="model">模型名称</label>
                          <input
                            id="model"
                            className="input mono"
                            required
                            value={aiModel}
                            onChange={(e) => setAiModel(e.target.value)}
                            placeholder="claude-sonnet-4-6 / gpt-5-mini / gemini-2.5-flash / deepseek-chat"
                          />
                        </div>
                        <div className="field-group">
                          <label className="field-label" htmlFor="apiKey">API 密钥</label>
                          <input
                            id="apiKey"
                            type="password"
                            className="input mono"
                            autoComplete="off"
                            value={aiKey}
                            onChange={(e) => setAiKey(e.target.value)}
                            placeholder={
                              aiCfg?.hasKey && aiCfg.endpoint === aiEndpoint.trim()
                                ? '已加密保存，留空沿用'
                                : 'sk-...'
                            }
                          />
                        </div>

                        {aiCfg?.hasKey && aiCfg.endpoint === aiEndpoint.trim() && !aiKey && (
                          <label style={{ display: 'flex', alignItems: 'center', gap: 7, fontSize: 12.5 }}>
                            <input
                              type="checkbox"
                              checked={clearAiKey}
                              onChange={(e) => setClearAiKey(e.target.checked)}
                            />
                            清除已保存的密钥
                          </label>
                        )}

                        {aiErr && <p id="settingsError" className="error-text">{aiErr}</p>}
                        {aiMsg && (
                          <p style={{ margin: 0, fontSize: 12.5, color: 'var(--green)', display: 'flex', alignItems: 'center', gap: 5 }}>
                            <Check size={14} />
                            {aiMsg}
                          </p>
                        )}

                        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
                          <button type="button" className="btn" style={{ minWidth: 86 }} disabled={aiBusy} onClick={() => void testAI()}>
                            测试连接
                          </button>
                          <button type="submit" className="btn btn-primary" style={{ minWidth: 86 }} disabled={aiBusy}>
                            保存配置
                          </button>
                        </div>
                      </div>
                    </div>
                  </>
                ) : (
                  <>
                    <div className="pref-section">
                      <div className="pref-section-title">本机 AI CLI 工具</div>
                      <div className="provider-grid">
                        {AI_CLI_PRESETS.map((p) => {
                          const det = detectedCLIs.find((d) => d.id === p.id);
                          const isCustom = p.id === 'custom';
                          const active = isCustom
                            ? !['claude', 'codex', 'agy'].includes(cliCommand.trim().toLowerCase())
                            : cliCommand.trim().toLowerCase() === p.command;
                          return (
                            <button
                              key={p.id}
                              type="button"
                              className={`provider-card ${active ? 'active' : ''}`}
                              title={det?.path || p.label}
                              onClick={() => {
                                if (!isCustom) {
                                  setCliCommand(p.command);
                                  setCliArgs('');
                                } else if (['claude', 'codex', 'agy'].includes(cliCommand.trim().toLowerCase())) {
                                  setCliCommand('');
                                }
                              }}
                            >
                              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', width: '100%', gap: 4 }}>
                                <span style={{ fontSize: 12.5, fontWeight: 600, color: 'var(--ink-primary)' }}>{p.label}</span>
                                {!isCustom && (
                                  <span
                                    style={{
                                      width: 7,
                                      height: 7,
                                      borderRadius: '50%',
                                      flexShrink: 0,
                                      background: det?.installed ? 'var(--green)' : 'var(--border-strong)',
                                    }}
                                  />
                                )}
                              </div>
                              <span className="mono" style={{ fontSize: 10.5, color: det?.installed ? 'var(--green)' : 'var(--ink-tertiary)' }}>
                                {isCustom ? '任意可执行程序' : det?.installed ? '已检测到' : '未安装'}
                              </span>
                            </button>
                          );
                        })}
                      </div>
                    </div>

                    <div className="pref-section">
                      <div className="pref-section-title">调用参数</div>
                      <div className="pref-group-pad">
                        <div className="field-row">
                          <div className="field-group grow">
                            <label className="field-label" htmlFor="cliCommand">CLI 命令或完整路径</label>
                            <input
                              id="cliCommand"
                              className="input mono"
                              required
                              value={cliCommand}
                              onChange={(e) => setCliCommand(e.target.value)}
                              placeholder="claude / codex / gemini"
                            />
                          </div>
                          <div className="field-group grow">
                            <label className="field-label" htmlFor="cliModel">指定模型（可选）</label>
                            <input
                              id="cliModel"
                              className="input mono"
                              value={cliModel}
                              onChange={(e) => setCliModel(e.target.value)}
                              placeholder={
                                AI_CLI_PRESETS.find((x) => x.command === cliCommand.trim().toLowerCase())?.modelHint ||
                                '留空使用 CLI 默认模型'
                              }
                            />
                          </div>
                        </div>

                        <div className="field-group">
                          <label className="field-label" htmlFor="cliArgs">附加参数（可选）</label>
                          <input
                            id="cliArgs"
                            className="input mono"
                            value={cliArgs}
                            onChange={(e) => setCliArgs(e.target.value)}
                            placeholder="默认通过 stdin 传入提示词；自定义命令可用 {prompt} 占位"
                          />
                        </div>

                        {aiErr && <p id="settingsError" className="error-text">{aiErr}</p>}
                        {aiMsg && (
                          <p style={{ margin: 0, fontSize: 12.5, color: 'var(--green)', display: 'flex', alignItems: 'center', gap: 5 }}>
                            <Check size={14} />
                            {aiMsg}
                          </p>
                        )}

                        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
                          <button type="button" className="btn" style={{ minWidth: 86 }} disabled={aiBusy} onClick={() => void testAI()}>
                            测试调用
                          </button>
                          <button type="submit" className="btn btn-primary" style={{ minWidth: 86 }} disabled={aiBusy}>
                            保存配置
                          </button>
                        </div>
                      </div>
                    </div>
                  </>
                )}
              </form>
            )}

            {tab === 'agent' && (
              <AgentSecurityTab onStatusChanged={onAgentStatusChanged} onToast={onToast} />
            )}

            {tab === 'backup' && (
              <>
                <div className="pref-section">
                  <div className="pref-section-title">外部工具迁移</div>
                  <div className="pref-group">
                    <div className="pref-row">
                      <span className="pref-row-label">导入其他 SSH 客户端配置</span>
                      <button
                        type="button"
                        className="btn btn-sm"
                        onClick={() => {
                          onClose();
                          onOpenImportSSH();
                        }}
                      >
                        <Download size={12} />
                        打开导入向导
                      </button>
                    </div>
                  </div>
                </div>

                <div className="pref-section">
                  <div className="pref-section-title">加密保险库备份与恢复</div>
                  <div className="pref-group-pad">
                    <div className="field-group">
                      <label className="field-label">备份加密口令</label>
                      <input
                        type="password"
                        className="input"
                        placeholder="留空导出不含凭据的明文 JSON"
                        value={vaultPass}
                        onChange={(e) => setVaultPass(e.target.value)}
                      />
                    </div>
                    <div className="pref-row" style={{ padding: '4px 0', minHeight: 34, borderBottom: 'none' }}>
                      <span className="pref-row-label" style={{ fontSize: 12.5 }}>包含已保存凭据</span>
                      <button
                        type="button"
                        role="switch"
                        aria-checked={includeSecrets && Boolean(vaultPass)}
                        disabled={!vaultPass}
                        className="apple-switch accent"
                        onClick={() => setIncludeSecrets((v) => !v)}
                      />
                    </div>
                    {backupErr && <p className="error-text">{backupErr}</p>}
                    <div style={{ display: 'flex', gap: 8 }}>
                      <button type="button" className="btn btn-primary" onClick={() => void doExport()}>
                        导出备份
                      </button>
                      <button type="button" className="btn" onClick={() => void doImport()}>
                        从备份恢复
                      </button>
                    </div>
                  </div>
                </div>
              </>
            )}

            {tab === 'about' && (
              <>
                <div className="about-hero">
                  <div className="welcome-app-icon" style={{ width: 52, height: 52, fontSize: 20 }}>
                    &gt;<span>_</span>
                  </div>
                  <div style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.03em' }}>Shell</div>
                  <div className="about-badges">
                    <span className="pill-badge accent mono">v{appInfo?.version || '1.2.0'}</span>
                    <span className="pill-badge neutral">Windows x64</span>
                    <span className="pill-badge neutral">MIT License</span>
                  </div>
                </div>

                <div className="pref-section">
                  <div className="pref-section-title">软件更新</div>
                  <div className="pref-group">
                    <div className="pref-row">
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <div className="pref-row-label" style={{ display: 'flex', alignItems: 'center', gap: 7 }}>
                          <span>版本更新</span>
                          {updateInfo?.hasUpdate ? (
                            <span className="pill-badge accent mono">发现新版本 v{updateInfo.latestVersion}</span>
                          ) : updateInfo ? (
                            <span className="pill-badge safe">已是最新</span>
                          ) : null}
                        </div>
                        <div className="pref-row-sub">
                          {updateProgress
                            ? updateProgress.message
                            : updateInfo?.hasUpdate
                              ? '点击「立即更新」将在应用内下载、校验 SHA-256 并自动重启生效'
                              : '支持应用内检测与一键更新，自动保留原有主机配置与凭据'}
                        </div>
                      </div>
                      <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
                        {updateInfo?.hasUpdate && onApplyUpdate && (
                          <button
                            id="applyUpdateBtn"
                            type="button"
                            className="btn btn-primary btn-sm"
                            disabled={updatingApp}
                            onClick={() => void onApplyUpdate()}
                          >
                            {updatingApp ? <Loader2 size={12} className="spin" /> : <Download size={12} />}
                            {updatingApp ? '更新中…' : '立即更新'}
                          </button>
                        )}
                        <button
                          id="checkUpdateBtn"
                          type="button"
                          className="btn btn-sm"
                          disabled={checkingUpdate || updatingApp}
                          onClick={() => void onCheckUpdate?.()}
                        >
                          {checkingUpdate ? <Loader2 size={12} className="spin" /> : <RefreshCw size={12} />}
                          检查更新
                        </button>
                      </div>
                    </div>

                    {updateProgress && updateProgress.phase !== 'error' && (
                      <div style={{ padding: '0 14px 10px' }}>
                        <div className="update-progress-track">
                          <div
                            className="update-progress-fill"
                            style={{ width: `${Math.max(4, Math.min(100, updateProgress.percent || 8))}%` }}
                          />
                        </div>
                        <div className="mono" style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11, color: 'var(--ink-tertiary)', marginTop: 4 }}>
                          <span>{updateProgress.message}</span>
                          <span>
                            {updateProgress.total > 0
                              ? `${(updateProgress.downloaded / 1048576).toFixed(1)} / ${(updateProgress.total / 1048576).toFixed(1)} MB (${updateProgress.percent}%)`
                              : `${updateProgress.percent}%`}
                          </span>
                        </div>
                      </div>
                    )}

                    {updateError && (
                      <div style={{ padding: '0 14px 10px' }}>
                        <p className="error-text">{updateError}</p>
                      </div>
                    )}

                    <div className="pref-row">
                      <span className="pref-row-label">启动时自动检查更新</span>
                      <button
                        type="button"
                        role="switch"
                        aria-checked={settings.autoCheckUpdate !== false}
                        className="apple-switch accent"
                        onClick={() => patch({ autoCheckUpdate: settings.autoCheckUpdate === false })}
                      />
                    </div>
                  </div>
                </div>

                <div className="pref-section">
                  <div className="pref-group">
                    <div className="pref-row">
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <div className="pref-row-label">本地数据目录</div>
                        <div className="pref-row-sub mono" title={appInfo?.dataDir || '%LOCALAPPDATA%\\Shell'}>
                          {appInfo?.dataDir || '%LOCALAPPDATA%\\Shell'}
                        </div>
                      </div>
                      <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
                        <button
                          type="button"
                          className="btn btn-sm btn-ghost"
                          onClick={() => {
                            void copyToClipboard(appInfo?.dataDir || '');
                            onToast('已复制目录路径');
                          }}
                        >
                          <Copy size={12} />
                        </button>
                        <button
                          type="button"
                          className="btn btn-sm"
                          onClick={() => void getApi().OpenDataDir().catch(() => {})}
                        >
                          <FolderOpen size={12} />
                          打开目录
                        </button>
                      </div>
                    </div>

                    <div className="pref-row">
                      <span className="pref-row-label">运行框架与仓库</span>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                        <span className="mono" style={{ fontSize: 11.5, color: 'var(--ink-secondary)' }}>
                          Go 1.26 · Wails v2 · xterm.js v6
                        </span>
                        <button
                          type="button"
                          className="btn btn-sm"
                          onClick={() => openExternalURL('https://github.com/haoch1/Shell')}
                        >
                          GitHub
                          <ExternalLink size={11} />
                        </button>
                      </div>
                    </div>
                  </div>
                </div>

                <div className="pref-section">
                  <div className="pref-section-title">键盘快捷键</div>
                  <div className="pref-group">
                    <div className="shortcut-grid">
                      {SHORTCUTS.map((sc) => (
                        <div key={sc.label} className="shortcut-cell">
                          <span>{sc.label}</span>
                          <span className="kbd-combo">
                            {sc.keys.map((k) => (
                              <kbd key={k} className="kbd-cap">{k}</kbd>
                            ))}
                          </span>
                        </div>
                      ))}
                    </div>
                  </div>
                </div>
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};
