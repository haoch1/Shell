import React, { useEffect, useState } from 'react';
import { FileCode2, Save, X } from 'lucide-react';
import { errorMessage, getApi } from '../lib/bridge';

interface RemoteEditorModalProps {
  sessionId: string;
  remotePath: string;
  onClose: () => void;
  onSaved: () => void;
  onToast: (msg: string) => void;
}

export const RemoteEditorModal: React.FC<RemoteEditorModalProps> = ({
  sessionId,
  remotePath,
  onClose,
  onSaved,
  onToast,
}) => {
  const [content, setContent] = useState('');
  const [original, setOriginal] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');

  useEffect(() => {
    setLoading(true);
    setErr('');
    void getApi()
      .ReadRemoteText(sessionId, remotePath)
      .then((text) => {
        setContent(text);
        setOriginal(text);
      })
      .catch((e) => setErr(errorMessage(e)))
      .finally(() => setLoading(false));
  }, [sessionId, remotePath]);

  const dirty = content !== original;

  const save = async () => {
    if (saving || loading) return;
    setSaving(true);
    setErr('');
    try {
      await getApi().WriteRemoteText(sessionId, remotePath, content);
      setOriginal(content);
      onSaved();
      onToast('已保存到远端服务器');
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setSaving(false);
    }
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        e.preventDefault();
        void save();
      }
      if (e.key === 'Escape') {
        onClose();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });

  const fileName = remotePath.split('/').pop() || remotePath;
  const lineCount = content.split('\n').length;

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-sheet editor" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <div style={{ display: 'flex', alignItems: 'center', gap: 9, minWidth: 0 }}>
            <FileCode2 size={16} style={{ color: 'var(--accent)', flexShrink: 0 }} />
            <div style={{ minWidth: 0 }}>
              <h2 style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <span>{fileName}</span>
                {dirty && (
                  <span
                    style={{ width: 7, height: 7, borderRadius: '50%', background: 'var(--orange)', display: 'inline-block' }}
                    title="有未保存更改"
                  />
                )}
              </h2>
              <div className="mono" style={{ fontSize: 11, color: 'var(--ink-tertiary)' }}>
                {remotePath}
              </div>
            </div>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <button type="button" className="btn btn-primary btn-sm" disabled={saving || loading || !dirty} onClick={() => void save()}>
              <Save size={13} />
              {saving ? '保存中…' : '保存 (Ctrl+S)'}
            </button>
            <button type="button" className="icon-btn" onClick={onClose}>
              <X size={15} />
            </button>
          </div>
        </div>

        {err && (
          <div style={{ padding: '8px 20px', borderBottom: '1px solid var(--border-hairline)' }}>
            <p className="error-text">{err}</p>
          </div>
        )}

        <div className="code-editor-wrap">
          {loading ? (
            <div style={{ margin: 'auto', color: 'var(--ink-tertiary)' }}>正在读取远端文件…</div>
          ) : (
            <textarea
              className="code-editor-textarea"
              spellCheck={false}
              value={content}
              onChange={(e) => setContent(e.target.value)}
            />
          )}
        </div>

        <div className="modal-footer" style={{ justifyContent: 'space-between', padding: '8px 20px', fontSize: 11.5, color: 'var(--ink-tertiary)' }}>
          <span className="mono">UTF-8 · {lineCount} 行 · {content.length} 字符</span>
          <span>原子写入并保留原文件权限</span>
        </div>
      </div>
    </div>
  );
};
