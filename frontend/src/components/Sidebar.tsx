import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowUpDown,
  Check,
  CheckSquare,
  ChevronDown,
  Copy,
  CopyPlus,
  Download,
  Eye,
  EyeOff,
  Folder,
  FolderMinus,
  GripVertical,
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
import { Checkbox } from './ui/Checkbox';
import { Tooltip } from './ui/Tooltip';

type HostSortMode = 'default' | 'name-asc' | 'name-desc' | 'recent' | 'host';

const GROUP_DRAG_MIME = 'application/x-shell-group';

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
  groupOrder?: string[];
  onReorderGroups?: (next: string[]) => void;
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
  onBatchGroupHosts?: (ids: string[], group: string) => Promise<void>;
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
  groupOrder = [],
  onReorderGroups,
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
  onBatchGroupHosts,
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
  const [addMenuPos, setAddMenuPos] = useState<{ left: number; top: number; width: number } | null>(null);
  const [collapsedGroups, setCollapsedGroups] = useState<Record<string, boolean>>({});
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; profile: Profile } | null>(null);
  const [selectMode, setSelectMode] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Record<string, boolean>>({});
  const [deletingBatch, setDeletingBatch] = useState(false);
  const [groupMenuPos, setGroupMenuPos] = useState<{ left: number; bottom: number } | null>(null);
  const [newGroupName, setNewGroupName] = useState('');
  const [groupingBatch, setGroupingBatch] = useState(false);
  const [draggingGroup, setDraggingGroup] = useState<string | null>(null);
  const [groupDropTarget, setGroupDropTarget] = useState<{ key: string; pos: 'before' | 'after' } | null>(null);
  const draggingGroupRef = useRef<string | null>(null);
  const groupInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!draggingGroup) return;
    const clearDrag = () => {
      draggingGroupRef.current = null;
      setDraggingGroup(null);
      setGroupDropTarget(null);
    };
    window.addEventListener('dragend', clearDrag, true);
    window.addEventListener('blur', clearDrag);
    return () => {
      window.removeEventListener('dragend', clearDrag, true);
      window.removeEventListener('blur', clearDrag);
    };
  }, [draggingGroup]);

  useEffect(() => {
    if (!addMenuPos && !sortMenuPos && !groupMenuPos && !selectMode) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented) return;
      if (document.querySelector('.modal-backdrop, .spotlight-backdrop, dialog[open]')) return;
      if (addMenuPos) {
        e.preventDefault();
        setAddMenuPos(null);
        return;
      }
      if (sortMenuPos) {
        e.preventDefault();
        setSortMenuPos(null);
        return;
      }
      if (groupMenuPos) {
        e.preventDefault();
        setGroupMenuPos(null);
        return;
      }
      if (selectMode) {
        e.preventDefault();
        exitSelectMode();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [addMenuPos, sortMenuPos, groupMenuPos, selectMode]);

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

  const groupOrderIndex = useMemo(() => {
    const map = new Map<string, number>();
    groupOrder.forEach((name, idx) => {
      const trimmed = name.trim();
      if (trimmed && !map.has(trimmed)) map.set(trimmed, idx);
    });
    return map;
  }, [groupOrder]);

  const sortCustomGroupNames = (names: string[]) =>
    [...names].sort((a, b) => {
      const ia = groupOrderIndex.get(a);
      const ib = groupOrderIndex.get(b);
      if (ia !== undefined && ib !== undefined) return ia - ib;
      if (ia !== undefined) return -1;
      if (ib !== undefined) return 1;
      return a.localeCompare(b, 'zh-CN');
    });

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
      const ia = groupOrderIndex.get(a[0]);
      const ib = groupOrderIndex.get(b[0]);
      if (ia !== undefined && ib !== undefined) return ia - ib;
      if (ia !== undefined) return -1;
      if (ib !== undefined) return 1;
      return a[0].localeCompare(b[0], 'zh-CN');
    });
    for (const [label, items] of sortedGroups) {
      list.push({ key: label, label, items: sortList(items) });
    }
    return list;
  }, [filtered, sortMode, groupOrderIndex]);

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
  const allSelected = filtered.length > 0 && chosenIds.length === filtered.length;
  const someSelected = chosenIds.length > 0 && chosenIds.length < filtered.length;
  const existingGroups = useMemo(
    () => sortCustomGroupNames([...new Set(profiles.map((p) => p.group?.trim()).filter((g): g is string => Boolean(g)))]),
    [profiles, groupOrderIndex],
  );

  const isGroupDrag = (e: React.DragEvent) =>
    Boolean(draggingGroupRef.current) && e.dataTransfer.types.includes(GROUP_DRAG_MIME);

  const commitGroupReorder = (draggedKey: string, targetKey: string, pos: 'before' | 'after') => {
    if (!onReorderGroups || draggedKey === targetKey) return;
    const base = existingGroups.length > 0
      ? existingGroups
      : sections.filter((s) => s.key !== '__pinned__' && s.key !== '全部主机').map((s) => s.key);
    if (!base.includes(draggedKey) || !base.includes(targetKey)) return;
    const withoutDragged = base.filter((g) => g !== draggedKey);
    const targetIdx = withoutDragged.indexOf(targetKey);
    if (targetIdx < 0) return;
    const insertIdx = pos === 'before' ? targetIdx : targetIdx + 1;
    const next = [...withoutDragged.slice(0, insertIdx), draggedKey, ...withoutDragged.slice(insertIdx)];
    if (next.length === base.length && next.every((g, i) => g === base[i])) return;
    onReorderGroups(next);
  };

  const exitSelectMode = () => {
    setSelectMode(false);
    setSelectedIds({});
    setGroupMenuPos(null);
    setNewGroupName('');
  };

  const commitBatchGroup = async (group: string) => {
    if (!chosenIds.length || groupingBatch || !onBatchGroupHosts) return;
    setGroupingBatch(true);
    try {
      await onBatchGroupHosts(chosenIds, group.trim());
      exitSelectMode();
    } finally {
      setGroupingBatch(false);
    }
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
    <aside className={`sidebar ${open ? '' : 'collapsed'} ${selectMode ? 'is-select-mode' : ''}`.trim()} aria-label="主机侧栏">
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
                if (query) {
                  e.stopPropagation();
                  setQuery('');
                } else if (groupMenuPos) {
                  e.stopPropagation();
                  setGroupMenuPos(null);
                } else if (selectMode) {
                  e.stopPropagation();
                  exitSelectMode();
                }
              }
            }}
          />
          <span className="kbd-hint">⌃K</span>
        </div>

        {selectMode ? (
          <div className="sidebar-select-header">
            <label className="sidebar-select-toggle">
              <Checkbox
                checked={allSelected}
                indeterminate={someSelected}
                onChange={toggleSelectAll}
                aria-label={allSelected ? '取消全选主机' : '全选主机'}
              />
              <span className="tabular">
                {chosenIds.length > 0 ? `已选 ${chosenIds.length}/${filtered.length}` : '全选'}
              </span>
            </label>
            <button
              type="button"
              className="btn btn-sm btn-ghost sidebar-select-done"
              onClick={exitSelectMode}
            >
              完成
            </button>
          </div>
        ) : (
          <div className="sidebar-action-row">
            <div className={`sidebar-new-split ${addMenuPos ? 'open' : ''}`}>
              <button
                id="addHost"
                type="button"
                className="btn sidebar-new-btn"
                onClick={() => {
                  setAddMenuPos(null);
                  setSortMenuPos(null);
                  onAddHost();
                }}
              >
                <Plus size={14} strokeWidth={2.2} className="sidebar-new-plus" />
                <span>新建主机</span>
              </button>
              <span className="sidebar-new-divider" aria-hidden="true" />
              <Tooltip label="导入与导出主机" side="bottom">
                <button
                  id="addHostMenuBtn"
                  type="button"
                  className={`sidebar-new-caret ${addMenuPos ? 'open' : ''}`}
                  aria-label="更多主机操作"
                  aria-expanded={Boolean(addMenuPos)}
                  aria-haspopup="menu"
                  onClick={(e) => {
                    e.stopPropagation();
                    setSortMenuPos(null);
                    if (addMenuPos) {
                      setAddMenuPos(null);
                      return;
                    }
                    const splitEl = e.currentTarget.closest('.sidebar-new-split');
                    const rect = (splitEl ?? e.currentTarget).getBoundingClientRect();
                    const menuW = Math.max(Math.round(rect.width), 160);
                    setAddMenuPos({
                      left: Math.max(8, Math.min(rect.left, window.innerWidth - menuW - 8)),
                      top: rect.bottom + 6,
                      width: menuW,
                    });
                  }}
                >
                  <ChevronDown size={13.5} strokeWidth={2.1} />
                </button>
              </Tooltip>
            </div>
            <Tooltip label={`排序：${SORT_OPTIONS.find((o) => o.id === sortMode)?.label}`} side="bottom">
              <button
                id="sortHostsBtn"
                type="button"
                className={`btn sidebar-sort-btn ${sortMenuPos ? 'open' : ''}`}
                aria-label="主机排序"
                aria-expanded={Boolean(sortMenuPos)}
                onClick={(e) => {
                  e.stopPropagation();
                  setAddMenuPos(null);
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
            </Tooltip>
            {profiles.length > 0 && (
              <Tooltip label="批量管理主机" side="bottom">
                <button
                  id="batchManageBtn"
                  type="button"
                  className="btn sidebar-sort-btn"
                  aria-label="批量管理"
                  onClick={() => {
                    setAddMenuPos(null);
                    setSortMenuPos(null);
                    setSelectMode(true);
                  }}
                >
                  <CheckSquare size={13.5} strokeWidth={1.8} />
                </button>
              </Tooltip>
            )}
          </div>
        )}
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
          const isCustomGroup = sec.key !== '__pinned__' && sec.key !== '全部主机';
          const canDragGroup = isCustomGroup && !selectMode && Boolean(onReorderGroups);
          const isDragging = draggingGroup === sec.key;
          const dropClass =
            groupDropTarget?.key === sec.key
              ? groupDropTarget.pos === 'before'
                ? 'drop-before'
                : 'drop-after'
              : '';
          return (
            <div
              key={sec.key}
              data-group-key={sec.key}
              className={`group-section ${isDragging ? 'dragging' : ''} ${dropClass}`.trim()}
              onDragOver={(e) => {
                if (!isCustomGroup || !isGroupDrag(e)) return;
                const src = draggingGroupRef.current;
                if (!src || src === sec.key) {
                  setGroupDropTarget((cur) => (cur ? null : cur));
                  return;
                }
                e.preventDefault();
                e.stopPropagation();
                e.dataTransfer.dropEffect = 'move';
                const rect = e.currentTarget.getBoundingClientRect();
                const pos: 'before' | 'after' = e.clientY - rect.top < rect.height / 2 ? 'before' : 'after';
                setGroupDropTarget((cur) => (cur?.key === sec.key && cur.pos === pos ? cur : { key: sec.key, pos }));
              }}
              onDragLeave={(e) => {
                if (!isGroupDrag(e)) return;
                if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
                  setGroupDropTarget((cur) => (cur?.key === sec.key ? null : cur));
                }
              }}
              onDrop={(e) => {
                if (!isCustomGroup || !isGroupDrag(e)) return;
                e.preventDefault();
                e.stopPropagation();
                const src = e.dataTransfer.getData(GROUP_DRAG_MIME) || draggingGroupRef.current;
                const rect = e.currentTarget.getBoundingClientRect();
                const pos: 'before' | 'after' = e.clientY - rect.top < rect.height / 2 ? 'before' : 'after';
                draggingGroupRef.current = null;
                setDraggingGroup(null);
                setGroupDropTarget(null);
                if (src && src !== sec.key) {
                  commitGroupReorder(src, sec.key, pos);
                }
              }}
            >
              <button
                type="button"
                className={`group-header ${canDragGroup ? 'draggable' : ''}`.trim()}
                draggable={canDragGroup}
                title={canDragGroup ? '点击折叠，拖拽调整分组顺序' : undefined}
                onDragStart={(e) => {
                  if (!canDragGroup) {
                    e.preventDefault();
                    return;
                  }
                  e.stopPropagation();
                  draggingGroupRef.current = sec.key;
                  setDraggingGroup(sec.key);
                  e.dataTransfer.effectAllowed = 'move';
                  e.dataTransfer.setData(GROUP_DRAG_MIME, sec.key);
                }}
                onDragEnd={() => {
                  draggingGroupRef.current = null;
                  setDraggingGroup(null);
                  setGroupDropTarget(null);
                }}
                onClick={() => toggleGroup(sec.key)}
              >
                <ChevronDown size={12} className={`group-chevron ${collapsed ? 'collapsed' : ''}`} />
                <span>{sec.label}</span>
                {canDragGroup && <GripVertical size={11} className="group-drag-grip" aria-hidden="true" />}
                <span className="group-count">{sec.items.length}</span>
              </button>

              {!collapsed &&
                sec.items.map((p) => {
                  const isActive = activeProfileId === p.id;
                  const source = livePanes.get(p.id)?.source;
                  const isLive = !!source;
                  const checked = Boolean(selectedIds[p.id]);
                  const displayName =
                    p.name && (!hideHostIp || p.name !== p.host)
                      ? p.name
                      : hideHostIp
                        ? p.user || 'SSH 主机'
                        : formatHostDisplay(p.host, p.port);
                  return (
                    <div
                      key={p.id}
                      data-profile-id={p.id}
                      draggable={!selectMode && isLive}
                      onDragStart={e => {
                        if (!selectMode && source) onDragPane(e, source);
                        else e.preventDefault();
                      }}
                      className={`host-row ${hideHostIp ? 'compact' : ''} ${!selectMode && isActive ? 'active' : ''} ${selectMode && checked ? 'selected' : ''} ${isLive ? 'live' : ''}`.trim()}
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
                      title={
                        hideHostIp
                          ? `${displayName}${p.notes ? '\n' + p.notes : ''}`
                          : `${p.user}@${p.host}:${p.port}${p.notes ? '\n' + p.notes : ''}`
                      }
                    >
                      <span className="host-check-slot" aria-hidden={!selectMode}>
                        <Checkbox
                          checked={checked}
                          tabIndex={selectMode ? 0 : -1}
                          aria-label={`选择 ${displayName}`}
                          onClick={(e) => e.stopPropagation()}
                          onChange={(e) =>
                            setSelectedIds((prev) => ({ ...prev, [p.id]: e.target.checked }))
                          }
                        />
                      </span>
                      <HostIcon profile={p} size="md" />
                      <div className="host-meta">
                        <div className="host-name-line">
                          <span className="host-name">{displayName}</span>
                        </div>
                        <div
                          className={`host-sub mono ip-collapsible ${hideHostIp ? 'is-hidden' : ''}`}
                          data-ip-hidden={hideHostIp ? 'true' : undefined}
                        >
                          {hideHostIp ? '' : formatHostDisplay(p.host, p.port)}
                        </div>
                      </div>
                      {isLive && !selectMode && <span className="host-live-dot" aria-label="在线" />}
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
          <button
            id="batchGroupBtn"
            type="button"
            className="btn btn-sm batch-action-btn"
            disabled={!chosenIds.length || groupingBatch || deletingBatch}
            onClick={(e) => {
              e.stopPropagation();
              if (groupMenuPos) {
                setGroupMenuPos(null);
                return;
              }
              const rect = e.currentTarget.getBoundingClientRect();
              const menuW = 216;
              setGroupMenuPos({
                left: Math.max(8, Math.min(rect.left, window.innerWidth - menuW - 8)),
                bottom: window.innerHeight - rect.top + 6,
              });
              setNewGroupName('');
              requestAnimationFrame(() => groupInputRef.current?.focus());
            }}
          >
            <Folder size={13} strokeWidth={1.8} />
            <span>移动分组</span>
          </button>
          <button
            type="button"
            className="btn btn-sm btn-danger batch-action-btn"
            disabled={!chosenIds.length || deletingBatch || groupingBatch}
            onClick={() => void commitBatchDelete()}
          >
            <Trash2 size={13} strokeWidth={1.8} />
            <span>删除{chosenIds.length > 0 ? ` (${chosenIds.length})` : ''}</span>
          </button>
        </div>
      ) : (
        <div className="sidebar-footer">
          <Tooltip label={hasUpdate ? '发现新版本 · 查看更新' : '偏好设置'} shortcut={hasUpdate ? undefined : 'Ctrl+,'} side="top">
            <button
              id="settingsBtn"
              type="button"
              className="sidebar-footer-settings"
              aria-label="偏好设置"
              onClick={() => onOpenSettings(hasUpdate ? 'about' : 'appearance')}
            >
              <Settings size={14} strokeWidth={1.7} />
              <span>设置</span>
              {hasUpdate && <span className="update-dot inline" />}
            </button>
          </Tooltip>
          <div className="sidebar-footer-group">
            <Tooltip label={hideHostIp ? '显示主机 IP' : '隐藏主机 IP'} side="top">
              <button
                id="toggleHideIpBtn"
                type="button"
                className={`icon-btn ${hideHostIp ? 'is-muted-active' : ''}`}
                aria-label="切换隐藏主机 IP"
                aria-pressed={hideHostIp}
                onClick={() => onToggleHideIp?.()}
              >
                {hideHostIp ? <EyeOff size={14} strokeWidth={1.7} /> : <Eye size={14} strokeWidth={1.7} />}
              </button>
            </Tooltip>
            {onToggleTheme && (
              <Tooltip label={isDark ? '切换浅色外观' : '切换深色外观'} side="top">
                <button
                  type="button"
                  className="icon-btn"
                  aria-label="切换深浅色外观"
                  onClick={onToggleTheme}
                >
                  {isDark ? <Sun size={14} strokeWidth={1.7} /> : <Moon size={14} strokeWidth={1.7} />}
                </button>
              </Tooltip>
            )}
          </div>
        </div>
      )}

      {groupMenuPos && (
        <>
          <div
            style={{ position: 'fixed', inset: 0, zIndex: 139 }}
            onClick={() => setGroupMenuPos(null)}
            onContextMenu={(e) => {
              e.preventDefault();
              setGroupMenuPos(null);
            }}
          />
          <div
            id="batchGroupPopover"
            className="context-menu batch-group-popover"
            style={{
              left: groupMenuPos.left,
              bottom: groupMenuPos.bottom,
              top: 'auto',
              width: 216,
            }}
          >
            <form
              className="batch-group-form"
              onSubmit={(e) => {
                e.preventDefault();
                const trimmed = newGroupName.trim();
                if (trimmed) void commitBatchGroup(trimmed);
              }}
            >
              <input
                ref={groupInputRef}
                id="batchGroupInput"
                className="input"
                placeholder="输入新分组名…"
                value={newGroupName}
                disabled={groupingBatch}
                onChange={(e) => setNewGroupName(e.target.value)}
              />
              <button
                type="submit"
                className="btn btn-primary btn-sm"
                disabled={!newGroupName.trim() || groupingBatch}
              >
                确定
              </button>
            </form>
            {existingGroups.length > 0 && (
              <>
                <div className="context-menu-sep" />
                <div className="batch-group-list">
                  {existingGroups.map((g) => (
                    <button
                      key={g}
                      type="button"
                      className="context-menu-item"
                      disabled={groupingBatch}
                      onClick={() => void commitBatchGroup(g)}
                    >
                      <Folder size={13} />
                      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {g}
                      </span>
                    </button>
                  ))}
                </div>
              </>
            )}
            <div className="context-menu-sep" />
            <button
              type="button"
              className="context-menu-item"
              disabled={groupingBatch}
              onClick={() => void commitBatchGroup('')}
            >
              <FolderMinus size={13} />
              <span>移出分组（默认）</span>
            </button>
          </div>
        </>
      )}

      {addMenuPos && (
        <>
          <div
            style={{ position: 'fixed', inset: 0, zIndex: 139 }}
            onClick={() => setAddMenuPos(null)}
            onContextMenu={(e) => {
              e.preventDefault();
              setAddMenuPos(null);
            }}
          />
          <div
            id="addHostMenu"
            role="menu"
            className="context-menu add-host-popover"
            style={{ left: addMenuPos.left, top: addMenuPos.top, minWidth: addMenuPos.width }}
          >
            <button
              id="importSshBtn"
              type="button"
              role="menuitem"
              className="context-menu-item"
              onClick={() => {
                setAddMenuPos(null);
                onOpenImportSSH();
              }}
            >
              <Download size={13.5} strokeWidth={1.9} className="add-host-menu-icon is-import" />
              <span>导入主机</span>
            </button>
            <button
              id="exportHostsBtn"
              type="button"
              role="menuitem"
              className="context-menu-item"
              onClick={() => {
                setAddMenuPos(null);
                onOpenExportHosts([]);
              }}
            >
              <Upload size={13.5} strokeWidth={1.9} className="add-host-menu-icon is-export" />
              <span>导出主机</span>
            </button>
          </div>
        </>
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
