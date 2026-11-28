import React, { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowDown,
  ArrowDownWideNarrow,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  ArrowUpNarrowWide,
  Bookmark,
  BookmarkCheck,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  Copy,
  Eye,
  EyeOff,
  FilePlus,
  FileUp,
  Folder,
  FolderPlus,
  FolderSearch,
  FolderUp,
  Home,
  Loader2,
  RefreshCw,
  Search,
  Terminal,
  Upload,
  X,
} from 'lucide-react';
import type { RemoteEntry, RemoteEntryDetail, SessionRecord, TransferProgress } from '../types/shell';
import { copyToClipboard, errorMessage, getApi, onDesktopEvent } from '../lib/bridge';
import { formatBytes } from '../lib/utils';
import {
  basenameRemotePath,
  bitsToMode,
  CHMOD_PRESETS,
  clampDrawerWidth,
  classifyFileKind,
  DEFAULT_SFTP_BOOKMARKS,
  formatFullTimestamp,
  getBookmarkStorageKey,
  getFileKindLabel,
  joinRemotePath,
  modeToBits,
  modeToOctal,
  modeToSymbolic,
  normalizeRemotePath,
  octalToMode,
  parentRemotePath,
  parseStoredBookmarks,
  pushNavHistory,
  resolveDestinationPath,
  SFTP_WIDTH_DEFAULT,
  SFTP_WIDTH_STORAGE_KEY,
  sortRemoteEntries,
  stepNavHistory,
  suggestCopyName,
  type ChmodBits,
  type SftpNavHistory,
  type SftpSortDir,
  type SftpSortKey,
} from '../lib/sftpUtils';
import { Select } from './ui/Select';
import { Checkbox } from './ui/Checkbox';
import {
  EntryKindIcon,
  SftpEntryList,
  type SftpBatchAction,
  type SftpEmptyAction,
  type SftpRowAction,
} from './SftpEntryList';
import '../styles/sftp.css';

export interface SftpDrawerProps {
  session: SessionRecord;
  transfers: TransferProgress[];
  onClose: () => void;
  onUpdateDir: (dir: string) => void;
  onCdInTerminal: (dir: string) => void;
  onEditFile: (remotePath: string) => void;
  onToast: (msg: string) => void;
}

type ActionModalState =
  | null
  | { kind: 'mkdir'; value: string }
  | { kind: 'newfile'; value: string; openAfterCreate: boolean }
  | { kind: 'rename'; entry: RemoteEntry; value: string }
  | { kind: 'copy'; entry: RemoteEntry; value: string }
  | { kind: 'chmod'; entries: RemoteEntry[]; octal: string; mode: number; recursive: boolean }
  | { kind: 'delete'; entries: RemoteEntry[] }
  | {
      kind: 'properties';
      entry: RemoteEntry;
      loading: boolean;
      detail: RemoteEntryDetail | null;
      error: string;
    };

const SORT_OPTIONS = [
  { value: 'name', label: '按名称' },
  { value: 'size', label: '按大小' },
  { value: 'modTime', label: '按时间' },
  { value: 'type', label: '按类型' },
];

const CHMOD_WHO: readonly { key: keyof ChmodBits; label: string }[] = [
  { key: 'owner', label: '所有者 (Owner)' },
  { key: 'group', label: '用户组 (Group)' },
  { key: 'other', label: '其他用户 (Other)' },
];

function readInitialDrawerWidth(): number {
  try {
    const saved = window.localStorage.getItem(SFTP_WIDTH_STORAGE_KEY);
    if (saved) return clampDrawerWidth(Number(saved));
  } catch {
    // Ignore storage read errors in restricted environments.
  }
  return SFTP_WIDTH_DEFAULT;
}

