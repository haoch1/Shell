import React, { useState } from 'react';
import {
  ArrowLeftRight,
  Code2,
  Command,
  CopyPlus,
  FolderOpen,
  Minus,
  PanelLeft,
  Plus,
  Radio,
  Search,
  Sparkles,
  SplitSquareHorizontal,
  SplitSquareVertical,
  Square,
  X,
} from 'lucide-react';
import type { DrawerMode, SessionRecord, WorkspaceTab } from '../types/shell';
import { collectLeaves } from '../lib/utils';
import { getRuntime } from '../lib/bridge';

interface TitlebarProps {
  sidebarOpen: boolean;
  onToggleSidebar: () => void;
  tabs: WorkspaceTab[];
  activeTabId: string | null;
  sessions: Record<string, SessionRecord>;
  drawer: DrawerMode;
  onSelectTab: (tabId: string) => void;
  onCloseTab: (tabId: string) => void;
  onCloseOtherTabs: (tabId: string) => void;
  onCloneTab: (tabId: string) => void;
  onNewConnection: () => void;
  onSplit: (dir: 'horizontal' | 'vertical') => void;
  onToggleBroadcast: () => void;
  onOpenSearch: () => void;
  onToggleDrawer: (mode: DrawerMode) => void;
  onOpenPalette: () => void;
}

