import React, { useEffect, useState } from 'react';
import { FileUp, ScanSearch, X } from 'lucide-react';
import type { ImportedHost } from '../types/shell';
import { errorMessage, getApi, onDesktopEvent } from '../lib/bridge';
import { Checkbox } from './ui/Checkbox';

interface SSHConfigImportModalProps {
  onClose: () => void;
  onImported: (count: number) => void;
}

const SUPPORTED_TOOLS = [
  'Netcatty',
  'Xshell',
  'MobaXterm',
  'Termius',
  'Tabby',
  'Electerm',
  'FinalShell',
  'SecureCRT',
  'PuTTY / KiTTY',
  'WindTerm',
  'OpenSSH config',
  'CSV / JSON',
];

export const SSHConfigImportModal: React.FC<SSHConfigImportModalProps> = ({ onClose, onImported }) => {
  const [detectedFormat, setDetectedFormat] = useState('');
  const [hosts, setHosts] = useState<ImportedHost[]>([]);
  const [selected, setSelected] = useState<Record<number, boolean>>({});
  const [overrideGroup, setOverrideGroup] = useState('');
  const [showGroupOverride, setShowGroupOverride] = useState(false);
  const [busy, setBusy] = useState(false);
  const [importing, setImporting] = useState(false);
  const [err, setErr] = useState('');

  const applyResult = (fmt: string, list: ImportedHost[]) => {
    setDetectedFormat(fmt);
    setHosts(list || []);
    const init: Record<number, boolean> = {};
    (list || []).forEach((h, idx) => {
      init[idx] = !h.alreadySaved;
    });
    setSelected(init);
  };

  const pickFiles = async () => {
    if (busy) return;
    setBusy(true);
    setErr('');
    try {
      const res = await getApi().PickAndParseImportFiles();
      if (res?.hosts?.length) {
        applyResult(res.detectedFormat, res.hosts);
      } else if (res?.warnings?.length) {
        setErr(res.warnings.join('；'));
      }
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const scanLocalSSHConfig = async () => {
    if (busy) return;
    setBusy(true);
    setErr('');
    try {
      const res = await getApi().DiscoverLocalSSHConfigImport();
      applyResult(res.detectedFormat, res.hosts || []);
      if (!res.hosts?.length) {
        setErr('未在 ~/.ssh/config 中发现主机配置');
      }
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    return onDesktopEvent<[{ paths: string[] }]>('shell:filedrop', async (payload) => {
      if (!payload?.paths?.length) return;
      setBusy(true);
      setErr('');
      try {
        const res = await getApi().ParseImportFilePaths(payload.paths);
        if (res?.hosts?.length) {
          applyResult(res.detectedFormat, res.hosts);
        }
      } catch (e) {
        setErr(errorMessage(e));
      } finally {
        setBusy(false);
      }
    });
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const chosen = hosts.filter((_, idx) => selected[idx]);
  const allSelected = hosts.length > 0 && chosen.length === hosts.length;
  const someSelected = chosen.length > 0 && !allSelected;

  const toggleAll = (checked: boolean) => {
    const next: Record<number, boolean> = {};
    hosts.forEach((_, idx) => {
      next[idx] = checked;
    });
    setSelected(next);
  };

  const doImport = async () => {
    if (!chosen.length || importing) return;
    setImporting(true);
    setErr('');
    try {
      const saved = await getApi().CommitImportedHosts(chosen, showGroupOverride ? overrideGroup : '');
      onImported(saved.length);
      onClose();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setImporting(false);
    }
  };

  return (
    <div id="importDialog" className="modal-backdrop" onClick={onClose}>
      <div
        className="modal-sheet"
        style={{ width: 'min(600px, calc(100vw - 40px))', maxWidth: 600, ['--wails-drop-target' as string]: 'drop' }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="modal-header">
          <h2>导入配置</h2>
          <button type="button" className="icon-btn" aria-label="关闭" onClick={onClose}>
            <X size={15} />
          </button>
        </div>

        <div className="modal-body">
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
            <button
              id="pickImportFilesBtn"
              type="button"
              className="soft-card"
              disabled={busy}
              style={{
                cursor: 'pointer',
                alignItems: 'center',
                textAlign: 'center',
                padding: '18px 12px',
                borderStyle: 'dashed',
              }}
              onClick={() => void pickFiles()}
            >
              <FileUp size={20} style={{ color: 'var(--accent)' }} />
              <div style={{ fontWeight: 600, fontSize: 13 }}>选择或拖入文件</div>
            </button>

            <button
              id="scanOpenSSHBtn"
              type="button"
              className="soft-card"
              disabled={busy}
              style={{
                cursor: 'pointer',
                alignItems: 'center',
                textAlign: 'center',
                padding: '18px 12px',
              }}
              onClick={() => void scanLocalSSHConfig()}
            >
              <ScanSearch size={20} style={{ color: 'var(--ink-secondary)' }} />
              <div style={{ fontWeight: 600, fontSize: 13 }}>扫描 ~/.ssh/config</div>
            </button>
          </div>

          {hosts.length === 0 && (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
              {SUPPORTED_TOOLS.map((t) => (
                <span key={t} className="pill-badge neutral" style={{ fontSize: 11 }}>
                  {t}
                </span>
              ))}
            </div>
          )}

          {err && <p className="error-text">{err}</p>}

          {hosts.length > 0 && (
            <>
              <div className="select-list-card">
                <div className="select-list-header">
                  <label className="select-list-master">
                    <Checkbox
                      checked={allSelected}
                      indeterminate={someSelected}
                      aria-label={allSelected ? '取消全选导入主机' : '全选导入主机'}
                      onChange={() => toggleAll(!allSelected)}
                    />
                    <span className="tabular">
                      {chosen.length > 0 ? `已选 ${chosen.length} / ${hosts.length}` : '全选'}
                    </span>
                  </label>
                  <span className="pill-badge accent">{detectedFormat}</span>
                </div>

                <div className="import-host-list">
                  {hosts.map((h, idx) => (
                    <label
                      key={`${h.host}-${h.port}-${idx}`}
                      className={`import-host-row ${selected[idx] ? 'is-selected' : ''}`}
                    >
                      <Checkbox
                        checked={Boolean(selected[idx])}
                        onChange={(ev) => setSelected({ ...selected, [idx]: ev.target.checked })}
                      />
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                          <span style={{ fontWeight: 600, fontSize: 12.5 }}>{h.name}</span>
                          {h.group && (
                            <span className="pill-badge neutral" style={{ fontSize: 10.5 }}>
                              {h.group}
                            </span>
                          )}
                          {h.proxyJump && <span className="pill-badge accent">跳板 {h.proxyJump}</span>}
                          {h.proxyType && (
                            <span className="pill-badge neutral">
                              代理 {h.proxyType === 'system' ? '系统' : h.proxyType === 'direct' ? '直连' : `${h.proxyType} ${h.proxyHost || ''}`}
                            </span>
                          )}
                          {h.alreadySaved && <span className="pill-badge caution">已存在</span>}
                        </div>
                        <div className="mono" style={{ fontSize: 11.5, color: 'var(--ink-secondary)' }}>
                          {h.user}@{h.host}:{h.port}
                          {h.keyPath ? ` · ${h.keyPath.split(/[\\/]/).pop()}` : ''}
                        </div>
                      </div>
                    </label>
                  ))}
                </div>
              </div>

              <div style={{ display: 'flex', alignItems: 'center', gap: 10, minHeight: 28 }}>
                <label style={{ display: 'flex', alignItems: 'center', gap: 7, fontSize: 12, color: 'var(--ink-secondary)', cursor: 'pointer', whiteSpace: 'nowrap' }}>
                  <Checkbox
                    checked={showGroupOverride}
                    onChange={(e) => setShowGroupOverride(e.target.checked)}
                  />
                  <span>统一覆盖分组</span>
                </label>
                {showGroupOverride && (
                  <input
                    className="input input-sm"
                    placeholder="输入分组名称"
                    value={overrideGroup}
                    onChange={(e) => setOverrideGroup(e.target.value)}
                  />
                )}
              </div>
            </>
          )}
        </div>

        <div className="modal-footer">
          <button type="button" className="btn" onClick={onClose}>
            取消
          </button>
          <button
            type="button"
            className="btn btn-primary"
            style={{ minWidth: 112 }}
            disabled={!chosen.length || importing}
            onClick={() => void doImport()}
          >
            {importing ? '导入中…' : `一键导入 (${chosen.length})`}
          </button>
        </div>
      </div>
    </div>
  );
};
