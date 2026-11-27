import assert from 'node:assert/strict';

export async function verifyHostExport({ call, evaluate, until, click, shot }) {
  await call('Page.navigate', { url: await evaluate("location.origin + '/?host-export'") });
  await until(() => evaluate("location.search === '?host-export' && document.querySelectorAll('.host-row').length === 3"), 'export fixture');
  const layout = () => evaluate(`(() => {
    const source = document.querySelector('#importSshBtn');
    const target = document.querySelector('#exportHostsBtn');
    const footer = source?.closest('.sidebar-footer');
    if (!source || !target || !footer) return false;
    const a = source.getBoundingClientRect(), b = target.getBoundingClientRect(), f = footer.getBoundingClientRect();
    return source.nextElementSibling === target && a.width && b.left >= a.right && b.left - a.right < 7
      && Math.abs(a.top - b.top) < 1 && [...footer.querySelectorAll('button')].every(button => {
        const r = button.getBoundingClientRect();
        return r.left >= f.left && r.right <= f.right && r.bottom <= f.bottom;
      });
  })()`);
  for (const width of [880, 1280]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: 820, deviceScaleFactor: 2, mobile: false });
    await until(layout, 'adjacent import and export controls at ' + width + 'px');
  }
  await evaluate(`(() => {
    window.exportRequests = [];
    window.exportResponse = 'cancel';
    window.go.main.App.ExportHosts = async (ids, format) => {
      window.exportRequests.push({ ids, format });
      if (window.exportResponse === 'cancel') return { path: '', count: 0 };
      if (window.exportResponse === 'error') throw new Error('Test save failed');
      return { path: 'config', count: ids.length || 3,
        warnings: ids.length ? ['已自动包含连接所需的跳板机'] : [] };
    };
  })()`);
  const dialogRect = () => evaluate(`(() => {
    const r = document.querySelector('#hostExportDialog')?.getBoundingClientRect();
    return r ? { width: r.width, height: r.height } : null;
  })()`);
  const assertSameSize = async (expected, label) => {
    const actual = await dialogRect();
    assert.ok(
      actual && Math.abs(actual.width - expected.width) <= 1 && Math.abs(actual.height - expected.height) <= 1,
      `Expected fixed export dialog size (${label}): got ${JSON.stringify(actual)} vs ${JSON.stringify(expected)}`,
    );
  };

  await click('#exportHostsBtn');
  assert.equal(await evaluate("!!document.querySelector('#hostExportFormat')"), false, 'Format dropdown must not be present');
  assert.equal(await evaluate("document.querySelector('#hostExportDialog').matches(':modal')"), true);
  assert.equal(await evaluate("document.querySelectorAll('#hostExportDialog .host-export-list input[type=checkbox]:checked').length"), 3, 'All hosts must be selected by default');
  assert.equal(await evaluate("document.querySelector('#toggleExportSelectAll')?.textContent.trim()"), '取消全选');
  const baseRect = await dialogRect();
  assert.ok(baseRect && Math.abs(baseRect.width - 440) <= 2 && Math.abs(baseRect.height - 432) <= 2, 'Expected fixed 440x432 export dialog, got ' + JSON.stringify(baseRect));
  await shot?.('19-HostExport');
  assert.equal(await evaluate(`(() => {
    const modal = document.querySelector('#hostExportDialog');
    document.querySelector('#importSshBtn').focus();
    document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'n', ctrlKey: true, bubbles: true }));
    return modal.contains(document.activeElement) && !document.querySelector('#connectDialog');
  })()`), true, 'Export must contain focus and leave background shortcuts inactive');
  await click('#saveHostExport');
  await until(() => evaluate("document.querySelector('#hostExportDialog [role=status]')?.textContent === '已取消保存'"), 'cancel from save dialog');
  await assertSameSize(baseRect, 'after cancel');
  await evaluate("window.exportResponse = 'error'");
  await click('#saveHostExport');
  await until(() => evaluate("document.querySelector('#hostExportDialog [role=alert]')?.textContent === 'Test save failed'"), 'native save error');
  await assertSameSize(baseRect, 'after error');
  await evaluate("window.exportResponse = 'save'");
  await click('#saveHostExport');
  await until(() => evaluate("document.querySelector('.host-export-path')?.textContent === 'config'"), 'OpenSSH export result');
  assert.deepEqual(await evaluate('window.exportRequests.at(-1)'), { ids: [], format: 'openssh' });
  await assertSameSize(baseRect, 'after OpenSSH save result');
  await click('#saveHostExport');

  await click('#exportHostsBtn');
  await assertSameSize(baseRect, 'reopen default');
  await click('#toggleExportSelectAll');
  assert.equal(await evaluate("document.querySelectorAll('#hostExportDialog .host-export-list input[type=checkbox]:checked').length"), 0, 'Toggle select-all should unselect all hosts');
  assert.equal(await evaluate("document.querySelector('#toggleExportSelectAll')?.textContent.trim()"), '全选');
  assert.equal(await evaluate("document.querySelector('#saveHostExport').disabled"), true, 'An empty selection must not export every host');
  await assertSameSize(baseRect, 'all unselected');
  await click('#hostExportDialog input[value="p2"]');
  assert.equal(await evaluate("document.querySelector('#saveHostExport').disabled"), false);
  assert.ok(await evaluate("!!document.querySelector('#exportJumpNotice')"), 'The user must see that OpenSSH includes the extra jump host before saving');
  await assertSameSize(baseRect, 'single host with jump notice');
  await click('#saveHostExport');
  await until(() => evaluate("document.querySelector('.host-export-path')?.textContent === 'config'"), 'Single-host OpenSSH export result');
  assert.deepEqual(await evaluate('window.exportRequests.at(-1)'), { ids: ['p2'], format: 'openssh' });
  assert.equal(await evaluate("document.querySelector('.host-export-notes')?.textContent"), '已自动包含连接所需的跳板机');
  assert.equal(
    await evaluate("window.exportRequests.length >= 4 && window.exportRequests.every(req => req.format === 'openssh')"),
    true,
    'All export requests must use unified openssh format',
  );
  await assertSameSize(baseRect, 'after single-host OpenSSH save result');
  await click('#saveHostExport');

  await click('.sidebar-footer button[aria-label="批量管理"]');
  assert.equal(
    await evaluate("!!document.querySelector('.sidebar-footer.batch-bar button[aria-label=\"导出已选主机\"]')"),
    false,
    'Batch manage must not render duplicate export button',
  );
  await click('.sidebar-footer.batch-bar > div > button:last-child');
  return baseRect;
}

