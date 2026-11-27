import React, { useEffect, useMemo, useState } from 'react';
import {
  ArrowUp,
  ChevronRight,
  Copy,
  Download,
  Eye,
  EyeOff,
  FileCode2,
  FilePlus,
  FileText,
  Folder,
  FolderPlus,
  Home,
  Pencil,
  RefreshCw,
  Search,
  Shield,
  Terminal,
  Trash2,
  Upload,
  X,
} from 'lucide-react';
import type { RemoteEntry, SessionRecord, TransferProgress } from '../types/shell';
import { copyToClipboard, errorMessage, getApi, onDesktopEvent } from '../lib/bridge';
import { formatBytes, formatModTime } from '../lib/utils';

interface SftpDrawerProps {
  session: SessionRecord;
  transfers: TransferProgress[];
  onClose: () => void;
  onUpdateDir: (dir: string) => void;
  onCdInTerminal: (dir: string) => void;
  onEditFile: (remotePath: string) => void;
  onToast: (msg: string) => void;
}

type SortKey = 'name' | 'size' | 'modTime';

export const SftpDrawer: React.FC<SftpDrawerProps> = ({
  session,
  transfers,
  onClose,
  onUpdateDir,
  onCdInTerminal,
  onEditFile,
  onToast,
}) => {
  const [dir, setDir] = useState(session.dir || '.');
  const [pathInput, setPathInput] = useState(session.dir || '.');
  const [entries, setEntries] = useState<RemoteEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');
  const [filter, setFilter] = useState('');
  const [showHidden, setShowHidden] = useState(false);
  const [sortBy, setSortBy] = useState<SortKey>('name');
  const [selected, setSelected] = useState<string | null>(null);
  const [dropActive, setDropActive] = useState(false);

  // Inline modal prompt for mkdir / new file / rename / chmod
  const [actionModal, setActionModal] = useState<
    | null
    | { kind: 'mkdir' | 'newfile'; value: string }
    | { kind: 'rename'; entry: RemoteEntry; value: string }
    | { kind: 'chmod'; entry: RemoteEntry; value: string }
    | { kind: 'delete'; entry: RemoteEntry }
  >(null);

  const loadDir = async (target: string) => {
    setLoading(true);
    setErr('');
    try {
      const api = getApi();
      const canonical = await api.RealPath(session.id, target || '.').catch(() => target || '.');
      const list = await api.ListFiles(session.id, canonical);
      setDir(canonical);
      setPathInput(canonical);
      onUpdateDir(canonical);
      setEntries(list || []);
      setSelected(null);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadDir(session.dir || '.');
  }, [session.id]);

  // Listen to native Windows file drops from Wails OnFileDrop
  useEffect(() => {
    const off = onDesktopEvent<[{ x: number; y: number; paths: string[] }]>('shell:filedrop', async (payload) => {
      if (!payload?.paths?.length) return;
      setDropActive(false);
      try {
        const done = await getApi().UploadLocalPaths(session.id, dir, payload.paths, true);
        if (done?.length) {
          onToast(`已上传 ${done.length} 个文件`);
          void loadDir(dir);
        }
      } catch (e) {
        setErr(errorMessage(e));
      }
    });
    return off;
  }, [session.id, dir]);

  const breadcrumbs = useMemo(() => {
    if (!dir || dir === '.') return [{ label: '~', path: '.' }];
    if (dir === '/') return [{ label: '/', path: '/' }];
    const parts = dir.split('/').filter(Boolean);
    const out = [{ label: '/', path: '/' }];
    let acc = '';
    for (const part of parts) {
      acc += '/' + part;
      out.push({ label: part, path: acc });
    }
    return out;
  }, [dir]);

  const visibleEntries = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return entries
      .filter((e) => (showHidden || !e.name.startsWith('.')) && (!q || e.name.toLowerCase().includes(q)))
      .sort((a, b) => {
        if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
        if (sortBy === 'size') return b.size - a.size;
        if (sortBy === 'modTime') return b.modTime - a.modTime;
        return a.name.localeCompare(b.name);
      });
  }, [entries, filter, showHidden, sortBy]);

  const parentDir = () => {
    if (dir === '/' || dir === '') return '/';
    const clean = dir.replace(/\/+$/, '');
    const idx = clean.lastIndexOf('/');
    if (idx <= 0) return '/';
    return clean.slice(0, idx);
  };

  const handleUpload = async () => {
    try {
      const paths = await getApi().PickUploadFiles();
      if (!paths?.length) return;
      const done = await getApi().UploadLocalPaths(session.id, dir, paths, true);
      if (done?.length) {
        onToast(`已上传 ${done.length} 个文件`);
        void loadDir(dir);
      }
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const handleDownload = async (entry: RemoteEntry) => {
    try {
      const saved = await getApi().DownloadFile(session.id, entry.path, true);
      if (saved) onToast(`已下载：${entry.name}`);
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const submitAction = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!actionModal) return;
    const api = getApi();
    try {
      if (actionModal.kind === 'mkdir') {
        const name = actionModal.value.trim();
        if (!name) return;
        const target = dir.endsWith('/') ? dir + name : dir + '/' + name;
        await api.MakeDir(session.id, target);
        onToast(`已创建文件夹 ${name}`);
      } else if (actionModal.kind === 'newfile') {
        const name = actionModal.value.trim();
        if (!name) return;
        const target = dir.endsWith('/') ? dir + name : dir + '/' + name;
        await api.CreateRemoteFile(session.id, target);
        onToast(`已创建文件 ${name}`);
      } else if (actionModal.kind === 'rename') {
        const nextName = actionModal.value.trim();
        if (!nextName || nextName === actionModal.entry.name) {
          setActionModal(null);
          return;
        }
        const parent = actionModal.entry.path.slice(0, actionModal.entry.path.lastIndexOf('/')) || '/';
        const nextPath = parent.endsWith('/') ? parent + nextName : parent + '/' + nextName;
        await api.RenameRemote(session.id, actionModal.entry.path, nextPath);
        onToast(`已重命名为 ${nextName}`);
      } else if (actionModal.kind === 'chmod') {
        const parsed = parseInt(actionModal.value.trim(), 8);
        if (!Number.isFinite(parsed) || parsed < 0 || parsed > 0o7777) {
          setErr('请输入有效的八进制权限，例如 755 或 644');
          return;
        }
        await api.ChmodRemote(session.id, actionModal.entry.path, parsed);
        onToast(`权限已更新为 ${actionModal.value.trim()}`);
      } else if (actionModal.kind === 'delete') {
        await api.DeleteRemote(session.id, actionModal.entry.path);
        onToast(`已删除 ${actionModal.entry.name}`);
      }
      setActionModal(null);
      void loadDir(dir);
    } catch (ex) {
      setErr(errorMessage(ex));
    }
  };

  const sessionTransfers = transfers.filter((t) => t.sessionId === session.id);

  return (
    <section
      id="filesPane"
      className="right-drawer"
      style={{ ['--wails-drop-target' as string]: 'drop' }}
      aria-label="SFTP 远程文件"
      onDragOver={(e) => {
        e.preventDefault();
        setDropActive(true);
      }}
      onDragLeave={() => setDropActive(false)}
      onDrop={(e) => {
        e.preventDefault();
        setDropActive(false);
      }}
    >
      <div className="drawer-header">
        <div className="drawer-title">
          <Folder size={15} strokeWidth={1.8} />
          <span>远程文件 (SFTP)</span>
        </div>
        <div style={{ display: 'flex', gap: 2 }}>
          <button
            type="button"
            className="icon-btn"
            title="在终端 cd 到此目录"
            onClick={() => onCdInTerminal(dir)}
          >
            <Terminal size={13} />
          </button>
          <button type="button" className="icon-btn" title="上传文件" onClick={handleUpload}>
            <Upload size={14} />
          </button>
          <button
            type="button"
            className="icon-btn"
            title="新建文件夹"
            onClick={() => setActionModal({ kind: 'mkdir', value: '' })}
          >
            <FolderPlus size={14} />
          </button>
          <button
            type="button"
            className="icon-btn"
            title="新建空文件"
            onClick={() => setActionModal({ kind: 'newfile', value: '' })}
          >
            <FilePlus size={14} />
          </button>
          <button type="button" className="icon-btn" title="关闭" onClick={onClose}>
            <X size={14} />
          </button>
        </div>
      </div>

      <div className="sftp-toolbar">
        <form
          style={{ display: 'flex', gap: 5 }}
          onSubmit={(e) => {
            e.preventDefault();
            void loadDir(pathInput);
          }}
        >
          <button type="button" className="icon-btn" title="主目录" onClick={() => void loadDir('.')}>
            <Home size={13} />
          </button>
          <button type="button" className="icon-btn" title="上一级" onClick={() => void loadDir(parentDir())}>
            <ArrowUp size={13} />
          </button>
          <input
            className="input input-sm mono"
            value={pathInput}
            onChange={(e) => setPathInput(e.target.value)}
            aria-label="远程路径"
          />
          <button type="button" className="icon-btn" title="刷新" onClick={() => void loadDir(dir)}>
            <RefreshCw size={13} className={loading ? 'spin' : ''} />
          </button>
        </form>

        <div className="sftp-breadcrumbs">
          {breadcrumbs.map((c, idx) => (
            <React.Fragment key={c.path}>
              {idx > 0 && <ChevronRight size={11} style={{ color: 'var(--ink-tertiary)', flexShrink: 0 }} />}
              <button
                type="button"
                className={`crumb-btn ${idx === breadcrumbs.length - 1 ? 'current' : ''}`}
                onClick={() => void loadDir(c.path)}
              >
                {c.label}
              </button>
            </React.Fragment>
          ))}
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <div className="search-box" style={{ flex: 1 }}>
            <Search size={12} />
            <input
              className="input input-sm"
              style={{ height: 26, paddingLeft: 26, paddingRight: 8, fontSize: 12 }}
              placeholder="筛选当前目录…"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
          </div>
          <select
            className="select"
            style={{ width: 76, height: 26, fontSize: 11.5 }}
            value={sortBy}
            onChange={(e) => setSortBy(e.target.value as SortKey)}
            aria-label="排序方式"
          >
            <option value="name">按名称</option>
            <option value="size">按大小</option>
            <option value="modTime">按时间</option>
          </select>
          <button
            type="button"
            className={`icon-btn ${showHidden ? 'active' : ''}`}
            style={{ height: 26, minWidth: 26 }}
            title={showHidden ? '隐藏点文件' : '显示隐藏文件'}
            onClick={() => setShowHidden((v) => !v)}
          >
            {showHidden ? <Eye size={13} /> : <EyeOff size={13} />}
          </button>
        </div>
        {err && <p className="error-text">{err}</p>}
      </div>

      <div className="drawer-body">
        {loading && entries.length === 0 ? (
          <div style={{ padding: 24, textAlign: 'center', color: 'var(--ink-tertiary)' }}>正在读取目录…</div>
        ) : visibleEntries.length === 0 ? (
          <div style={{ padding: 28, textAlign: 'center', color: 'var(--ink-tertiary)', fontSize: 12.5 }}>
            目录为空，可拖放本地文件到此处上传
          </div>
        ) : (
          visibleEntries.map((entry) => {
            const isSel = selected === entry.path;
            return (
              <div
                key={entry.path}
                className={`sftp-row ${isSel ? 'selected' : ''}`}
                onClick={() => setSelected(entry.path)}
                onDoubleClick={() => {
                  if (entry.isDir) void loadDir(entry.path);
                  else onEditFile(entry.path);
                }}
              >
                {entry.isDir ? (
                  <Folder size={15} strokeWidth={1.7} style={{ color: 'var(--accent)', flexShrink: 0 }} />
                ) : (
                  <FileText size={15} strokeWidth={1.6} style={{ color: 'var(--ink-secondary)', flexShrink: 0 }} />
                )}

                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="sftp-row-name" title={entry.name}>
                    {entry.name}
                    {entry.isSymlink ? ' ↗' : ''}
                  </div>
                  <div style={{ fontSize: 10.5, color: 'var(--ink-tertiary)' }} className="mono">
                    {entry.perm} · {formatModTime(entry.modTime)}
                  </div>
                </div>

                <span className="sftp-row-meta mono">
                  {entry.isDir ? '目录' : formatBytes(entry.size)}
                </span>

                <div className="sftp-row-actions">
                  {!entry.isDir && (
                    <>
                      <button
                        type="button"
                        className="host-action-btn"
                        title="在线编辑"
                        onClick={(e) => {
                          e.stopPropagation();
                          onEditFile(entry.path);
                        }}
                      >
                        <FileCode2 size={13} />
                      </button>
                      <button
                        type="button"
                        className="host-action-btn"
                        title="下载"
                        onClick={(e) => {
                          e.stopPropagation();
                          void handleDownload(entry);
                        }}
                      >
                        <Download size={13} />
                      </button>
                    </>
                  )}
                  <button
                    type="button"
                    className="host-action-btn"
                    title="复制路径"
                    onClick={(e) => {
                      e.stopPropagation();
                      void copyToClipboard(entry.path);
                      onToast('已复制远端路径');
                    }}
                  >
                    <Copy size={12} />
                  </button>
                  <button
                    type="button"
                    className="host-action-btn"
                    title="重命名"
                    onClick={(e) => {
                      e.stopPropagation();
                      setActionModal({ kind: 'rename', entry, value: entry.name });
                    }}
                  >
                    <Pencil size={12} />
                  </button>
                  <button
                    type="button"
                    className="host-action-btn"
                    title="修改权限 (chmod)"
                    onClick={(e) => {
                      e.stopPropagation();
                      setActionModal({ kind: 'chmod', entry, value: entry.mode.toString(8).padStart(3, '0') });
                    }}
                  >
                    <Shield size={12} />
                  </button>
                  <button
                    type="button"
                    className="host-action-btn"
                    title="删除"
                    style={{ color: 'var(--red)' }}
                    onClick={(e) => {
                      e.stopPropagation();
                      setActionModal({ kind: 'delete', entry });
                    }}
                  >
                    <Trash2 size={12} />
                  </button>
                </div>
              </div>
            );
          })
        )}
      </div>

      {sessionTransfers.length > 0 && (
        <div className="transfer-dock">
          {sessionTransfers.slice(-4).map((tr) => {
            const pct = tr.total > 0 ? Math.min(100, Math.round((tr.transferred / tr.total) * 100)) : tr.status === 'done' ? 100 : 0;
            return (
              <div key={tr.transferId} className="transfer-item">
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6 }}>
                  <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {tr.direction === 'upload' ? '↑ ' : '↓ '}
                    {tr.name}
                  </span>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span className="mono" style={{ color: tr.status === 'error' ? 'var(--red)' : 'var(--ink-tertiary)' }}>
                      {tr.status === 'done'
                        ? '完成'
                        : tr.status === 'error'
                          ? '失败'
                          : tr.status === 'canceled'
                            ? '已取消'
                            : `${pct}%`}
                    </span>
                    {tr.status === 'running' && (
                      <button
                        type="button"
                        className="host-action-btn"
                        style={{ width: 18, height: 18 }}
                        title="取消传输"
                        onClick={() => void getApi().CancelTransfer(tr.transferId)}
                      >
                        <X size={11} />
                      </button>
                    )}
                  </div>
                </div>
                <div className="metric-bar-track">
                  <div
                    className={`metric-bar-fill ${tr.status === 'error' ? 'high' : ''}`}
                    style={{ width: `${pct}%`, background: tr.status === 'done' ? 'var(--green)' : undefined }}
                  />
                </div>
              </div>
            );
          })}
        </div>
      )}

      {dropActive && (
        <div className="drop-overlay">
          <Upload size={26} />
          <span>松开即可上传到当前目录</span>
        </div>
      )}

      {actionModal && (
        <div className="modal-backdrop" onClick={() => setActionModal(null)}>
          <form className="modal-sheet" style={{ maxWidth: 380 }} onClick={(e) => e.stopPropagation()} onSubmit={submitAction}>
            <div className="modal-header">
              <h2>
                {actionModal.kind === 'mkdir'
                  ? '新建文件夹'
                  : actionModal.kind === 'newfile'
                    ? '新建空文件'
                    : actionModal.kind === 'rename'
                      ? '重命名'
                      : actionModal.kind === 'chmod'
                        ? '修改文件权限'
                        : '确认删除'}
              </h2>
              <button type="button" className="icon-btn" onClick={() => setActionModal(null)}>
                <X size={15} />
              </button>
            </div>
            <div className="modal-body">
              {actionModal.kind === 'delete' ? (
                <p style={{ margin: 0 }}>
                  确定要永久删除远端{actionModal.entry.isDir ? '目录（含内部所有文件）' : '文件'}「
                  <span className="mono">{actionModal.entry.name}</span>」？此操作不可撤销。
                </p>
              ) : (
                <div className="field-group">
                  <label className="field-label">
                    {actionModal.kind === 'chmod' ? '八进制权限（如 755 / 644）' : '名称'}
                  </label>
                  <input
                    className="input mono"
                    autoFocus
                    value={actionModal.value}
                    onChange={(e) => setActionModal({ ...actionModal, value: e.target.value })}
                    placeholder={actionModal.kind === 'chmod' ? '755' : ''}
                  />
                </div>
              )}
            </div>
            <div className="modal-footer">
              <button type="button" className="btn" onClick={() => setActionModal(null)}>
                取消
              </button>
              <button
                type="submit"
                className={`btn ${actionModal.kind === 'delete' ? 'btn-danger' : 'btn-primary'}`}
              >
                {actionModal.kind === 'delete' ? '永久删除' : '确定'}
              </button>
            </div>
          </form>
        </div>
      )}
    </section>
  );
};
