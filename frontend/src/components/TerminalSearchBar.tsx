import React, { useEffect, useRef, useState } from 'react';
import { ChevronDown, ChevronUp, X } from 'lucide-react';
import { terminalBus } from '../lib/terminalBus';

interface TerminalSearchBarProps {
  sessionId: string;
  onClose: () => void;
}

export const TerminalSearchBar: React.FC<TerminalSearchBarProps> = ({ sessionId, onClose }) => {
  const [term, setTerm] = useState('');
  const [caseSensitive, setCaseSensitive] = useState(false);
  const [regex, setRegex] = useState(false);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    inputRef.current?.focus();
    return () => {
      terminalBus.get(sessionId)?.search.clearDecorations();
    };
  }, [sessionId]);

  const runSearch = (dir: 'next' | 'prev') => {
    const ctrl = terminalBus.get(sessionId);
    if (!ctrl || !term) return;
    const opts = { caseSensitive, regex };
    if (dir === 'next') ctrl.search.findNext(term, opts);
    else ctrl.search.findPrevious(term, opts);
  };

  useEffect(() => {
    const ctrl = terminalBus.get(sessionId);
    if (!ctrl) return;
    if (!term) {
      ctrl.search.clearDecorations();
      return;
    }
    ctrl.search.findNext(term, { caseSensitive, regex, incremental: true });
  }, [term, caseSensitive, regex, sessionId]);

  return (
    <div className="term-search-pill">
      <input
        ref={inputRef}
        placeholder="在终端中搜索…"
        value={term}
        onChange={(e) => setTerm(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') onClose();
          if (e.key === 'Enter') {
            e.preventDefault();
            runSearch(e.shiftKey ? 'prev' : 'next');
          }
        }}
      />
      <button
        type="button"
        className={`icon-btn ${caseSensitive ? 'active' : ''}`}
        style={{ height: 24, minWidth: 24, fontSize: 11 }}
        title="区分大小写"
        onClick={() => setCaseSensitive((v) => !v)}
      >
        Aa
      </button>
      <button
        type="button"
        className={`icon-btn ${regex ? 'active' : ''}`}
        style={{ height: 24, minWidth: 24, fontSize: 11 }}
        title="正则表达式"
        onClick={() => setRegex((v) => !v)}
      >
        .*
      </button>
      <button
        type="button"
        className="icon-btn"
        style={{ height: 24, minWidth: 24 }}
        title="上一个 (Shift+Enter)"
        onClick={() => runSearch('prev')}
      >
        <ChevronUp size={14} />
      </button>
      <button
        type="button"
        className="icon-btn"
        style={{ height: 24, minWidth: 24 }}
        title="下一个 (Enter)"
        onClick={() => runSearch('next')}
      >
        <ChevronDown size={14} />
      </button>
      <button
        type="button"
        className="icon-btn"
        style={{ height: 24, minWidth: 24 }}
        title="关闭 (Esc)"
        onClick={onClose}
      >
        <X size={13} />
      </button>
    </div>
  );
};