export async function verifyEmptyHostExport({ call, evaluate, until, click, expectedRect }) {
  // A fresh fixture page keeps the existing profiles and split sessions intact throughout the other smoke checks.
  const url = await evaluate("location.origin + '/?empty-hosts'");
  await call('Page.navigate', { url });
  await until(() => evaluate("location.search === '?empty-hosts' && !!document.querySelector('#exportHostsBtn') && !document.querySelector('.host-row')"), 'empty host fixture');
  await click('#exportHostsBtn');
  assert.equal(await evaluate("!!document.querySelector('#hostExportFormat')"), false, 'Empty export must not show format dropdown');
  assert.equal(await evaluate("document.querySelector('.host-export-empty')?.textContent"), '暂无可导出的主机');
  assert.equal(await evaluate("document.querySelector('#saveHostExport').disabled"), true);
  const emptyRect = await evaluate(`(() => {
    const r = document.querySelector('#hostExportDialog')?.getBoundingClientRect();
    return r ? { width: r.width, height: r.height } : null;
  })()`);
  const target = expectedRect || { width: 440, height: 432 };
  assert.ok(
    emptyRect && Math.abs(emptyRect.width - target.width) <= 2 && Math.abs(emptyRect.height - target.height) <= 2,
    'Expected empty export modal to keep fixed size ' + JSON.stringify(target) + ', got ' + JSON.stringify(emptyRect),
  );
}
