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
  Sparkles,
  SplitSquareHorizontal,
  SplitSquareVertical,
  Sun,
  Terminal,
} from 'lucide-react';
import type { Profile, Snippet } from '../types/shell';
import { parseQuickConnect } from '../lib/utils';
import { HostIcon } from './HostIcon';

interface PaletteAction {
  id: string;
  label: string;
  subtitle?: string;
  category: string;
  icon: React.ReactNode;
  run: () => void;
}

interface CommandPaletteProps {
  profiles: Profile[];
  snippets: Snippet[];
  hasActiveSession: boolean;
  isDark: boolean;
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

export const CommandPalette: React.FC<CommandPaletteProps> = ({
  profiles,
  snippets,
  hasActiveSession,
  isDark,
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

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const items = useMemo<PaletteAction[]>(() => {
    const q = query.trim().toLowerCase();
    const out: PaletteAction[] = [];

    const parsedQuick = query.trim() ? parseQuickConnect(query) : null;
    if (parsedQuick && (query.includes('@') || query.includes('.') || query.includes(':'))) {
      out.push({
        id: 'quick-connect',
        label: `快速连接 ${parsedQuick.user}@${parsedQuick.host}:${parsedQuick.port}`,
        subtitle: '立即建立临时 SSH 会话',
        category: '快速连接',
        icon: <Terminal size={14} />,
        run: () => onQuickConnect(parsedQuick),
      });
    }

    for (const p of profiles) {
      const hay = `${p.name} ${p.host} ${p.user} ${p.group ?? ''} ${(p.tags ?? []).join(' ')}`.toLowerCase();
      if (!q || hay.includes(q)) {
        out.push({
          id: `host-${p.id}`,
          label: p.name,
          subtitle: `${p.user}@${p.host}${p.port !== 22 ? ':' + p.port : ''}${p.group ? ' · ' + p.group : ''}`,
          category: '主机',
          icon: <HostIcon profile={p} size="sm" />,
          run: () => onConnectProfile(p),
        });
      }
    }

    const cmds: PaletteAction[] = [
      { id: 'act-new', label: '新建 SSH 主机', subtitle: 'Ctrl+N', category: '操作', icon: <Plus size={14} />, run: onNewHost },
      { id: 'act-import', label: '导入 SSH 配置', category: '操作', icon: <Download size={14} />, run: onImportSSH },
      { id: 'act-ai', label: 'AI 助手', subtitle: 'Ctrl+Shift+A', category: '操作', icon: <Sparkles size={14} />, run: onToggleAI },
      { id: 'act-agent', label: 'MCP 接入与安全', category: '安全', icon: <Settings size={14} />, run: () => onOpenSettings('agent') },
      {
        id: 'act-theme',
        label: isDark ? '切换浅色外观' : '切换深色外观',
        category: '外观',
        icon: isDark ? <Sun size={14} /> : <Moon size={14} />,
        run: onToggleTheme,
      },
      { id: 'act-keys', label: 'SSH 密钥管理', category: '操作', icon: <KeyRound size={14} />, run: () => onOpenSettings('keys') },
      { id: 'act-settings', label: '偏好设置', subtitle: 'Ctrl+,', category: '操作', icon: <Settings size={14} />, run: () => onOpenSettings('appearance') },
    ];

    if (hasActiveSession) {
      cmds.unshift(
        { id: 'act-sftp', label: '打开 / 关闭 SFTP 文件管理', category: '当前会话', icon: <FolderOpen size={14} />, run: onToggleSFTP },
        { id: 'act-tunnels', label: '管理端口转发隧道 (-L / -R / -D)', category: '当前会话', icon: <ArrowLeftRight size={14} />, run: onToggleTunnels },
        { id: 'act-split-h', label: '向右分屏', category: '当前会话', icon: <SplitSquareHorizontal size={14} />, run: () => onSplit('horizontal') },
        { id: 'act-split-v', label: '向下分屏', category: '当前会话', icon: <SplitSquareVertical size={14} />, run: () => onSplit('vertical') },
      );
    }

    for (const c of cmds) {
      if (!q || `${c.label} ${c.subtitle ?? ''} ${c.category}`.toLowerCase().includes(q)) {
        out.push(c);
      }
    }

    if (hasActiveSession) {
      for (const s of snippets) {
        if (!q || `${s.name} ${s.command} ${s.category}`.toLowerCase().includes(q)) {
          out.push({
            id: `snip-${s.id}`,
            label: s.name,
            subtitle: s.command,
            category: '命令片段',
            icon: <Code2 size={14} />,
            run: () => onRunSnippet(s),
          });
        }
      }
    }

    return out.slice(0, 24);
  }, [query, profiles, snippets, hasActiveSession, isDark]);

  useEffect(() => {
    setSelectedIdx(0);
  }, [query]);

  const execute = (item?: PaletteAction) => {
    if (!item) return;
    onClose();
    item.run();
  };

  return (
    <div id="commandPalette" className="spotlight-backdrop" onClick={onClose}>
      <div className="spotlight-box" onClick={(e) => e.stopPropagation()}>
        <div className="spotlight-input-row">
          <Search size={16} style={{ color: 'var(--ink-tertiary)', flexShrink: 0 }} />
          <input
            ref={inputRef}
            placeholder="搜索主机、命令片段、操作，或输入 user@host 快速连接…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') onClose();
              if (e.key === 'ArrowDown') {
                e.preventDefault();
                setSelectedIdx((i) => Math.min(items.length - 1, i + 1));
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

        <div className="spotlight-results">
          {items.length === 0 ? (
            <div style={{ padding: 24, textAlign: 'center', color: 'var(--ink-tertiary)', fontSize: 12.5 }}>
              无匹配项
            </div>
          ) : (
            items.map((item, idx) => (
              <div
                key={item.id}
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
                <span style={{ fontSize: 11, color: 'var(--ink-tertiary)' }}>{item.category}</span>
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  );
};
