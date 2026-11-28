import React, { useMemo, useState } from 'react';
import { Code2, CornerDownLeft, Pencil, Play, Plus, Search, Trash2, X } from 'lucide-react';
import type { Snippet } from '../types/shell';
import { applySnippetVars, extractSnippetVars } from '../lib/utils';

interface SnippetsDrawerProps {
  snippets: Snippet[];
  hasActiveSession: boolean;
  onClose: () => void;
  onInsertCommand: (cmd: string, execute: boolean) => void;
  onSaveSnippet: (s: Snippet) => Promise<void>;
  onDeleteSnippet: (id: string) => Promise<void>;
}

export const SnippetsDrawer: React.FC<SnippetsDrawerProps> = ({
  snippets,
  hasActiveSession,
  onClose,
  onInsertCommand,
  onSaveSnippet,
  onDeleteSnippet,
}) => {
  const [query, setQuery] = useState('');
  const [cat, setCat] = useState('全部');
  const [editing, setEditing] = useState<Snippet | null>(null);
  const [varPrompt, setVarPrompt] = useState<{
    snippet: Snippet;
    vars: string[];
    values: Record<string, string>;
    execute: boolean;
  } | null>(null);

  const categories = useMemo(() => {
    const set = new Set<string>(['全部']);
    for (const s of snippets) set.add(s.category || '通用');
    return [...set];
  }, [snippets]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return snippets
      .filter((s) => (cat === '全部' || s.category === cat) && (!q || `${s.name} ${s.command} ${s.description ?? ''}`.toLowerCase().includes(q)))
      .sort((a, b) => Number(Boolean(b.pinned)) - Number(Boolean(a.pinned)));
  }, [snippets, cat, query]);

  const triggerSnippet = (s: Snippet, execute: boolean) => {
    const vars = extractSnippetVars(s.command);
    if (vars.length > 0) {
      const initial: Record<string, string> = {};
      for (const v of vars) initial[v] = '';
      setVarPrompt({ snippet: s, vars, values: initial, execute });
      return;
    }
    onInsertCommand(s.command, execute);
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!editing) return;
    await onSaveSnippet(editing);
    setEditing(null);
  };

  return (
    <section className="right-drawer" aria-label="快捷命令片段">
      <div className="drawer-header">
        <div className="drawer-title">
          <Code2 size={15} strokeWidth={1.8} />
          <span>快捷命令片段</span>
        </div>
        <div style={{ display: 'flex', gap: 2 }}>
          <button
            type="button"
            className="icon-btn"
            title="新建片段"
            onClick={() =>
              setEditing({ id: '', name: '', category: '通用', command: '', description: '', pinned: false })
            }
          >
            <Plus size={15} />
          </button>
          <button type="button" className="icon-btn" title="关闭" onClick={onClose}>
            <X size={14} />
          </button>
        </div>
      </div>

      <div className="sftp-toolbar">
        <div className="search-box search-box-sm">
          <Search size={12} />
          <input
            className="input input-sm"
            placeholder="搜索命令片段…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
        <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
          {categories.map((c) => (
            <button
              key={c}
              type="button"
              className={`pill-badge ${cat === c ? 'accent' : 'neutral'}`}
              style={{ border: 'none', cursor: 'pointer', padding: '3px 9px' }}
              onClick={() => setCat(c)}
            >
              {c}
            </button>
          ))}
        </div>
      </div>

      <div className="drawer-body">
        <div className="card-list">
          {filtered.map((s) => (
            <div key={s.id} className="soft-card">
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6 }}>
                <div style={{ fontWeight: 600, fontSize: 12.5 }}>{s.name}</div>
                <span className="pill-badge neutral">{s.category}</span>
              </div>
              {s.description && (
                <div style={{ fontSize: 11.5, color: 'var(--ink-secondary)' }}>{s.description}</div>
              )}
              <div className="code-block">{s.command}</div>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <div style={{ display: 'flex', gap: 4 }}>
                  <button
                    type="button"
                    className="btn btn-sm"
                    disabled={!hasActiveSession}
                    title="填入终端但不回车"
                    onClick={() => triggerSnippet(s, false)}
                  >
                    <CornerDownLeft size={12} />
                    插入
                  </button>
                  <button
                    type="button"
                    className="btn btn-primary btn-sm"
                    disabled={!hasActiveSession}
                    title="直接发送到终端执行"
                    onClick={() => triggerSnippet(s, true)}
                  >
                    <Play size={11} />
                    执行
                  </button>
                </div>
                <div style={{ display: 'flex', gap: 2 }}>
                  <button
                    type="button"
                    className="host-action-btn"
                    title="编辑"
                    onClick={() => setEditing(s)}
                  >
                    <Pencil size={12} />
                  </button>
                  <button
                    type="button"
                    className="host-action-btn"
                    title="删除"
                    style={{ color: 'var(--red)' }}
                    onClick={() => void onDeleteSnippet(s.id)}
                  >
                    <Trash2 size={12} />
                  </button>
                </div>
              </div>
            </div>
          ))}
        </div>
      </div>

      {editing && (
        <div className="modal-backdrop" onClick={() => setEditing(null)}>
          <form className="modal-sheet" role="dialog" aria-modal="true" style={{ maxWidth: 440 }} onClick={(e) => e.stopPropagation()} onSubmit={handleSave}>
            <div className="modal-header">
              <h2>{editing.id ? '编辑命令片段' : '新建命令片段'}</h2>
              <button type="button" className="icon-btn" aria-label="关闭" onClick={() => setEditing(null)}>
                <X size={15} />
              </button>
            </div>
            <div className="modal-body">
              <div className="field-row">
                <div className="field-group grow">
                  <label className="field-label">名称</label>
                  <input
                    className="input"
                    required
                    value={editing.name}
                    onChange={(e) => setEditing({ ...editing, name: e.target.value })}
                    placeholder="例如 查看 Nginx 错误日志"
                  />
                </div>
                <div className="field-group" style={{ width: 110 }}>
                  <label className="field-label">分类</label>
                  <input
                    className="input"
                    value={editing.category}
                    onChange={(e) => setEditing({ ...editing, category: e.target.value })}
                    placeholder="系统"
                  />
                </div>
              </div>
              <div className="field-group">
                <label className="field-label">命令模板</label>
                <textarea
                  className="textarea mono"
                  required
                  rows={3}
                  style={{ resize: 'none' }}
                  value={editing.command}
                  onChange={(e) => setEditing({ ...editing, command: e.target.value })}
                  placeholder="tail -n {{lines}} /var/log/nginx/error.log"
                />
              </div>
              <div className="field-group">
                <label className="field-label">说明</label>
                <input
                  className="input"
                  value={editing.description ?? ''}
                  onChange={(e) => setEditing({ ...editing, description: e.target.value })}
                />
              </div>
            </div>
            <div className="modal-footer">
              <button type="button" className="btn" onClick={() => setEditing(null)}>
                取消
              </button>
              <button type="submit" className="btn btn-primary">
                保存片段
              </button>
            </div>
          </form>
        </div>
      )}

      {varPrompt && (
        <div className="modal-backdrop" onClick={() => setVarPrompt(null)}>
          <form
            className="modal-sheet"
            role="dialog"
            aria-modal="true"
            style={{ maxWidth: 420 }}
            onClick={(e) => e.stopPropagation()}
            onSubmit={(e) => {
              e.preventDefault();
              const resolved = applySnippetVars(varPrompt.snippet.command, varPrompt.values);
              onInsertCommand(resolved, varPrompt.execute);
              setVarPrompt(null);
            }}
          >
            <div className="modal-header">
              <h2>填写变量 · {varPrompt.snippet.name}</h2>
              <button type="button" className="icon-btn" aria-label="关闭" onClick={() => setVarPrompt(null)}>
                <X size={15} />
              </button>
            </div>
            <div className="modal-body">
              {varPrompt.vars.map((v, idx) => (
                <div key={v} className="field-group">
                  <label className="field-label mono">{`{{${v}}}`}</label>
                  <input
                    className="input mono"
                    autoFocus={idx === 0}
                    required
                    value={varPrompt.values[v] ?? ''}
                    onChange={(e) =>
                      setVarPrompt({
                        ...varPrompt,
                        values: { ...varPrompt.values, [v]: e.target.value },
                      })
                    }
                  />
                </div>
              ))}
              <div className="code-block">{applySnippetVars(varPrompt.snippet.command, varPrompt.values)}</div>
            </div>
            <div className="modal-footer">
              <button type="button" className="btn" onClick={() => setVarPrompt(null)}>
                取消
              </button>
              <button type="submit" className="btn btn-primary">
                {varPrompt.execute ? '执行命令' : '插入终端'}
              </button>
            </div>
          </form>
        </div>
      )}
    </section>
  );
};
