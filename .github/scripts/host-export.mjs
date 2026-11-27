import assert from 'node:assert/strict';

export async function verifyHostExport({ call, evaluate, until, click }) {
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
      return { path: format === 'openssh' ? 'config' : 'hosts.reg', count: ids.length || 3,
        warnings: format === 'putty' ? ['测试字段未映射'] : [] };
    };
  })()`);
  await click('#exportHostsBtn');
  assert.equal(await evaluate("document.querySelector('#hostExportFormat').value"), 'openssh');
  assert.equal(await evaluate("document.querySelector('#hostExportDialog').matches(':modal')"), true);
  assert.equal(await evaluate(`(() => {
    const modal = document.querySelector('#hostExportDialog');
    document.querySelector('#importSshBtn').focus();
    document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'n', ctrlKey: true, bubbles: true }));
    return modal.contains(document.activeElement) && !document.querySelector('#connectDialog');
  })()`), true, 'Export must contain focus and leave background shortcuts inactive');
  await click('#saveHostExport');
  await until(() => evaluate("document.querySelector('#hostExportDialog [role=status]')?.textContent === '已取消保存'"), 'cancel from save dialog');
  await evaluate("window.exportResponse = 'error'");
  await click('#saveHostExport');
  await until(() => evaluate("document.querySelector('#hostExportDialog [role=alert]')?.textContent === 'Test save failed'"), 'native save error');
  await evaluate("window.exportResponse = 'save'");
  await click('#saveHostExport');
  await until(() => evaluate("document.querySelector('.host-export-path')?.textContent === 'config'"), 'OpenSSH export result');
  assert.deepEqual(await evaluate('window.exportRequests.at(-1)'), { ids: [], format: 'openssh' });
  await click('#saveHostExport');
  await click('#exportHostsBtn');
  await click('#hostExportDialog [data-scope=selected]');
  assert.equal(await evaluate("document.querySelector('#saveHostExport').disabled"), true, 'An empty selection must not export every host');
  await click('#hostExportDialog input[value="p2"]');
  assert.ok(await evaluate("!!document.querySelector('#exportJumpNotice')"), 'The user must see that OpenSSH includes the extra jump host before saving');
  await evaluate(`(() => {
    const format = document.querySelector('#hostExportFormat');
    format.value = 'putty'; format.dispatchEvent(new Event('change', { bubbles: true }));
  })()`);
  assert.equal(await evaluate("!!document.querySelector('#exportJumpNotice')"), false);
  await click('#saveHostExport');
  await until(() => evaluate("document.querySelector('.host-export-path')?.textContent === 'hosts.reg'"), 'PuTTY export result');
  assert.deepEqual(await evaluate('window.exportRequests.at(-1)'), { ids: ['p2'], format: 'putty' });
  assert.equal(await evaluate("document.querySelector('.host-export-notes')?.textContent"), '测试字段未映射');
  await click('#saveHostExport');
  await click('.sidebar-footer button[aria-label="批量管理"]');
  await click('.host-row');
  await click('.sidebar-footer button[aria-label="导出已选主机"]');
  assert.equal(await evaluate("document.querySelector('#hostExportDialog [data-scope=selected]').getAttribute('aria-pressed')"), 'true');
  assert.equal(await evaluate("document.querySelectorAll('#hostExportDialog input:checked').length"), 1);
  await click('#hostExportDialog button[aria-label="关闭导出"]');
  await click('.sidebar-footer.batch-bar > div > button:last-child');
}

export async function verifyEmptyHostExport({ call, evaluate, until, click }) {
  // A fresh fixture page keeps the existing profiles and split sessions intact throughout the other smoke checks.
  const url = await evaluate("location.origin + '/?empty-hosts'");
  await call('Page.navigate', { url });
  await until(() => evaluate("location.search === '?empty-hosts' && !!document.querySelector('#exportHostsBtn') && !document.querySelector('.host-row')"), 'empty host fixture');
  await click('#exportHostsBtn');
  assert.equal(await evaluate("document.querySelector('.host-export-empty')?.textContent"), '暂无可导出的主机');
  assert.equal(await evaluate("document.querySelector('#saveHostExport').disabled"), true);
}
