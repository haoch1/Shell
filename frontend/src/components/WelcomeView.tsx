import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Check, ChevronDown } from 'lucide-react';
import type { LocalShellProfile, Profile } from '../types/shell';
import { formatHostDisplay, parseQuickConnect } from '../lib/utils';
import { HostIcon } from './HostIcon';

interface WelcomeViewProps {
  profiles: Profile[];
  localShells?: LocalShellProfile[];
  defaultLocalShellId?: string;
  hideHostIp?: boolean;
  onConnect: (profile: Profile) => void;
  onQuickConnect: (target: { user: string; host: string; port: number }) => void;
  onOpenLocalTerminal?: (shellId?: string) => void;
  onNewConnection: () => void;
  onImportSSH: () => void;
  onClearRecent?: () => void;
  onOpenKeys: () => void;
  onOpenSettings: () => void;
  onOpenAI: () => void;
  onOpenPalette: () => void;
}

export const WelcomeView: React.FC<WelcomeViewProps> = ({
  profiles,
  localShells = [],
  defaultLocalShellId,
  hideHostIp = false,
  onConnect,
  onQuickConnect,
  onOpenLocalTerminal,
  onNewConnection,
  onImportSSH,
  onClearRecent,
}) => {
  const [quickInput, setQuickInput] = useState('');
  const [quickErr, setQuickErr] = useState('');
  const [shellMenuOpen, setShellMenuOpen] = useState(false);
  const localPillRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!shellMenuOpen) return;
    const onMouseDown = (e: MouseEvent) => {
      if (localPillRef.current && !localPillRef.current.contains(e.target as Node)) {
        setShellMenuOpen(false);
      }
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        setShellMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', onMouseDown);
    window.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onMouseDown);
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [shellMenuOpen]);

  const defaultShell = useMemo(() => {
    if (!localShells.length) return undefined;
    if (defaultLocalShellId) {
      const matched = localShells.find((s) => s.id === defaultLocalShellId);
      if (matched) return matched;
    }
    return localShells.find((s) => s.isDefault) ?? localShells[0];
  }, [localShells, defaultLocalShellId]);

  const winShells = useMemo(() => localShells.filter((s) => s.category !== 'wsl'), [localShells]);
  const wslShells = useMemo(() => localShells.filter((s) => s.category === 'wsl'), [localShells]);

  const recentHosts = useMemo(() => {
    return profiles
      .filter((p) => Boolean(p.lastUsedAt && p.lastUsedAt > 0))
      .sort((a, b) => (b.lastUsedAt ?? 0) - (a.lastUsedAt ?? 0))
      .slice(0, 6);
  }, [profiles]);

  const submitQuick = (e: React.FormEvent) => {
    e.preventDefault();
    const parsed = parseQuickConnect(quickInput);
    if (!parsed) {
      setQuickErr('请输入有效地址，如 root@10.0.0.8 或 deploy@host:2222');
      return;
    }
    setQuickErr('');
    onQuickConnect(parsed);
    setQuickInput('');
  };

  const renderShellMenu = () => {
    if (!shellMenuOpen || localShells.length <= 1) return null;
    const renderItem = (sh: LocalShellProfile) => {
      const isDef = defaultShell?.id === sh.id;
      return (
        <button
          key={sh.id}
          type="button"
          role="menuitem"
          className={`context-menu-item local-shell-item ${isDef ? 'active-row' : ''}`}
          onClick={() => {
            setShellMenuOpen(false);
            onOpenLocalTerminal?.(sh.id);
          }}
        >
          <HostIcon iconOverride={sh.icon} size="xs" />
          <span className="local-shell-name">{sh.name}</span>
          {isDef && <Check size={12} className="local-shell-check" />}
        </button>
      );
    };
    return (
      <div id="welcomeLocalShellMenu" role="menu" className="context-menu local-shell-menu welcome-local-menu">
        {winShells.map(renderItem)}
        {winShells.length > 0 && wslShells.length > 0 && <div className="context-menu-sep" />}
        {wslShells.map(renderItem)}
      </div>
    );
  };

  return (
    <div className="welcome-canvas">
      <div className="welcome-inner">
        <div className="welcome-hero">
          <div className="welcome-app-icon">
            &gt;<span>_</span>
          </div>
          <h1>Shell</h1>
        </div>

        <form className="quick-connect-form" onSubmit={submitQuick}>
          <input
            id="quickConnectInput"
            placeholder="ssh user@host -p 22"
            value={quickInput}
            onChange={(e) => {
              setQuickInput(e.target.value);
              if (quickErr) setQuickErr('');
            }}
            spellCheck={false}
          />
          <button type="submit" className="btn btn-primary btn-sm">
            连接
          </button>
        </form>
        {quickErr && <p className="error-text" style={{ marginTop: -14 }}>{quickErr}</p>}

        {profiles.length === 0 ? (
          <div className="quick-actions-row">
            <button id="firstConnect" type="button" className="btn btn-primary btn-sm" onClick={onNewConnection}>
              新建主机
            </button>
            <button type="button" className="btn btn-sm" onClick={onImportSSH}>
              导入配置
            </button>
            {onOpenLocalTerminal && (
              <div ref={localPillRef} className={`welcome-local-pill compact ${shellMenuOpen ? 'open' : ''}`}>
                <button
                  id="welcomeLocalTermBtn"
                  type="button"
                  className="welcome-local-main"
                  onClick={() => {
                    setShellMenuOpen(false);
                    onOpenLocalTerminal(defaultShell?.id);
                  }}
                >
                  <HostIcon iconOverride={defaultShell?.icon || 'powershell'} size="xs" />
                  <span className="welcome-local-label">本地终端</span>
                </button>
                {localShells.length > 1 && (
                  <>
                    <span className="welcome-local-sep" aria-hidden="true" />
                    <button
                      id="welcomeLocalShellMenuBtn"
                      type="button"
                      className="welcome-local-caret"
                      aria-label="切换本地终端"
                      aria-expanded={shellMenuOpen}
                      aria-haspopup="menu"
                      onClick={() => setShellMenuOpen((v) => !v)}
                    >
                      <ChevronDown size={12} strokeWidth={2} />
                    </button>
                  </>
                )}
                {renderShellMenu()}
              </div>
            )}
          </div>
        ) : (
          <>
            {onOpenLocalTerminal && (
              <div className="welcome-local-bar">
                <div ref={localPillRef} className={`welcome-local-pill ${shellMenuOpen ? 'open' : ''}`}>
                  <button
                    id="welcomeLocalTermBtn"
                    type="button"
                    className="welcome-local-main"
                    title="打开默认本地终端 (Ctrl+Shift+T)"
                    onClick={() => {
                      setShellMenuOpen(false);
                      onOpenLocalTerminal(defaultShell?.id);
                    }}
                  >
                    <HostIcon iconOverride={defaultShell?.icon || 'powershell'} size="xs" />
                    <span className="welcome-local-label">本地终端</span>
                    {defaultShell?.name && <span className="welcome-local-shell">{defaultShell.name}</span>}
                  </button>
                  {localShells.length > 1 && (
                    <>
                      <span className="welcome-local-sep" aria-hidden="true" />
                      <button
                        id="welcomeLocalShellMenuBtn"
                        type="button"
                        className="welcome-local-caret"
                        title="选择本机 Shell / WSL"
                        aria-label="选择本机 Shell 或 WSL"
                        aria-expanded={shellMenuOpen}
                        aria-haspopup="menu"
                        onClick={() => setShellMenuOpen((v) => !v)}
                      >
                        <ChevronDown size={12.5} strokeWidth={2} />
                      </button>
                    </>
                  )}
                  {renderShellMenu()}
                </div>
              </div>
            )}

            {recentHosts.length > 0 && (
              <div className="recent-section">
                <div className="recent-head">
                  <div className="recent-title">最近使用</div>
                  {onClearRecent && (
                    <button
                      id="clearRecentBtn"
                      type="button"
                      className="recent-clear-btn"
                      onClick={onClearRecent}
                    >
                      清空
                    </button>
                  )}
                </div>
                <div className="recent-grid">
                  {recentHosts.map((p) => {
                    const displayName =
                      p.name && (!hideHostIp || p.name !== p.host)
                        ? p.name
                        : hideHostIp
                          ? p.user || 'SSH 主机'
                          : formatHostDisplay(p.host, p.port);
                    return (
                      <button
                        key={p.id}
                        type="button"
                        className="recent-card"
                        title={hideHostIp ? displayName : `${p.user}@${p.host}:${p.port}`}
                        onClick={() => onConnect(p)}
                      >
                        <HostIcon profile={p} size="lg" />
                        <div className="recent-card-meta">
                          <div className="recent-card-name">{displayName}</div>
                          <div
                            className={`recent-card-sub mono ip-collapsible ${hideHostIp ? 'is-hidden' : ''}`}
                            data-ip-hidden={hideHostIp ? 'true' : undefined}
                          >
                            {hideHostIp ? '' : formatHostDisplay(p.host, p.port)}
                          </div>
                        </div>
                      </button>
                    );
                  })}
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
};
