import React, { useEffect, useState } from 'react';
import {
  Archive,
  Bot,
  Check,
  Copy,
  Info,
  KeyRound,
  Palette,
  Plus,
  Send,
  Shield,
  Sparkles,
  Trash2,
  X,
} from 'lucide-react';
import type {
  AgentStatusView,
  AIConfigView,
  AppInfoView,
  AppSettings,
  KnownHostEntry,
  LocalKeyInfo,
} from '../types/shell';
import { TERMINAL_THEMES } from '../lib/themes';
import { copyToClipboard, errorMessage, getApi } from '../lib/bridge';
import { AgentSecurityTab } from './AgentSecurityTab';

export type SettingsTab = 'appearance' | 'ssh' | 'keys' | 'ai' | 'agent' | 'backup' | 'about';

interface SettingsModalProps {
  initialTab: SettingsTab;
  settings: AppSettings;
  activeSessionId?: string;
  onUpdateSettings: (next: AppSettings) => Promise<void>;
  onAgentStatusChanged: (st: AgentStatusView) => void;
  onClose: () => void;
  onProfilesImported: () => Promise<void>;
  onToast: (msg: string) => void;
}

const AI_PRESETS = [
  { label: 'DeepSeek', endpoint: 'https://api.deepseek.com/v1', model: 'deepseek-chat' },
  { label: 'OpenAI', endpoint: 'https://api.openai.com/v1', model: 'gpt-4o-mini' },
  { label: 'Ollama 本地', endpoint: 'http://127.0.0.1:11434/v1', model: 'qwen2.5-coder:7b' },
  { label: 'OpenRouter', endpoint: 'https://openrouter.ai/api/v1', model: 'anthropic/claude-3.5-haiku' },
];

