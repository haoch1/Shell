import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowLeftRight,
  Code2,
  Download,
  FolderOpen,
  KeyRound,
  Moon,
  Plus,
  Search,
  Settings,
  Shield,
  Sparkles,
  SplitSquareHorizontal,
  SplitSquareVertical,
  Sun,
  Terminal,
} from 'lucide-react';
import type { Profile, Snippet } from '../types/shell';
import { formatHostDisplay, parseQuickConnect } from '../lib/utils';
import { sameSessionTarget } from '../lib/sessionAttempt';
import { HostIcon } from './HostIcon';

interface PaletteAction {
  id: string;
  label: string;
  subtitle?: string;
  tag?: string;
  shortcut?: string;
  category: string;
  icon: React.ReactNode;
  run: () => void;
}

interface CommandPaletteProps {
  profiles: Profile[];
  splitSource?: Profile;
  snippets: Snippet[];
  hasActiveSession: boolean;
  isDark: boolean;
  hideHostIp?: boolean;
  onClose: () => void;
  onConnectProfile: (p: Profile) => void;
  onQuickConnect: (target: { user: string; host: string; port: number }) => void;
  onRunSnippet: (s: Snippet) => void;
  onNewHost: () => void;
  onImportSSH: () => void;
  onSplit: (dir: 'horizontal' | 'vertical') => void;
  onToggleSFTP: () => void;
  onToggleTunnels: () => void;
  onToggleAI: () => void;
  onToggleTheme: () => void;
  onOpenSettings: (tab?: 'appearance' | 'ssh' | 'keys' | 'ai' | 'agent' | 'backup' | 'about') => void;
}

const DEFAULT_VISIBLE_SERVERS = 12;
const MAX_PALETTE_ITEMS = 40;