export const Titlebar: React.FC<TitlebarProps> = ({
  sidebarOpen,
  onToggleSidebar,
  tabs,
  activeTabId,
  sessions,
  drawer,
  onSelectTab,
  onCloseTab,
  onCloseOtherTabs,
  onCloneTab,
  onNewConnection,
  onSplit,
  onToggleBroadcast,
  onOpenSearch,
  onToggleDrawer,
  onOpenPalette,
}) => {
  const [tabMenu, setTabMenu] = useState<{ x: number; y: number; tabId: string } | null>(null);
  const activeTab = tabs.find((t) => t.id === activeTabId) ?? null;
  const activeLeaves = activeTab ? collectLeaves(activeTab.root) : [];
  const activeLeaf = activeLeaves.find((l) => l.paneId === activeTab?.activePaneId) ?? activeLeaves[0];
  const activeSession = activeLeaf ? sessions[activeLeaf.sessionId] : undefined;
  const isConnected = activeSession?.state === 'connected';
  const isMultiPane = activeLeaves.length > 1;
  const rt = getRuntime();

  const tabState = (tab: WorkspaceTab): 'connected' | 'connecting' | 'closed' => {
    const leaves = collectLeaves(tab.root);
    const states = leaves.map((l) => sessions[l.sessionId]?.state ?? 'closed');
    if (states.includes('connected')) return 'connected';
    if (states.includes('connecting')) return 'connecting';
    return 'closed';
  };

  return (
    <header
      className="titlebar"
      onDoubleClick={(e) => {
        if ((e.target as HTMLElement).closest('button')) return;
        rt?.WindowToggleMaximise();
      }}
    >
      <div className="titlebar-group">
        <button
          id="toggleSidebar"
          type="button"
          className={`icon-btn ${sidebarOpen ? 'active' : ''}`}
          title="切换侧栏 (Ctrl+B)"
          aria-label="切换侧栏"
          onClick={onToggleSidebar}
        >
          <PanelLeft size={15} strokeWidth={1.7} />
        </button>
        <div className="brand-pill">
          <span className="brand-icon">&gt;<span>_</span></span>
          {tabs.length === 0 && <span>Shell</span>}
        </div>
      </div>

      <div className="tab-strip" role="tablist" aria-label="会话标签">
        {tabs.map((tab) => {
          const st = tabState(tab);
          const active = tab.id === activeTabId;
          return (
            <div
              key={tab.id}
              role="tab"
              aria-selected={active}
              className={`tab-item ${active ? 'active' : ''}`}
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
              title={tab.title}
            >
              <span className={`tab-dot ${st}`} />
              <span className="tab-label">{tab.title}</span>
              <button
                type="button"
                className="tab-close"
                aria-label={`关闭 ${tab.title}`}
                onClick={(e) => {
                  e.stopPropagation();
                  onCloseTab(tab.id);
                }}
              >
                <X size={12} strokeWidth={2} />
              </button>
            </div>
          );
        })}
        <button
          id="newTabBtn"
          type="button"
          className="icon-btn tab-add"
          title="新建连接 (Ctrl+N)"
          aria-label="新建连接"
          onClick={onNewConnection}
        >
          <Plus size={15} strokeWidth={1.8} />
        </button>
      </div>

      <div className="titlebar-group">
        {activeTab && (
          <>
            <button
              type="button"
              className="icon-btn"
              title="终端内搜索 (Ctrl+Shift+F)"
              aria-label="搜索终端"
              onClick={onOpenSearch}
            >
              <Search size={14} strokeWidth={1.7} />
            </button>
            <button
              type="button"
              className="icon-btn"
              title="左右分屏"
              aria-label="左右分屏"
              onClick={() => onSplit('horizontal')}
            >
              <SplitSquareHorizontal size={14} strokeWidth={1.7} />
            </button>
            <button
              type="button"
              className="icon-btn"
              title="上下分屏"
              aria-label="上下分屏"
              onClick={() => onSplit('vertical')}
            >
              <SplitSquareVertical size={14} strokeWidth={1.7} />
            </button>
            {isMultiPane && (
              <button
                type="button"
                className={`icon-btn ${activeTab.broadcast ? 'active' : ''}`}
                title="广播输入到当前标签所有分屏"
                aria-label="广播输入"
                onClick={onToggleBroadcast}
              >
                <Radio size={14} strokeWidth={1.7} />
              </button>
            )}
            <span className="titlebar-divider" />
            <button
              id="sftpBtn"
              type="button"
              className={`icon-btn ${drawer === 'sftp' ? 'active' : ''}`}
              disabled={!isConnected}
              title="SFTP 文件管理"
              onClick={() => onToggleDrawer('sftp')}
            >
              <FolderOpen size={14} strokeWidth={1.7} />
              <span>文件</span>
            </button>

            <button
              id="tunnelsBtn"
              type="button"
              className={`icon-btn ${drawer === 'tunnels' ? 'active' : ''}`}
              disabled={!isConnected}
              title="端口转发隧道"
              onClick={() => onToggleDrawer('tunnels')}
            >
              <ArrowLeftRight size={14} strokeWidth={1.7} />
              <span>转发</span>
            </button>
          </>
        )}

        <button
          id="snippetsBtn"
          type="button"
          className={`icon-btn ${drawer === 'snippets' ? 'active' : ''}`}
          title="快捷命令片段"
          onClick={() => onToggleDrawer('snippets')}
        >
          <Code2 size={14} strokeWidth={1.7} />
          <span>片段</span>
        </button>

        <button
          id="aiOpen"
          type="button"
          className={`icon-btn ${drawer === 'ai' ? 'active-purple' : ''}`}
          title="AI 命令助手 (Ctrl+Shift+A)"
          onClick={() => onToggleDrawer('ai')}
        >
          <Sparkles size={14} strokeWidth={1.7} />
          <span>AI</span>
        </button>

        <button
          id="paletteBtn"
          type="button"
          className="icon-btn"
          title="命令面板 (Ctrl+K)"
          aria-label="命令面板"
          onClick={onOpenPalette}
        >
          <Command size={14} strokeWidth={1.7} />
        </button>
      </div>

      <div className="win-controls">
        <button
          type="button"
          className="win-btn"
          aria-label="最小化"
          onClick={() => rt?.WindowMinimise()}
        >
          <Minus size={14} strokeWidth={1.6} />
        </button>
        <button
          type="button"
          className="win-btn"
          aria-label="最大化"
          onClick={() => rt?.WindowToggleMaximise()}
        >
          <Square size={11} strokeWidth={1.7} />
        </button>
        <button
          type="button"
          className="win-btn close"
          aria-label="关闭窗口"
          onClick={() => rt?.Quit()}
        >
          <X size={14} strokeWidth={1.7} />
        </button>
      </div>

      {tabMenu && (
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
            style={{ left: Math.min(tabMenu.x, window.innerWidth - 190), top: Math.min(tabMenu.y, window.innerHeight - 200) }}
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
                onSplit('horizontal');
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
                onSplit('vertical');
                setTabMenu(null);
              }}
            >
              <SplitSquareVertical size={13} />
              向下分屏
            </button>
            <div className="context-menu-sep" />
            {tabs.length > 1 && (
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
            )}
            <button
              type="button"
              className="context-menu-item danger"
              onClick={() => {
                onCloseTab(tabMenu.tabId);
                setTabMenu(null);
              }}
            >
              <X size={13} />
              关闭此标签
            </button>
          </div>
        </>
      )}
    </header>
  );
};
