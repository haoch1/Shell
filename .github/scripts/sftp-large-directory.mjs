import assert from 'node:assert/strict';

export async function verifyLargeDirectory(evaluate, until, { baseline = false } = {}) {
  const rows = 5000;
  await evaluate(`(() => {
    const api = window.go.main.App;
    const old = { ListFiles: api.ListFiles, BrowseDirectory: api.BrowseDirectory, ResolveFileLink: api.ResolveFileLink };
    const entries = Array.from({ length: ${rows} }, (_, index) => {
      const name = 'item-' + String(index).padStart(5, '0');
      return { name, path: '/fixture/' + name, size: index, mode: 420, modTime: 1730000000 + index,
        isDir: false, isSymlink: index % 5 === 1, perm: '-rw-r--r--' };
    });
    entries.push({ ...entries[0], name: '.hidden', path: '/fixture/.hidden' });
    window.sftpFixture = { browseCalls: 0, linkCalls: 0, restore: () => Object.assign(api, old) };
    api.ListFiles = async () => entries;
    api.BrowseDirectory = async () => { window.sftpFixture.browseCalls++; return { path: '/fixture', entries }; };
    api.ResolveFileLink = async (_id, path) => {
      window.sftpFixture.linkCalls++;
      const entry = entries.find(item => item.path === path);
      if (!entry?.isSymlink) throw new Error('Unexpected link lookup');
      return entry;
    };
    const marker = new MutationObserver(() => {
      if (!document.querySelector('.sftp-row-name[title="item-00000"]')) return;
      marker.disconnect();
      requestAnimationFrame(() => requestAnimationFrame(() => {
        window.sftpFixture.elapsed = Math.round(performance.now() - window.sftpFixture.start);
      }));
    });
    marker.observe(document.querySelector('#filesPane .drawer-body').parentElement, { childList: true, subtree: true });
    window.sftpFixture.start = performance.now();
    document.querySelector('.sftp-toolbar button[title="刷新"]').click();
  })()`);
  await until(() => evaluate("Number.isFinite(window.sftpFixture.elapsed)"), 'large SFTP directory', 45000);
  const metrics = await evaluate(`(() => {
    return { rows: document.querySelectorAll('.sftp-row').length,
      descendants: document.querySelector('#filesPane .drawer-body').getElementsByTagName('*').length,
      elapsedMs: window.sftpFixture.elapsed };
  })()`);
  console.log('[SFTP] ' + JSON.stringify({ fixtureEntries: rows, ...metrics }));
  try {
    if (baseline) return metrics;
    assert.ok(metrics.rows > 0 && metrics.rows < 60, 'The directory must mount only viewport rows and overscan');
    assert.equal(await evaluate("document.querySelector('.sftp-grid').getAttribute('aria-rowcount')"), String(rows));
    await evaluate("document.querySelector('.sftp-grid').style.maxHeight = '160px'");
    await until(() => evaluate("document.querySelectorAll('.sftp-row').length <= 12"), 'virtual rows after viewport resize');
    await evaluate("document.querySelector('.sftp-grid').style.maxHeight = ''");
    await evaluate(`(() => {
      const grid = document.querySelector('.sftp-grid');
      grid.focus(); grid.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }));
    })()`);
    await until(() => evaluate(`document.querySelector('.sftp-row[aria-selected="true"] .sftp-row-name')?.title === 'item-04999'
      && document.querySelector('.sftp-grid').scrollTop > 0`), 'keyboard navigation to final virtual file');
    await evaluate(`(() => {
      const input = document.querySelector('.sftp-toolbar input[placeholder]');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, 'item-04999');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    await until(() => evaluate("document.querySelector('.sftp-grid')?.getAttribute('aria-rowcount') === '1' && document.querySelector('.sftp-row-name')?.title === 'item-04999'"), 'filtering all underlying entries');
    await evaluate(`(() => {
      const input = document.querySelector('.sftp-toolbar input[placeholder]');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, '');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    async function checkOrder(first, sortBy, label) {
      await until(async () => {
        const state = await evaluate(`(() => {
          const grid = document.querySelector('#filesPane .sftp-grid');
          const firstRow = grid?.querySelector('[aria-rowindex="1"]');
          return { sort: document.querySelector('#filesPane select')?.value,
            filter: document.querySelector('.sftp-toolbar input[placeholder]')?.value,
            count: grid?.getAttribute('aria-rowcount'), scroll: grid?.scrollTop,
            first: firstRow?.querySelector('.sftp-row-name')?.title,
            firstMountedIndex: grid?.querySelector('.sftp-row')?.getAttribute('aria-rowindex') };
        })()`);
        if (state.sort === sortBy && state.count === String(rows) && state.first === first && state.scroll === 0) return true;
        throw Error(JSON.stringify(state));
      }, label);
    }
    await checkOrder('item-00000', 'name', 'clearing the directory filter after End');
    await evaluate(`(() => {
      const sort = document.querySelector('#filesPane select[aria-label="排序方式"]');
      sort.value = 'size';
      sort.dispatchEvent(new Event('change', { bubbles: true }));
    })()`);
    await checkOrder('item-04999', 'size', 'size sorting');
    await evaluate(`document.querySelector('#filesPane button[title="显示隐藏文件"]').click()`);
    await until(() => evaluate("document.querySelector('.sftp-grid')?.getAttribute('aria-rowcount') === '5001'"), 'hidden-file toggle');
    assert.deepEqual(await evaluate('({ browse: window.sftpFixture.browseCalls, links: window.sftpFixture.linkCalls })'),
      { browse: 1, links: 0 });
    await evaluate(`document.querySelector('.sftp-row-name[title="item-04996"]').closest('.sftp-row')
      .dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))`);
    await until(() => evaluate("document.querySelector('.modal-sheet.editor h2')?.textContent.includes('item-04996')"), 'opening a single symlink');
    assert.equal(await evaluate('window.sftpFixture.linkCalls'), 1, 'Only the activated link should be inspected');
    await evaluate("document.querySelector('.modal-sheet.editor .icon-btn').click()");
    return metrics;
  } finally {
    await evaluate('window.sftpFixture.restore()');
  }
}
