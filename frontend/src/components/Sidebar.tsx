import React, { useEffect, useMemo, useState } from 'react';
import {
  ArrowUpDown,
  Check,
  CheckSquare,
  ChevronDown,
  ChevronRight,
  Copy,
  CopyPlus,
  Download,
  Eye,
  EyeOff,
  KeyRound,
  Moon,
  Pencil,
  Pin,
  Plus,
  Search,
  Settings,
  Sun,
  Terminal,
  Trash2,
  Upload,
} from 'lucide-react';
import type { Profile, SessionRecord, WorkspaceTab } from '../types/shell';
import { connectedProfilePanes, type PaneSource } from '../lib/paneLayout';
import { copyToClipboard } from '../lib/bridge';
import { formatHostDisplay, parseQuickConnect } from '../lib/utils';
import { HostIcon } from './HostIcon';

type HostSortMode = 'default' | 'name-asc' | 'name-desc' | 'recent' | 'host';

const SORT_OPTIONS: { id: HostSortMode; label: string }[] = [
  { id: 'default', label: '默认顺序' },
  { id: 'name-asc', label: '名称 A → Z' },
  { id: 'name-desc', label: '名称 Z → A' },
  { id: 'recent', label: '最近使用' },
  { id: 'host', label: '主机地址' },
];

function loadSortMode(): HostSortMode {
  try {
    const v = localStorage.getItem('shell.sidebar.sort') as HostSortMode | null;
    if (v && SORT_OPTIONS.some((o) => o.id === v)) return v;
  } catch {
    // ignore storage restrictions
  }
  return 'default';
}

interface SidebarProps {
  open: boolean;
  profiles: Profile[];
  sessions: Record<string, SessionRecord>;
  tabs: WorkspaceTab[];
  activeTabId: string | null;
  onDragPane: (event: React.DragEvent, source: PaneSource) => void;
  activeProfileId?: string;
  isDark?: boolean;
  hideHostIp?: boolean;
  hasUpdate?: boolean;
  onConnect: (profile: Profile) => void;
  onConnectNewSession?: (profile: Profile) => void;
  onQuickConnect: (target: { user: string; host: string; port: number }) => void;
  onAddHost: () => void;
  onEditHost: (profile: Profile) => void;
  onDuplicateHost: (profile: Profile) => void;
  onDeleteHost: (profile: Profile) => void;
  onBatchDeleteHosts: (ids: string[]) => Promise<void>;
  onTogglePin: (profile: Profile) => void;
  onOpenImportSSH: () => void;
  onOpenExportHosts: (ids: string[]) => void;
  onOpenSettings: (tab?: 'appearance' | 'ssh' | 'keys' | 'ai' | 'agent' | 'backup' | 'about') => void;
  onToggleHideIp?: () => void;
  onToggleTheme?: () => void;
  onToast?: (msg: string) => void;
}

