import assert from 'node:assert/strict';

export async function verifyHostExport({ call, evaluate, until, click, shot }) {
  await call('Page.navigate', { url: await evaluate("location.origin + '/?host-export'") });
  await until(() => evaluate("location.search === '?host-export' && document.querySelectorAll('.host-row').length === 3"), 'export fixture');
  const openExportFromSplitMenu = async () => {
    await click('#addHostMenuBtn');
    await until(() => evaluate("!!document.querySelector('#addHostMenu #exportHostsBtn')"), 'add-host split dropdown menu');
    await click('#exportHostsBtn');
  };
  const layout = () => evaluate(`(() => {
    const primary = document.querySelector('#addHost');
    const caret = document.querySelector('#addHostMenuBtn');
    const split = primary?.closest('.sidebar-new-split');
    const top = primary?.closest('.sidebar-top');
    const footer = document.querySelector('.sidebar-footer');
    if (!primary || !caret || !split || !top || !footer) return false;
    const a = primary.getBoundingClientRect(), b = caret.getBoundingClientRect(), s = split.getBoundingClientRect(), t = top.getBoundingClientRect(), f = footer.getBoundingClientRect();
    return a.width > 60 && b.width >= 24 && b.left >= a.right && Math.abs(a.top - b.top) < 1
      && s.left >= t.left && s.right <= t.right
      && !footer.querySelector('#importSshBtn') && !footer.querySelector('#exportHostsBtn')
      && [...footer.querySelectorAll('button')].every(button => {
        const r = button.getBoundingClientRect();
        return r.left >= f.left && r.right <= f.right && r.bottom <= f.bottom;
      });
  })()`);
  for (const width of [880, 1280]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: 820, deviceScaleFactor: 2, mobile: false });
    await until(layout, 'split new-host menu and balanced footer at ' + width + 'px');
  }
  await evaluate(`(() => {
    window.exportRequests = [];
    window.exportResponse = 'cancel';
    window.go.main.App.ExportHosts = async (ids, format) => {
      window.exportRequests.push({ ids, format });
      if (window.exportResponse === 'cancel') return { path: '', count: 0 };
      if (window.exportResponse === 'error') throw new Error('Test save failed');
      const isOpenSSH = format === 'openssh';
      return {
        path: isOpenSSH ? 'config' : 'Shell-hosts.csv',
        count: ids.length || 3,
        warnings: isOpenSSH && ids.length ? ['已自动包含连接所需的跳板机'] : [],
      };
    };
  })()`);
  const dialogRect = () =>
    until(
      () =>
        evaluate(`(() => {
          const el = document.querySelector('#hostExportDialog');
          if (!el) return null;
          if (el.getAnimations?.().some(a => a.playState === 'running')) return null;
          const r = el.getBoundingClientRect();
          return { width: r.width, height: r.height };
        })()`),
      'settled export dialog geometry',
    );
  const assertSameSize = async (expected, label) => {
    const actual = await dialogRect();
    assert.ok(
      actual && Math.abs(actual.width - expected.width) <= 1 && Math.abs(actual.height - expected.height) <= 1,
      `Expected fixed export dialog size (${label}): got ${JSON.stringify(actual)} vs ${JSON.stringify(expected)}`,
    );
  };

  await openExportFromSplitMenu();
  assert.equal(await evaluate("!!document.querySelector('#hostExportFormat')"), false, 'Legacy format dropdown must not be present');
  assert.equal(await evaluate("document.querySelector('#hostExportDialog').matches(':modal')"), true);
  assert.equal(await evaluate("document.querySelectorAll('#hostExportDialog .host-export-list input[type=checkbox]:checked').length"), 3, 'All hosts must be selected by default');
  const defaultControls = await evaluate(`(() => {
    const toggle = document.querySelector('#hostExportDialog .modal-header #exportFormatToggle');
    const csvBtn = toggle?.querySelector('[data-format="csv"]');
    const sshBtn = toggle?.querySelector('[data-format="openssh"]');
    const secretsCb = document.querySelector('#hostExportDialog .modal-footer #exportIncludeSecrets');
    const notes = document.querySelectorAll('#hostExportDialog .host-export-note, #exportJumpNotice').length;
    return {
      hasToggleInHeader: Boolean(toggle),
      csvText: csvBtn?.textContent?.trim() || '',
      sshText: sshBtn?.textContent?.trim() || '',
      csvPressed: csvBtn?.getAttribute('aria-pressed'),
      sshPressed: sshBtn?.getAttribute('aria-pressed'),
      hasSecretsInFooter: Boolean(secretsCb),
      secretsChecked: Boolean(secretsCb?.checked),
      notesCount: notes,
    };
  })()`);
  assert.deepEqual(
    defaultControls,
    {
      hasToggleInHeader: true,
      csvText: 'CSV',
      sshText: 'OpenSSH',
      csvPressed: 'true',
      sshPressed: 'false',
      hasSecretsInFooter: true,
      secretsChecked: true,
      notesCount: 0,
    },
    'Expected header #exportFormatToggle (default csv), footer #exportIncludeSecrets (default checked), and zero explanatory notes: ' + JSON.stringify(defaultControls),
  );
  const exportSelectMetrics = await evaluate(`(() => {
    const master = document.querySelector('#toggleExportSelectAll');
    const firstRow = document.querySelector('#hostExportDialog .host-export-list input[type=checkbox]');
    const labelText = master?.closest('.select-list-master')?.textContent?.trim() || '';
    return {
      isCheckbox: master?.tagName === 'INPUT' && master?.type === 'checkbox',
      checked: Boolean(master?.checked),
      indeterminate: Boolean(master?.indeterminate),
      labelText,
      masterLeft: master ? master.getBoundingClientRect().left : -1,
      firstRowLeft: firstRow ? firstRow.getBoundingClientRect().left : -1,
    };
  })()`);
  assert.equal(exportSelectMetrics.isCheckbox, true, 'Expected unified in-card master <Checkbox> for #toggleExportSelectAll');
  assert.equal(exportSelectMetrics.checked, true, 'Expected master checkbox checked by default');
  assert.equal(exportSelectMetrics.indeterminate, false, 'Expected master checkbox not indeterminate when all selected');
  assert.ok(exportSelectMetrics.labelText.includes('已选 3 / 3'), 'Expected count label on master checkbox, got ' + exportSelectMetrics.labelText);
  assert.ok(
    exportSelectMetrics.masterLeft > 0 && Math.abs(exportSelectMetrics.masterLeft - exportSelectMetrics.firstRowLeft) <= 0.5,
    'Expected export master checkbox aligned pixel-for-pixel with first row checkbox: ' + JSON.stringify(exportSelectMetrics),
  );
  const baseRect = await dialogRect();
  assert.ok(baseRect && Math.abs(baseRect.width - 440) <= 2 && Math.abs(baseRect.height - 432) <= 2, 'Expected fixed 440x432 export dialog, got ' + JSON.stringify(baseRect));
  await shot?.('19-HostExport');
  assert.equal(await evaluate(`(() => {
    const modal = document.querySelector('#hostExportDialog');
    document.querySelector('#addHostMenuBtn').focus();
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
  await until(() => evaluate("document.querySelector('.host-export-path')?.textContent === 'Shell-hosts.csv'"), 'Default CSV export result');
  assert.deepEqual(await evaluate('window.exportRequests.at(-1)'), { ids: [], format: 'csv' });
  assert.equal(await evaluate("!!document.querySelector('#exportFormatToggle')"), false, 'Format toggle should hide in result view');
  assert.equal(await evaluate("!!document.querySelector('#exportIncludeSecrets')"), false, 'Secrets checkbox should hide in result view');
  await assertSameSize(baseRect, 'after CSV save result');
  await click('#saveHostExport');

  // Unchecking #exportIncludeSecrets exports sanitized CSV without secrets
  await openExportFromSplitMenu();
  await assertSameSize(baseRect, 'reopen default');
  assert.equal(await evaluate("Boolean(document.querySelector('#exportIncludeSecrets')?.checked)"), true, 'Secrets checkbox must default to checked on reopen');
  await click('#exportIncludeSecrets');
  assert.equal(await evaluate("Boolean(document.querySelector('#exportIncludeSecrets')?.checked)"), false, 'Clicking #exportIncludeSecrets must uncheck it');
  await click('#saveHostExport');
  await until(() => evaluate("document.querySelector('.host-export-path')?.textContent === 'Shell-hosts.csv'"), 'Sanitized CSV export result');
  assert.deepEqual(await evaluate('window.exportRequests.at(-1)'), { ids: [], format: 'csv-sanitized' });
  await assertSameSize(baseRect, 'after csv-sanitized save result');
  await click('#saveHostExport');

  // Single host selection + OpenSSH toggle hides #exportIncludeSecrets and passes openssh
  await openExportFromSplitMenu();
  await assertSameSize(baseRect, 'reopen for openssh');
  await click('#toggleExportSelectAll');
  assert.equal(await evaluate("document.querySelectorAll('#hostExportDialog .host-export-list input[type=checkbox]:checked').length"), 0, 'Toggle select-all should unselect all hosts');
  const unselectedMaster = await evaluate(`(() => {
    const m = document.querySelector('#toggleExportSelectAll');
    return { checked: Boolean(m?.checked), indeterminate: Boolean(m?.indeterminate), label: m?.closest('.select-list-master')?.textContent?.trim() || '' };
  })()`);
  assert.deepEqual(unselectedMaster, { checked: false, indeterminate: false, label: '全选' }, 'Expected unchecked master checkbox with 全选 label after unselect-all');
  assert.equal(await evaluate("document.querySelector('#saveHostExport').disabled"), true, 'An empty selection must not export every host');
  await assertSameSize(baseRect, 'all unselected');
  await click('#hostExportDialog input[value="p2"]');
  const partialMaster = await evaluate(`(() => {
    const m = document.querySelector('#toggleExportSelectAll');
    return { checked: Boolean(m?.checked), indeterminate: Boolean(m?.indeterminate), label: m?.closest('.select-list-master')?.textContent?.trim() || '' };
  })()`);
  assert.deepEqual(partialMaster, { checked: false, indeterminate: true, label: '已选 1 / 3' }, 'Expected indeterminate master checkbox when 1/3 hosts selected');
  assert.equal(await evaluate("document.querySelector('#saveHostExport').disabled"), false);
  await click('#exportFormatToggle [data-format="openssh"]');
  assert.equal(
    await evaluate("document.querySelector('#exportFormatToggle [data-format=\"openssh\"]')?.getAttribute('aria-pressed')"),
    'true',
    'Expected OpenSSH segment active after click',
  );
  assert.equal(await evaluate("!!document.querySelector('#exportIncludeSecrets')"), false, 'Switching to OpenSSH must hide #exportIncludeSecrets');
  assert.equal(
    await evaluate("document.querySelectorAll('#hostExportDialog .host-export-note, #exportJumpNotice').length"),
    0,
    'Normal OpenSSH view must not show verbose .host-export-note or #exportJumpNotice',
  );
  await assertSameSize(baseRect, 'single host OpenSSH');
  await click('#saveHostExport');
  await until(() => evaluate("document.querySelector('.host-export-path')?.textContent === 'config'"), 'Single-host OpenSSH export result');
  assert.deepEqual(await evaluate('window.exportRequests.at(-1)'), { ids: ['p2'], format: 'openssh' });
  assert.equal(await evaluate("document.querySelector('.host-export-notes')?.textContent"), '已自动包含连接所需的跳板机');
  await assertSameSize(baseRect, 'after single-host OpenSSH save result');
  await click('#saveHostExport');

  await click('#batchManageBtn');
  assert.equal(
    await evaluate("!!document.querySelector('.sidebar-footer.batch-bar button[aria-label=\"导出已选主机\"]')"),
    false,
    'Batch manage must not render duplicate export button',
  );
  await click('.sidebar-select-header .sidebar-select-done');
  await until(() => evaluate("!document.querySelector('.sidebar-select-header')"), 'exit sidebar batch select mode');
  return baseRect;
}

export async function verifyEmptyHostExport({ call, evaluate, until, click, expectedRect }) {
  // A fresh fixture page keeps the existing profiles and split sessions intact throughout the other smoke checks.
  const url = await evaluate("location.origin + '/?empty-hosts'");
  await call('Page.navigate', { url });
  await until(() => evaluate("location.search === '?empty-hosts' && !!document.querySelector('#addHostMenuBtn') && !document.querySelector('.host-row')"), 'empty host fixture');
  await click('#addHostMenuBtn');
  await until(() => evaluate("!!document.querySelector('#addHostMenu #exportHostsBtn')"), 'empty host split dropdown menu');
  await click('#exportHostsBtn');
  assert.equal(await evaluate("!!document.querySelector('#hostExportFormat')"), false, 'Empty export must not show legacy format dropdown');
  assert.equal(await evaluate("!!document.querySelector('#exportFormatToggle')"), false, 'Empty export must not show #exportFormatToggle');
  assert.equal(await evaluate("!!document.querySelector('#exportIncludeSecrets')"), false, 'Empty export must not show #exportIncludeSecrets');
  assert.equal(await evaluate("document.querySelector('.host-export-empty')?.textContent"), '暂无可导出的主机');
  assert.equal(await evaluate("document.querySelector('#saveHostExport').disabled"), true);
  const target = expectedRect || { width: 440, height: 432 };
  const emptyRect = await until(
    () =>
      evaluate(`(() => {
        const el = document.querySelector('#hostExportDialog');
        if (!el) return null;
        if (el.getAnimations?.().some(a => a.playState === 'running')) return null;
        const r = el.getBoundingClientRect();
        return { width: r.width, height: r.height };
      })()`),
    'settled empty export modal geometry',
  );
  assert.ok(
    emptyRect && Math.abs(emptyRect.width - target.width) <= 2 && Math.abs(emptyRect.height - target.height) <= 2,
    'Expected empty export modal to keep fixed size ' + JSON.stringify(target) + ', got ' + JSON.stringify(emptyRect),
  );
}
