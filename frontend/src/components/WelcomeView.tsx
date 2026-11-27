import React, { useMemo, useState } from 'react';
import { ArrowRight, Download, Plus } from 'lucide-react';
import type { Profile } from '../types/shell';
import { formatHostDisplay, parseQuickConnect } from '../lib/utils';
import { HostIcon } from './HostIcon';

interface WelcomeViewProps {
  profiles: Profile[];
  onConnect: (profile: Profile) => void;
  onQuickConnect: (target: { user: string; host: string; port: number }) => void;
  onNewConnection: () => void;
  onImportSSH: () => void;
  onOpenKeys: () => void;
  onOpenSettings: () => void;
  onOpenAI: () => void;
  onOpenPalette: () => void;
}

export const WelcomeView: React.FC<WelcomeViewProps> = ({
  profiles,
  onConnect,
  onQuickConnect,
  onNewConnection,
  onImportSSH,
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
            <span>连接</span>
            <ArrowRight size={13} />
          </button>
        </form>
        {quickErr && <p className="error-text" style={{ marginTop: -14 }}>{quickErr}</p>}

        {profiles.length === 0 ? (
          <div className="quick-actions-row">
            <button id="firstConnect" type="button" className="btn btn-primary btn-sm" onClick={onNewConnection}>
              <Plus size={14} />
              新建主机
            </button>
            <button type="button" className="btn btn-sm" onClick={onImportSSH}>
              <Download size={13} />
              导入配置
            </button>
          </div>
        ) : recentHosts.length > 0 ? (
          <div className="recent-section">
            <div className="recent-title">最近使用</div>
            <div className="recent-grid">
              {recentHosts.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  className="recent-card"
                  title={`${p.user}@${p.host}:${p.port}`}
                  onClick={() => onConnect(p)}
                >
                  <HostIcon profile={p} size="lg" />
                  <div className="recent-card-meta">
                    <div className="recent-card-name">{p.name}</div>
                    <div className="recent-card-sub mono">
                      {formatHostDisplay(p.host, p.port)}
                    </div>
                  </div>
                </button>
              ))}
            </div>
          </div>
        ) : null}
      </div>
    </div>
  );
};