export const Sidebar: React.FC<SidebarProps> = React.memo(({
  open,
  profiles,
  sessions,
  tabs,
  activeTabId,
  onDragPane,
  activeProfileId,
  isDark = false,
  hideHostIp = false,
  hasUpdate = false,
  onConnect,
  onConnectNewSession,
  onQuickConnect,
  onAddHost,
  onEditHost,
  onDuplicateHost,
  onDeleteHost,
  onBatchDeleteHosts,
  onTogglePin,
  onOpenImportSSH,
  onOpenExportHosts,
  onOpenSettings,
  onToggleHideIp,
  onToggleTheme,
  onToast,
}) => {
  const [query, setQuery] = useState('');
  const [sortMode, setSortMode] = useState<HostSortMode>(loadSortMode);
  const [sortMenuPos, setSortMenuPos] = useState<{ left: number; top: number } | null>(null);
  const [collapsedGroups, setCollapsedGroups] = useState<Record<string, boolean>>({});
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; profile: Profile } | null>(null);
  const [selectMode, setSelectMode] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Record<string, boolean>>({});
  const [deletingBatch, setDeletingBatch] = useState(false);

  useEffect(() => {
    if (!sortMenuPos) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setSortMenuPos(null);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [sortMenuPos]);

  const changeSortMode = (next: HostSortMode) => {
    setSortMode(next);
    setSortMenuPos(null);
    try {
      localStorage.setItem('shell.sidebar.sort', next);
    } catch {
      // ignore
    }
  };

  const livePanes = useMemo(() => connectedProfilePanes(profiles, tabs, sessions, activeTabId), [profiles, tabs, sessions, activeTabId]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return profiles;
    return profiles.filter((p) => {
      const hay = `${p.name} ${p.host} ${p.user} ${p.group ?? ''} ${(p.tags ?? []).join(' ')}`.toLowerCase();
      return hay.includes(q);
    });
  }, [profiles, query]);

  const sections = useMemo(() => {
    const sortList = (items: Profile[]) => {
      if (sortMode === 'default') return items;
      const copy = [...items];
      copy.sort((a, b) => {
        switch (sortMode) {
          case 'name-asc':
            return a.name.localeCompare(b.name, 'zh-CN', { numeric: true, sensitivity: 'base' });
          case 'name-desc':
            return b.name.localeCompare(a.name, 'zh-CN', { numeric: true, sensitivity: 'base' });
          case 'recent':
            return (
              (b.lastUsedAt ?? 0) - (a.lastUsedAt ?? 0) ||
              a.name.localeCompare(b.name, 'zh-CN', { numeric: true, sensitivity: 'base' })
            );
          case 'host':
            return a.host.localeCompare(b.host, undefined, { numeric: true, sensitivity: 'base' });
          default:
            return 0;
        }
      });
      return copy;
    };

    const pinned = sortList(filtered.filter((p) => p.pinned));
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
      list.push({ key: label, label, items: sortList(items) });
    }
    return list;
  }, [filtered, sortMode]);

  const quickTarget = useMemo(() => {
    if (!query.trim() || filtered.length > 0) return null;
    return parseQuickConnect(query);
  }, [query, filtered.length]);

  const toggleGroup = (key: string) =>
    setCollapsedGroups((prev) => ({ ...prev, [key]: !prev[key] }));

  const chosenIds = useMemo(
    () => filtered.filter((p) => selectedIds[p.id]).map((p) => p.id),
    [filtered, selectedIds],
  );
  const exportIds = useMemo(
    () => profiles.filter((p) => selectedIds[p.id]).map((p) => p.id),
    [profiles, selectedIds],
  );

  const exitSelectMode = () => {
    setSelectMode(false);
    setSelectedIds({});
  };

  const toggleSelectAll = () => {
    if (chosenIds.length === filtered.length && filtered.length > 0) {
      setSelectedIds({});
    } else {
      const next: Record<string, boolean> = {};
      for (const p of filtered) next[p.id] = true;
      setSelectedIds(next);
    }
  };

  const commitBatchDelete = async () => {
    if (!chosenIds.length || deletingBatch) return;
    setDeletingBatch(true);
    try {
      await onBatchDeleteHosts(chosenIds);
      exitSelectMode();
    } finally {
      setDeletingBatch(false);
    }
  };

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
              if (e.key === 'Escape') {
                if (selectMode) exitSelectMode();
                else setQuery('');
              }
            }}
          />
          <span className="kbd-hint">⌃K</span>
        </div>

        <div className="sidebar-action-row">
          <button
            id="addHost"
            type="button"
            className="btn btn-primary"
            style={{ flex: 1 }}
            onClick={onAddHost}
          >
            <Plus size={13.5} strokeWidth={2} />
            <span>新建主机</span>
          </button>
          <button
            id="sortHostsBtn"
            type="button"
            className={`btn sidebar-sort-btn ${sortMode !== 'default' || sortMenuPos ? 'active' : ''}`}
            title={`排序：${SORT_OPTIONS.find((o) => o.id === sortMode)?.label}`}
            aria-label="主机排序"
            onClick={(e) => {
              e.stopPropagation();
              if (sortMenuPos) {
                setSortMenuPos(null);
                return;
              }
              const rect = e.currentTarget.getBoundingClientRect();
              const menuW = 152;
              setSortMenuPos({
                left: Math.max(8, Math.min(rect.right - menuW, window.innerWidth - menuW - 8)),
                top: rect.bottom + 6,
              });
            }}
          >
            <ArrowUpDown size={13.5} strokeWidth={1.8} />
          </button>
        </div>
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
                {formatHostDisplay(quickTarget.host, quickTarget.port)}
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
              <button type="button" className="btn btn-sm" onClick={onOpenImportSSH}>
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
                  const source = livePanes.get(p.id)?.source;
                  const isLive = !!source;
                  const checked = Boolean(selectedIds[p.id]);
                  return (
                    <div
                      key={p.id}
                      data-profile-id={p.id}
                      draggable={!selectMode && isLive}
                      onDragStart={e => {
                        if (!selectMode && source) onDragPane(e, source);
                        else e.preventDefault();
                      }}
                      className={`host-row ${isActive ? 'active' : ''} ${isLive ? 'live' : ''}`}
                      onClick={() => {
                        if (selectMode) {
                          setSelectedIds((prev) => ({ ...prev, [p.id]: !prev[p.id] }));
                        } else {
                          onConnect(p);
                        }
                      }}
                      onContextMenu={(e) => {
                        e.preventDefault();
                        setCtxMenu({ x: e.clientX, y: e.clientY, profile: p });
                      }}
                      title={`${p.user}@${hideHostIp ? formatHostDisplay(p.host, p.port, true) : `${p.host}:${p.port}`}${p.notes ? '\n' + p.notes : ''}`}
                    >
                      {selectMode && (
                        <input
                          type="checkbox"
                          checked={checked}
                          onChange={() => {}}
                          style={{ margin: 0, flexShrink: 0, cursor: 'pointer' }}
                        />
                      )}
                      <HostIcon profile={p} size="md" />
                      <div className="host-meta">
                        <div className="host-name-line">
                          <span className="host-name">{p.name}</span>
                          {isLive && !selectMode && <span className="host-live-pill">在线</span>}
                        </div>
                        <div className="host-sub mono">
                          {formatHostDisplay(p.host, p.port, hideHostIp)}
                        </div>
                      </div>
                      {!selectMode && (
                        <div className="host-actions">
                          <button
                            type="button"
                            className="host-action-btn"
                            title={hideHostIp ? '复制主机地址' : `复制地址 (${p.host})`}
                            onClick={(e) => {
                              e.stopPropagation();
                              void copyToClipboard(p.host);
                              onToast?.('已复制主机地址');
                            }}
                          >
                            <Copy size={12.5} strokeWidth={1.8} />
                          </button>
                          <button
                            type="button"
                            className="host-action-btn"
                            title="编辑主机"
                            onClick={(e) => {
                              e.stopPropagation();
                              onEditHost(p);
                            }}
                          >
                            <Pencil size={12.5} strokeWidth={1.8} />
                          </button>
                        </div>
                      )}
                    </div>
                  );
                })}
            </div>
          );
        })}
      </div>

      {selectMode ? (
        <div className="sidebar-footer batch-bar">
          <button type="button" className="btn btn-sm btn-ghost" onClick={toggleSelectAll}>
            {chosenIds.length === filtered.length && filtered.length > 0 ? '取消全选' : '全选'}
          </button>
          <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
            <button
              type="button"
              className="btn btn-sm"
              title="导出已选主机"
              aria-label="导出已选主机"
              disabled={!exportIds.length}
              onClick={() => onOpenExportHosts(exportIds)}
            >
              <Upload size={13} />
            </button>
            <button
              type="button"
              className="btn btn-sm btn-danger"
              disabled={!chosenIds.length || deletingBatch}
              onClick={() => void commitBatchDelete()}
            >
              删除 ({chosenIds.length})
            </button>
            <button type="button" className="btn btn-sm" onClick={exitSelectMode}>
              完成
            </button>
          </div>
        </div>
      ) : (
        <div className="sidebar-footer">
          <div className="sidebar-footer-group sidebar-file-actions">
            <button
              id="importSshBtn"
              type="button"
              className="icon-btn"
              title="导入主机配置"
              aria-label="导入主机配置"
              onClick={onOpenImportSSH}
            >
              <Download size={14} strokeWidth={1.7} />
            </button>
            <button
              id="exportHostsBtn"
              type="button"
              className="icon-btn"
              title="导出主机配置"
              aria-label="导出主机配置"
              onClick={() => onOpenExportHosts([])}
            >
              <Upload size={14} strokeWidth={1.7} />
            </button>
            {profiles.length > 0 && (
              <button
                type="button"
                className="icon-btn"
                title="批量管理主机"
                aria-label="批量管理"
                onClick={() => setSelectMode(true)}
              >
                <CheckSquare size={14} strokeWidth={1.7} />
              </button>
            )}
          </div>
          <div className="sidebar-footer-group">
            <button
              id="toggleHideIpBtn"
              type="button"
              className={`icon-btn ${hideHostIp ? 'active' : ''}`}
              title={hideHostIp ? '显示主机 IP' : '隐藏主机 IP（隐私模式）'}
              aria-label="切换隐藏主机 IP"
              aria-pressed={hideHostIp}
              onClick={() => onToggleHideIp?.()}
            >
              {hideHostIp ? <EyeOff size={14} strokeWidth={1.7} /> : <Eye size={14} strokeWidth={1.7} />}
            </button>
            {onToggleTheme && (
              <button
                type="button"
                className="icon-btn"
                title={isDark ? '切换浅色外观' : '切换深色外观'}
                aria-label="切换深浅色外观"
                onClick={onToggleTheme}
              >
                {isDark ? <Sun size={14} strokeWidth={1.7} /> : <Moon size={14} strokeWidth={1.7} />}
              </button>
            )}
            <button
              id="keysBtn"
              type="button"
              className="icon-btn"
              title="SSH 密钥管理"
              aria-label="SSH 密钥管理"
              onClick={() => onOpenSettings('keys')}
            >
              <KeyRound size={14} strokeWidth={1.7} />
            </button>
            <button
              id="settingsBtn"
              type="button"
              className="icon-btn"
              style={{ position: 'relative' }}
              title={hasUpdate ? '发现新版本 · 点击查看更新' : '偏好设置 (Ctrl+,)'}
              aria-label="偏好设置"
              onClick={() => onOpenSettings(hasUpdate ? 'about' : 'appearance')}
            >
              <Settings size={14} strokeWidth={1.7} />
              {hasUpdate && <span className="update-dot" />}
            </button>
          </div>
        </div>
      )}

      {sortMenuPos && (
        <>
          <div
            style={{ position: 'fixed', inset: 0, zIndex: 139 }}
            onClick={() => setSortMenuPos(null)}
            onContextMenu={(e) => {
              e.preventDefault();
              setSortMenuPos(null);
            }}
          />
          <div
            className="context-menu sort-popover"
            style={{ left: sortMenuPos.left, top: sortMenuPos.top, minWidth: 152 }}
          >
            {SORT_OPTIONS.map((opt) => (
              <button
                key={opt.id}
                type="button"
                className="context-menu-item"
                onClick={() => changeSortMode(opt.id)}
              >
                <span style={{ width: 14, display: 'inline-flex', justifyContent: 'center' }}>
                  {sortMode === opt.id && <Check size={13} />}
                </span>
                <span>{opt.label}</span>
              </button>
            ))}
          </div>
        </>
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
              left: Math.min(ctxMenu.x, window.innerWidth - 190),
              top: Math.min(ctxMenu.y, window.innerHeight - 260),
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
              {livePanes.has(ctxMenu.profile.id) ? '切换到当前会话' : '立即连接'}
            </button>
            {livePanes.has(ctxMenu.profile.id) && onConnectNewSession && (
              <button
                type="button"
                className="context-menu-item"
                onClick={() => {
                  onConnectNewSession(ctxMenu.profile);
                  setCtxMenu(null);
                }}
              >
                <CopyPlus size={13} />
                新建并行会话
              </button>
            )}
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
                void copyToClipboard(ctxMenu.profile.host);
                onToast?.('已复制主机地址');
                setCtxMenu(null);
              }}
            >
              <Copy size={13} />
              复制主机地址
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
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                setSelectMode(true);
                setSelectedIds({ [ctxMenu.profile.id]: true });
                setCtxMenu(null);
              }}
            >
              <CheckSquare size={13} />
              批量选择
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
});
