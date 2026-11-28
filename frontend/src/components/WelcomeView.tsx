import React, { useMemo, useState } from 'react';
import type { Profile } from '../types/shell';
import { formatHostDisplay, parseQuickConnect } from '../lib/utils';
import { HostIcon } from './HostIcon';

interface WelcomeViewProps {
  profiles: Profile[];
  hideHostIp?: boolean;
  onConnect: (profile: Profile) => void;
  onQuickConnect: (target: { user: string; host: string; port: number }) => void;
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
  hideHostIp = false,
  onConnect,
  onQuickConnect,
  onNewConnection,
  onImportSSH,
  onClearRecent,
}) => {
  const [quickInput, setQuickInput] = useState('');
  const [quickErr, setQuickErr] = useState('');

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
          </div>
        ) : (
          recentHosts.length > 0 && (
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
          )
        )}
      </div>
    </div>
  );
};
