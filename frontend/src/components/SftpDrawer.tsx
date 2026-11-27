import React, { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowUp,
  ChevronRight,
  Eye,
  EyeOff,
  FilePlus,
  Folder,
  FolderPlus,
  Home,
  RefreshCw,
  Search,
  Terminal,
  Upload,
  X,
} from 'lucide-react';
import type { RemoteEntry, SessionRecord, TransferProgress } from '../types/shell';
import { copyToClipboard, errorMessage, getApi, onDesktopEvent } from '../lib/bridge';
import { SftpEntryList, type SftpRowAction } from './SftpEntryList';

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
const names = new Intl.Collator();

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
  const deferredFilter = useDeferredValue(filter);
  const [dropActive, setDropActive] = useState(false);
  const directoryRequest = useRef(0);
  const pendingDirectory = useRef('');
  const activationRequest = useRef(0);

  // Inline modal prompt for mkdir / new file / rename / chmod
  const [actionModal, setActionModal] = useState<
    | null
    | { kind: 'mkdir' | 'newfile'; value: string }
    | { kind: 'rename'; entry: RemoteEntry; value: string }
    | { kind: 'chmod'; entry: RemoteEntry; value: string }
    | { kind: 'delete'; entry: RemoteEntry }
  >(null);

  const loadDir = async (target: string, afterMutation = false) => {
    target = target || '.';
    if (pendingDirectory.current === target && !afterMutation) return;
    pendingDirectory.current = target;
    const request = ++directoryRequest.current;
    setLoading(true);
    setErr('');
    try {
      const { path: canonical, entries: list } = await getApi().BrowseDirectory(session.id, target);
      if (request !== directoryRequest.current) return;
      setDir(canonical);
      setPathInput(canonical);
      onUpdateDir(canonical);
      setEntries(list || []);
    } catch (e) {
      if (request === directoryRequest.current) setErr(errorMessage(e));
    } finally {
      if (request === directoryRequest.current) {
        pendingDirectory.current = '';
        setLoading(false);
      }
    }
  };

  useEffect(() => {
    void loadDir(session.dir || '.');
    return () => { directoryRequest.current++; pendingDirectory.current = ''; };
  }, [session.id]);

  // Listen to native Windows file drops from Wails OnFileDrop
  useEffect(() => {
    const off = onDesktopEvent<[{ x: number; y: number; paths: string[] }]>('shell:filedrop', async (payload) => {
      if (!payload?.paths?.length) return;
      const request = directoryRequest.current;
      setDropActive(false);
      try {
        const done = await getApi().UploadLocalPaths(session.id, dir, payload.paths, true);
        if (done?.length) {
          onToast(`已上传 ${done.length} 个文件`);
          if (request === directoryRequest.current) void loadDir(dir, true);
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

  const sortedEntries = useMemo(() => [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    if (sortBy === 'size') return b.size - a.size;
    if (sortBy === 'modTime') return b.modTime - a.modTime;
    return names.compare(a.name, b.name);
  }), [entries, sortBy]);
  const visibleEntries = useMemo(() => {
    const q = deferredFilter.trim().toLowerCase();
    return sortedEntries.filter((e) => (showHidden || !e.name.startsWith('.')) && (!q || e.name.toLowerCase().includes(q)));
  }, [sortedEntries, deferredFilter, showHidden]);

  const parentDir = () => {
    if (dir === '/' || dir === '') return '/';
    const clean = dir.replace(/\/+$/, '');
    const idx = clean.lastIndexOf('/');
    if (idx <= 0) return '/';
    return clean.slice(0, idx);
  };

  const handleUpload = async () => {
    const request = directoryRequest.current;
    try {
      const paths = await getApi().PickUploadFiles();
      if (!paths?.length || request !== directoryRequest.current) return;
      const done = await getApi().UploadLocalPaths(session.id, dir, paths, true);
      if (done?.length) {
        onToast(`已上传 ${done.length} 个文件`);
        if (request === directoryRequest.current) void loadDir(dir, true);
      }
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const rowAction = useRef<(kind: SftpRowAction, entry: RemoteEntry) => void>(() => {});
  rowAction.current = async (kind, entry) => {
    try {
      switch (kind) {
        case 'copy': await copyToClipboard(entry.path); onToast('已复制远端路径'); return;
        case 'rename': setActionModal({ kind, entry, value: entry.name }); return;
        case 'chmod': setActionModal({ kind, entry, value: entry.mode.toString(8).padStart(3, '0') }); return;
        case 'delete': setActionModal({ kind, entry }); return;
      }
      const request = directoryRequest.current;
      const activation = ++activationRequest.current;
      const target = entry.isSymlink ? await getApi().ResolveFileLink(session.id, entry.path) : entry;
      if (request !== directoryRequest.current || activation !== activationRequest.current) return;
      if (target.isDir) {
        if (kind === 'download') onToast('此链接指向目录');
        else void loadDir(entry.path);
      } else if (kind === 'download') {
        const saved = await getApi().DownloadFile(session.id, entry.path, true);
        if (saved) onToast(`已下载：${entry.name}`);
      } else {
        onEditFile(entry.path);
      }
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  const handleRowAction = useCallback((kind: SftpRowAction, entry: RemoteEntry) => rowAction.current(kind, entry), []);

  const submitAction = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!actionModal) return;
    const request = directoryRequest.current;
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
        const mode = actionModal.value.trim();
        if (!/^[0-7]{1,4}$/.test(mode)) {
          setErr('请输入有效的八进制权限，例如 755 或 644');
          return;
        }
        await api.ChmodRemote(session.id, actionModal.entry.path, parseInt(mode, 8));
        onToast(`权限已更新为 ${actionModal.value.trim()}`);
      } else if (actionModal.kind === 'delete') {
        await api.DeleteRemote(session.id, actionModal.entry.path);
        onToast(`已删除 ${actionModal.entry.name}`);
      }
      setActionModal(null);
      if (request === directoryRequest.current) void loadDir(dir, true);
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

      <SftpEntryList key={dir} entries={visibleEntries} busy={loading && entries.length === 0}
        filtered={Boolean(deferredFilter.trim())} onAction={handleRowAction} />

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
