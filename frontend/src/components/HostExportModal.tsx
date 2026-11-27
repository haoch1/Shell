import { useLayoutEffect, useMemo, useRef, useState } from 'react';
import { X } from 'lucide-react';
import { errorMessage, getApi } from '../lib/bridge';
import { formatHostDisplay } from '../lib/utils';
import type { HostExportFormat, HostExportResult, Profile } from '../types/shell';

export function HostExportModal({ profiles, initialIds, hideHostIp = false, onClose }: {
  profiles: Profile[]; initialIds: string[]; hideHostIp?: boolean; onClose: () => void;
}) {
  const [format, setFormat] = useState<HostExportFormat>('openssh');
  const [selected, setSelected] = useState<Set<string>>(() =>
    new Set(initialIds.length > 0 ? initialIds : profiles.map(p => p.id)),
  );
  const [result, setResult] = useState<HostExportResult | null>(null);
  const [error, setError] = useState('');
  const [canceled, setCanceled] = useState(false);
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  const dialog = useRef<HTMLDialogElement>(null);
  const ids = profiles.filter(p => selected.has(p.id)).map(p => p.id);
  const allSelected = profiles.length > 0 && ids.length === profiles.length;
  const savedIds = useMemo(() => new Set(profiles.map(p => p.id)), [profiles]);
  const includesJumpHost = format === 'openssh' && profiles.some(p =>
    selected.has(p.id) && p.jumpHostId && savedIds.has(p.jumpHostId) && !selected.has(p.jumpHostId));

  useLayoutEffect(() => {
    const modal = dialog.current;
    modal?.showModal();
    return () => { modal?.close(); };
  }, []);

  function toggle(id: string) {
    setSelected(current => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function toggleSelectAll() {
    setSelected(allSelected ? new Set() : new Set(profiles.map(p => p.id)));
  }

  async function save() {
    if (pending.current || !ids.length) return;
    pending.current = true;
    setBusy(true);
    setError('');
    setCanceled(false);
    try {
      const exportIds = ids.length === profiles.length ? [] : ids;
      const saved = await getApi().ExportHosts(exportIds, format);
      if (saved.path) setResult(saved);
      else setCanceled(true);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      pending.current = false;
      setBusy(false);
    }
  }

  function close() {
    if (!pending.current) onClose();
  }

  return (
    <dialog id="hostExportDialog" ref={dialog} className="modal-sheet host-export" aria-labelledby="exportTitle"
      onCancel={event => { event.preventDefault(); close(); }}
      onKeyDown={event => event.stopPropagation()}
      onClick={event => {
        if (event.target !== event.currentTarget) return;
        const rect = event.currentTarget.getBoundingClientRect();
        if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) close();
      }}>
      <form onSubmit={event => { event.preventDefault(); if (result) close(); else void save(); }}>
        <div className="modal-header">
          <h2 id="exportTitle">{result ? `已导出 ${result.count} 台主机` : '导出主机'}</h2>
          <button type="button" className="icon-btn" aria-label="关闭导出" disabled={busy} onClick={close}><X size={16} /></button>
        </div>
        <div className="modal-body">
          {result ? (
            <div className="host-export-result">
              <p className="host-export-path mono">{result.path}</p>
              {Boolean(result.warnings?.length) && (
                <ul className="host-export-notes">{Array.from(new Set(result.warnings)).map(note => <li key={note}>{note}</li>)}</ul>
              )}
            </div>
          ) : profiles.length === 0 ? (
            <div className="host-export-empty-wrap">
              <p className="host-export-empty">暂无可导出的主机</p>
            </div>
          ) : (
            <>
              <div className="field-group">
                <label className="field-label" htmlFor="hostExportFormat">文件格式</label>
                <select className="select" id="hostExportFormat" autoFocus value={format} disabled={busy}
                  onChange={event => setFormat(event.target.value as HostExportFormat)}>
                  <option value="openssh">OpenSSH config</option>
                  <option value="putty">PuTTY 注册表 (.reg)</option>
                </select>
              </div>
              <div className="host-export-list-section">
                <div className="host-export-list-head">
                  <div className="host-export-list-title">
                    <span className="field-label">选择主机</span>
                    <span className="host-export-count tabular">已选 {ids.length} / {profiles.length}</span>
                  </div>
                  <button
                    type="button"
                    id="toggleExportSelectAll"
                    className="host-export-select-all"
                    disabled={busy}
                    onClick={toggleSelectAll}
                  >
                    {allSelected ? '取消全选' : '全选'}
                  </button>
                </div>
                <div className="host-export-list" role="group" aria-label="选择要导出的主机">
                  {profiles.map(p => {
                    const addr = `${p.user}@${formatHostDisplay(p.host, p.port, hideHostIp)}`;
                    return (
                      <label key={p.id} title={addr}>
                        <input type="checkbox" value={p.id} checked={selected.has(p.id)} disabled={busy} onChange={() => toggle(p.id)} />
                        <span>{p.name || formatHostDisplay(p.host, p.port, hideHostIp)}</span>
                        <small className="mono">{addr}</small>
                      </label>
                    );
                  })}
                </div>
              </div>
              <div className="host-export-meta">
                <p className="host-export-note">
                  {includesJumpHost && <span id="exportJumpNotice">文件还会包含连接所需的跳板机。</span>}
                  仅导出连接配置。密码和私钥内容不会写入文件。
                </p>
                {error && <p className="error-text" role="alert">{error}</p>}
                {canceled && <p className="host-export-note" role="status">已取消保存</p>}
              </div>
            </>
          )}
        </div>
        <div className="modal-footer">
          {!result && <button type="button" className="btn" autoFocus={!profiles.length} disabled={busy} onClick={close}>取消</button>}
          <button type="submit" id="saveHostExport" className="btn btn-primary" disabled={!result && (busy || !ids.length)}>
            {result ? '完成' : busy ? '正在导出…' : '导出'}
          </button>
        </div>
      </form>
    </dialog>
  );
}
