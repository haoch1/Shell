import React, { useMemo, useState } from 'react';
import {
  ChevronDown,
  ChevronRight,
  Copy,
  Download,
  KeyRound,
  Pencil,
  Pin,
  Plus,
  Search,
  Settings,
  Terminal,
  Trash2,
} from 'lucide-react';
import type { Profile, SessionRecord } from '../types/shell';
import { parseQuickConnect } from '../lib/utils';
import { HostIcon } from './HostIcon';

interface SidebarProps {
  open: boolean;
  profiles: Profile[];
  sessions: Record<string, SessionRecord>;
  activeProfileId?: string;
  onConnect: (profile: Profile) => void;
  onQuickConnect: (target: { user: string; host: string; port: number }) => void;
  onAddHost: () => void;
  onEditHost: (profile: Profile) => void;
  onDuplicateHost: (profile: Profile) => void;
  onDeleteHost: (profile: Profile) => void;
  onTogglePin: (profile: Profile) => void;
  onOpenImportSSH: () => void;
  onOpenSettings: (tab?: 'appearance' | 'ssh' | 'keys' | 'ai' | 'agent' | 'backup' | 'about') => void;
}

export const Sidebar: React.FC<SidebarProps> = ({
  open,
  profiles,
  sessions,
  activeProfileId,
  onConnect,
  onQuickConnect,
  onAddHost,
  onEditHost,
  onDuplicateHost,
  onDeleteHost,
  onTogglePin,
  onOpenImportSSH,
  onOpenSettings,
}) => {
  const [query, setQuery] = useState('');
  const [collapsedGroups, setCollapsedGroups] = useState<Record<string, boolean>>({});
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; profile: Profile } | null>(null);

  const liveProfileIds = useMemo(() => {
    const set = new Set<string>();
    for (const s of Object.values(sessions)) {
      if (s.state === 'connected' && s.profile.id) set.add(s.profile.id);
    }
    return set;
  }, [sessions]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return profiles;
    return profiles.filter((p) => {
      const hay = `${p.name} ${p.host} ${p.user} ${p.group ?? ''} ${(p.tags ?? []).join(' ')}`.toLowerCase();
      return hay.includes(q);
    });
  }, [profiles, query]);

  const sections = useMemo(() => {
    const pinned = filtered.filter((p) => p.pinned);
    const groupMap = new Map<string, Profile[]>();
    for (const p of filtered) {
      if (p.pinned) continue;
      const g = p.group?.trim() || '全部主机';
      const arr = groupMap.get(g) ?? [];
      arr.push(p);
      groupMap.set(g, arr);
    }
    const list: { key: string; label: string; items: Profile[] }[] = [];
    if (pinned.length > 0) {
      list.push({ key: '__pinned__', label: '置顶', items: pinned });
    }
    const sortedGroups = [...groupMap.entries()].sort((a, b) => {
      if (a[0] === '全部主机') return 1;
      if (b[0] === '全部主机') return -1;
      return a[0].localeCompare(b[0], 'zh-CN');
    });
    for (const [label, items] of sortedGroups) {
      list.push({ key: label, label, items });
    }
    return list;
  }, [filtered]);

  const quickTarget = useMemo(() => {
    if (!query.trim() || filtered.length > 0) return null;
    return parseQuickConnect(query);
  }, [query, filtered.length]);

  const toggleGroup = (key: string) =>
    setCollapsedGroups((prev) => ({ ...prev, [key]: !prev[key] }));

  return (
    <aside className={`sidebar ${open ? '' : 'collapsed'}`} aria-label="主机侧栏">
      <div className="sidebar-top">
        <div className="search-box">
          <Search size={13} strokeWidth={1.8} />
          <input
            className="input"
            placeholder="搜索主机…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                if (filtered.length === 1) {
                  onConnect(filtered[0]);
                } else if (quickTarget) {
                  onQuickConnect(quickTarget);
                  setQuery('');
                }
              }
              if (e.key === 'Escape') setQuery('');
            }}
          />
          <span className="kbd-hint">⌃K</span>
        </div>

        <button
          id="addHost"
          type="button"
          className="btn btn-primary btn-sm"
          style={{ width: '100%' }}
          onClick={onAddHost}
        >
          <Plus size={14} strokeWidth={2} />
          <span>新建主机</span>
        </button>
      </div>

      <div className="sidebar-list" onClick={() => setCtxMenu(null)}>
        {quickTarget && (
          <div
            className="host-row active"
            onClick={() => {
              onQuickConnect(quickTarget);
              setQuery('');
            }}
          >
            <Terminal size={14} style={{ color: 'var(--accent)', flexShrink: 0 }} />
            <div className="host-meta">
              <div className="host-name">快速连接</div>
              <div className="host-sub mono">
                {quickTarget.user}@{quickTarget.host}:{quickTarget.port}
              </div>
            </div>
          </div>
        )}

        {sections.length === 0 && !quickTarget && (
          <div className="sidebar-empty">
            <Terminal size={24} strokeWidth={1.4} style={{ opacity: 0.45 }} />
            <div style={{ fontSize: 12.5 }}>
              {profiles.length === 0 ? '暂无保存的主机' : '未找到匹配主机'}
            </div>
            {profiles.length === 0 && (
              <button
                type="button"
                className="btn btn-sm"
                onClick={onOpenImportSSH}
              >
                <Download size={13} />
                导入主机配置
              </button>
            )}
          </div>
        )}

        {sections.map((sec) => {
          const collapsed = Boolean(collapsedGroups[sec.key]);
          return (
            <div key={sec.key} className="group-section">
              <button
                type="button"
                className="group-header"
                onClick={() => toggleGroup(sec.key)}
              >
                {collapsed ? <ChevronRight size={12} /> : <ChevronDown size={12} />}
                <span>{sec.label}</span>
                <span className="group-count">{sec.items.length}</span>
              </button>

              {!collapsed &&
                sec.items.map((p) => {
                  const isActive = activeProfileId === p.id;
                  const isLive = liveProfileIds.has(p.id);
                  return (
                    <div
                      key={p.id}
                      className={`host-row ${isActive ? 'active' : ''}`}
                      onClick={() => onConnect(p)}
                      onContextMenu={(e) => {
                        e.preventDefault();
                        setCtxMenu({ x: e.clientX, y: e.clientY, profile: p });
                      }}
                      title={`${p.user}@${p.host}:${p.port}${p.notes ? '\n' + p.notes : ''}`}
                    >
                      <HostIcon profile={p} size="md" isLive={isLive} />
                      <div className="host-meta">
                        <div className="host-name-line">
                          <span className="host-name">{p.name}</span>
                        </div>
                        <div className="host-sub mono">
                          {p.user}@{p.host}
                          {p.port !== 22 ? `:${p.port}` : ''}
                        </div>
                      </div>
                      <div className="host-actions">
                        <button
                          type="button"
                          className="host-action-btn"
                          title="编辑主机"
                          onClick={(e) => {
                            e.stopPropagation();
                            onEditHost(p);
                          }}
                        >
                          <Pencil size={12} strokeWidth={1.8} />
                        </button>
                      </div>
                    </div>
                  );
                })}
            </div>
          );
        })}
      </div>

      <div className="sidebar-footer">
        <button
          id="importSshBtn"
          type="button"
          className="icon-btn"
          title="从 Xshell / MobaXterm / Termius / OpenSSH 等导入配置"
          onClick={onOpenImportSSH}
        >
          <Download size={14} strokeWidth={1.7} />
          <span>导入</span>
        </button>
        <div style={{ display: 'flex', gap: 2 }}>
          <button
            id="keysBtn"
            type="button"
            className="icon-btn"
            title="SSH 密钥管理"
            aria-label="密钥管理"
            onClick={() => onOpenSettings('keys')}
          >
            <KeyRound size={14} strokeWidth={1.7} />
          </button>
          <button
            id="settingsBtn"
            type="button"
            className="icon-btn"
            title="偏好设置 (Ctrl+,)"
            aria-label="偏好设置"
            onClick={() => onOpenSettings('appearance')}
          >
            <Settings size={14} strokeWidth={1.7} />
          </button>
        </div>
      </div>

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
              left: Math.min(ctxMenu.x, window.innerWidth - 190),
              top: Math.min(ctxMenu.y, window.innerHeight - 210),
            }}
          >
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onConnect(ctxMenu.profile);
                setCtxMenu(null);
              }}
            >
              <Terminal size={13} />
              立即连接
            </button>
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onEditHost(ctxMenu.profile);
                setCtxMenu(null);
              }}
            >
              <Pencil size={13} />
              编辑资料
            </button>
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onTogglePin(ctxMenu.profile);
                setCtxMenu(null);
              }}
            >
              <Pin size={13} />
              {ctxMenu.profile.pinned ? '取消置顶' : '置顶主机'}
            </button>
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onDuplicateHost(ctxMenu.profile);
                setCtxMenu(null);
              }}
            >
              <Copy size={13} />
              复制配置
            </button>
            <div className="context-menu-sep" />
            <button
              type="button"
              className="context-menu-item danger"
              onClick={() => {
                onDeleteHost(ctxMenu.profile);
                setCtxMenu(null);
              }}
            >
              <Trash2 size={13} />
              删除主机
            </button>
          </div>
        </>
      )}
    </aside>
  );
};
