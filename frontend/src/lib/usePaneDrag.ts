import { useEffect, useRef, useState, type DragEvent } from 'react';
import type { WorkspaceTab } from '../types/shell';
import { canMovePane, nearestEdge, type Edge, type PaneSource } from './paneLayout';
import { randomId } from './utils';

const paneFormat = 'application/x-shell-terminal-pane';

export function usePaneDrag(tabs: WorkspaceTab[], onMove: (from: PaneSource, to: PaneSource, edge: Edge) => void) {
  const source = useRef<{pane: PaneSource; nonce: string} | null>(null);
  const hover = useRef<{id: string; timer: number} | null>(null);
  const [hoverTab, setHoverTab] = useState<string | null>(null);
  const [preview, setPreview] = useState<{paneId: string; edge: Edge} | null>(null);
  const stopHover = () => {
    if (hover.current) window.clearTimeout(hover.current.timer);
    hover.current = null;
  };
  const end = () => {
    stopHover();
    source.current = null;
    setPreview(null);
    setHoverTab(null);
  };
  useEffect(() => {
    window.addEventListener('dragend', end);
    window.addEventListener('blur', end);
    return () => {
      stopHover();
      window.removeEventListener('dragend', end);
      window.removeEventListener('blur', end);
    };
  }, []);
  const internal = (e: DragEvent) => source.current && e.dataTransfer.types.includes(paneFormat) && !e.dataTransfer.types.includes('Files');
  const start = (e: DragEvent, pane: PaneSource) => {
    if ((e.target as HTMLElement).closest('button')) {
      e.preventDefault();
      return;
    }
    const nonce = randomId();
    source.current = {pane, nonce};
    e.dataTransfer.clearData();
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData(paneFormat, nonce);
  };
  const overTab = (e: DragEvent, tabId: string) => {
    if (!internal(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'none';
    setPreview(null);
    if (hover.current?.id === tabId) return;
    stopHover();
    hover.current = {id: tabId, timer: window.setTimeout(() => setHoverTab(tabId), 400)};
  };
  const edgeAt = (e: DragEvent) => {
    const r = e.currentTarget.getBoundingClientRect();
    return nearestEdge((e.clientX - r.left) / Math.max(1, r.width), (e.clientY - r.top) / Math.max(1, r.height));
  };
  const over = (e: DragEvent, target: PaneSource) => {
    if (!internal(e)) return;
    stopHover();
    e.preventDefault();
    if (!canMovePane(tabs, source.current!.pane, target)) {
      e.dataTransfer.dropEffect = 'none';
      setPreview(null);
      return;
    }
    e.dataTransfer.dropEffect = 'move';
    const edge = edgeAt(e);
    setPreview(p => p?.paneId === target.paneId && p.edge === edge ? p : {paneId: target.paneId, edge});
  };
  const drop = (e: DragEvent, target: PaneSource) => {
    if (!internal(e)) return;
    e.preventDefault();
    e.stopPropagation();
    const drag = source.current!;
    if (e.dataTransfer.getData(paneFormat) === drag.nonce && canMovePane(tabs, drag.pane, target)) {
      onMove(drag.pane, target, edgeAt(e));
    }
    end();
  };
  const leave = (e: DragEvent) => {
    if (internal(e) && !e.currentTarget.contains(e.relatedTarget as Node | null)) setPreview(null);
  };
  const leaveTab = (e: DragEvent) => {
    if (!e.currentTarget.contains(e.relatedTarget as Node | null)) stopHover();
  };
  return {start, end, overTab, leaveTab, over, leave, drop, hoverTab, preview};
}
