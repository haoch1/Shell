import React, { memo, useCallback, useId, useLayoutEffect, useRef, useState } from 'react';
import { defaultRangeExtractor, useVirtualizer, type Range } from '@tanstack/react-virtual';
import { Copy, Download, FileCode2, FileText, Folder, Pencil, Shield, Trash2 } from 'lucide-react';
import type { RemoteEntry } from '../types/shell';
import { formatBytes, formatModTime } from '../lib/utils';

export type SftpRowAction = 'open' | 'edit' | 'download' | 'copy' | 'rename' | 'chmod' | 'delete';
type OnAction = (action: SftpRowAction, entry: RemoteEntry) => void;

const ROW_HEIGHT = 44;
const estimateSize = () => ROW_HEIGHT;

export const SftpEntryList = memo(function SftpEntryList({ entries, busy, filtered, onAction }: {
  entries: RemoteEntry[]; busy: boolean; filtered: boolean; onAction: OnAction;
}) {
  const viewport = useRef<HTMLDivElement>(null);
  const id = useId();
  const [selected, setSelected] = useState(-1);
  const getKey = useCallback((index: number) => entries[index].path, [entries]);
  const rangeExtractor = useCallback((range: Range) => {
    const visible = defaultRangeExtractor(range);
    // Keep keyboard focus and aria-activedescendant valid when the selected row scrolls out of view.
    if (selected >= 0 && selected < range.count && !visible.includes(selected)) {
      visible.push(selected);
      visible.sort((a, b) => a - b);
    }
    return visible;
  }, [selected]);
  const list = useVirtualizer({
    count: entries.length, getScrollElement: () => viewport.current, getItemKey: getKey,
    estimateSize, overscan: 5, rangeExtractor,
  });
  useLayoutEffect(() => {
    setSelected(-1);
    if (viewport.current) viewport.current.scrollTop = 0;
  }, [entries]);

  const onKey = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (!entries.length || event.ctrlKey || event.metaKey || event.altKey || event.target !== viewport.current) return;
    const page = Math.max(1, Math.floor((viewport.current?.clientHeight || ROW_HEIGHT) / ROW_HEIGHT));
    let next: number;
    switch (event.key) {
      case 'ArrowDown': next = selected + 1; break;
      case 'ArrowUp': next = selected - 1; break;
      case 'PageDown': next = selected + page; break;
      case 'PageUp': next = selected - page; break;
      case 'Home': next = 0; break;
      case 'End': next = entries.length - 1; break;
      case 'Enter': {
        if (selected >= 0 && entries[selected]) {
          event.preventDefault();
          onAction('open', entries[selected]);
        }
        return;
      }
      default: return;
    }
    event.preventDefault();
    next = Math.max(0, Math.min(entries.length - 1, next));
    setSelected(next);
    list.scrollToIndex(next, { align: 'auto' });
  };

  return (
    <div className="drawer-body sftp-grid" ref={viewport} role="grid" aria-label="远程文件列表"
      aria-rowcount={entries.length} aria-busy={busy} tabIndex={0} onKeyDown={onKey}
      aria-activedescendant={selected >= 0 && selected < entries.length ? id + '-' + selected : undefined}>
      {entries.length === 0 ? (
        <div className="sftp-empty" role="status">
          {busy ? '正在读取目录…' : filtered ? '没有匹配的文件' : '目录为空，可拖放本地文件到此处上传'}
        </div>
      ) : (
        <div role="rowgroup" className="sftp-rows" style={{ height: list.getTotalSize() }}>
          {list.getVirtualItems().map(({ index, key, start, size }) => {
            const entry = entries[index];
            return <FileRow key={key} id={id + '-' + index} index={index} start={start} size={size}
              entry={entry} selected={selected === index} onSelect={setSelected} onAction={onAction} />;
          })}
        </div>
      )}
    </div>
  );
});

const buttons = [
  ['edit', '在线编辑', FileCode2], ['download', '下载', Download], ['copy', '复制路径', Copy],
  ['rename', '重命名', Pencil], ['chmod', '修改权限 (chmod)', Shield], ['delete', '删除', Trash2],
] as const;

const FileRow = memo(function FileRow({ id, index, start, size, entry, selected, onSelect, onAction }: {
  id: string; index: number; start: number; size: number; entry: RemoteEntry; selected: boolean;
  onSelect: (index: number) => void; onAction: OnAction;
}) {
  return (
    <div id={id} role="row" aria-rowindex={index + 1} aria-selected={selected}
      className={`sftp-row ${selected ? 'selected' : ''}`} style={{ height: size, transform: `translateY(${start}px)` }}
      onClick={(e) => { onSelect(index); e.currentTarget.closest<HTMLElement>('[role="grid"]')?.focus({ preventScroll: true }); }}
      onDoubleClick={() => onAction('open', entry)}>
      {entry.isDir ? <Folder size={15} strokeWidth={1.7} className="sftp-folder-icon" /> : <FileText size={15} strokeWidth={1.6} className="sftp-file-icon" />}
      <div role="gridcell" style={{ flex: 1, minWidth: 0 }}>
        <div className="sftp-row-name" title={entry.name}>{entry.name}{entry.isSymlink ? ' ↗' : ''}</div>
        <div className="mono sftp-permissions">{entry.perm} · {formatModTime(entry.modTime)}</div>
      </div>
      <span role="gridcell" className="sftp-row-meta mono">{entry.isDir ? '目录' : entry.isSymlink ? '链接' : formatBytes(entry.size)}</span>
      <div role="gridcell" className="sftp-row-actions" onDoubleClick={(e) => e.stopPropagation()}>
        {buttons.map(([kind, label, Icon]) => {
          if (entry.isDir && (kind === 'edit' || kind === 'download')) return null;
          return <button key={kind} type="button" className="host-action-btn" title={label} aria-label={label}
            tabIndex={selected ? 0 : -1} style={kind === 'delete' ? { color: 'var(--red)' } : undefined}
            onClick={(e) => { e.stopPropagation(); onAction(kind, entry); }}>
            <Icon size={kind === 'edit' || kind === 'download' ? 13 : 12} />
          </button>;
        })}
      </div>
    </div>
  );
});
