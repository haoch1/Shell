import type { Profile, SessionRecord, SplitNode, WorkspaceTab } from '../types/shell';
import { sameSessionTarget } from './sessionAttempt.ts';
import { collectLeaves, removePaneNode } from './utils.ts';

export type Edge = 'left' | 'right' | 'top' | 'bottom';
export type PaneSource = { tabId: string; paneId: string };
export type Bounds = { x: number; y: number; width: number; height: number };
type Leaf = Extract<SplitNode, {type: 'leaf'}>;
type Branch = Extract<SplitNode, {type: 'split'}>;

export function connectedProfilePanes(profiles: Profile[], tabs: WorkspaceTab[], sessions: Record<string, SessionRecord>, activeTabId: string | null) {
  const saved = new Map(profiles.map(p => [p.id, p]));
  const result = new Map<string, {source: PaneSource; priority: number; startedAt: number}>();
  for (const tab of tabs) {
    for (const leaf of collectLeaves(tab.root)) {
      const session = sessions[leaf.sessionId];
      const profile = session && saved.get(session.profile.id);
      if (!profile || session.state !== 'connected' || !sameSessionTarget(profile, session.profile)) continue;
      // Prefer a movable tab's selected pane over its siblings and the current destination.
      const selected = leaf.paneId === tab.activePaneId;
      const priority = selected ? (tab.id === activeTabId ? 0 : 2) : 1;
      const prev = result.get(profile.id);
      if (!prev || priority > prev.priority || (priority === prev.priority && session.startedAt > prev.startedAt)) {
        result.set(profile.id, {source: {tabId: tab.id, paneId: leaf.paneId}, priority, startedAt: session.startedAt});
      }
    }
  }
  return result;
}

export function nearestEdge(x: number, y: number): Edge {
  if (Math.min(x, 1 - x) < Math.min(y, 1 - y)) return x < 0.5 ? 'left' : 'right';
  return y < 0.5 ? 'top' : 'bottom';
}

export function canMovePane(tabs: WorkspaceTab[], source: PaneSource, target: PaneSource): boolean {
  if (source.paneId === target.paneId) return false;
  const from = tabs.find(t => t.id === source.tabId);
  const to = tabs.find(t => t.id === target.tabId);
  if (!from || !to || !collectLeaves(from.root).some(l => l.paneId === source.paneId)) return false;
  const leaves = collectLeaves(to.root);
  return (from === to || leaves.length < 6) && leaves.some(l => l.paneId === target.paneId);
}

function insertBeside(node: SplitNode, targetId: string, leaf: Leaf, edge: Edge, id: string): SplitNode {
  if (node.type === 'leaf') {
    if (node.paneId !== targetId) return node;
    const before = edge === 'left' || edge === 'top';
    return {
      type: 'split', id, ratio: 0.5, direction: edge === 'left' || edge === 'right' ? 'horizontal' : 'vertical',
      first: before ? leaf : node, second: before ? node : leaf,
    };
  }
  return {...node, first: insertBeside(node.first, targetId, leaf, edge, id), second: insertBeside(node.second, targetId, leaf, edge, id)};
}

export function movePane(tabs: WorkspaceTab[], source: PaneSource, target: PaneSource, edge: Edge, splitId: string): WorkspaceTab[] {
  if (!canMovePane(tabs, source, target)) return tabs;
  const from = tabs.find(t => t.id === source.tabId)!;
  const moved = collectLeaves(from.root).find(l => l.paneId === source.paneId)!;
  const leaf: Leaf = {type: 'leaf', ...moved};
  return tabs.flatMap(tab => {
    let root: SplitNode | null = tab.root;
    if (tab.id === source.tabId) root = removePaneNode(root, source.paneId);
    if (!root) return [];
    if (tab.id === target.tabId) {
      return [{...tab, root: insertBeside(root, target.paneId, leaf, edge, splitId), activePaneId: moved.paneId}];
    }
    if (tab.id !== source.tabId) return [tab];
    return [{...tab, root, activePaneId: tab.activePaneId === moved.paneId ? collectLeaves(root)[0].paneId : tab.activePaneId,
      broadcast: root.type === 'split' && tab.broadcast}];
  });
}

// Keep every terminal mounted in one flat React list. The split tree only determines geometry.
export function paneGeometry(root: SplitNode) {
  const panes: {leaf: Leaf; bounds: Bounds}[] = [];
  const dividers: {node: Branch; bounds: Bounds}[] = [];
  const walk = (node: SplitNode, bounds: Bounds) => {
    if (node.type === 'leaf') {
      panes.push({leaf: node, bounds});
      return;
    }
    dividers.push({node, bounds});
    const {x, y, width, height} = bounds;
    if (node.direction === 'horizontal') {
      const first = width * node.ratio;
      walk(node.first, {...bounds, width: first});
      walk(node.second, {...bounds, x: x + first, width: width - first});
    } else {
      const first = height * node.ratio;
      walk(node.first, {...bounds, height: first});
      walk(node.second, {...bounds, y: y + first, height: height - first});
    }
  };
  walk(root, {x: 0, y: 0, width: 1, height: 1});
  return {panes, dividers};
}
