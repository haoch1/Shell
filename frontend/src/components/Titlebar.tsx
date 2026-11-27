import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowLeftRight,
  ArrowUpRight,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Code2,
  Columns2,
  Command,
  CopyPlus,
  FolderOpen,
  PanelLeft,
  Plus,
  Radio,
  Search,
  Sparkles,
  SplitSquareHorizontal,
  SplitSquareVertical,
  X,
} from 'lucide-react';
import type { DrawerMode, Profile, SessionRecord, WorkspaceTab } from '../types/shell';
import { collectLeaves, formatHostDisplay } from '../lib/utils';
import { getRuntime } from '../lib/bridge';
import { HostIcon } from './HostIcon';
import { Tooltip } from './ui/Tooltip';
import type { usePaneDrag } from '../lib/usePaneDrag';

function profileDisplayName(profile?: Profile, hideHostIp = false): string {
  if (!profile) return '';
  if (profile.name && (!hideHostIp || profile.name !== profile.host)) {
    return profile.name;
  }
  if (hideHostIp) {
    return profile.user || 'SSH 主机';
  }
  return formatHostDisplay(profile.host) || profile.name || profile.host;
}

interface TitlebarProps {
  paneDrag: ReturnType<typeof usePaneDrag>;
  sidebarOpen: boolean;
  onToggleSidebar: () => void;
  profiles: Profile[];
  tabs: WorkspaceTab[];
  activeTabId: string | null;
  sessions: Record<string, SessionRecord>;
  drawer: DrawerMode;
  hideHostIp?: boolean;
  onSelectTab: (tabId: string) => void;
  onCloseTab: (tabId: string) => void;
  onCloseOtherTabs: (tabId: string) => void;
  onCloseAllTabs: () => void;
  onCloneTab: (tabId: string) => void;
  onDetachPane: (tabId: string, paneId: string) => void;
  onNewConnection: () => void;
  onSplit: (dir: 'horizontal' | 'vertical', tabId?: string) => void;
  onToggleBroadcast: () => void;
  onOpenSearch: () => void;
  onToggleDrawer: (mode: DrawerMode) => void;
  onOpenPalette: () => void;
}

