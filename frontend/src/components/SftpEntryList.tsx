import React, {
  memo,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import { defaultRangeExtractor, useVirtualizer, type Range } from '@tanstack/react-virtual';
import { subscribeWindowDismiss } from '../lib/windowFocusGuard';
import {
  Copy,
  CopyPlus,
  Download,
  File,
  FileArchive,
  FileCode2,
  FileCog,
  FileImage,
  FilePlus,
  FileTerminal,
  FileText,
  FileUp,
  Folder,
  FolderOpen,
  FolderPlus,
  FolderUp,
  Info,
  KeyRound,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  ScrollText,
  Shield,
  Terminal,
  Trash2,
  X,
} from 'lucide-react';
import type { RemoteEntry } from '../types/shell';
import { formatBytes, formatModTime } from '../lib/utils';
import { classifyFileKind, getFileKindToneClass, type FileKind } from '../lib/sftpUtils';

export type SftpRowAction =
  | 'open'
  | 'edit'
  | 'download'
  | 'cd'
  | 'copyPath'
  | 'duplicate'
  | 'rename'
  | 'chmod'
  | 'properties'
  | 'delete';

export type SftpBatchAction =
  | 'batchDownload'
  | 'batchChmod'
  | 'batchCopyPaths'
  | 'batchDelete';

export type SftpEmptyAction =
  | 'uploadFiles'
  | 'uploadFolder'
  | 'newFile'
  | 'newFolder'
  | 'refresh'
  | 'cdCurrent';

export interface SftpEntryListProps {
  entries: RemoteEntry[];
  busy: boolean;
  filtered: boolean;
  selectedPaths: ReadonlySet<string>;
  onSelectedPathsChange: (next: Set<string>) => void;
  onRowAction: (action: SftpRowAction, entry: RemoteEntry) => void;
  onBatchAction: (action: SftpBatchAction, entries: RemoteEntry[]) => void;
  onEmptyAction: (action: SftpEmptyAction) => void;
  onGoParent: () => void;
}

type ContextMenuState =
  | null
  | { kind: 'single'; x: number; y: number; entry: RemoteEntry }
  | { kind: 'multi'; x: number; y: number; entries: RemoteEntry[] }
  | { kind: 'empty'; x: number; y: number };

const ROW_HEIGHT = 46;
const estimateSize = () => ROW_HEIGHT;

export function EntryKindIcon({
  name,
  isDir,
  isSymlink,
  size = 15,
}: {
  name: string;
  isDir: boolean;
  isSymlink: boolean;
  size?: number;
}) {
  const kind: FileKind = classifyFileKind(name, isDir, isSymlink);
  const className = `sftp-row-icon ${getFileKindToneClass(kind)}`;
  const props = { size, strokeWidth: 1.7, className };

  switch (kind) {
    case 'dir':
      return <Folder {...props} />;
    case 'code':
      return <FileCode2 {...props} />;
    case 'config':
      return <FileCog {...props} />;
    case 'shell':
      return <FileTerminal {...props} />;
    case 'archive':
      return <FileArchive {...props} />;
    case 'image':
      return <FileImage {...props} />;
    case 'log':
      return <ScrollText {...props} />;
    case 'key':
      return <KeyRound {...props} />;
    case 'doc':
      return <FileText {...props} />;
    case 'file':
    default:
      return <File {...props} />;
  }
}

export const SftpEntryList = memo(function SftpEntryList({
  entries,
  busy,
  filtered,
  selectedPaths,
  onSelectedPathsChange,
  onRowAction,
  onBatchAction,
  onEmptyAction,
  onGoParent,
}: SftpEntryListProps) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const id = useId();
  const [activeIndex, setActiveIndex] = useState(-1);
  const anchorIndexRef = useRef(-1);
  const [contextMenu, setContextMenu] = useState<ContextMenuState>(null);
  const [menuPos, setMenuPos] = useState<{ left: number; top: number } | null>(null);

  const selectedEntries = useMemo(
    () => entries.filter((entry) => selectedPaths.has(entry.path)),
    [entries, selectedPaths],
  );

  // Keep activeIndex / anchorIndex valid when list shrinks
  useEffect(() => {
    if (activeIndex >= entries.length) {
      setActiveIndex(entries.length - 1);
    }
    if (anchorIndexRef.current >= entries.length) {
      anchorIndexRef.current = entries.length - 1;
    }
  }, [entries.length, activeIndex]);

  const getKey = useCallback((index: number) => entries[index].path, [entries]);

  const rangeExtractor = useCallback(
    (range: Range) => {
      const visible = defaultRangeExtractor(range);
      if (activeIndex >= 0 && activeIndex < range.count && !visible.includes(activeIndex)) {
        visible.push(activeIndex);
        visible.sort((a, b) => a - b);
      }
      return visible;
    },
    [activeIndex],
  );

  const list = useVirtualizer({
    count: entries.length,
    getScrollElement: () => viewportRef.current,
    getItemKey: getKey,
    estimateSize,
    overscan: 6,
    rangeExtractor,
  });

  // Reset scroll offset to top whenever the visible entry list changes (filter, sort, hidden toggle, reload)
  useLayoutEffect(() => {
    list.scrollToOffset(0);
  }, [entries, list]);

  // Clamp context menu within viewport bounds
  useLayoutEffect(() => {
    if (!contextMenu) {
      setMenuPos(null);
      return;
    }
    const el = menuRef.current;
    if (!el) {
      setMenuPos({ left: contextMenu.x, top: contextMenu.y });
      return;
    }
    const rect = el.getBoundingClientRect();
    const pad = 8;
    const left = Math.max(pad, Math.min(contextMenu.x, window.innerWidth - rect.width - pad));
    const top = Math.max(pad, Math.min(contextMenu.y, window.innerHeight - rect.height - pad));
    setMenuPos({ left, top });
  }, [contextMenu]);

  // Dismiss context menu on outside click, Escape, scroll, blur, resize
  useEffect(() => {
    if (!contextMenu) return;
    const onPointerDown = (e: PointerEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setContextMenu(null);
      }
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        setContextMenu(null);
      }
    };
    const close = () => setContextMenu(null);
    const vp = viewportRef.current;

    const offWindowDismiss = subscribeWindowDismiss(close);
    window.addEventListener('pointerdown', onPointerDown, true);
    window.addEventListener('keydown', onKeyDown, true);
    window.addEventListener('resize', close);
    vp?.addEventListener('scroll', close, { passive: true });
    return () => {
      offWindowDismiss();
      window.removeEventListener('pointerdown', onPointerDown, true);
      window.removeEventListener('keydown', onKeyDown, true);
      window.removeEventListener('resize', close);
      vp?.removeEventListener('scroll', close);
    };
  }, [contextMenu]);

  const handleRowClick = useCallback(
    (index: number, e: React.MouseEvent) => {
      const entry = entries[index];
      if (!entry) return;
      setContextMenu(null);

      const isMod = e.ctrlKey || e.metaKey;
      if (e.shiftKey) {
        const anchor = anchorIndexRef.current >= 0 ? anchorIndexRef.current : index;
        const start = Math.min(anchor, index);
        const end = Math.max(anchor, index);
        const next = isMod ? new Set(selectedPaths) : new Set<string>();
        for (let i = start; i <= end; i++) {
          if (entries[i]) next.add(entries[i].path);
        }
        onSelectedPathsChange(next);
        setActiveIndex(index);
      } else if (isMod) {
        const next = new Set(selectedPaths);
        if (next.has(entry.path)) {
          next.delete(entry.path);
        } else {
          next.add(entry.path);
        }
        onSelectedPathsChange(next);
        anchorIndexRef.current = index;
        setActiveIndex(index);
      } else {
        onSelectedPathsChange(new Set([entry.path]));
        anchorIndexRef.current = index;
        setActiveIndex(index);
      }

      viewportRef.current?.focus({ preventScroll: true });
    },
    [entries, selectedPaths, onSelectedPathsChange],
  );

  const handleRowContextMenu = useCallback(
    (index: number, e: React.MouseEvent) => {
      e.preventDefault();
      e.stopPropagation();
      const entry = entries[index];
      if (!entry) return;

      if (selectedPaths.has(entry.path) && selectedPaths.size > 1) {
        const multi = entries.filter((item) => selectedPaths.has(item.path));
        setContextMenu({ kind: 'multi', x: e.clientX, y: e.clientY, entries: multi });
        return;
      }

      if (!selectedPaths.has(entry.path) || selectedPaths.size !== 1) {
        onSelectedPathsChange(new Set([entry.path]));
        anchorIndexRef.current = index;
        setActiveIndex(index);
      }
      setContextMenu({ kind: 'single', x: e.clientX, y: e.clientY, entry });
    },
    [entries, selectedPaths, onSelectedPathsChange],
  );

  const handleRowMoreButton = useCallback(
    (index: number, entry: RemoteEntry, rect: DOMRect) => {
      if (!selectedPaths.has(entry.path)) {
        onSelectedPathsChange(new Set([entry.path]));
        anchorIndexRef.current = index;
        setActiveIndex(index);
      }
      setContextMenu({
        kind: 'single',
        x: Math.max(8, rect.right - 196),
        y: rect.bottom + 4,
        entry,
      });
    },
    [selectedPaths, onSelectedPathsChange],
  );

  const handleViewportClick = (e: React.MouseEvent<HTMLDivElement>) => {
    if (e.target === viewportRef.current || (e.target as HTMLElement).classList.contains('sftp-rows') || (e.target as HTMLElement).classList.contains('sftp-empty')) {
      if (selectedPaths.size > 0) {
        onSelectedPathsChange(new Set());
      }
      setActiveIndex(-1);
      anchorIndexRef.current = -1;
      setContextMenu(null);
    }
  };

  const handleViewportContextMenu = (e: React.MouseEvent<HTMLDivElement>) => {
    e.preventDefault();
    setContextMenu({ kind: 'empty', x: e.clientX, y: e.clientY });
  };

  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const tag = (event.target as HTMLElement | null)?.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;

    const isMod = event.ctrlKey || event.metaKey;

    if (isMod && event.key.toLowerCase() === 'a') {
      if (!entries.length) return;
      event.preventDefault();
      onSelectedPathsChange(new Set(entries.map((e) => e.path)));
      if (activeIndex < 0) setActiveIndex(0);
      if (anchorIndexRef.current < 0) anchorIndexRef.current = 0;
      return;
    }

    if (event.key === 'Escape') {
      if (contextMenu) {
        event.preventDefault();
        setContextMenu(null);
        return;
      }
      if (selectedPaths.size > 0) {
        event.preventDefault();
        onSelectedPathsChange(new Set());
        setActiveIndex(-1);
        anchorIndexRef.current = -1;
      }
      return;
    }

    if (event.key === 'F5') {
      event.preventDefault();
      onEmptyAction('refresh');
      return;
    }

    if (event.altKey || isMod) return;

    if (event.key === 'Backspace') {
      event.preventDefault();
      onGoParent();
      return;
    }

    if (event.key === 'F2') {
      if (selectedEntries.length === 1) {
        event.preventDefault();
        onRowAction('rename', selectedEntries[0]);
      }
      return;
    }

    if (event.key === 'Delete') {
      if (selectedEntries.length === 1) {
        event.preventDefault();
        onRowAction('delete', selectedEntries[0]);
      } else if (selectedEntries.length > 1) {
        event.preventDefault();
        onBatchAction('batchDelete', selectedEntries);
      }
      return;
    }

    if (event.key === 'Enter') {
      if (selectedEntries.length === 1) {
        event.preventDefault();
        onRowAction('open', selectedEntries[0]);
      } else if (activeIndex >= 0 && entries[activeIndex]) {
        event.preventDefault();
        onRowAction('open', entries[activeIndex]);
      }
      return;
    }

    if (!entries.length) return;
    const page = Math.max(1, Math.floor((viewportRef.current?.clientHeight || ROW_HEIGHT) / ROW_HEIGHT));
    const current = activeIndex >= 0 ? activeIndex : 0;
    let next: number;
    switch (event.key) {
      case 'ArrowDown':
        next = activeIndex < 0 ? 0 : activeIndex + 1;
        break;
      case 'ArrowUp':
        next = activeIndex < 0 ? entries.length - 1 : activeIndex - 1;
        break;
      case 'PageDown':
        next = current + page;
        break;
      case 'PageUp':
        next = current - page;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = entries.length - 1;
        break;
      default:
        return;
    }

    event.preventDefault();
    next = Math.max(0, Math.min(entries.length - 1, next));

    if (event.shiftKey) {
      const anchor = anchorIndexRef.current >= 0 ? anchorIndexRef.current : current;
      const start = Math.min(anchor, next);
      const end = Math.max(anchor, next);
      const rangeSet = new Set<string>();
      for (let i = start; i <= end; i++) {
        if (entries[i]) rangeSet.add(entries[i].path);
      }
      onSelectedPathsChange(rangeSet);
    } else {
      onSelectedPathsChange(new Set([entries[next].path]));
      anchorIndexRef.current = next;
    }

    setActiveIndex(next);
    list.scrollToIndex(next, { align: 'auto' });
  };

  const runRowMenu = (action: SftpRowAction, entry: RemoteEntry) => {
    setContextMenu(null);
    onRowAction(action, entry);
  };

  const runBatchMenu = (action: SftpBatchAction, items: RemoteEntry[]) => {
    setContextMenu(null);
    onBatchAction(action, items);
  };

  const runEmptyMenu = (action: SftpEmptyAction) => {
    setContextMenu(null);
    onEmptyAction(action);
  };

  return (
    <div className="sftp-list-wrap">
      {selectedEntries.length > 1 && (
        <div className="sftp-batch-bar" role="toolbar" aria-label="批量文件操作">
          <span className="sftp-batch-count">已选 {selectedEntries.length} 项</span>
          <div className="sftp-batch-actions">
            <button
              type="button"
              className="sftp-batch-btn"
              onClick={() => onBatchAction('batchDownload', selectedEntries)}
            >
              <Download size={12.5} />
              <span>下载</span>
            </button>
            <button
              type="button"
              className="sftp-batch-btn"
              onClick={() => onBatchAction('batchChmod', selectedEntries)}
            >
              <Shield size={12.5} />
              <span>改权限</span>
            </button>
            <button
              type="button"
              className="sftp-batch-btn danger"
              onClick={() => onBatchAction('batchDelete', selectedEntries)}
            >
              <Trash2 size={12.5} />
              <span>删除</span>
            </button>
            <button
              type="button"
              className="sftp-batch-close"
              title="取消选择 (Esc)"
              aria-label="取消选择"
              onClick={() => onSelectedPathsChange(new Set())}
            >
              <X size={13} />
            </button>
          </div>
        </div>
      )}

      <div
        className={`drawer-body sftp-grid ${selectedPaths.size > 1 ? 'has-multi' : ''}`}
        ref={viewportRef}
        role="grid"
        aria-label="远程文件列表"
        aria-multiselectable="true"
        aria-rowcount={entries.length}
        aria-busy={busy}
        tabIndex={0}
        onClick={handleViewportClick}
        onContextMenu={handleViewportContextMenu}
        onKeyDown={handleKeyDown}
        aria-activedescendant={activeIndex >= 0 && activeIndex < entries.length ? `${id}-${activeIndex}` : undefined}
      >
        {entries.length === 0 ? (
          <div className="sftp-empty" role="status">
            {busy ? '正在读取目录…' : filtered ? '没有匹配的文件' : '目录为空，可拖放文件或文件夹到此处上传'}
          </div>
        ) : (
          <div role="rowgroup" className="sftp-rows" style={{ height: list.getTotalSize() }}>
            {list.getVirtualItems().map(({ index, key, start, size }) => {
              const entry = entries[index];
              const isSelected = selectedPaths.has(entry.path);
              return (
                <FileRow
                  key={key}
                  id={`${id}-${index}`}
                  index={index}
                  start={start}
                  size={size}
                  entry={entry}
                  selected={isSelected}
                  isActive={activeIndex === index}
                  onRowClick={handleRowClick}
                  onRowContextMenu={handleRowContextMenu}
                  onMoreClick={handleRowMoreButton}
                  onRowAction={onRowAction}
                />
              );
            })}
          </div>
        )}
      </div>

      {contextMenu &&
        createPortal(
          <div
            ref={menuRef}
            className="context-menu sftp-ctx-menu"
            role="menu"
            style={{
              left: menuPos?.left ?? contextMenu.x,
              top: menuPos?.top ?? contextMenu.y,
              visibility: menuPos ? 'visible' : 'hidden',
            }}
            onContextMenu={(e) => e.preventDefault()}
          >
            {contextMenu.kind === 'single' && !contextMenu.entry.isDir && (
              <>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('edit', contextMenu.entry)}
                >
                  <FileCode2 size={13.5} />
                  <span className="sftp-ctx-label">在线编辑</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('download', contextMenu.entry)}
                >
                  <Download size={13.5} />
                  <span className="sftp-ctx-label">下载</span>
                </button>
                <div className="context-menu-sep" />
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('copyPath', contextMenu.entry)}
                >
                  <Copy size={13.5} />
                  <span className="sftp-ctx-label">复制路径</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('duplicate', contextMenu.entry)}
                >
                  <CopyPlus size={13.5} />
                  <span className="sftp-ctx-label">创建副本</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('rename', contextMenu.entry)}
                >
                  <Pencil size={13.5} />
                  <span className="sftp-ctx-label">移动 / 重命名</span>
                  <span className="sftp-ctx-shortcut">F2</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('chmod', contextMenu.entry)}
                >
                  <Shield size={13.5} />
                  <span className="sftp-ctx-label">修改权限</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('properties', contextMenu.entry)}
                >
                  <Info size={13.5} />
                  <span className="sftp-ctx-label">属性详情</span>
                </button>
                <div className="context-menu-sep" />
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item danger"
                  onClick={() => runRowMenu('delete', contextMenu.entry)}
                >
                  <Trash2 size={13.5} />
                  <span className="sftp-ctx-label">删除</span>
                  <span className="sftp-ctx-shortcut">Del</span>
                </button>
              </>
            )}

            {contextMenu.kind === 'single' && contextMenu.entry.isDir && (
              <>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('open', contextMenu.entry)}
                >
                  <FolderOpen size={13.5} />
                  <span className="sftp-ctx-label">打开目录</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('download', contextMenu.entry)}
                >
                  <Download size={13.5} />
                  <span className="sftp-ctx-label">下载文件夹</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('cd', contextMenu.entry)}
                >
                  <Terminal size={13.5} />
                  <span className="sftp-ctx-label">在终端打开 (cd)</span>
                </button>
                <div className="context-menu-sep" />
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('copyPath', contextMenu.entry)}
                >
                  <Copy size={13.5} />
                  <span className="sftp-ctx-label">复制路径</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('duplicate', contextMenu.entry)}
                >
                  <CopyPlus size={13.5} />
                  <span className="sftp-ctx-label">创建副本</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('rename', contextMenu.entry)}
                >
                  <Pencil size={13.5} />
                  <span className="sftp-ctx-label">移动 / 重命名</span>
                  <span className="sftp-ctx-shortcut">F2</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('chmod', contextMenu.entry)}
                >
                  <Shield size={13.5} />
                  <span className="sftp-ctx-label">修改权限</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runRowMenu('properties', contextMenu.entry)}
                >
                  <Info size={13.5} />
                  <span className="sftp-ctx-label">属性详情</span>
                </button>
                <div className="context-menu-sep" />
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item danger"
                  onClick={() => runRowMenu('delete', contextMenu.entry)}
                >
                  <Trash2 size={13.5} />
                  <span className="sftp-ctx-label">删除</span>
                  <span className="sftp-ctx-shortcut">Del</span>
                </button>
              </>
            )}

            {contextMenu.kind === 'multi' && (
              <>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runBatchMenu('batchDownload', contextMenu.entries)}
                >
                  <Download size={13.5} />
                  <span className="sftp-ctx-label">批量下载 ({contextMenu.entries.length} 项)</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runBatchMenu('batchChmod', contextMenu.entries)}
                >
                  <Shield size={13.5} />
                  <span className="sftp-ctx-label">批量修改权限</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runBatchMenu('batchCopyPaths', contextMenu.entries)}
                >
                  <Copy size={13.5} />
                  <span className="sftp-ctx-label">复制全部路径</span>
                </button>
                <div className="context-menu-sep" />
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item danger"
                  onClick={() => runBatchMenu('batchDelete', contextMenu.entries)}
                >
                  <Trash2 size={13.5} />
                  <span className="sftp-ctx-label">批量删除 ({contextMenu.entries.length} 项)</span>
                </button>
              </>
            )}

            {contextMenu.kind === 'empty' && (
              <>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runEmptyMenu('uploadFiles')}
                >
                  <FileUp size={13.5} />
                  <span className="sftp-ctx-label">上传文件</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runEmptyMenu('uploadFolder')}
                >
                  <FolderUp size={13.5} />
                  <span className="sftp-ctx-label">上传文件夹</span>
                </button>
                <div className="context-menu-sep" />
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runEmptyMenu('newFile')}
                >
                  <FilePlus size={13.5} />
                  <span className="sftp-ctx-label">新建文件</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runEmptyMenu('newFolder')}
                >
                  <FolderPlus size={13.5} />
                  <span className="sftp-ctx-label">新建文件夹</span>
                </button>
                <div className="context-menu-sep" />
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runEmptyMenu('refresh')}
                >
                  <RefreshCw size={13.5} />
                  <span className="sftp-ctx-label">刷新</span>
                  <span className="sftp-ctx-shortcut">F5</span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="context-menu-item"
                  onClick={() => runEmptyMenu('cdCurrent')}
                >
                  <Terminal size={13.5} />
                  <span className="sftp-ctx-label">在终端 cd 到此目录</span>
                </button>
              </>
            )}
          </div>,
          document.body,
        )}
    </div>
  );
});

