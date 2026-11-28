import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Check, ChevronDown, Folder, Plus, X } from 'lucide-react';
import { shouldOpenDropdownOnFocus, subscribeWindowDismiss } from '../../lib/windowFocusGuard';

export interface ComboBoxProps {
  id?: string;
  value: string;
  onChange: (value: string) => void;
  options: string[];
  placeholder?: string;
  emptyOptionLabel?: string;
}

interface MenuEntry {
  kind: 'option' | 'create' | 'clear';
  value: string;
  label: string;
}

export const ComboBox: React.FC<ComboBoxProps> = ({
  id,
  value,
  onChange,
  options,
  placeholder,
  emptyOptionLabel,
}) => {
  const wrapRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [activeIdx, setActiveIdx] = useState(-1);
  const [coords, setCoords] = useState<{ left: number; top: number; width: number; flipUp: boolean } | null>(null);

  const trimmed = value.trim();
  const hasExactMatch = useMemo(
    () => Boolean(trimmed && options.some((o) => o.toLowerCase() === trimmed.toLowerCase())),
    [options, trimmed],
  );

  const entries = useMemo<MenuEntry[]>(() => {
    const showAll = !trimmed || hasExactMatch;
    const matched = showAll
      ? options
      : options.filter((o) => o.toLowerCase().includes(trimmed.toLowerCase()));

    const list: MenuEntry[] = matched.map((o) => ({ kind: 'option', value: o, label: o }));
    if (trimmed && !hasExactMatch) {
      list.push({ kind: 'create', value: trimmed, label: `新建分组「${trimmed}」` });
    }
    if (trimmed && emptyOptionLabel) {
      list.push({ kind: 'clear', value: '', label: emptyOptionLabel });
    }
    return list;
  }, [options, trimmed, hasExactMatch, emptyOptionLabel]);

  const canOpen = options.length > 0 || entries.length > 0;

  const updatePosition = () => {
    const el = wrapRef.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    const estHeight = Math.min(220, Math.max(44, entries.length * 32 + 14));
    const spaceBelow = window.innerHeight - rect.bottom - 12;
    const flipUp = spaceBelow < estHeight && rect.top > estHeight + 16;
    setCoords({
      left: Math.max(8, Math.min(rect.left, window.innerWidth - Math.max(rect.width, 168) - 8)),
      top: flipUp ? rect.top - 5 : rect.bottom + 5,
      width: Math.max(rect.width, 168),
      flipUp,
    });
  };

  useLayoutEffect(() => {
    if (!open) return;
    updatePosition();
  }, [open, entries.length]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as Node | null;
      if (wrapRef.current?.contains(target) || menuRef.current?.contains(target)) return;
      setOpen(false);
    };
    const onResizeOrScroll = () => updatePosition();
    const offWindowDismiss = subscribeWindowDismiss(() => setOpen(false));
    document.addEventListener('pointerdown', onPointerDown, true);
    window.addEventListener('resize', onResizeOrScroll);
    window.addEventListener('scroll', onResizeOrScroll, true);
    return () => {
      offWindowDismiss();
      document.removeEventListener('pointerdown', onPointerDown, true);
      window.removeEventListener('resize', onResizeOrScroll);
      window.removeEventListener('scroll', onResizeOrScroll, true);
    };
  }, [open, entries.length]);

  useEffect(() => {
    if (!open) {
      setActiveIdx(-1);
    } else if (activeIdx >= entries.length) {
      setActiveIdx(entries.length > 0 ? 0 : -1);
    }
  }, [open, entries.length, activeIdx]);

  const selectEntry = (entry: MenuEntry) => {
    onChange(entry.value);
    setOpen(false);
    setActiveIdx(-1);
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown') {
      if (!canOpen) return;
      e.preventDefault();
      if (!open) {
        setOpen(true);
        setActiveIdx(0);
      } else if (entries.length > 0) {
        setActiveIdx((i) => (i + 1) % entries.length);
      }
      return;
    }
    if (e.key === 'ArrowUp') {
      if (!open || entries.length === 0) return;
      e.preventDefault();
      setActiveIdx((i) => (i <= 0 ? entries.length - 1 : i - 1));
      return;
    }
    if (e.key === 'Enter' && open && activeIdx >= 0 && activeIdx < entries.length) {
      e.preventDefault();
      selectEntry(entries[activeIdx]);
      return;
    }
    if (e.key === 'Escape' && open) {
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
      return;
    }
    if (e.key === 'Tab' && open) {
      setOpen(false);
    }
  };

  const showMenu = open && entries.length > 0 && coords !== null;

  return (
    <div ref={wrapRef} className={`ui-combobox ${showMenu ? 'open' : ''}`}>
      <input
        ref={inputRef}
        id={id}
        type="text"
        className={`input ui-combobox-input ${options.length > 0 ? 'has-toggle' : ''}`}
        placeholder={placeholder}
        autoComplete="off"
        value={value}
        aria-expanded={showMenu}
        aria-autocomplete="list"
        onFocus={() => {
          if (canOpen && shouldOpenDropdownOnFocus()) setOpen(true);
        }}
        onClick={() => {
          if (canOpen && !open) setOpen(true);
        }}
        onChange={(e) => {
          onChange(e.target.value);
          setActiveIdx(-1);
          if (options.length > 0 || e.target.value.trim()) {
            setOpen(true);
          }
        }}
        onKeyDown={handleKeyDown}
      />
      {options.length > 0 && (
        <button
          type="button"
          tabIndex={-1}
          className="ui-combobox-toggle"
          aria-label={showMenu ? '收起分组列表' : '展开已有分组'}
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => {
            if (open) {
              setOpen(false);
            } else {
              inputRef.current?.focus();
              setOpen(true);
            }
          }}
        >
          <ChevronDown size={13.5} strokeWidth={2} />
        </button>
      )}
      {showMenu &&
        createPortal(
          <div
            ref={menuRef}
            data-combobox-open="true"
            className="ui-select-content ui-combobox-content"
            style={{
              position: 'fixed',
              left: coords.left,
              top: coords.top,
              width: coords.width,
              transform: coords.flipUp ? 'translateY(-100%)' : undefined,
            }}
            onClick={(e) => e.stopPropagation()}
          >
            <div className="ui-select-viewport">
              {entries.map((entry, idx) => {
                const isSelected = entry.kind === 'option' && entry.value === trimmed;
                const isHighlighted = idx === activeIdx;
                const prevKind = idx > 0 ? entries[idx - 1].kind : null;
                const needsSep = prevKind === 'option' && entry.kind !== 'option';
                return (
                  <React.Fragment key={`${entry.kind}:${entry.value}`}>
                    {needsSep && <div className="context-menu-sep" />}
                    <button
                      type="button"
                      className={`ui-select-item ui-combobox-item ${entry.kind === 'create' ? 'is-create' : ''} ${entry.kind === 'clear' ? 'is-clear' : ''}`}
                      data-highlighted={isHighlighted ? '' : undefined}
                      onMouseDown={(e) => e.preventDefault()}
                      onMouseEnter={() => setActiveIdx(idx)}
                      onMouseLeave={() => setActiveIdx(-1)}
                      onClick={() => selectEntry(entry)}
                    >
                      <span className="ui-combobox-item-main">
                        {entry.kind === 'option' && <Folder size={13} strokeWidth={1.8} className="ui-combobox-item-icon" />}
                        {entry.kind === 'create' && <Plus size={13} strokeWidth={2.2} className="ui-combobox-item-icon" />}
                        {entry.kind === 'clear' && <X size={13} strokeWidth={2} className="ui-combobox-item-icon" />}
                        <span className="ui-combobox-item-label">{entry.label}</span>
                      </span>
                      {isSelected && (
                        <span className="ui-select-check">
                          <Check size={13} strokeWidth={2.2} />
                        </span>
                      )}
                    </button>
                  </React.Fragment>
                );
              })}
            </div>
          </div>,
          document.body,
        )}
    </div>
  );
};
