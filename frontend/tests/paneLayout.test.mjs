import assert from 'node:assert/strict';
import test from 'node:test';
import { movePane, nearestEdge, paneGeometry } from '../src/lib/paneLayout.ts';

const leaf = paneId => ({type: 'leaf', paneId, sessionId: 'session-' + paneId});
const branch = (first, second) => ({type: 'split', id: 'divider-' + first.paneId, direction: 'horizontal', ratio: 0.5, first, second});
const tab = (id, root, activePaneId) => ({id, root, title: id, activePaneId, broadcast: false});
const a = {tabId: 'A', paneId: 'a'};
const b = {tabId: 'B', paneId: 'b'};

test('four edges place the original session on the chosen side and keep tab order', () => {
  const tabs = [tab('A', leaf('a'), 'a'), tab('B', leaf('b'), 'b'), tab('C', leaf('c'), 'c')];
  for (const [edge, x, y, direction, first] of [
    ['left', 0.05, 0.5, 'horizontal', 'a'], ['right', 0.95, 0.5, 'horizontal', 'b'],
    ['top', 0.5, 0.05, 'vertical', 'a'], ['bottom', 0.5, 0.95, 'vertical', 'b'],
  ]) {
    assert.equal(nearestEdge(x, y), edge);
    const moved = movePane(tabs, a, b, edge, 'new');
    assert.deepEqual(moved.map(t => t.id), ['B', 'C']);
    assert.equal(moved[1], tabs[2]);
    assert.equal(moved[0].root.direction, direction);
    assert.equal(moved[0].root.first.paneId, first);
    assert.equal(moved[0].activePaneId, 'a');
    assert.deepEqual(paneGeometry(moved[0].root).panes.map(p => p.leaf.sessionId).sort(), ['session-a', 'session-b']);
  }
});

test('moving one pane collapses the source tree and repairs focus and broadcast', () => {
  const source = {...tab('A', branch(leaf('a'), leaf('c')), 'a'), broadcast: true};
  const [left, right] = movePane([source, tab('B', leaf('b'), 'b')], a, b, 'bottom', 'new');
  assert.deepEqual(left, {...source, root: leaf('c'), activePaneId: 'c', broadcast: false});
  assert.deepEqual(right.root.second, leaf('a'));
});

test('full targets reject new panes but allow rearranging an existing one', () => {
  const six = ['b', 'c', 'd', 'e', 'f', 'g'].map(leaf).reduceRight((r, l) => branch(l, r));
  const tabs = [tab('A', leaf('a'), 'a'), tab('B', six, 'b')];
  assert.equal(movePane(tabs, a, b, 'right', 'new'), tabs);
  assert.equal(movePane(tabs, a, a, 'right', 'new'), tabs);
  assert.equal(movePane(tabs, {...a, paneId: 'closed'}, b, 'right', 'new'), tabs);
  assert.equal(movePane(tabs, b, {tabId: 'A', paneId: 'closed'}, 'right', 'new'), tabs);
  const moved = movePane(tabs, {tabId: 'B', paneId: 'g'}, b, 'top', 'new');
  const panes = paneGeometry(moved[1].root).panes;
  assert.equal(moved[1].activePaneId, 'g');
  assert.deepEqual(panes.map(p => p.leaf.paneId).sort(), ['b', 'c', 'd', 'e', 'f', 'g']);
  assert.equal(panes.reduce((area, p) => area + p.bounds.width * p.bounds.height, 0), 1);
});
