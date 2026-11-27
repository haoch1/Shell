import assert from 'node:assert/strict';

export async function verifySplitWorkspace({ call, evaluate, until, click }) {
  const page = (fn, ...args) => evaluate('(' + fn.toString() + ')(...' + JSON.stringify(args) + ')');
  const wait = (label, fn, ...args) => until(() => page(fn, ...args), label);
  const state = () => page(() => ({
    tabs: document.querySelectorAll('[role=tab][data-tab-id]').length,
    panes: document.querySelectorAll('.pane-box[data-pane-id]').length,
    tab: document.querySelector('[role=tab][aria-selected=true]').dataset.tabId,
    dials: window.splitFixture.dials.map(d => d.profile),
    disconnects: window.splitFixture.disconnects,
  }));
  const picker = async (direction) => {
    await click('button[aria-label="' + direction + '分屏"]');
    await wait('split host picker', () => !!document.querySelector('[role=dialog][aria-label="选择分屏主机"]'));
  };
  const choose = id => click('#commandPalette [data-action-id="' + id + '"]');
  const waitConnected = (profile, count) => wait('connected split host ' + profile, (name, total) => {
    const calls = window.splitFixture.dials;
    const last = calls.at(-1);
    if (calls.length !== total || last?.profile !== name) return false;
    const pane = document.querySelector('[data-session-id="' + last.id + '"]');
    return !!pane?.querySelector('.xterm') && !pane.querySelector('.pane-connect-trace, .pane-connecting-bar, .pane-closed-bar');
  }, profile, count);

  await call('Page.reload');
  await wait('fresh workspace', () => !document.querySelector('[role=tab]') && document.querySelectorAll('.host-row').length === 3);
  await page(async () => {
    const fixture = window.splitFixture = { dials: [], disconnects: 0, history: false };
    const api = window.go.main.App;
    for (const [id, auth] of [['source', 'agent'], ['other', 'agent'], ['password', 'password'], ['drag', 'agent']]) {
      await api.SaveProfile({ id, name: 'Fixture ' + id, host: id + '.test', port: 22, user: 'fixture', auth });
    }
    const connect = api.Connect;
    api.Connect = async (id, profile) => {
      fixture.dials.push({ id, profile: profile.id });
      return connect(id, profile);
    };
    api.Disconnect = async () => { fixture.disconnects++; };
    api.SaveProfileWithSecret = profile => api.SaveProfile({ ...profile, id: profile.id || 'created' });
    api.SaveTranscript = async (_name, content) => {
      fixture.history = content.includes('SCROLLBACK-START') && content.includes('SCROLLBACK-END');
      return '';
    };
    window.runtime.EventsEmit('agent:connect_profile', 'source');
  });
  await waitConnected('source', 1);
  const initial = await state();
  assert.equal(initial.tabs, 1);
  assert.equal(initial.panes, 1);
  for (const direction of ['左右', '上下']) {
    await picker(direction);
    assert.deepEqual(await state(), initial, 'Opening the picker must not create a connection');
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await wait('closed split picker', () => !document.querySelector('#commandPalette'));
    assert.deepEqual(await state(), initial, 'Canceling a split changed the workspace');
  }
  await picker('上下');
  await choose('host-password');
  await wait('password prompt', () => !!document.querySelector('#connectDialog #password'));
  await click('#connectDialog [data-close]');
  assert.deepEqual(await state(), initial, 'Canceling the credential dialog must not dial');

  await picker('左右');
  await choose('host-other');
  await waitConnected('other', 2);
  const afterOther = await state();
  assert.deepEqual(afterOther, { ...initial, panes: 2, dials: ['source', 'other'] });
  const aligned = (axis, first, second) => page((axis, first, second) => {
    const rect = index => document.querySelector('[data-session-id="' + window.splitFixture.dials[index].id + '"]').getBoundingClientRect();
    const a = rect(first);
    const b = rect(second);
    return a.width > 0 && b.width > 0 && (axis === 'x'
      ? a.right <= b.left + 2 && Math.abs(a.top - b.top) < 3
      : a.bottom <= b.top + 2 && Math.abs(a.left - b.left) < 3);
  }, axis, first, second);
  assert.ok(await aligned('x', 0, 1), 'Horizontal split should put the selected host to the right');
  await picker('上下');
  await choose('split-current');
  await waitConnected('other', 3);
  assert.ok(await aligned('y', 1, 2), 'Vertical split should put a new session for the current host below it');
  await picker('左右');
  await choose('split-new');
  await click('#authMode [data-auth=agent]');
  await page(() => {
    for (const [selector, value] of [['#address', 'created.test'], ['#login', 'fixture'], ['#alias', 'Fixture New']]) {
      const field = document.querySelector(selector);
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(field, value);
      field.dispatchEvent(new Event('input', { bubbles: true }));
    }
  });
  await click('#connect');
  await waitConnected('created', 4);
  const beforeDrag = await state();
  assert.deepEqual(beforeDrag, { ...initial, panes: 4, dials: ['source', 'other', 'other', 'created'] });

  await click('.host-row[title^="fixture@drag.test"]');
  await waitConnected('drag', 5);
  const extra = await state();
  assert.equal(extra.tabs, 2);
  assert.notEqual(extra.tab, initial.tab);
  await page(() => {
    const f = window.splitFixture;
    f.moved = f.dials.at(-1).id;
    f.originalTerminal = document.querySelector('[data-session-id="' + f.moved + '"] .xterm');
    const lines = ['SCROLLBACK-START', ...Array.from({ length: 90 }, (_, i) => 'line ' + i), 'SCROLLBACK-END'];
    window.runtime.EventsEmit('ssh:data', f.moved, btoa(lines.join('\r\n') + '\r\n'));
  });
  const checkHistory = () => until(async () => {
    await page(() => {
      const f = window.splitFixture;
      f.history = false;
      const pane = document.querySelector('[data-session-id="' + f.moved + '"]');
      const r = pane.getBoundingClientRect();
      pane.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: r.x + 10, clientY: r.y + 10 }));
    });
    await wait('export terminal history', () => {
      const button = [...document.querySelectorAll('.context-menu-item')].find(el => el.textContent.trim() === '导出终端日志');
      if (!button) return false;
      button.click();
      return true;
    });
    return page(() => window.splitFixture.history);
  }, 'exported transcript retains both ends of the scrollback');
  await checkHistory();
  await page((sourceTab, targetTab) => {
    const f = window.splitFixture;
    const source = document.querySelector('[data-tab-id="' + sourceTab + '"][draggable=true]');
    const hoverTab = document.querySelector('[data-tab-id="' + targetTab + '"]');
    const transfer = new DataTransfer();
    const event = (type, point = {}) => new DragEvent(type, { dataTransfer: transfer, bubbles: true, cancelable: true, ...point });
    source.dispatchEvent(event('dragstart'));
    if (!transfer.types.includes('application/x-shell-terminal-pane')) throw new Error('Tab drag missing its session payload');
    hoverTab.dispatchEvent(event('dragover'));
    f.dragToEdge = () => {
      const target = document.querySelector('[data-session-id="' + f.dials[0].id + '"]');
      const rect = target.getBoundingClientRect();
      const point = { clientX: rect.left + 2, clientY: rect.top + rect.height / 2 };
      target.dispatchEvent(event('dragover', point));
      f.finishDrag = () => { target.dispatchEvent(event('drop', point)); source.dispatchEvent(event('dragend')); };
    };
  }, extra.tab, initial.tab);
  await wait('hovered destination tab becomes visible', () => {
    const target = document.querySelector('[data-session-id="' + window.splitFixture.dials[0].id + '"]');
    return target.getBoundingClientRect().width > 0 && getComputedStyle(target).visibility === 'visible';
  });
  await page(() => window.splitFixture.dragToEdge());
  await wait('left edge drop preview', () => !!document.querySelector('[data-drop-edge=left]'));
  await page(() => window.splitFixture.finishDrag());
  await wait('empty source tab removed', () => document.querySelectorAll('[role=tab][data-tab-id]').length === 1);
  assert.deepEqual(await state(), { ...extra, tabs: 1, tab: initial.tab }, 'Moving a live session must reuse the connection and remove only its empty tab');
  assert.ok(await aligned('x', 4, 0), 'Dropping on the left must place the moved session before the target');
  assert.ok(await page(() => {
    const f = window.splitFixture;
    return document.querySelector('[data-session-id="' + f.moved + '"] .xterm') === f.originalTerminal;
  }), 'Dragging must preserve the xterm DOM instance');
  await checkHistory();

  const beforeSidebar = await state();
  await page(() => {
    const source = document.querySelector('.host-row[data-profile-id="drag"][draggable=true]');
    const f = window.splitFixture;
    const target = document.querySelector('[data-session-id="' + f.dials[0].id + '"]');
    const transfer = new DataTransfer();
    const event = (type, coords = {}) => new DragEvent(type, { ...coords, bubbles: true, cancelable: true, dataTransfer: transfer });
    const rect = target.getBoundingClientRect();
    const coords = { clientX: rect.right - 2, clientY: rect.top + rect.height / 2 };
    source.dispatchEvent(event('dragstart'));
    target.dispatchEvent(event('dragover', coords));
    f.finishSidebar = () => { target.dispatchEvent(event('drop', coords)); source.dispatchEvent(event('dragend')); };
  });
  await wait('sidebar session drop preview', () => !!document.querySelector('[data-drop-edge=right]'));
  await page(() => window.splitFixture.finishSidebar());
  await until(() => aligned('x', 0, 4), 'sidebar moved the original session to the right');
  assert.deepEqual(await state(), beforeSidebar, 'Rearranging an existing session must not dial or disconnect');

  // Verify split tab title/count and detaching a split pane into an independent tab preserves live session & scrollback.
  const splitTabMeta = await page((tabId) => {
    const el = document.querySelector('[role=tab][data-tab-id="' + tabId + '"]');
    return {
      hasSplitIcon: !!el?.querySelector('.tab-split-icon'),
      count: el?.querySelector('.tab-split-count')?.textContent?.trim(),
      label: el?.querySelector('.tab-label')?.textContent?.trim() || '',
    };
  }, initial.tab);
  assert.equal(splitTabMeta.hasSplitIcon, true, 'Split tab must render split icon');
  assert.equal(splitTabMeta.count, '5', 'Split tab must render pane count badge');
  assert.ok(splitTabMeta.label.includes('Fixture source') && splitTabMeta.label.includes('Fixture drag'), 'Split tab must join host names, got ' + splitTabMeta.label);

  await page(() => {
    const f = window.splitFixture;
    const btn = document.querySelector('[data-session-id="' + f.moved + '"] .pane-mini-bar button[aria-label="移出为独立标签页"]');
    if (!btn) throw new Error('Missing detach button in split pane mini-bar');
    btn.click();
  });
  await wait('detached pane becomes standalone tab', () => document.querySelectorAll('[role=tab][data-tab-id]').length === 2);
  const afterDetach = await state();
  assert.equal(afterDetach.tabs, 2);
  assert.equal(afterDetach.panes, 5);
  assert.notEqual(afterDetach.tab, initial.tab);
  assert.deepEqual(afterDetach.dials, beforeSidebar.dials, 'Detaching a pane must not dial a new SSH connection');
  assert.equal(afterDetach.disconnects, 0, 'Detaching a pane must not disconnect the live session');
  assert.deepEqual(
    await page((origTab, detachedTab) => {
      const ids = [...document.querySelectorAll('[role=tab][data-tab-id]')].map(el => el.dataset.tabId);
      const origCount = document.querySelector('[role=tab][data-tab-id="' + origTab + '"] .tab-split-count')?.textContent?.trim();
      const detachedHasMiniBar = !!document.querySelector('[data-session-id="' + window.splitFixture.moved + '"] .pane-mini-bar');
      const sameTerm = document.querySelector('[data-session-id="' + window.splitFixture.moved + '"] .xterm') === window.splitFixture.originalTerminal;
      return { adjacent: ids[0] === origTab && ids[1] === detachedTab, origCount, detachedHasMiniBar, sameTerm };
    }, initial.tab, afterDetach.tab),
    { adjacent: true, origCount: '4', detachedHasMiniBar: false, sameTerm: true },
    'Detached pane must appear adjacent as a standalone tab reusing the exact xterm DOM instance',
  );
  await checkHistory();

  // Also verify Netcatty-style tab right-click menu can detach an individual pane from a split tab.
  await page((tabId) => {
    const tab = document.querySelector('[role=tab][data-tab-id="' + tabId + '"]');
    const r = tab.getBoundingClientRect();
    tab.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: r.x + 12, clientY: r.y + 12 }));
  }, initial.tab);
  await wait('tab context menu detach item', () => {
    const item = [...document.querySelectorAll('.context-menu-item')].find(el => el.textContent.trim() === '移出 Fixture New');
    if (!item) return false;
    item.click();
    return true;
  });
  await wait('second detached tab created', () => document.querySelectorAll('[role=tab][data-tab-id]').length === 3);
  const afterCtxDetach = await state();
  assert.equal(afterCtxDetach.disconnects, 0);
  assert.deepEqual(afterCtxDetach.dials, beforeSidebar.dials);
}
