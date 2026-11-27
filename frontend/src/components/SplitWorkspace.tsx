import React, { useRef } from 'react';
import type { AppSettings, SessionRecord, SplitNode } from '../types/shell';
import { TerminalPane } from './TerminalPane';

interface SplitWorkspaceProps {
  node: SplitNode;
  activePaneId: string;
  multiPane: boolean;
  sessions: Record<string, SessionRecord>;
  settings: AppSettings;
  onFocusPane: (paneId: string) => void;
  onRatioChange: (splitId: string, ratio: number) => void;
  onPaneInput: (paneId: string, sessionId: string, data: string | Uint8Array) => void;
  onPaneResize: (sessionId: string, cols: number, rows: number) => void;
  onPaneCwdChange: (sessionId: string, dir: string) => void;
  onReconnectPane: (paneId: string, sessionId: string) => void;
  onClosePane: (paneId: string) => void;
  onSplitPane: (paneId: string, dir: 'horizontal' | 'vertical') => void;
  onOpenSearch: () => void;
  onOpenSFTP: () => void;
  onExplainSelection: (text: string) => void;
  onExportLog: (session: SessionRecord, content: string) => void;
  onFontZoom: (delta: number) => void;
  onToast: (msg: string) => void;
}

export const SplitWorkspace: React.FC<SplitWorkspaceProps> = (props) => {
  const { node } = props;
  const containerRef = useRef<HTMLDivElement | null>(null);

  if (node.type === 'leaf') {
    const session = props.sessions[node.sessionId];
    if (!session) return null;
    return (
      <TerminalPane
        paneId={node.paneId}
        session={session}
        settings={props.settings}
        focused={props.activePaneId === node.paneId}
        multiPane={props.multiPane}
        onFocus={() => props.onFocusPane(node.paneId)}
        onInput={(d) => props.onPaneInput(node.paneId, node.sessionId, d)}
        onResize={(c, r) => props.onPaneResize(node.sessionId, c, r)}
        onCwdChange={(dir) => props.onPaneCwdChange(node.sessionId, dir)}
        onReconnect={() => props.onReconnectPane(node.paneId, node.sessionId)}
        onClosePane={() => props.onClosePane(node.paneId)}
        onSplit={(dir) => props.onSplitPane(node.paneId, dir)}
        onOpenSearch={props.onOpenSearch}
        onOpenSFTP={props.onOpenSFTP}
        onExplainSelection={props.onExplainSelection}
        onExportLog={(content) => props.onExportLog(session, content)}
        onFontZoom={props.onFontZoom}
        onToast={props.onToast}
      />
    );
  }

  const startDrag = (e: React.MouseEvent) => {
    e.preventDefault();
    const box = containerRef.current;
    if (!box) return;
    const rect = box.getBoundingClientRect();
    const handle = e.currentTarget as HTMLElement;
    handle.classList.add('dragging');

    const onMove = (mv: MouseEvent) => {
      const ratio =
        node.direction === 'horizontal'
          ? (mv.clientX - rect.left) / Math.max(1, rect.width)
          : (mv.clientY - rect.top) / Math.max(1, rect.height);
      props.onRatioChange(node.id, ratio);
    };

    const onUp = () => {
      handle.classList.remove('dragging');
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };

    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  };

  const firstPct = `${(node.ratio * 100).toFixed(2)}%`;
  const secondPct = `${((1 - node.ratio) * 100).toFixed(2)}%`;

  return (
    <div ref={containerRef} className={`split-container ${node.direction}`}>
      <div className="split-branch" style={{ flexBasis: firstPct, flexGrow: 0, flexShrink: 0 }}>
        <SplitWorkspace {...props} node={node.first} />
      </div>
      <div
        className={`split-handle ${node.direction}`}
        onMouseDown={startDrag}
        role="separator"
        aria-orientation={node.direction === 'horizontal' ? 'vertical' : 'horizontal'}
      />
      <div className="split-branch" style={{ flexBasis: secondPct, flexGrow: 1, flexShrink: 1 }}>
        <SplitWorkspace {...props} node={node.second} />
      </div>
    </div>
  );
};