export const Titlebar: React.FC<TitlebarProps> = ({
  paneDrag,
  sidebarOpen,
  onToggleSidebar,
  profiles,
  tabs,
  activeTabId,
  sessions,
  drawer,
  hideHostIp = false,
  onSelectTab,
  onCloseTab,
  onCloseOtherTabs,
  onCloseAllTabs,
  onCloneTab,
  onDetachPane,
  onNewConnection,
  onSplit,
  onToggleBroadcast,
  onOpenSearch,
  onToggleDrawer,
  onOpenPalette,
}) => {
  const [tabMenu, setTabMenu] = useState<{ x: number; y: number; tabId: string } | null>(null);
  const [allTabsMenu, setAllTabsMenu] = useState<{ left: number; top: number } | null>(null);
  const [tabFilter, setTabFilter] = useState('');
  const [maximised, setMaximised] = useState(false);
  const [canScrollLeft, setCanScrollLeft] = useState(false);
  const [canScrollRight, setCanScrollRight] = useState(false);
  const stripRef = useRef<HTMLDivElement>(null);
  const tabFilterRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!allTabsMenu) {
      setTabFilter('');
      return;
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        setAllTabsMenu(null);
      }
    };
    window.addEventListener('keydown', onKey);
    const frame = requestAnimationFrame(() => tabFilterRef.current?.focus());
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener('keydown', onKey);
    };
  }, [allTabsMenu]);

  const profileById = useMemo(() => {
    const map = new Map<string, Profile>();
    for (const p of profiles) map.set(p.id, p);
    return map;
  }, [profiles]);

  const activeTab = tabs.find((t) => t.id === activeTabId) ?? null;
  const activeLeaves = activeTab ? collectLeaves(activeTab.root) : [];
  const activeLeaf = activeLeaves.find((l) => l.paneId === activeTab?.activePaneId) ?? activeLeaves[0];
  const activeSession = activeLeaf ? sessions[activeLeaf.sessionId] : undefined;
  const isConnected = activeSession?.state === 'connected';
  const isMultiPane = activeLeaves.length > 1;
  const rt = getRuntime();

  const updateScrollState = useCallback(() => {
    const el = stripRef.current;
    if (!el) {
      setCanScrollLeft(false);
      setCanScrollRight(false);
      return;
    }
    setCanScrollLeft(el.scrollLeft > 4);
    setCanScrollRight(el.scrollLeft + el.clientWidth < el.scrollWidth - 4);
  }, []);

  useEffect(() => {
    updateScrollState();
    const el = stripRef.current;
    if (!el) return;
    const ro = new ResizeObserver(updateScrollState);
    ro.observe(el);
    return () => ro.disconnect();
  }, [tabs.length, updateScrollState]);

  // Smoothly scroll the active tab into view whenever switching or opening a new tab.
  useEffect(() => {
    if (!activeTabId) return;
    const el = stripRef.current;
    if (!el) return;
    const node = el.querySelector<HTMLElement>(`[data-tab-id="${activeTabId}"]`);
    if (node) {
      node.scrollIntoView({ behavior: 'smooth', inline: 'nearest', block: 'nearest' });
    }
    const timer = window.setTimeout(updateScrollState, 160);
    return () => window.clearTimeout(timer);
  }, [activeTabId, tabs.length, updateScrollState]);

  const scrollTabsBy = (delta: number) => {
    const el = stripRef.current;
    if (!el) return;
    el.scrollBy({ left: delta, behavior: 'smooth' });
  };

  const syncMaximised = useCallback(() => {
    if (!rt?.WindowIsMaximised) return;
    void rt
      .WindowIsMaximised()
      .then((val) => setMaximised(Boolean(val)))
      .catch(() => {});
  }, [rt]);

  useEffect(() => {
    syncMaximised();
    let timer: number | null = null;
    const onResize = () => {
      if (timer !== null) window.clearTimeout(timer);
      timer = window.setTimeout(syncMaximised, 80);
    };
    window.addEventListener('resize', onResize);
    return () => {
      if (timer !== null) window.clearTimeout(timer);
      window.removeEventListener('resize', onResize);
    };
  }, [syncMaximised]);

  const handleToggleMaximise = () => {
    rt?.WindowToggleMaximise();
    setMaximised((v) => !v);
    window.setTimeout(syncMaximised, 120);
  };

  const tabState = (tab: WorkspaceTab): 'connected' | 'connecting' | 'closed' => {
    const leaves = collectLeaves(tab.root);
    const states = leaves.map((l) => sessions[l.sessionId]?.state ?? 'closed');
    if (states.includes('connected')) return 'connected';
    if (states.includes('connecting')) return 'connecting';
    return 'closed';
  };

  const sessionProfile = (sessionId: string): Profile | undefined => {
    const sess = sessions[sessionId];
    if (!sess) return undefined;
    // Prefer latest profile from profiles state so auto-detected OS icons reflect immediately.
    return (sess.profile.id ? profileById.get(sess.profile.id) : undefined) ?? sess.profile;
  };

  const tabInfo = (tab: WorkspaceTab) => {
    const leaves = collectLeaves(tab.root);
    const activeLeafForTab = leaves.find((l) => l.paneId === tab.activePaneId) ?? leaves[0];
    const prof = activeLeafForTab ? sessionProfile(activeLeafForTab.sessionId) : undefined;
    const isSplit = leaves.length > 1;
    const title = isSplit
      ? leaves.map((l) => profileDisplayName(sessionProfile(l.sessionId), hideHostIp) || '终端').join(' / ')
      : profileDisplayName(prof, hideHostIp) || tab.title;
    return { leaves, prof, isSplit, title };
  };

  const hasOverflow = canScrollLeft || canScrollRight;

  return (
    <header
      className="titlebar"
      onDoubleClick={(e) => {
        if ((e.target as HTMLElement).closest('button, [role="tab"]')) return;
        handleToggleMaximise();
      }}
    >
      <div className="titlebar-group">
        <Tooltip label="切换侧栏" shortcut="Ctrl+B" side="bottom">
          <button
            id="toggleSidebar"
            type="button"
            className={`icon-btn ${sidebarOpen ? 'active' : ''}`}
            aria-label="切换侧栏"
            onClick={onToggleSidebar}
          >
            <PanelLeft size={14} strokeWidth={1.7} />
          </button>
        </Tooltip>
        <div className="brand-pill">
          <span className="brand-icon">&gt;<span>_</span></span>
          <span>Shell</span>
        </div>
      </div>

      <div className="tab-bar-shell">
        {canScrollLeft && (
          <button
            type="button"
            className="tab-scroll-btn"
            title="向左滚动标签"
            aria-label="向左滚动标签"
            onClick={() => scrollTabsBy(-260)}
          >
            <ChevronLeft size={14} />
          </button>
        )}

        <div
          ref={stripRef}
          className="tab-strip"
          role="tablist"
          aria-label="会话标签"
          onScroll={updateScrollState}
          onWheel={(e) => {
            const el = stripRef.current;
            if (!el || el.scrollWidth <= el.clientWidth) return;
            if (Math.abs(e.deltaY) > Math.abs(e.deltaX)) {
              e.preventDefault();
              el.scrollLeft += e.deltaY;
              updateScrollState();
            }
          }}
        >
          {tabs.map((tab) => {
            const st = tabState(tab);
            const { leaves, prof, isSplit, title } = tabInfo(tab);
            const active = tab.id === (paneDrag.hoverTab ?? activeTabId);
            const tooltip = isSplit
              ? `${title} (${leaves.length} 个分屏)\n拖动当前会话到终端边缘分屏`
              : (prof && !hideHostIp ? `${title} (${prof.user}@${formatHostDisplay(prof.host, prof.port)})` : title) + '\n拖动当前会话到终端边缘分屏';
            return (
              <div
                key={tab.id}
                data-tab-id={tab.id}
                draggable
                onDragStart={(e) => paneDrag.start(e, {tabId: tab.id, paneId: tab.activePaneId})}
                onDragEnd={paneDrag.end}
                onDragOver={(e) => paneDrag.overTab(e, tab.id)}
                onDragLeave={e => paneDrag.leaveTab(e, tab.id)}
                role="tab"
                aria-selected={active}
                className={`tab-item state-${st} ${active ? 'active' : ''} ${isSplit ? 'split-tab' : ''}`}
                onClick={() => onSelectTab(tab.id)}
                onContextMenu={(e) => {
                  e.preventDefault();
                  onSelectTab(tab.id);
                  setTabMenu({ x: e.clientX, y: e.clientY, tabId: tab.id });
                }}
                onMouseDown={(e) => {
                  if (e.button === 1) {
                    e.preventDefault();
                    onCloseTab(tab.id);
                  }
                }}
                title={tooltip}
              >
                {isSplit ? (
                  <Columns2 size={12} strokeWidth={1.8} className="tab-split-icon" aria-hidden="true" />
                ) : (
                  <HostIcon profile={prof} size="xs" />
                )}
                {st === 'connecting' && <span className="tab-connecting-dot" aria-hidden="true" />}
                <span className="tab-label">{title}</span>
                {isSplit && <span className="tab-split-count tabular">{leaves.length}</span>}
                <button
                  type="button"
                  className="tab-close"
                  title={`关闭 ${title}`}
                  aria-label={`关闭 ${title}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    onCloseTab(tab.id);
                  }}
                >
                  <X size={12.5} strokeWidth={2} />
                </button>
              </div>
            );
          })}
        </div>

        {canScrollRight && (
          <button
            type="button"
            className="tab-scroll-btn"
            title="向右滚动标签"
            aria-label="向右滚动标签"
            onClick={() => scrollTabsBy(260)}
          >
            <ChevronRight size={14} />
          </button>
        )}

        {(hasOverflow || tabs.length >= 4) && (
          <button
            id="allTabsBtn"
            type="button"
            className={`tab-count-btn ${allTabsMenu ? 'active' : ''}`}
            title={`全部已打开会话 (${tabs.length}) · Ctrl+Tab 快速切换`}
            aria-label={`展开全部会话标签 (${tabs.length})`}
            onClick={(e) => {
              e.stopPropagation();
              if (allTabsMenu) {
                setAllTabsMenu(null);
                return;
              }
              const rect = e.currentTarget.getBoundingClientRect();
              const menuW = 276;
              setAllTabsMenu({
                left: Math.max(8, Math.min(rect.right - menuW, window.innerWidth - menuW - 8)),
                top: rect.bottom + 6,
              });
            }}
          >
            <span className="tabular">{tabs.length}</span>
            <ChevronDown size={12.5} strokeWidth={2} />
          </button>
        )}

        <Tooltip label="新建连接" shortcut="Ctrl+N" side="bottom">
          <button
            id="newTabBtn"
            type="button"
            className="icon-btn tab-add"
            aria-label="新建连接"
            onClick={onNewConnection}
          >
            <Plus size={14} strokeWidth={1.8} />
          </button>
        </Tooltip>
      </div>

      <div className="titlebar-group">
        {activeTab && (
          <>
            <Tooltip label="终端内搜索" shortcut="Ctrl+Shift+F" side="bottom">
              <button
                type="button"
                className="icon-btn"
                aria-label="搜索终端"
                onClick={onOpenSearch}
              >
                <Search size={14} strokeWidth={1.75} />
              </button>
            </Tooltip>
            <Tooltip label="左右分屏" side="bottom">
              <button
                type="button"
                className="icon-btn"
                aria-label="左右分屏"
                onClick={() => onSplit('horizontal')}
              >
                <SplitSquareHorizontal size={14} strokeWidth={1.75} />
              </button>
            </Tooltip>
            <Tooltip label="上下分屏" side="bottom">
              <button
                type="button"
                className="icon-btn"
                aria-label="上下分屏"
                onClick={() => onSplit('vertical')}
              >
                <SplitSquareVertical size={14} strokeWidth={1.75} />
              </button>
            </Tooltip>
            {isMultiPane && (
              <Tooltip label="广播输入到所有分屏" side="bottom">
                <button
                  type="button"
                  className={`icon-btn ${activeTab.broadcast ? 'active' : ''}`}
                  aria-label="广播输入"
                  onClick={onToggleBroadcast}
                >
                  <Radio size={14} strokeWidth={1.75} />
                </button>
              </Tooltip>
            )}
            <span className="titlebar-divider" />
            <Tooltip label="SFTP 文件管理" side="bottom">
              <button
                id="sftpBtn"
                type="button"
                className={`icon-btn ${drawer === 'sftp' ? 'active' : ''}`}
                disabled={!isConnected}
                aria-label="SFTP 文件管理"
                onClick={() => onToggleDrawer('sftp')}
              >
                <FolderOpen size={14} strokeWidth={1.75} />
              </button>
            </Tooltip>

            <Tooltip label="端口转发隧道" side="bottom">
              <button
                id="tunnelsBtn"
                type="button"
                className={`icon-btn ${drawer === 'tunnels' ? 'active' : ''}`}
                disabled={!isConnected}
                aria-label="端口转发隧道"
                onClick={() => onToggleDrawer('tunnels')}
              >
                <ArrowLeftRight size={14} strokeWidth={1.75} />
              </button>
            </Tooltip>
          </>
        )}

        <Tooltip label="快捷命令片段" side="bottom">
          <button
            id="snippetsBtn"
            type="button"
            className={`icon-btn ${drawer === 'snippets' ? 'active' : ''}`}
            aria-label="快捷命令片段"
            onClick={() => onToggleDrawer('snippets')}
          >
            <Code2 size={14} strokeWidth={1.75} />
          </button>
        </Tooltip>

        <Tooltip label="AI 命令助手" shortcut="Ctrl+Shift+A" side="bottom">
          <button
            id="aiOpen"
            type="button"
            className={`icon-btn ${drawer === 'ai' ? 'active-purple' : ''}`}
            aria-label="AI 命令助手"
            onClick={() => onToggleDrawer('ai')}
          >
            <Sparkles size={14} strokeWidth={1.75} />
          </button>
        </Tooltip>

        <Tooltip label="命令面板" shortcut="Ctrl+K" side="bottom">
          <button
            id="paletteBtn"
            type="button"
            className="icon-btn"
            aria-label="命令面板"
            onClick={onOpenPalette}
          >
            <Command size={14} strokeWidth={1.75} />
          </button>
        </Tooltip>
      </div>

      <div className="win-controls">
        <button
          type="button"
          className="win-btn"
          title="最小化"
          aria-label="最小化"
          onClick={() => rt?.WindowMinimise()}
        >
          <svg width="10.5" height="10.5" viewBox="0 0 11 11" fill="none" aria-hidden="true">
            <path d="M1.5 5.5H9.5" stroke="currentColor" strokeWidth="1.15" strokeLinecap="round" />
          </svg>
        </button>
        <button
          type="button"
          className="win-btn"
          title={maximised ? '向下还原' : '最大化'}
          aria-label={maximised ? '向下还原' : '最大化'}
          onClick={handleToggleMaximise}
        >
          {maximised ? (
            <svg width="10.5" height="10.5" viewBox="0 0 11 11" fill="none" aria-hidden="true">
              <rect x="1.5" y="3.2" width="6.3" height="6.3" rx="1" stroke="currentColor" strokeWidth="1.1" />
              <path d="M3.4 3.2V2.2C3.4 1.75 3.75 1.4 4.2 1.4H8.8C9.25 1.4 9.6 1.75 9.6 2.2V6.8C9.6 7.25 9.25 7.6 8.8 7.6H7.8" stroke="currentColor" strokeWidth="1.1" strokeLinecap="round" />
            </svg>
          ) : (
            <svg width="10.5" height="10.5" viewBox="0 0 11 11" fill="none" aria-hidden="true">
              <rect x="1.6" y="1.6" width="7.8" height="7.8" rx="1.2" stroke="currentColor" strokeWidth="1.15" />
            </svg>
          )}
        </button>
        <button
          type="button"
          className="win-btn close"
          title="关闭"
          aria-label="关闭窗口"
          onClick={() => rt?.Quit()}
        >
          <svg width="10.5" height="10.5" viewBox="0 0 11 11" fill="none" aria-hidden="true">
            <path d="M2 2L9 9M9 2L2 9" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
          </svg>
        </button>
      </div>

      {allTabsMenu && (() => {
        const q = tabFilter.trim().toLowerCase();
        const filteredTabs = tabs.filter((tab) => {
          if (!q) return true;
          const { prof, title } = tabInfo(tab);
          const hay = `${title} ${prof?.host ?? ''} ${prof?.group ?? ''}`.toLowerCase();
          return hay.includes(q);
        });
        return (
        <>
          <div style={{ position: 'fixed', inset: 0, zIndex: 139 }} onClick={() => setAllTabsMenu(null)} />
          <div
            className="context-menu tabs-switcher-popover"
            style={{
              left: allTabsMenu.left,
              top: allTabsMenu.top,
              width: 276,
            }}
          >
            <div className="tabs-switcher-header">
              <span className="tabs-switcher-title tabular">已打开会话 ({tabs.length})</span>
              {tabs.length > 1 && (
                <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                  {activeTabId && (
                    <button
                      type="button"
                      className="tabs-switcher-action"
                      onClick={() => {
                        onCloseOtherTabs(activeTabId);
                        setAllTabsMenu(null);
                      }}
                    >
                      关闭其他
                    </button>
                  )}
                  <button
                    type="button"
                    className="tabs-switcher-action danger"
                    onClick={() => {
                      onCloseAllTabs();
                      setAllTabsMenu(null);
                    }}
                  >
                    关闭全部
                  </button>
                </div>
              )}
            </div>
            {tabs.length >= 4 && (
              <div className="tabs-switcher-search">
                <Search size={12.5} strokeWidth={1.8} />
                <input
                  ref={tabFilterRef}
                  className="input"
                  placeholder="筛选已打开会话…"
                  value={tabFilter}
                  onChange={(e) => setTabFilter(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && filteredTabs.length > 0) {
                      e.preventDefault();
                      onSelectTab(filteredTabs[0].id);
                      setAllTabsMenu(null);
                    }
                  }}
                />
              </div>
            )}
            <div className="tabs-switcher-list">
              {filteredTabs.length === 0 ? (
                <div className="tabs-switcher-empty">未找到匹配会话</div>
              ) : (
                filteredTabs.map((tab) => {
                  const st = tabState(tab);
                  const { leaves, prof, isSplit, title } = tabInfo(tab);
                  const active = tab.id === activeTabId;
                  return (
                    <div
                      key={tab.id}
                      className={`tabs-switcher-row ${active ? 'active' : ''}`}
                      title={isSplit ? `${title} (${leaves.length} 个分屏)` : prof && !hideHostIp ? `${title} (${prof.user}@${formatHostDisplay(prof.host, prof.port)})` : title}
                      onClick={() => {
                        onSelectTab(tab.id);
                        setAllTabsMenu(null);
                      }}
                    >
                      <div className="tabs-switcher-main" style={{ opacity: st === 'closed' ? 0.55 : 1 }}>
                        {isSplit ? (
                          <Columns2 size={13} strokeWidth={1.8} className="tab-split-icon" aria-hidden="true" />
                        ) : (
                          <HostIcon profile={prof} size="xs" />
                        )}
                        <span className="tabs-switcher-name">{title}</span>
                        {isSplit && <span className="tab-split-count tabular">{leaves.length}</span>}
                        {st === 'connecting' && <span className="tab-connecting-dot" aria-hidden="true" />}
                        {st === 'connected' && <span className="tabs-switcher-live-dot" aria-hidden="true" />}
                      </div>
                      <button
                        type="button"
                        className="tabs-switcher-close"
                        title={`关闭 ${title}`}
                        aria-label={`关闭 ${title}`}
                        onClick={(e) => {
                          e.stopPropagation();
                          onCloseTab(tab.id);
                        }}
                      >
                        <X size={14} strokeWidth={2} />
                      </button>
                    </div>
                  );
                })
              )}
            </div>
          </div>
        </>
        );
      })()}

      {tabMenu && (() => {
        const menuTab = tabs.find((t) => t.id === tabMenu.tabId);
        const menuLeaves = menuTab ? collectLeaves(menuTab.root) : [];
        const estHeight = 200 + (menuLeaves.length > 1 ? menuLeaves.length * 32 + 12 : 0);
        return (
        <>
          <div
            style={{ position: 'fixed', inset: 0, zIndex: 139 }}
            onClick={() => setTabMenu(null)}
            onContextMenu={(e) => {
              e.preventDefault();
              setTabMenu(null);
            }}
          />
          <div
            className="context-menu"
            style={{
              left: Math.min(tabMenu.x, window.innerWidth - 210),
              top: Math.max(8, Math.min(tabMenu.y, window.innerHeight - estHeight)),
            }}
          >
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onCloneTab(tabMenu.tabId);
                setTabMenu(null);
              }}
            >
              <CopyPlus size={13} />
              复制新会话标签
            </button>
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onSplit('horizontal', tabMenu.tabId);
                setTabMenu(null);
              }}
            >
              <SplitSquareHorizontal size={13} />
              向右分屏
            </button>
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onSplit('vertical', tabMenu.tabId);
                setTabMenu(null);
              }}
            >
              <SplitSquareVertical size={13} />
              向下分屏
            </button>
            {menuLeaves.length > 1 && (
              <>
                <div className="context-menu-sep" />
                {menuLeaves.map((leaf) => {
                  const name = profileDisplayName(sessionProfile(leaf.sessionId), hideHostIp) || '终端';
                  return (
                    <button
                      key={leaf.paneId}
                      type="button"
                      className="context-menu-item"
                      onClick={() => {
                        onDetachPane(tabMenu.tabId, leaf.paneId);
                        setTabMenu(null);
                      }}
                    >
                      <ArrowUpRight size={13} />
                      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        移出 {name}
                      </span>
                    </button>
                  );
                })}
              </>
            )}
            <div className="context-menu-sep" />
            <button
              type="button"
              className="context-menu-item"
              onClick={() => {
                onCloseTab(tabMenu.tabId);
                setTabMenu(null);
              }}
            >
              <X size={13} />
              关闭此标签
            </button>
            {tabs.length > 1 && (
              <>
                <button
                  type="button"
                  className="context-menu-item"
                  onClick={() => {
                    onCloseOtherTabs(tabMenu.tabId);
                    setTabMenu(null);
                  }}
                >
                  <X size={13} />
                  关闭其他标签
                </button>
                <button
                  type="button"
                  className="context-menu-item danger"
                  onClick={() => {
                    onCloseAllTabs();
                    setTabMenu(null);
                  }}
                >
                  <X size={13} />
                  关闭所有标签
                </button>
              </>
            )}
          </div>
        </>
        );
      })()}
    </header>
  );
};