export const SettingsModal: React.FC<SettingsModalProps> = ({
  initialTab,
  settings,
  activeSessionId,
  onUpdateSettings,
  onAgentStatusChanged,
  onClose,
  onProfilesImported,
  onToast,
}) => {
  const [tab, setTab] = useState<SettingsTab>(initialTab);
  const [knownHosts, setKnownHosts] = useState<KnownHostEntry[]>([]);
  const [keys, setKeys] = useState<LocalKeyInfo[]>([]);
  const [appInfo, setAppInfo] = useState<AppInfoView | null>(null);

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
  const [aiEndpoint, setAiEndpoint] = useState('http://127.0.0.1:11434/v1');
  const [aiModel, setAiModel] = useState('');
  const [aiKey, setAiKey] = useState('');
  const [clearAiKey, setClearAiKey] = useState(false);
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
    void api.KnownHosts().then(setKnownHosts).catch(() => {});
    void api.LocalKeys().then(setKeys).catch(() => {});
    void api
      .AIConfig()
      .then((c) => {
        setAiCfg(c);
        setAiEndpoint(c.endpoint || 'http://127.0.0.1:11434/v1');
        setAiModel(c.model || '');
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

  const handleGenerateKey = async (e: React.FormEvent) => {
    e.preventDefault();
    setGenerating(true);
    setKeyErr('');
    try {
      await getApi().GenerateSSHKey(keyName, keyAlgo, keyPass, keyComment);
      const refreshed = await getApi().LocalKeys();
      setKeys(refreshed);
      setGenOpen(false);
      setKeyPass('');
      onToast(`已生成 SSH 密钥：${keyName}`);
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
      onToast('公钥已部署至当前主机的 ~/.ssh/authorized_keys');
    } catch (ex) {
      onToast(errorMessage(ex));
    }
  };

  const saveAI = async (e: React.FormEvent) => {
    e.preventDefault();
    setAiBusy(true);
    setAiErr('');
    setAiMsg('');
    try {
      const keep = !aiKey.trim() && !clearAiKey && Boolean(aiCfg?.hasKey && aiCfg.endpoint === aiEndpoint.trim());
      await getApi().ConfigureAI(aiEndpoint.trim(), aiModel.trim(), aiKey.trim(), keep);
      setAiCfg({ endpoint: aiEndpoint.trim(), model: aiModel.trim(), hasKey: Boolean(aiKey.trim() || keep) });
      setAiKey('');
      setClearAiKey(false);
      setAiMsg('AI 配置已加密保存。');
      onToast('AI 设置已保存');
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
      const keep = !aiKey.trim() && !clearAiKey && Boolean(aiCfg?.hasKey && aiCfg.endpoint === aiEndpoint.trim());
      await getApi().TestAI(aiEndpoint.trim(), aiModel.trim(), aiKey.trim(), keep);
      setAiMsg('连接成功，接口响应正常。');
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
      if (file) onToast('配置备份已导出');
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
        onToast(`已导入 ${sum.profiles} 个主机、${sum.snippets} 个片段`);
      }
    } catch (ex) {
      setBackupErr(errorMessage(ex));
    }
  };

  const navItems: { id: SettingsTab; label: string; icon: React.ReactNode }[] = [
    { id: 'appearance', label: '外观与终端', icon: <Palette size={14} /> },
    { id: 'ssh', label: '连接与指纹', icon: <Shield size={14} /> },
    { id: 'keys', label: 'SSH 密钥', icon: <KeyRound size={14} /> },
    { id: 'ai', label: 'AI API 服务', icon: <Sparkles size={14} /> },
    { id: 'agent', label: 'Agent 与安全', icon: <Bot size={14} /> },
    { id: 'backup', label: '备份与迁移', icon: <Archive size={14} /> },
    { id: 'about', label: '关于 Shell', icon: <Info size={14} /> },
  ];

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
                <div className="setting-row">
                  <div style={{ fontWeight: 500 }}>界面外观</div>
                  <div className="segmented">
                    {(['light', 'dark', 'system'] as const).map((t) => (
                      <button
                        key={t}
                        type="button"
                        className={settings.theme === t ? 'active' : ''}
                        onClick={() => patch({ theme: t })}
                      >
                        {t === 'light' ? '浅色' : t === 'dark' ? '深色' : '自动'}
                      </button>
                    ))}
                  </div>
                </div>

                <div className="field-group">
                  <div style={{ fontWeight: 600, marginBottom: 4 }}>终端配色方案</div>
                  <div className="theme-grid">
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

                <div className="field-row">
                  <div className="field-group grow">
                    <label className="field-label">终端字体</label>
                    <input
                      className="input input-sm mono"
                      value={settings.fontFamily}
                      onChange={(e) => patch({ fontFamily: e.target.value })}
                    />
                  </div>
                  <div className="field-group" style={{ width: 84 }}>
                    <label className="field-label">字号 (px)</label>
                    <input
                      className="input input-sm mono"
                      type="number"
                      min={10}
                      max={28}
                      value={settings.fontSize}
                      onChange={(e) => patch({ fontSize: Number(e.target.value) || 14 })}
                    />
                  </div>
                  <div className="field-group" style={{ width: 84 }}>
                    <label className="field-label">行高</label>
                    <input
                      className="input input-sm mono"
                      type="number"
                      step={0.05}
                      min={1}
                      max={2}
                      value={settings.lineHeight}
                      onChange={(e) => patch({ lineHeight: Number(e.target.value) || 1.28 })}
                    />
                  </div>
                </div>

                <div className="setting-row">
                  <div style={{ fontWeight: 500 }}>光标样式</div>
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

                {[
                  { key: 'closeToTray' as const, title: '关闭窗口时最小化到托盘' },
                  { key: 'copyOnSelect' as const, title: '选中即复制' },
                  { key: 'rightClickPaste' as const, title: '右键快捷复制 / 粘贴' },
                  { key: 'showTelemetry' as const, title: '状态栏性能监控' },
                  { key: 'webglEnabled' as const, title: 'WebGL 硬件加速' },
                ].map((opt) => (
                  <div key={opt.key} className="setting-row">
                    <div style={{ fontWeight: 500 }}>{opt.title}</div>
                    <button
                      type="button"
                      role="switch"
                      aria-checked={settings[opt.key]}
                      className="apple-switch accent"
                      onClick={() => patch({ [opt.key]: !settings[opt.key] })}
                    />
                  </div>
                ))}
              </>
            )}

            {tab === 'ssh' && (
              <>
                <div className="setting-row">
                  <div style={{ fontWeight: 500 }}>SSH 心跳间隔 (秒)</div>
                  <input
                    className="input input-sm mono"
                    style={{ width: 90 }}
                    type="number"
                    min={0}
                    max={3600}
                    value={settings.defaultKeepAlive}
                    onChange={(e) => patch({ defaultKeepAlive: Number(e.target.value) || 0 })}
                  />
                </div>

                <div className="setting-row">
                  <div style={{ fontWeight: 500 }}>终端历史行数</div>
                  <input
                    className="input input-sm mono"
                    style={{ width: 100 }}
                    type="number"
                    min={500}
                    max={100000}
                    step={1000}
                    value={settings.scrollback}
                    onChange={(e) => patch({ scrollback: Number(e.target.value) || 10000 })}
                  />
                </div>

                <div className="field-group" style={{ marginTop: 6 }}>
                  <div style={{ fontWeight: 600 }}>已信任主机指纹 (known_hosts)</div>
                  {knownHosts.length === 0 ? (
                    <div className="soft-card" style={{ color: 'var(--ink-tertiary)', textAlign: 'center', padding: 18 }}>
                      暂无已保存的主机指纹记录
                    </div>
                  ) : (
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                      {knownHosts.map((kh) => (
                        <div
                          key={kh.line}
                          className="soft-card"
                          style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', padding: '8px 12px' }}
                        >
                          <div style={{ minWidth: 0 }}>
                            <div className="mono" style={{ fontWeight: 600, fontSize: 12 }}>
                              {kh.hosts.join(', ')}
                            </div>
                            <div className="mono" style={{ fontSize: 11, color: 'var(--ink-tertiary)' }}>
                              {kh.keyType} · {kh.fingerprint}
                            </div>
                          </div>
                          <button
                            type="button"
                            className="btn btn-sm btn-danger"
                            onClick={async () => {
                              await getApi().RemoveKnownHost(kh.line);
                              setKnownHosts(await getApi().KnownHosts());
                              onToast('已移除主机指纹');
                            }}
                          >
                            <Trash2 size={12} />
                            移除
                          </button>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              </>
            )}

            {tab === 'keys' && (
              <>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                  <div style={{ fontWeight: 600 }}>本地 SSH 密钥 (~/.ssh)</div>
                  <button type="button" className="btn btn-primary btn-sm" onClick={() => setGenOpen((v) => !v)}>
                    <Plus size={13} />
                    生成密钥
                  </button>
                </div>

                {genOpen && (
                  <form className="soft-card" onSubmit={handleGenerateKey}>
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
                          <option value="ed25519">Ed25519 (推荐)</option>
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
                      <button type="submit" className="btn btn-primary btn-sm" disabled={generating}>
                        {generating ? '生成中…' : '创建密钥对'}
                      </button>
                    </div>
                  </form>
                )}

                {keys.length === 0 ? (
                  <div className="soft-card" style={{ textAlign: 'center', padding: 20, color: 'var(--ink-tertiary)' }}>
                    在 %USERPROFILE%\.ssh 中未找到本地私钥
                  </div>
                ) : (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                    {keys.map((k) => (
                      <div key={k.path} className="soft-card">
                        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                          <div style={{ display: 'flex', alignItems: 'center', gap: 7 }}>
                            <span style={{ fontWeight: 600 }} className="mono">{k.name}</span>
                            {k.keyType && <span className="pill-badge neutral">{k.keyType}</span>}
                            {k.encrypted && <span className="pill-badge caution">已加密</span>}
                          </div>
                          <div style={{ display: 'flex', gap: 5 }}>
                            {k.publicKey && (
                              <>
                                <button
                                  type="button"
                                  className="btn btn-sm"
                                  onClick={() => {
                                    void copyToClipboard(k.publicKey);
                                    onToast('已复制公钥内容');
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
                                    部署到当前主机
                                  </button>
                                )}
                              </>
                            )}
                          </div>
                        </div>
                        <div className="mono" style={{ fontSize: 11, color: 'var(--ink-tertiary)' }}>
                          {k.fingerprint || k.path}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </>
            )}

            {tab === 'ai' && (
              <form id="settingsForm" onSubmit={saveAI} style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
                <div className="field-group">
                  <span className="field-label">快捷服务商预设</span>
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                    {AI_PRESETS.map((p) => (
                      <button
                        key={p.label}
                        type="button"
                        className="btn btn-sm"
                        onClick={() => {
                          setAiEndpoint(p.endpoint);
                          setAiModel(p.model);
                          setAiKey('');
                        }}
                      >
                        {p.label}
                      </button>
                    ))}
                  </div>
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="endpoint">API 地址</label>
                  <input
                    id="endpoint"
                    className="input mono"
                    required
                    value={aiEndpoint}
                    onChange={(e) => setAiEndpoint(e.target.value)}
                    placeholder="https://api.openai.com/v1"
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
                    placeholder="gpt-4o-mini / deepseek-chat / qwen2.5-coder"
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
                        ? '已安全保存，留空沿用原密钥'
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
                    移除已保存的 API 密钥
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
                  <button type="button" className="btn" disabled={aiBusy} onClick={() => void testAI()}>
                    测试连接
                  </button>
                  <button type="submit" className="btn btn-primary" disabled={aiBusy}>
                    保存设置
                  </button>
                </div>
              </form>
            )}

            {tab === 'agent' && (
              <AgentSecurityTab onStatusChanged={onAgentStatusChanged} onToast={onToast} />
            )}

            {tab === 'backup' && (
              <>
                <div className="soft-card">
                  <div style={{ fontWeight: 600 }}>保险库备份与恢复</div>
                  <div className="field-group">
                    <label className="field-label">加密口令</label>
                    <input
                      type="password"
                      className="input"
                      placeholder="留空导出明文 JSON"
                      value={vaultPass}
                      onChange={(e) => setVaultPass(e.target.value)}
                    />
                  </div>
                  <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, cursor: 'pointer' }}>
                    <input
                      type="checkbox"
                      checked={includeSecrets}
                      disabled={!vaultPass}
                      onChange={(e) => setIncludeSecrets(e.target.checked)}
                    />
                    包含已保存凭据
                  </label>
                  {backupErr && <p className="error-text">{backupErr}</p>}
                  <div style={{ display: 'flex', gap: 8, marginTop: 4 }}>
                    <button type="button" className="btn btn-primary" onClick={() => void doExport()}>
                      导出备份文件
                    </button>
                    <button type="button" className="btn" onClick={() => void doImport()}>
                      从备份文件导入
                    </button>
                  </div>
                </div>
              </>
            )}

            {tab === 'about' && (
              <>
                <div style={{ display: 'flex', alignItems: 'center', gap: 14 }}>
                  <div className="welcome-app-icon" style={{ width: 52, height: 52, fontSize: 20 }}>
                    &gt;<span>_</span>
                  </div>
                  <div>
                    <div style={{ fontSize: 17, fontWeight: 600 }}>Shell</div>
                    <div className="mono" style={{ fontSize: 12, color: 'var(--ink-secondary)' }}>
                      v{appInfo?.version || '1.0.0'}
                    </div>
                  </div>
                </div>

                <div className="soft-card" style={{ gap: 6, fontSize: 12 }}>
                  <div><b>技术栈：</b>Go 1.26 + Wails v2 (WebView2) + React + xterm.js v6</div>
                  <div>
                    <b>本机数据目录：</b>
                    <span className="mono">{appInfo?.dataDir || '%LOCALAPPDATA%\\Shell'}</span>
                  </div>
                  <div><b>安全特性：</b>Windows DPAPI 凭据加密、原子写入文件锁、SHA-256 主机指纹校验</div>
                </div>

                <div className="soft-card" style={{ gap: 6, fontSize: 12 }}>
                  <div style={{ fontWeight: 600 }}>常用快捷键</div>
                  <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '4px 12px' }} className="mono">
                    <span>Ctrl+K · 命令面板</span>
                    <span>Ctrl+N · 新建主机</span>
                    <span>Ctrl+B · 切换侧栏</span>
                    <span>Ctrl+, · 偏好设置</span>
                    <span>Ctrl+Shift+F · 终端搜索</span>
                    <span>Ctrl+Shift+A · AI 助手</span>
                    <span>Ctrl+Shift+C/V · 复制/粘贴</span>
                    <span>Ctrl+ +/-/0 · 字体缩放</span>
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
