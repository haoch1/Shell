import React, { useRef } from 'react';
import type { AppSettings, SessionRecord, WorkspaceTab } from '../types/shell';
import { paneGeometry } from '../lib/paneLayout';
import type { usePaneDrag } from '../lib/usePaneDrag';
import { TerminalPane } from './TerminalPane';

interface SplitWorkspaceProps {
  tabs: WorkspaceTab[];
  activeTabId: string | null;
  paneDrag: ReturnType<typeof usePaneDrag>;
  sessions: Record<string, SessionRecord>;
  settings: AppSettings;
  onFocusPane: (tabId: string, paneId: string) => void;
  onRatioChange: (tabId: string, splitId: string, ratio: number) => void;
  onPaneInput: (tabId: string, paneId: string, sessionId: string, data: string | Uint8Array) => void;
  onPaneResize: (sessionId: string, cols: number, rows: number) => void;
  onPaneCwdChange: (sessionId: string, dir: string) => void;
  onReconnectPane: (tabId: string, paneId: string, sessionId: string) => void;
  onClosePane: (tabId: string, paneId: string) => void;
  onSplitPane: (tabId: string, paneId: string, dir: 'horizontal' | 'vertical') => void;
  onOpenSearch: () => void;
  onOpenSFTP: () => void;
  onExplainSelection: (text: string) => void;
  onExportLog: (session: SessionRecord, content: string) => void;
  onFontZoom: (delta: number) => void;
  onToast: (msg: string) => void;
}

const percent = (value: number) => `${value * 100}%`;

export const SplitWorkspace: React.FC<SplitWorkspaceProps> = (props) => {
  const board = useRef<HTMLDivElement>(null);
  const { paneDrag } = props;
  const layouts = props.tabs.map(tab => ({tab, ...paneGeometry(tab.root)}));
  const slots = new Map(layouts.flatMap(({tab, panes}) => panes.map(p => [p.leaf.sessionId, {tab, ...p}] as const)));
  const activeId = paneDrag.hoverTab ?? props.activeTabId;
  const active = layouts.find(l => l.tab.id === activeId);

  return (
    <div className="workspace-layout" ref={board}>
      {Object.values(props.sessions).map(session => {
        const slot = slots.get(session.id);
        if (!slot) return null;
        const {tab, leaf, bounds} = slot;
        const target = {tabId: tab.id, paneId: leaf.paneId};
        const visible = tab.id === activeId;
        const edge = visible && paneDrag.preview?.paneId === leaf.paneId ? paneDrag.preview.edge : undefined;
        return (
          <div key={session.id} className="terminal-slot" data-drop-edge={edge}
            style={{display: visible ? 'flex' : 'none', left: percent(bounds.x), top: percent(bounds.y),
              width: percent(bounds.width), height: percent(bounds.height)}}
            onDragOver={e => paneDrag.over(e, target)} onDragLeave={paneDrag.leave} onDrop={e => paneDrag.drop(e, target)}>
            <TerminalPane
              paneId={leaf.paneId}
              onDragPane={e => paneDrag.start(e, target)}
              session={session}
              settings={props.settings}
              focused={visible && tab.activePaneId === leaf.paneId}
              multiPane={tab.root.type === 'split'}
              onFocus={() => props.onFocusPane(tab.id, leaf.paneId)}
              onInput={d => props.onPaneInput(tab.id, leaf.paneId, session.id, d)}
              onResize={(c, r) => props.onPaneResize(session.id, c, r)}
              onCwdChange={dir => props.onPaneCwdChange(session.id, dir)}
              onReconnect={() => props.onReconnectPane(tab.id, leaf.paneId, session.id)}
              onClosePane={() => props.onClosePane(tab.id, leaf.paneId)}
              onSplit={dir => props.onSplitPane(tab.id, leaf.paneId, dir)}
              onOpenSearch={props.onOpenSearch}
              onOpenSFTP={props.onOpenSFTP}
              onExplainSelection={props.onExplainSelection}
              onExportLog={content => props.onExportLog(session, content)}
              onFontZoom={props.onFontZoom}
              onToast={props.onToast}
            />
          </div>
        );
      })}
      {active?.dividers.map(({node, bounds}) => {
        const horizontal = node.direction === 'horizontal';
        const style: React.CSSProperties = horizontal
          ? {left: percent(bounds.x + bounds.width * node.ratio), top: percent(bounds.y), height: percent(bounds.height)}
          : {left: percent(bounds.x), top: percent(bounds.y + bounds.height * node.ratio), width: percent(bounds.width)};
        return (
          <div key={node.id} className={`split-handle ${node.direction}`} style={style}
            role="separator" aria-orientation={horizontal ? 'vertical' : 'horizontal'}
            onPointerDown={e => {
              if (e.button !== 0) return;
              e.preventDefault();
              e.currentTarget.setPointerCapture(e.pointerId);
            }}
            onPointerMove={e => {
              if (!board.current || !e.currentTarget.hasPointerCapture(e.pointerId)) return;
              const rect = board.current.getBoundingClientRect();
              const ratio = horizontal
                ? ((e.clientX - rect.left) / Math.max(1, rect.width) - bounds.x) / bounds.width
                : ((e.clientY - rect.top) / Math.max(1, rect.height) - bounds.y) / bounds.height;
              props.onRatioChange(active.tab.id, node.id, ratio);
            }}
          />
        );
      })}
    </div>
  );
};