export const CommandPalette: React.FC<CommandPaletteProps> = ({
  profiles,
  splitSource,
  snippets,
  hasActiveSession,
  isDark,
  hideHostIp = false,
  onClose,
  onConnectProfile,
  onQuickConnect,
  onRunSnippet,
  onNewHost,
  onImportSSH,
  onSplit,
  onToggleSFTP,
  onToggleTunnels,
  onToggleAI,
  onToggleTheme,
  onOpenSettings,
}) => {
  const [query, setQuery] = useState('');
  const [selectedIdx, setSelectedIdx] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const resultsRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const sortedProfiles = useMemo(() => {
    return [...profiles].sort((a, b) => {
      const pinDiff = Number(Boolean(b.pinned)) - Number(Boolean(a.pinned));
      if (pinDiff !== 0) return pinDiff;
      const recentDiff = (b.lastUsedAt ?? 0) - (a.lastUsedAt ?? 0);
      if (recentDiff !== 0) return recentDiff;
      return a.name.localeCompare(b.name, 'zh-CN');
    });
  }, [profiles]);

  const items = useMemo<PaletteAction[]>(() => {
    const q = query.trim().toLowerCase();
    const out: PaletteAction[] = [];

    const parsedQuick = query.trim() ? parseQuickConnect(query) : null;
    if (parsedQuick && (query.includes('@') || query.includes('.') || query.includes(':'))) {
      out.push({
        id: 'quick-connect',
        label: `快速连接 ${parsedQuick.user}@${parsedQuick.host}:${parsedQuick.port}`,
        category: '快速连接',
        icon: <Terminal size={14} />,
        run: () => onQuickConnect(parsedQuick),
      });
    }

    if (splitSource) {
      const currentLabel = '当前主机';
      if (!q || `${currentLabel} ${splitSource.name} ${splitSource.host}`.toLowerCase().includes(q)) {
        out.push({
          id: 'split-current',
          label: currentLabel,
          subtitle: hideHostIp
            ? splitSource.user || undefined
            : `${splitSource.user}@${formatHostDisplay(splitSource.host, splitSource.port)}`,
          tag: splitSource.group || undefined,
          category: '分屏',
          icon: <HostIcon profile={splitSource} size="sm" />,
          run: () => onConnectProfile(splitSource),
        });
      }
      if (!q || '新建 SSH 主机'.toLowerCase().includes(q)) {
        out.push({
          id: 'split-new',
          label: '新建 SSH 主机',
          category: '分屏',
          icon: <Plus size={14} />,
          run: onNewHost,
        });
      }
    }

    let serverCount = 0;
    for (const p of sortedProfiles) {
      if (splitSource && sameSessionTarget(splitSource, p)) continue;
      const hay = `${p.name} ${p.host} ${p.user} ${p.group ?? ''} ${(p.tags ?? []).join(' ')}`.toLowerCase();
      if (!q || hay.includes(q)) {
        if (!splitSource && !q && serverCount >= DEFAULT_VISIBLE_SERVERS) break;
        const displayName =
          p.name && (!hideHostIp || p.name !== p.host)
            ? p.name
            : hideHostIp
              ? p.user || 'SSH 主机'
              : formatHostDisplay(p.host, p.port);
        out.push({
          id: `host-${p.id}`,
          label: displayName,
          subtitle: hideHostIp
            ? p.user && p.user !== displayName
              ? p.user
              : undefined
            : `${p.user}@${formatHostDisplay(p.host, p.port)}`,
          tag: p.group || undefined,
          category: '服务器',
          icon: <HostIcon profile={p} size="sm" />,
          run: () => onConnectProfile(p),
        });
        serverCount += 1;
      }
    }

    if (splitSource) return out.slice(0, 24);

    const actions: PaletteAction[] = [
      { id: 'act-new', label: '新建 SSH 主机', shortcut: 'Ctrl+N', category: '操作', icon: <Plus size={14} />, run: onNewHost },
      { id: 'act-import', label: '导入 SSH 配置', category: '操作', icon: <Download size={14} />, run: onImportSSH },
      { id: 'act-ai', label: 'AI 助手', shortcut: 'Ctrl+Shift+A', category: '操作', icon: <Sparkles size={14} />, run: onToggleAI },
      { id: 'act-settings', label: '偏好设置', shortcut: 'Ctrl+,', category: '操作', icon: <Settings size={14} />, run: () => onOpenSettings('appearance') },
      {
        id: 'act-theme',
        label: isDark ? '切换浅色外观' : '切换深色外观',
        category: '操作',
        icon: isDark ? <Sun size={14} /> : <Moon size={14} />,
        run: onToggleTheme,
      },
      { id: 'act-keys', label: 'SSH 密钥管理', category: '操作', icon: <KeyRound size={14} />, run: () => onOpenSettings('keys') },
      { id: 'act-agent', label: 'MCP 接入与安全', category: '操作', icon: <Shield size={14} />, run: () => onOpenSettings('agent') },
    ];

    for (const c of actions) {
      if (!q || `${c.label} ${c.subtitle ?? ''} ${c.shortcut ?? ''} ${c.category}`.toLowerCase().includes(q)) {
        out.push(c);
      }
    }

    if (hasActiveSession) {
      const sessionActions: PaletteAction[] = [
        { id: 'act-sftp', label: '打开 / 关闭 SFTP 文件管理', category: '当前会话', icon: <FolderOpen size={14} />, run: onToggleSFTP },
        { id: 'act-tunnels', label: '管理端口转发隧道 (-L / -R / -D)', category: '当前会话', icon: <ArrowLeftRight size={14} />, run: onToggleTunnels },
        { id: 'act-split-h', label: '向右分屏', category: '当前会话', icon: <SplitSquareHorizontal size={14} />, run: () => onSplit('horizontal') },
        { id: 'act-split-v', label: '向下分屏', category: '当前会话', icon: <SplitSquareVertical size={14} />, run: () => onSplit('vertical') },
      ];

      for (const c of sessionActions) {
        if (!q || `${c.label} ${c.subtitle ?? ''} ${c.shortcut ?? ''} ${c.category}`.toLowerCase().includes(q)) {
          out.push(c);
        }
      }

      for (const s of snippets) {
        if (!q || `${s.name} ${s.command} ${s.category}`.toLowerCase().includes(q)) {
          out.push({
            id: `snip-${s.id}`,
            label: s.name,
            subtitle: s.command,
            tag: s.category || undefined,
            category: '命令片段',
            icon: <Code2 size={14} />,
            run: () => onRunSnippet(s),
          });
        }
      }
    }

    return out.slice(0, MAX_PALETTE_ITEMS);
  }, [
    query,
    sortedProfiles,
    snippets,
    splitSource,
    hasActiveSession,
    isDark,
    hideHostIp,
    onConnectProfile,
    onQuickConnect,
    onNewHost,
    onImportSSH,
    onToggleAI,
    onOpenSettings,
    onToggleTheme,
    onToggleSFTP,
    onToggleTunnels,
    onSplit,
    onRunSnippet,
  ]);

  useEffect(() => {
    setSelectedIdx(0);
  }, [query]);

  useEffect(() => {
    if (items.length > 0 && selectedIdx >= items.length) {
      setSelectedIdx(items.length - 1);
    }
  }, [items.length, selectedIdx]);

  useEffect(() => {
    const container = resultsRef.current;
    if (!container) return;
    if (selectedIdx === 0) {
      container.scrollTop = 0;
      return;
    }
    const el = container.querySelector<HTMLElement>(`[data-palette-idx="${selectedIdx}"]`);
    el?.scrollIntoView({ block: 'nearest' });
  }, [selectedIdx]);

  const execute = (item?: PaletteAction) => {
    if (!item) return;
    onClose();
    item.run();
  };

  return (
    <div id="commandPalette" className="spotlight-backdrop" onClick={onClose}>
      <div
        className="spotlight-box"
        role="dialog"
        aria-modal="true"
        aria-label={splitSource ? '选择分屏主机' : '命令面板'}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="spotlight-input-row">
          <Search size={16} style={{ color: 'var(--ink-tertiary)', flexShrink: 0 }} />
          <input
            ref={inputRef}
            aria-label={splitSource ? '选择分屏主机' : '搜索命令'}
            placeholder={splitSource ? '选择分屏主机，或输入 user@host…' : '搜索服务器、命令片段、操作，或输入 user@host 快速连接…'}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') onClose();
              if (e.key === 'ArrowDown') {
                e.preventDefault();
                setSelectedIdx((i) => (items.length === 0 ? 0 : Math.min(items.length - 1, i + 1)));
              }
              if (e.key === 'ArrowUp') {
                e.preventDefault();
                setSelectedIdx((i) => Math.max(0, i - 1));
              }
              if (e.key === 'Enter') {
                e.preventDefault();
                execute(items[selectedIdx]);
              }
            }}
          />
          <span className="pill-badge neutral mono">ESC</span>
        </div>

        <div ref={resultsRef} className="spotlight-results">
          {items.length === 0 ? (
            <div style={{ padding: 24, textAlign: 'center', color: 'var(--ink-tertiary)', fontSize: 12.5 }}>
              无匹配项
            </div>
          ) : (
            items.map((item, idx) => {
              const showGroup = idx === 0 || items[idx - 1].category !== item.category;
              return (
                <React.Fragment key={item.id}>
                  {showGroup && <div className="spotlight-group-title">{item.category}</div>}
                  <div
                    data-action-id={item.id}
                    data-palette-idx={idx}
                    className={`spotlight-item ${idx === selectedIdx ? 'selected' : ''}`}
                    onMouseEnter={() => setSelectedIdx(idx)}
                    onClick={() => execute(item)}
                  >
                    <span style={{ display: 'inline-flex', width: 24, justifyContent: 'center', flexShrink: 0 }}>
                      {item.icon}
                    </span>
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div style={{ fontSize: 13, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {item.label}
                      </div>
                      {item.subtitle && (
                        <div
                          className="mono"
                          style={{
                            fontSize: 11,
                            color: 'var(--ink-tertiary)',
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                          }}
                        >
                          {item.subtitle}
                        </div>
                      )}
                    </div>
                    {item.tag && <span className="spotlight-tag">{item.tag}</span>}
                    {item.shortcut && <span className="spotlight-kbd mono">{item.shortcut}</span>}
                  </div>
                </React.Fragment>
              );
            })
          )}
        </div>
      </div>
    </div>
  );
};