const FileRow = memo(function FileRow({
  id,
  index,
  start,
  size,
  entry,
  selected,
  isActive,
  onRowClick,
  onRowContextMenu,
  onMoreClick,
  onRowAction,
}: {
  id: string;
  index: number;
  start: number;
  size: number;
  entry: RemoteEntry;
  selected: boolean;
  isActive: boolean;
  onRowClick: (index: number, e: React.MouseEvent) => void;
  onRowContextMenu: (index: number, e: React.MouseEvent) => void;
  onMoreClick: (index: number, entry: RemoteEntry, rect: DOMRect) => void;
  onRowAction: (action: SftpRowAction, entry: RemoteEntry) => void;
}) {
  return (
    <div
      id={id}
      role="row"
      aria-rowindex={index + 1}
      aria-selected={selected}
      className={`sftp-row ${selected ? 'selected' : ''} ${isActive ? 'active-focus' : ''}`}
      style={{ height: size, transform: `translateY(${start}px)` }}
      onClick={(e) => onRowClick(index, e)}
      onContextMenu={(e) => onRowContextMenu(index, e)}
      onDoubleClick={() => onRowAction('open', entry)}
    >
      <EntryKindIcon name={entry.name} isDir={entry.isDir} isSymlink={entry.isSymlink} size={15.5} />

      <div role="gridcell" className="sftp-row-main">
        <div className="sftp-row-title">
          <span className="sftp-row-name" title={entry.name}>
            {entry.name}
          </span>
          {entry.isSymlink && (
            <span className="sftp-symlink-badge" title="符号链接">
              ↗
            </span>
          )}
        </div>
        <div className="mono sftp-row-sub">
          {entry.perm} · {formatModTime(entry.modTime)}
        </div>
      </div>

      <span role="gridcell" className="sftp-row-meta mono">
        {entry.isDir ? '目录' : entry.isSymlink ? '链接' : formatBytes(entry.size)}
      </span>

      <div role="gridcell" className="sftp-row-actions" onDoubleClick={(e) => e.stopPropagation()}>
        {!entry.isDir && (
          <button
            type="button"
            className="sftp-row-btn"
            title="在线编辑"
            aria-label="在线编辑"
            tabIndex={selected ? 0 : -1}
            onClick={(e) => {
              e.stopPropagation();
              onRowAction('edit', entry);
            }}
          >
            <FileCode2 size={13.5} />
          </button>
        )}
        <button
          type="button"
          className="sftp-row-btn"
          title={entry.isDir ? '下载文件夹' : '下载'}
          aria-label={entry.isDir ? '下载文件夹' : '下载'}
          tabIndex={selected ? 0 : -1}
          onClick={(e) => {
            e.stopPropagation();
            onRowAction('download', entry);
          }}
        >
          <Download size={13.5} />
        </button>
        <button
          type="button"
          className="sftp-row-btn"
          title="更多操作"
          aria-label="更多操作"
          tabIndex={selected ? 0 : -1}
          onClick={(e) => {
            e.stopPropagation();
            onMoreClick(index, entry, e.currentTarget.getBoundingClientRect());
          }}
        >
          <MoreHorizontal size={14} />
        </button>
      </div>
    </div>
  );
});