export const SftpDrawer: React.FC<SftpDrawerProps> = ({
  session,
  transfers,
  onClose,
  onUpdateDir,
  onCdInTerminal,
  onEditFile,
  onToast,
}) => {
  const [drawerWidth, setDrawerWidth] = useState<number>(readInitialDrawerWidth);
  const [resizing, setResizing] = useState(false);

  const [dir, setDir] = useState(session.dir || '.');
  const [pathInput, setPathInput] = useState(session.dir || '.');
  const [entries, setEntries] = useState<RemoteEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');
  const [filter, setFilter] = useState('');
  const [showHidden, setShowHidden] = useState(false);
  const [sortBy, setSortBy] = useState<SftpSortKey>('name');
  const [sortDir, setSortDir] = useState<SftpSortDir>('asc');
  const deferredFilter = useDeferredValue(filter);
  const [dropActive, setDropActive] = useState(false);

  // Navigation history stack
  const [navHistory, setNavHistory] = useState<SftpNavHistory>({ stack: [], index: -1 });
  const currentDirRef = useRef(dir);
  currentDirRef.current = dir;

  // Quick bookmarks per host
  const bookmarkStorageKey = useMemo(
    () => getBookmarkStorageKey(session.profile.id || session.profile.host),
    [session.profile.id, session.profile.host],
  );
  const [bookmarksOpen, setBookmarksOpen] = useState(false);
  const [pinnedBookmarks, setPinnedBookmarks] = useState<string[]>(() => {
    try {
      return parseStoredBookmarks(window.localStorage.getItem(bookmarkStorageKey));
    } catch {
      return [];
    }
  });
  const bookmarkAnchorRef = useRef<HTMLDivElement>(null);
  const breadcrumbsRef = useRef<HTMLDivElement>(null);

  // Selection state (shared across entry list, batch bar, modals, footer)
  const [selectedPaths, setSelectedPaths] = useState<Set<string>>(() => new Set());

  // Transfer dock UI state
  const [dismissedTransferIds, setDismissedTransferIds] = useState<Set<string>>(() => new Set());
  const [transferCollapsed, setTransferCollapsed] = useState(false);

  // Modals & mutation busy state
  const [actionModal, setActionModal] = useState<ActionModalState>(null);
  const [modalBusy, setModalBusy] = useState(false);

  const directoryRequest = useRef(0);
  const pendingDirectory = useRef('');
  const activationRequest = useRef(0);
  const statRequest = useRef(0);

  // Reload pinned bookmarks when session/host changes
  useEffect(() => {
    try {
      setPinnedBookmarks(parseStoredBookmarks(window.localStorage.getItem(bookmarkStorageKey)));
    } catch {
      setPinnedBookmarks([]);
    }
  }, [bookmarkStorageKey]);

  // Close bookmarks popover on outside click / Escape
  useEffect(() => {
    if (!bookmarksOpen) return;
    const onPointerDown = (e: PointerEvent) => {
      if (bookmarkAnchorRef.current && !bookmarkAnchorRef.current.contains(e.target as Node)) {
        setBookmarksOpen(false);
      }
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        setBookmarksOpen(false);
      }
    };
    window.addEventListener('pointerdown', onPointerDown, true);
    window.addEventListener('keydown', onKeyDown, true);
    return () => {
      window.removeEventListener('pointerdown', onPointerDown, true);
      window.removeEventListener('keydown', onKeyDown, true);
    };
  }, [bookmarksOpen]);

  // Close active modal on Escape
  useEffect(() => {
    if (!actionModal) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !modalBusy) {
        e.stopPropagation();
        setActionModal(null);
      }
    };
    window.addEventListener('keydown', onKeyDown, true);
    return () => window.removeEventListener('keydown', onKeyDown, true);
  }, [actionModal, modalBusy]);

  // Keep deepest breadcrumb scrolled into view
  useEffect(() => {
    const el = breadcrumbsRef.current;
    if (el) {
      el.scrollLeft = el.scrollWidth;
    }
  }, [dir]);

  const loadDir = useCallback(
    async (
      target: string,
      options?: { afterMutation?: boolean; historyMode?: 'push' | 'replace' | 'none' },
    ) => {
      const normalizedTarget = target || '.';
      const afterMutation = options?.afterMutation ?? false;
      const historyMode = options?.historyMode ?? 'push';

      if (pendingDirectory.current === normalizedTarget && !afterMutation) return;
      pendingDirectory.current = normalizedTarget;
      const request = ++directoryRequest.current;
      const prevDir = currentDirRef.current;
      setLoading(true);
      setErr('');
      try {
        const { path: canonical, entries: list } = await getApi().BrowseDirectory(
          session.id,
          normalizedTarget,
        );
        if (request !== directoryRequest.current) return;
        const nextEntries = list || [];
        const dirChanged = canonical !== prevDir;
        setDir(canonical);
        setPathInput(canonical);
        onUpdateDir(canonical);
        setEntries(nextEntries);

        if (dirChanged) {
          setSelectedPaths(new Set());
          setFilter('');
        } else {
          setSelectedPaths((prev) => {
            if (prev.size === 0) return prev;
            const validPaths = new Set(nextEntries.map((item) => item.path));
            const pruned = new Set<string>();
            prev.forEach((p) => {
              if (validPaths.has(p)) pruned.add(p);
            });
            return pruned.size === prev.size ? prev : pruned;
          });
        }

        if (historyMode === 'push') {
          setNavHistory((prev) => pushNavHistory(prev, canonical));
        } else if (historyMode === 'replace') {
          setNavHistory({ stack: [canonical], index: 0 });
        }
      } catch (e) {
        if (request === directoryRequest.current) setErr(errorMessage(e));
      } finally {
        if (request === directoryRequest.current) {
          pendingDirectory.current = '';
          setLoading(false);
        }
      }
    },
    [session.id, onUpdateDir],
  );

  useEffect(() => {
    void loadDir(session.dir || '.', { historyMode: 'replace', afterMutation: true });
    return () => {
      directoryRequest.current++;
      pendingDirectory.current = '';
    };
  }, [session.id]);

  // Listen to native Windows file & folder drops from Wails OnFileDrop
  useEffect(() => {
    const off = onDesktopEvent<[{ x: number; y: number; paths: string[] }]>(
      'shell:filedrop',
      async (payload) => {
        if (!payload?.paths?.length) return;
        const request = directoryRequest.current;
        const targetDir = currentDirRef.current;
        setDropActive(false);
        try {
          const done = await getApi().UploadLocalPaths(session.id, targetDir, payload.paths, true);
          if (done?.length) {
            onToast(`已上传 ${done.length} 项`);
            if (request === directoryRequest.current) {
              void loadDir(targetDir, { afterMutation: true, historyMode: 'none' });
            }
          }
        } catch (e) {
          setErr(errorMessage(e));
        }
      },
    );
    return off;
  }, [session.id, loadDir, onToast]);

  // Left-edge drag resize handler
  const handleResizePointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    const startX = e.clientX;
    const startWidth = drawerWidth;
    let latestWidth = startWidth;

    setResizing(true);
    const prevCursor = document.body.style.cursor;
    const prevSelect = document.body.style.userSelect;
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';

    const onMove = (moveEvent: PointerEvent) => {
      latestWidth = clampDrawerWidth(startWidth + (startX - moveEvent.clientX));
      setDrawerWidth(latestWidth);
    };

    const onUp = () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
      document.body.style.cursor = prevCursor;
      document.body.style.userSelect = prevSelect;
      setResizing(false);
      try {
        window.localStorage.setItem(SFTP_WIDTH_STORAGE_KEY, String(latestWidth));
      } catch {
        // Ignore storage write errors.
      }
    };

    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
  };

  const resetDrawerWidth = () => {
    setDrawerWidth(SFTP_WIDTH_DEFAULT);
    try {
      window.localStorage.setItem(SFTP_WIDTH_STORAGE_KEY, String(SFTP_WIDTH_DEFAULT));
    } catch {
      // Ignore storage write errors.
    }
  };

  const canGoBack = navHistory.index > 0;
  const canGoForward = navHistory.index >= 0 && navHistory.index < navHistory.stack.length - 1;

  const handleHistoryBack = useCallback(() => {
    const step = stepNavHistory(navHistory, -1);
    if (!step.path) return;
    setNavHistory(step.history);
    void loadDir(step.path, { afterMutation: true, historyMode: 'none' });
  }, [navHistory, loadDir]);

  const handleHistoryForward = useCallback(() => {
    const step = stepNavHistory(navHistory, 1);
    if (!step.path) return;
    setNavHistory(step.history);
    void loadDir(step.path, { afterMutation: true, historyMode: 'none' });
  }, [navHistory, loadDir]);

  const handleGoParent = useCallback(() => {
    const target = parentRemotePath(currentDirRef.current);
    if (target !== currentDirRef.current) {
      void loadDir(target);
    }
  }, [loadDir]);

  const handleRefresh = useCallback(() => {
    void loadDir(currentDirRef.current, { afterMutation: true, historyMode: 'none' });
  }, [loadDir]);

  const persistBookmarks = (next: string[]) => {
    setPinnedBookmarks(next);
    try {
      window.localStorage.setItem(bookmarkStorageKey, JSON.stringify(next));
    } catch {
      // Ignore storage write errors.
    }
  };

  const normalizedDir = normalizeRemotePath(dir);
  const isCurrentPinned = pinnedBookmarks.includes(normalizedDir);

  const togglePinCurrentDir = () => {
    if (!normalizedDir || normalizedDir === '.') return;
    if (isCurrentPinned) {
      persistBookmarks(pinnedBookmarks.filter((p) => p !== normalizedDir));
      onToast('已取消收藏当前目录');
    } else {
      persistBookmarks([normalizedDir, ...pinnedBookmarks.filter((p) => p !== normalizedDir)].slice(0, 24));
      onToast('已收藏当前目录');
    }
  };

  const removeBookmark = (pathToRemove: string) => {
    persistBookmarks(pinnedBookmarks.filter((p) => p !== pathToRemove));
  };

  const breadcrumbs = useMemo(() => {
    if (!dir || dir === '.') return [{ label: '~', path: '.' }];
    if (dir === '/') return [{ label: '/', path: '/' }];
    const parts = dir.split('/').filter(Boolean);
    const out = [{ label: '/', path: '/' }];
    let acc = '';
    for (const part of parts) {
      acc += `/${part}`;
      out.push({ label: part, path: acc });
    }
    return out;
  }, [dir]);

  const sortedEntries = useMemo(
    () => sortRemoteEntries(entries, sortBy, sortDir),
    [entries, sortBy, sortDir],
  );

  const visibleEntries = useMemo(() => {
    const q = deferredFilter.trim().toLowerCase();
    return sortedEntries.filter(
      (e) => (showHidden || !e.name.startsWith('.')) && (!q || e.name.toLowerCase().includes(q)),
    );
  }, [sortedEntries, deferredFilter, showHidden]);

  const selectedVisibleEntries = useMemo(
    () => visibleEntries.filter((e) => selectedPaths.has(e.path)),
    [visibleEntries, selectedPaths],
  );

  const selectedBytes = useMemo(
    () =>
      selectedVisibleEntries.reduce(
        (sum, entry) => (entry.isDir || entry.isSymlink ? sum : sum + entry.size),
        0,
      ),
    [selectedVisibleEntries],
  );

  const visibleSummary = useMemo(() => {
    let dirs = 0;
    let files = 0;
    for (const entry of visibleEntries) {
      if (entry.isDir) dirs++;
      else files++;
    }
    return { dirs, files };
  }, [visibleEntries]);

  const handleUploadFiles = useCallback(async () => {
    const request = directoryRequest.current;
    const targetDir = currentDirRef.current;
    try {
      const paths = await getApi().PickUploadFiles();
      if (!paths?.length || request !== directoryRequest.current) return;
      const done = await getApi().UploadLocalPaths(session.id, targetDir, paths, true);
      if (done?.length) {
        onToast(`已上传 ${done.length} 个文件`);
        if (request === directoryRequest.current) {
          void loadDir(targetDir, { afterMutation: true, historyMode: 'none' });
        }
      }
    } catch (e) {
      setErr(errorMessage(e));
    }
  }, [session.id, loadDir, onToast]);

  const handleUploadFolder = useCallback(async () => {
    const request = directoryRequest.current;
    const targetDir = currentDirRef.current;
    try {
      const folder = await getApi().PickUploadFolder();
      if (!folder || request !== directoryRequest.current) return;
      const done = await getApi().UploadLocalPaths(session.id, targetDir, [folder], true);
      if (done?.length) {
        onToast(`已上传文件夹：${basenameRemotePath(done[0])}`);
      } else {
        onToast('已上传文件夹');
      }
      if (request === directoryRequest.current) {
        void loadDir(targetDir, { afterMutation: true, historyMode: 'none' });
      }
    } catch (e) {
      setErr(errorMessage(e));
    }
  }, [session.id, loadDir, onToast]);

  const openPropertiesModal = useCallback(
    (entry: RemoteEntry) => {
      const reqId = ++statRequest.current;
      setActionModal({ kind: 'properties', entry, loading: true, detail: null, error: '' });
      void getApi()
        .StatRemote(session.id, entry.path)
        .then((detail) => {
          if (reqId !== statRequest.current) return;
          setActionModal((prev) =>
            prev && prev.kind === 'properties' && prev.entry.path === entry.path
              ? { ...prev, loading: false, detail, error: '' }
              : prev,
          );
        })
        .catch((e) => {
          if (reqId !== statRequest.current) return;
          setActionModal((prev) =>
            prev && prev.kind === 'properties' && prev.entry.path === entry.path
              ? { ...prev, loading: false, detail: null, error: errorMessage(e) }
              : prev,
          );
        });
    },
    [session.id],
  );

  const openChmodModal = useCallback((targets: RemoteEntry[]) => {
    if (!targets.length) return;
    const baseMode = (targets[0].mode || 0o644) & 0o7777;
    setActionModal({
      kind: 'chmod',
      entries: targets,
      mode: baseMode,
      octal: modeToOctal(baseMode),
      recursive: false,
    });
  }, []);

  const rowActionRef = useRef<(action: SftpRowAction, entry: RemoteEntry) => void>(() => {});
  rowActionRef.current = async (action, entry) => {
    setErr('');
    try {
      switch (action) {
        case 'copyPath': {
          await copyToClipboard(entry.path);
          onToast('已复制远端路径');
          return;
        }
        case 'cd': {
          onCdInTerminal(entry.isDir ? entry.path : parentRemotePath(entry.path));
          return;
        }
        case 'duplicate': {
          const suggested = suggestCopyName(
            entry.name,
            entry.isDir,
            entries.map((item) => item.name),
          );
          setActionModal({ kind: 'copy', entry, value: suggested });
          return;
        }
        case 'rename': {
          setActionModal({ kind: 'rename', entry, value: entry.name });
          return;
        }
        case 'chmod': {
          openChmodModal([entry]);
          return;
        }
        case 'properties': {
          openPropertiesModal(entry);
          return;
        }
        case 'delete': {
          setActionModal({ kind: 'delete', entries: [entry] });
          return;
        }
        case 'open':
        case 'edit':
        case 'download':
          break;
      }

      const request = directoryRequest.current;
      const activation = ++activationRequest.current;
      const target = entry.isSymlink ? await getApi().ResolveFileLink(session.id, entry.path) : entry;
      if (request !== directoryRequest.current || activation !== activationRequest.current) return;

      if (target.isDir) {
        if (action === 'download') {
          const saved = await getApi().DownloadDirectory(session.id, entry.path);
          if (saved) onToast(`已下载文件夹：${entry.name}`);
        } else {
          void loadDir(entry.path);
        }
      } else if (action === 'download') {
        const saved = await getApi().DownloadFile(session.id, entry.path, true);
        if (saved) onToast(`已下载：${entry.name}`);
      } else {
        onEditFile(entry.path);
      }
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const handleRowAction = useCallback(
    (action: SftpRowAction, entry: RemoteEntry) => rowActionRef.current(action, entry),
    [],
  );

  const batchActionRef = useRef<(action: SftpBatchAction, items: RemoteEntry[]) => void>(() => {});
  batchActionRef.current = async (action, items) => {
    if (!items.length) return;
    setErr('');
    try {
      switch (action) {
        case 'batchDownload': {
          const savedDir = await getApi().BatchDownloadRemote(
            session.id,
            items.map((item) => item.path),
          );
          if (savedDir) onToast(`已批量下载 ${items.length} 项`);
          return;
        }
        case 'batchChmod': {
          openChmodModal(items);
          return;
        }
        case 'batchCopyPaths': {
          await copyToClipboard(items.map((item) => item.path).join('\n'));
          onToast(`已复制 ${items.length} 条远端路径`);
          return;
        }
        case 'batchDelete': {
          setActionModal({ kind: 'delete', entries: items });
          return;
        }
      }
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const handleBatchAction = useCallback(
    (action: SftpBatchAction, items: RemoteEntry[]) => batchActionRef.current(action, items),
    [],
  );

  const handleEmptyAction = useCallback(
    (action: SftpEmptyAction) => {
      switch (action) {
        case 'uploadFiles':
          void handleUploadFiles();
          break;
        case 'uploadFolder':
          void handleUploadFolder();
          break;
        case 'newFile':
          setActionModal({ kind: 'newfile', value: '', openAfterCreate: true });
          break;
        case 'newFolder':
          setActionModal({ kind: 'mkdir', value: '' });
          break;
        case 'refresh':
          handleRefresh();
          break;
        case 'cdCurrent':
          onCdInTerminal(currentDirRef.current);
          break;
      }
    },
    [handleUploadFiles, handleUploadFolder, handleRefresh, onCdInTerminal],
  );

  const submitAction = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!actionModal || actionModal.kind === 'properties' || modalBusy) return;
    const request = directoryRequest.current;
    const targetDir = currentDirRef.current;
    const api = getApi();

    setModalBusy(true);
    setErr('');
    try {
      if (actionModal.kind === 'mkdir') {
        const name = actionModal.value;
        if (!name.trim()) return;
        const target = joinRemotePath(targetDir, name);
        await api.MakeDir(session.id, target);
        onToast(`已创建文件夹 ${name}`);
      } else if (actionModal.kind === 'newfile') {
        const name = actionModal.value;
        if (!name.trim()) return;
        const target = joinRemotePath(targetDir, name);
        await api.CreateRemoteFile(session.id, target);
        onToast(`已创建文件 ${name}`);
        const shouldOpen = actionModal.openAfterCreate;
        setActionModal(null);
        if (request === directoryRequest.current) {
          void loadDir(targetDir, { afterMutation: true, historyMode: 'none' });
        }
        if (shouldOpen) {
          onEditFile(target);
        }
        return;
      } else if (actionModal.kind === 'rename') {
        const rawInput = actionModal.value;
        if (!rawInput.trim()) return;
        const nextPath = resolveDestinationPath(actionModal.entry.path, rawInput);
        if (nextPath === actionModal.entry.path) {
          setActionModal(null);
          return;
        }
        await api.RenameRemote(session.id, actionModal.entry.path, nextPath);
        onToast(rawInput.includes('/') ? `已移动到 ${nextPath}` : `已重命名为 ${rawInput}`);
      } else if (actionModal.kind === 'copy') {
        const rawInput = actionModal.value;
        if (!rawInput.trim()) return;
        const dstPath = resolveDestinationPath(actionModal.entry.path, rawInput);
        if (dstPath === actionModal.entry.path) {
          setErr('目标路径不能与源路径相同');
          return;
        }
        await api.CopyRemote(session.id, actionModal.entry.path, dstPath);
        onToast(`已创建副本 ${rawInput}`);
      } else if (actionModal.kind === 'chmod') {
        const parsed = octalToMode(actionModal.octal);
        if (parsed === null) {
          setErr('请输入有效的 3~4 位八进制权限，例如 755 或 644');
          return;
        }
        for (const item of actionModal.entries) {
          if (actionModal.recursive && item.isDir) {
            await api.ChmodRemoteRecursive(session.id, item.path, parsed, true);
          } else {
            await api.ChmodRemote(session.id, item.path, parsed);
          }
        }
        onToast(
          actionModal.entries.length > 1
            ? `已更新 ${actionModal.entries.length} 项权限为 ${modeToOctal(parsed)}`
            : `权限已更新为 ${modeToOctal(parsed)}`,
        );
      } else if (actionModal.kind === 'delete') {
        if (actionModal.entries.length === 1) {
          await api.DeleteRemote(session.id, actionModal.entries[0].path);
          onToast(`已删除 ${actionModal.entries[0].name}`);
        } else {
          const deleted = await api.DeleteRemoteBatch(
            session.id,
            actionModal.entries.map((item) => item.path),
          );
          onToast(`已删除 ${deleted} 项`);
        }
        setSelectedPaths(new Set());
      }

      setActionModal(null);
      if (request === directoryRequest.current) {
        void loadDir(targetDir, { afterMutation: true, historyMode: 'none' });
      }
    } catch (ex) {
      setErr(errorMessage(ex));
    } finally {
      setModalBusy(false);
    }
  };

  // Drawer-level navigation keyboard shortcuts (Alt+Left / Alt+Right / F5)
  const handleDrawerKeyDown = (e: React.KeyboardEvent<HTMLElement>) => {
    if (actionModal) return;
    if (e.altKey && e.key === 'ArrowLeft') {
      e.preventDefault();
      handleHistoryBack();
    } else if (e.altKey && e.key === 'ArrowRight') {
      e.preventDefault();
      handleHistoryForward();
    } else if (e.key === 'F5') {
      e.preventDefault();
      handleRefresh();
    }
  };

  const sessionTransfers = useMemo(
    () => transfers.filter((t) => t.sessionId === session.id),
    [transfers, session.id],
  );
  const visibleTransfers = useMemo(
    () => sessionTransfers.filter((t) => !dismissedTransferIds.has(t.transferId)),
    [sessionTransfers, dismissedTransferIds],
  );
  const runningTransferCount = useMemo(
    () => visibleTransfers.filter((t) => t.status === 'running').length,
    [visibleTransfers],
  );
  const finishedTransferCount = visibleTransfers.length - runningTransferCount;

  const clearFinishedTransfers = () => {
    setDismissedTransferIds((prev) => {
      const next = new Set(prev);
      for (const tr of sessionTransfers) {
        if (tr.status !== 'running') next.add(tr.transferId);
      }
      return next;
    });
  };

  const handleRevealLocalPath = async (localPath: string) => {
    try {
      await getApi().RevealLocalPath(localPath);
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const toggleChmodBit = (who: keyof ChmodBits, bit: 'r' | 'w' | 'x') => {
    setActionModal((prev) => {
      if (!prev || prev.kind !== 'chmod') return prev;
      const nextBits = modeToBits(prev.mode);
      nextBits[who] = { ...nextBits[who], [bit]: !nextBits[who][bit] };
      const nextMode = bitsToMode(nextBits, prev.mode & 0o7000);
      return {
        ...prev,
        mode: nextMode,
        octal: modeToOctal(nextMode),
      };
    });
  };

  return (
    <section
      id="filesPane"
      className={`right-drawer sftp-drawer ${resizing ? 'is-resizing' : ''}`}
      style={{
        width: `${drawerWidth}px`,
        ['--wails-drop-target' as string]: 'drop',
      }}
      aria-label="SFTP 远程文件"
      onKeyDown={handleDrawerKeyDown}
      onDragOver={(e) => {
        e.preventDefault();
        setDropActive(true);
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) {
          setDropActive(false);
        }
      }}
      onDrop={(e) => {
        e.preventDefault();
        setDropActive(false);
      }}
    >
      <div
        className="sftp-resize-handle"
        role="separator"
        aria-orientation="vertical"
        aria-label="调整 SFTP 面板宽度"
        title="拖动调整宽度（双击还原默认）"
        onPointerDown={handleResizePointerDown}
        onDoubleClick={resetDrawerWidth}
      />

      <div className="sftp-header">
        <div className="sftp-header-title">
          <Folder size={15} strokeWidth={1.8} />
          <span>远程文件 (SFTP)</span>
        </div>
        <div className="sftp-header-actions">
          <button
            type="button"
            className="sftp-tool-btn"
            title="在终端 cd 到此目录"
            aria-label="在终端 cd 到此目录"
            onClick={() => onCdInTerminal(dir)}
          >
            <Terminal size={14} />
          </button>
          <button
            type="button"
            className="sftp-tool-btn"
            title="上传文件"
            aria-label="上传文件"
            onClick={() => void handleUploadFiles()}
          >
            <FileUp size={14} />
          </button>
          <button
            type="button"
            className="sftp-tool-btn"
            title="上传文件夹"
            aria-label="上传文件夹"
            onClick={() => void handleUploadFolder()}
          >
            <FolderUp size={14} />
          </button>
          <button
            type="button"
            className="sftp-tool-btn"
            title="新建文件夹"
            aria-label="新建文件夹"
            onClick={() => setActionModal({ kind: 'mkdir', value: '' })}
          >
            <FolderPlus size={14} />
          </button>
          <button
            type="button"
            className="sftp-tool-btn"
            title="新建文件"
            aria-label="新建文件"
            onClick={() => setActionModal({ kind: 'newfile', value: '', openAfterCreate: true })}
          >
            <FilePlus size={14} />
          </button>
          <button
            type="button"
            className="sftp-tool-btn"
            title="关闭"
            aria-label="关闭"
            onClick={onClose}
          >
            <X size={14} />
          </button>
        </div>
      </div>

      <div className="sftp-toolbar-pro">
        <form
          className="sftp-nav-row"
          onSubmit={(e) => {
            e.preventDefault();
            void loadDir(pathInput);
          }}
        >
          <div className="sftp-nav-cluster">
            <button
              type="button"
              className="sftp-nav-btn"
              title="后退 (Alt+←)"
              aria-label="后退"
              disabled={!canGoBack}
              onClick={handleHistoryBack}
            >
              <ArrowLeft size={13} />
            </button>
            <button
              type="button"
              className="sftp-nav-btn"
              title="前进 (Alt+→)"
              aria-label="前进"
              disabled={!canGoForward}
              onClick={handleHistoryForward}
            >
              <ArrowRight size={13} />
            </button>
            <button
              type="button"
              className="sftp-nav-btn"
              title="上一级 (Backspace)"
              aria-label="上一级"
              disabled={dir === '/'}
              onClick={handleGoParent}
            >
              <ArrowUp size={13} />
            </button>
            <button
              type="button"
              className="sftp-nav-btn"
              title="主目录 (~)"
              aria-label="主目录"
              onClick={() => void loadDir('.')}
            >
              <Home size={13} />
            </button>
            <div className="sftp-bookmark-anchor" ref={bookmarkAnchorRef}>
              <button
                type="button"
                className={`sftp-nav-btn ${bookmarksOpen || isCurrentPinned ? 'active' : ''}`}
                title="目录书签"
                aria-label="目录书签"
                aria-expanded={bookmarksOpen}
                onClick={() => setBookmarksOpen((v) => !v)}
              >
                {isCurrentPinned ? <BookmarkCheck size={13} /> : <Bookmark size={13} />}
              </button>
              {bookmarksOpen && (
                <div className="sftp-bookmark-popover" role="menu" aria-label="快捷目录书签">
                  <button
                    type="button"
                    className="sftp-bookmark-item pin-action"
                    onClick={togglePinCurrentDir}
                  >
                    {isCurrentPinned ? <BookmarkCheck size={13} /> : <Bookmark size={13} />}
                    <span className="sftp-bookmark-path">
                      {isCurrentPinned ? '取消收藏当前目录' : '收藏当前目录'}
                    </span>
                  </button>

                  {pinnedBookmarks.length > 0 && (
                    <>
                      <div className="context-menu-sep" />
                      <div className="sftp-bookmark-section">已收藏</div>
                      {pinnedBookmarks.map((bmPath) => (
                        <div key={bmPath} className="sftp-bookmark-row">
                          <button
                            type="button"
                            className={`sftp-bookmark-item mono ${normalizedDir === bmPath ? 'active' : ''}`}
                            title={bmPath}
                            onClick={() => {
                              setBookmarksOpen(false);
                              void loadDir(bmPath);
                            }}
                          >
                            <Folder size={12.5} />
                            <span className="sftp-bookmark-path">{bmPath}</span>
                          </button>
                          <button
                            type="button"
                            className="sftp-bookmark-remove"
                            title="移除书签"
                            aria-label={`移除书签 ${bmPath}`}
                            onClick={(e) => {
                              e.stopPropagation();
                              removeBookmark(bmPath);
                            }}
                          >
                            <X size={11.5} />
                          </button>
                        </div>
                      ))}
                    </>
                  )}

                  <div className="context-menu-sep" />
                  <div className="sftp-bookmark-section">常用目录</div>
                  {DEFAULT_SFTP_BOOKMARKS.map((item) => (
                    <button
                      key={item.path}
                      type="button"
                      className={`sftp-bookmark-item mono ${normalizedDir === item.path ? 'active' : ''}`}
                      onClick={() => {
                        setBookmarksOpen(false);
                        void loadDir(item.path);
                      }}
                    >
                      <Folder size={12.5} />
                      <span className="sftp-bookmark-path">{item.label}</span>
                    </button>
                  ))}
                </div>
              )}
            </div>
          </div>

          <input
            className="input input-sm mono sftp-path-input"
            value={pathInput}
            onChange={(e) => setPathInput(e.target.value)}
            aria-label="远程路径"
          />

          <button
            type="button"
            className="sftp-nav-btn"
            title="刷新 (F5)"
            aria-label="刷新"
            onClick={handleRefresh}
          >
            <RefreshCw size={13} className={loading ? 'spin' : ''} />
          </button>
        </form>

        <div className="sftp-breadcrumbs" ref={breadcrumbsRef}>
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

        <div className="sftp-filter-row">
          <div className="sftp-search-box">
            <Search size={12} className="sftp-search-icon" />
            <input
              className="input input-sm sftp-search-input"
              placeholder="筛选当前目录…"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              aria-label="筛选当前目录"
            />
            {filter && (
              <button
                type="button"
                className="sftp-search-clear"
                title="清空筛选"
                aria-label="清空筛选"
                onClick={() => setFilter('')}
              >
                <X size={11.5} />
              </button>
            )}
          </div>

          <Select
            size="sm"
            className="sftp-sort-select"
            value={sortBy}
            onValueChange={(val) => setSortBy(val as SftpSortKey)}
            options={SORT_OPTIONS}
            ariaLabel="排序方式"
          />

          <button
            type="button"
            className="sftp-nav-btn"
            title={sortDir === 'asc' ? '当前升序（点击切换降序）' : '当前降序（点击切换升序）'}
            aria-label={sortDir === 'asc' ? '切换为降序' : '切换为升序'}
            onClick={() => setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'))}
          >
            {sortDir === 'asc' ? <ArrowUpNarrowWide size={13.5} /> : <ArrowDownWideNarrow size={13.5} />}
          </button>

          <button
            type="button"
            className={`sftp-nav-btn ${showHidden ? 'active' : ''}`}
            title={showHidden ? '隐藏点文件' : '显示隐藏文件'}
            aria-label={showHidden ? '隐藏点文件' : '显示隐藏文件'}
            onClick={() => setShowHidden((v) => !v)}
          >
            {showHidden ? <Eye size={13.5} /> : <EyeOff size={13.5} />}
          </button>
        </div>

        {err && (
          <div className="sftp-err-banner" role="alert">
            <span>{err}</span>
            <button
              type="button"
              className="sftp-err-close"
              aria-label="关闭错误提示"
              onClick={() => setErr('')}
            >
              <X size={12} />
            </button>
          </div>
        )}
      </div>

      <SftpEntryList
        key={dir}
        entries={visibleEntries}
        busy={loading && entries.length === 0}
        filtered={Boolean(deferredFilter.trim())}
        selectedPaths={selectedPaths}
        onSelectedPathsChange={setSelectedPaths}
        onRowAction={handleRowAction}
        onBatchAction={handleBatchAction}
        onEmptyAction={handleEmptyAction}
        onGoParent={handleGoParent}
      />

      {visibleTransfers.length > 0 && (
        <div className={`sftp-transfer-dock ${transferCollapsed ? '' : 'is-expanded'}`}>
          <div className="sftp-transfer-header">
            <button
              type="button"
              className="sftp-transfer-toggle"
              aria-expanded={!transferCollapsed}
              onClick={() => setTransferCollapsed((c) => !c)}
            >
              {transferCollapsed ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
              <span>
                传输队列 (进行中 {runningTransferCount} / 共 {visibleTransfers.length})
              </span>
            </button>
            <button
              type="button"
              className="sftp-transfer-clear"
              disabled={finishedTransferCount === 0}
              onClick={clearFinishedTransfers}
            >
              清空已完成
            </button>
          </div>

          {!transferCollapsed && (
            <div className="sftp-transfer-list">
              {[...visibleTransfers].reverse().map((tr) => {
                const pct =
                  tr.total > 0
                    ? Math.min(100, Math.round((tr.transferred / tr.total) * 100))
                    : tr.status === 'done'
                      ? 100
                      : 0;
                const statusLabel =
                  tr.status === 'done'
                    ? '完成'
                    : tr.status === 'error'
                      ? '失败'
                      : tr.status === 'canceled'
                        ? '已取消'
                        : tr.total === 0 && tr.transferred === 0
                          ? '准备中…'
                          : `${pct}%`;

                return (
                  <div key={tr.transferId} className="sftp-transfer-card">
                    <div className="sftp-transfer-top">
                      <div className="sftp-transfer-name" title={tr.remotePath || tr.name}>
                        {tr.direction === 'upload' ? (
                          <ArrowUp size={12.5} className="sftp-transfer-dir-up" />
                        ) : (
                          <ArrowDown size={12.5} className="sftp-transfer-dir-down" />
                        )}
                        <span className="sftp-transfer-name-text">{tr.name}</span>
                      </div>

                      <div className="sftp-transfer-actions">
                        <span className={`mono sftp-transfer-status ${tr.status}`}>{statusLabel}</span>
                        {tr.status === 'running' && (
                          <button
                            type="button"
                            className="sftp-row-btn"
                            style={{ width: 20, height: 20 }}
                            title="取消传输"
                            aria-label="取消传输"
                            onClick={() => void getApi().CancelTransfer(tr.transferId)}
                          >
                            <X size={11.5} />
                          </button>
                        )}
                        {tr.status === 'done' && tr.direction === 'download' && tr.localPath && (
                          <button
                            type="button"
                            className="sftp-row-btn"
                            style={{ width: 20, height: 20 }}
                            title="在文件夹中显示"
                            aria-label="在文件夹中显示"
                            onClick={() => void handleRevealLocalPath(tr.localPath!)}
                          >
                            <FolderSearch size={12} />
                          </button>
                        )}
                      </div>
                    </div>

                    <div className="sftp-transfer-sub">
                      <span className="sftp-transfer-current mono" title={tr.currentItem || ''}>
                        {tr.currentItem || ''}
                      </span>
                      <span className="sftp-transfer-stats mono">
                        <span>
                          {formatBytes(tr.transferred)} / {tr.total > 0 ? formatBytes(tr.total) : '—'}
                        </span>
                        {tr.status === 'running' && tr.speedBps && tr.speedBps > 0 ? (
                          <span>· {formatBytes(tr.speedBps)}/s</span>
                        ) : null}
                      </span>
                    </div>

                    <div className="metric-bar-track">
                      <div
                        className={`sftp-transfer-bar-fill ${tr.status}`}
                        style={{ width: `${pct}%` }}
                      />
                    </div>

                    {tr.status === 'error' && tr.error && (
                      <div className="sftp-transfer-error">{tr.error}</div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}

      <div className="sftp-footer">
        <div className="sftp-footer-left">
          <span className="tabular">
            {visibleEntries.length} 项
            {entries.length !== visibleEntries.length ? ` / 共 ${entries.length} 项` : ''}
          </span>
          {selectedVisibleEntries.length > 0 && (
            <span className="sftp-footer-pill tabular">
              已选 {selectedVisibleEntries.length} 项
              {selectedBytes > 0 ? ` · ${formatBytes(selectedBytes)}` : ''}
            </span>
          )}
        </div>
        <span className="tabular">
          {visibleSummary.dirs} 目录 · {visibleSummary.files} 文件
        </span>
      </div>

      {dropActive && (
        <div className="drop-overlay">
          <Upload size={26} />
          <span>松开即可上传文件或文件夹到当前目录</span>
        </div>
      )}

      {actionModal && (
        <div
          className="modal-backdrop"
          onClick={() => {
            if (!modalBusy) setActionModal(null);
          }}
        >
          {actionModal.kind === 'properties' ? (
            <div
              className="modal-sheet sftp-modal-sheet"
              onClick={(e) => e.stopPropagation()}
              role="dialog"
              aria-modal="true"
              aria-label="属性详情"
            >
              <div className="modal-header">
                <h2>属性详情</h2>
                <button
                  type="button"
                  className="icon-btn"
                  aria-label="关闭"
                  onClick={() => setActionModal(null)}
                >
                  <X size={15} />
                </button>
              </div>
              <div className="modal-body">
                {actionModal.loading ? (
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '18px 4px', color: 'var(--ink-secondary)' }}>
                    <Loader2 size={15} className="spin" />
                    <span>正在读取远端文件属性…</span>
                  </div>
                ) : actionModal.error ? (
                  <div className="sftp-err-banner">
                    <span>{actionModal.error}</span>
                  </div>
                ) : (
                  (() => {
                    const detail = actionModal.detail;
                    const item = detail ?? actionModal.entry;
                    const kind = classifyFileKind(item.name, item.isDir, item.isSymlink);
                    const octalStr = detail?.octal || modeToOctal(item.mode);
                    const symStr = item.perm || modeToSymbolic(item.mode);
                    const typeText = item.isSymlink
                      ? `符号链接 (${item.isDir ? '目录' : getFileKindLabel(kind)})`
                      : item.isDir
                        ? '目录'
                        : `${getFileKindLabel(kind)}文件`;

                    return (
                      <>
                        <div className="sftp-prop-hero">
                          <EntryKindIcon
                            name={item.name}
                            isDir={item.isDir}
                            isSymlink={item.isSymlink}
                            size={22}
                          />
                          <div style={{ minWidth: 0, flex: 1 }}>
                            <div className="sftp-prop-name">{item.name}</div>
                            <div className="sftp-prop-type">{typeText}</div>
                          </div>
                        </div>

                        <div className="sftp-prop-grid">
                          <span className="sftp-prop-label">完整路径</span>
                          <div className="sftp-prop-val mono">
                            <span style={{ flex: 1, minWidth: 0 }}>{item.path}</span>
                            <button
                              type="button"
                              className="sftp-row-btn"
                              title="复制路径"
                              aria-label="复制路径"
                              onClick={() => {
                                void copyToClipboard(item.path);
                                onToast('已复制远端路径');
                              }}
                            >
                              <Copy size={12.5} />
                            </button>
                          </div>

                          {detail?.linkTarget && (
                            <>
                              <span className="sftp-prop-label">链接目标</span>
                              <div className="sftp-prop-val mono">{detail.linkTarget}</div>
                            </>
                          )}

                          <span className="sftp-prop-label">大小</span>
                          <div className="sftp-prop-val mono">
                            {formatBytes(item.size)} ({item.size.toLocaleString()} 字节)
                          </div>

                          {detail && detail.isDir && !detail.isSymlink && (
                            <>
                              <span className="sftp-prop-label">直接子项</span>
                              <div className="sftp-prop-val tabular">{detail.itemCount ?? 0} 项</div>
                            </>
                          )}

                          <span className="sftp-prop-label">权限</span>
                          <div className="sftp-prop-val mono">
                            {octalStr} · {symStr}
                          </div>

                          {detail && (
                            <>
                              <span className="sftp-prop-label">UID / GID</span>
                              <div className="sftp-prop-val mono">
                                {detail.uid} / {detail.gid}
                              </div>
                            </>
                          )}

                          <span className="sftp-prop-label">修改时间</span>
                          <div className="sftp-prop-val mono">{formatFullTimestamp(item.modTime)}</div>

                          {detail?.accessTime ? (
                            <>
                              <span className="sftp-prop-label">访问时间</span>
                              <div className="sftp-prop-val mono">
                                {formatFullTimestamp(detail.accessTime)}
                              </div>
                            </>
                          ) : null}
                        </div>
                      </>
                    );
                  })()
                )}
              </div>
              <div className="modal-footer">
                {!actionModal.loading && !actionModal.error && (
                  <button
                    type="button"
                    className="btn"
                    onClick={() => openChmodModal([actionModal.entry])}
                  >
                    修改权限
                  </button>
                )}
                {actionModal.error && (
                  <button
                    type="button"
                    className="btn"
                    onClick={() => openPropertiesModal(actionModal.entry)}
                  >
                    重试
                  </button>
                )}
                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={() => setActionModal(null)}
                >
                  关闭
                </button>
              </div>
            </div>
          ) : (
            <form
              className="modal-sheet sftp-modal-sheet"
              onClick={(e) => e.stopPropagation()}
              onSubmit={submitAction}
            >
              <div className="modal-header">
                <h2>
                  {actionModal.kind === 'mkdir'
                    ? '新建文件夹'
                    : actionModal.kind === 'newfile'
                      ? '新建文件'
                      : actionModal.kind === 'rename'
                        ? '移动 / 重命名'
                        : actionModal.kind === 'copy'
                          ? '创建副本'
                          : actionModal.kind === 'chmod'
                            ? actionModal.entries.length > 1
                              ? `批量修改权限 (${actionModal.entries.length} 项)`
                              : '修改文件权限'
                            : actionModal.entries.length > 1
                              ? `确认批量删除 (${actionModal.entries.length} 项)`
                              : '确认删除'}
                </h2>
                <button
                  type="button"
                  className="icon-btn"
                  aria-label="关闭"
                  disabled={modalBusy}
                  onClick={() => setActionModal(null)}
                >
                  <X size={15} />
                </button>
              </div>

              <div className="modal-body">
                {actionModal.kind === 'delete' && (
                  <>
                    {actionModal.entries.length === 1 ? (
                      <p style={{ margin: 0, lineHeight: 1.5 }}>
                        确定要永久删除远端
                        {actionModal.entries[0].isDir ? '目录（含内部所有文件）' : '文件'}「
                        <span className="mono">{actionModal.entries[0].name}</span>」？此操作不可撤销。
                      </p>
                    ) : (
                      <>
                        <p style={{ margin: 0, lineHeight: 1.5 }}>
                          确定永久删除所选 <strong>{actionModal.entries.length}</strong> 项？此操作不可撤销。
                        </p>
                        <div className="sftp-delete-list">
                          {actionModal.entries.slice(0, 4).map((item) => (
                            <div key={item.path} className="sftp-delete-item">
                              <EntryKindIcon
                                name={item.name}
                                isDir={item.isDir}
                                isSymlink={item.isSymlink}
                                size={13.5}
                              />
                              <span className="mono">{item.name}</span>
                            </div>
                          ))}
                          {actionModal.entries.length > 4 && (
                            <div className="sftp-delete-more">
                              等共 {actionModal.entries.length} 项
                            </div>
                          )}
                        </div>
                      </>
                    )}
                  </>
                )}

                {actionModal.kind === 'mkdir' && (
                  <div className="field-group">
                    <label className="field-label">文件夹名称</label>
                    <input
                      className="input mono"
                      autoFocus
                      value={actionModal.value}
                      onChange={(e) => setActionModal({ ...actionModal, value: e.target.value })}
                      placeholder="例如 logs 或 deploy/releases"
                    />
                  </div>
                )}

                {actionModal.kind === 'newfile' && (
                  <div className="field-group">
                    <label className="field-label">文件名称</label>
                    <input
                      className="input mono"
                      autoFocus
                      value={actionModal.value}
                      onChange={(e) => setActionModal({ ...actionModal, value: e.target.value })}
                      placeholder="例如 docker-compose.yml"
                    />
                    <label className="sftp-checkbox-row">
                      <Checkbox
                        checked={actionModal.openAfterCreate}
                        onChange={(e) =>
                          setActionModal({ ...actionModal, openAfterCreate: e.target.checked })
                        }
                      />
                      <span>创建后立即打开编辑</span>
                    </label>
                  </div>
                )}

                {actionModal.kind === 'rename' && (
                  <div className="field-group">
                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 4 }}>
                      <label className="field-label" style={{ margin: 0 }}>
                        新名称或完整目标路径
                      </label>
                      {!actionModal.value.startsWith('/') && (
                        <button
                          type="button"
                          className="sftp-transfer-clear"
                          onClick={() =>
                            setActionModal({ ...actionModal, value: actionModal.entry.path })
                          }
                        >
                          编辑完整路径
                        </button>
                      )}
                    </div>
                    <input
                      className="input mono"
                      autoFocus
                      onFocus={(e) => {
                        const input = e.currentTarget;
                        const val = input.value;
                        if (!actionModal.entry.isDir && !val.includes('/')) {
                          const dot = val.lastIndexOf('.');
                          if (dot > 0) {
                            input.setSelectionRange(0, dot);
                            return;
                          }
                        }
                        input.select();
                      }}
                      value={actionModal.value}
                      onChange={(e) => setActionModal({ ...actionModal, value: e.target.value })}
                    />
                    <p className="sftp-modal-hint">
                      直接输入名称原地重命名，或输入以 <span className="mono">/</span> 开头的完整路径移动位置。
                    </p>
                    {actionModal.value.trim() && (
                      <div className="sftp-modal-preview mono">
                        目标：{resolveDestinationPath(actionModal.entry.path, actionModal.value)}
                      </div>
                    )}
                  </div>
                )}

                {actionModal.kind === 'copy' && (
                  <div className="field-group">
                    <label className="field-label">副本名称或目标路径</label>
                    <input
                      className="input mono"
                      autoFocus
                      onFocus={(e) => {
                        const input = e.currentTarget;
                        const val = input.value;
                        if (!actionModal.entry.isDir && !val.includes('/')) {
                          const dot = val.lastIndexOf('.');
                          if (dot > 0) {
                            input.setSelectionRange(0, dot);
                            return;
                          }
                        }
                        input.select();
                      }}
                      value={actionModal.value}
                      onChange={(e) => setActionModal({ ...actionModal, value: e.target.value })}
                    />
                    {actionModal.value.trim() && (
                      <div className="sftp-modal-preview mono">
                        目标：{resolveDestinationPath(actionModal.entry.path, actionModal.value)}
                      </div>
                    )}
                  </div>
                )}

                {actionModal.kind === 'chmod' && (() => {
                  const bits = modeToBits(actionModal.mode);
                  const validOctal = octalToMode(actionModal.octal) !== null;
                  const hasDir = actionModal.entries.some((item) => item.isDir);

                  return (
                    <div>
                      <p className="sftp-chmod-target mono" title={actionModal.entries.map((e) => e.path).join('\n')}>
                        {actionModal.entries.length === 1
                          ? actionModal.entries[0].path
                          : `已选 ${actionModal.entries.length} 项（${actionModal.entries
                              .slice(0, 2)
                              .map((e) => e.name)
                              .join('、')}${actionModal.entries.length > 2 ? ' 等' : ''}）`}
                      </p>

                      <div className="sftp-chmod-matrix" role="group" aria-label="权限矩阵">
                        <span />
                        <span className="sftp-chmod-colhead">读取 (4)</span>
                        <span className="sftp-chmod-colhead">写入 (2)</span>
                        <span className="sftp-chmod-colhead">执行 (1)</span>

                        {CHMOD_WHO.map(({ key, label }) => (
                          <React.Fragment key={key}>
                            <span className="sftp-chmod-rowlabel">{label}</span>
                            <label className="sftp-chmod-cell">
                              <Checkbox
                                checked={bits[key].r}
                                aria-label={`${label} 读取`}
                                onChange={() => toggleChmodBit(key, 'r')}
                              />
                            </label>
                            <label className="sftp-chmod-cell">
                              <Checkbox
                                checked={bits[key].w}
                                aria-label={`${label} 写入`}
                                onChange={() => toggleChmodBit(key, 'w')}
                              />
                            </label>
                            <label className="sftp-chmod-cell">
                              <Checkbox
                                checked={bits[key].x}
                                aria-label={`${label} 执行`}
                                onChange={() => toggleChmodBit(key, 'x')}
                              />
                            </label>
                          </React.Fragment>
                        ))}
                      </div>

                      <div className="sftp-chmod-controls">
                        <input
                          className="input mono sftp-chmod-octal"
                          value={actionModal.octal}
                          aria-label="八进制权限"
                          placeholder="755"
                          onChange={(e) => {
                            const raw = e.target.value;
                            const parsed = octalToMode(raw);
                            setActionModal({
                              ...actionModal,
                              octal: raw,
                              mode: parsed !== null ? parsed : actionModal.mode,
                            });
                          }}
                        />
                        <div className="sftp-chmod-symbolic mono" aria-label="符号权限">
                          {modeToSymbolic(actionModal.mode)}
                        </div>
                      </div>

                      {!validOctal && (
                        <p className="error-text" style={{ margin: '0 0 8px' }}>
                          请输入 3~4 位八进制权限（0–7），例如 644、755、0600
                        </p>
                      )}

                      <div className="sftp-chmod-presets">
                        {CHMOD_PRESETS.map((preset) => (
                          <button
                            key={preset.octal}
                            type="button"
                            className={`sftp-preset-pill ${modeToOctal(actionModal.mode) === preset.octal ? 'active' : ''}`}
                            onClick={() =>
                              setActionModal({
                                ...actionModal,
                                octal: preset.octal,
                                mode: parseInt(preset.octal, 8),
                              })
                            }
                          >
                            <span className="mono">{preset.octal}</span>
                            <span>{preset.label}</span>
                          </button>
                        ))}
                      </div>

                      {hasDir && (
                        <label className="sftp-checkbox-row">
                          <Checkbox
                            checked={actionModal.recursive}
                            onChange={(e) =>
                              setActionModal({ ...actionModal, recursive: e.target.checked })
                            }
                          />
                          <span>递归应用到子目录和文件</span>
                        </label>
                      )}
                    </div>
                  );
                })()}
              </div>

              <div className="modal-footer">
                <button
                  type="button"
                  className="btn"
                  disabled={modalBusy}
                  onClick={() => setActionModal(null)}
                >
                  取消
                </button>
                <button
                  type="submit"
                  className={`btn ${actionModal.kind === 'delete' ? 'btn-danger-solid' : 'btn-primary'}`}
                  disabled={
                    modalBusy ||
                    (actionModal.kind === 'chmod' && octalToMode(actionModal.octal) === null) ||
                    ((actionModal.kind === 'mkdir' ||
                      actionModal.kind === 'newfile' ||
                      actionModal.kind === 'rename' ||
                      actionModal.kind === 'copy') &&
                      !actionModal.value.trim())
                  }
                >
                  {modalBusy
                    ? '处理中…'
                    : actionModal.kind === 'delete'
                      ? '永久删除'
                      : actionModal.kind === 'copy'
                        ? '创建副本'
                        : '确定'}
                </button>
              </div>
            </form>
          )}
        </div>
      )}
    </section>
  );
};
