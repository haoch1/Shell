import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  AlertCircle, ArrowUp, Check, ChevronRight, Copy, Loader2, Play,
  RotateCcw, Settings, Shield, Sparkles, Square, Terminal, X,
} from 'lucide-react';
import type { AgentStepEvent, AgentTurn, AIConfigView, PermissionMode } from '../types/shell';
import { copyToClipboard, errorMessage, getApi, onDesktopEvent } from '../lib/bridge';

interface AIDrawerProps {
  configVersion?: number;
  activeSessionId?: string;
  activeSessionName?: string;
  isLocalSession?: boolean;
  hasActiveSession: boolean;
  permissionMode?: PermissionMode;
  osHint?: string;
  initialExplainText?: string;
  onClearInitialExplain: () => void;
  onClose: () => void;
  onOpenAISettings: () => void;
  onOpenAgentSecurity: () => void;
  onInsertCommand: (cmd: string, execute: boolean) => void;
  onToast: (msg: string) => void;
}

interface ToolStep {
  id: string;
  step: number;
  tool: string;
  label: string;
  status: 'running' | 'done' | 'error';
  output?: string;
  error?: string;
  redactedCount?: number;
  expanded: boolean;
}

interface ChatMessage {
  id: string;
  role: 'user' | 'assistant';
  content: string;
  selectionLines?: number;
  steps?: ToolStep[];
  running?: boolean;
  error?: string;
  retryGoal?: string;
  retryHistory?: AgentTurn[];
}

const MAX_SELECTION_CHARS = 6000;

const SSH_QUICK_CHIPS = [
  '巡检当前服务器的 CPU、内存与磁盘占用',
  '查看所有监听端口及对应服务进程',
  '检查异常失败的 systemd 服务和最近报错日志',
  '查看运行中的 Docker 容器及资源占用',
];

const LOCAL_QUICK_CHIPS = [
  '查看当前占用 CPU 和内存最高的 5 个进程',
  '检查本机所有监听端口及对应程序',
  '查看各磁盘分区剩余空间与网络 IP 配置',
  '读取最近终端输出并排查异常报错',
];

const countLines = (s: string) => (s.trim() ? s.trim().split(/\r?\n/).length : 0);
const truncateSelection = (raw: string) => {
  const s = raw.trim();
  return s.length <= MAX_SELECTION_CHARS ? s : `${s.slice(0, MAX_SELECTION_CHARS)}\n…(已截断)`;
};

function formatToolStepLabel(tool: string, argsJson?: string): string {
  let args: Record<string, unknown> = {};
  if (argsJson) {
    try {
      const p = JSON.parse(argsJson);
      if (p && typeof p === 'object') args = p as Record<string, unknown>;
    } catch { /* ignore */ }
  }
  const str = (k: string) => (typeof args[k] === 'string' ? (args[k] as string).trim() : '');
  switch (tool) {
    case 'shell_exec': return str('command') ? `$ ${str('command').replace(/\s+/g, ' ')}` : '$ (执行命令)';
    case 'shell_sftp_read': return `读取 ${str('path') || '文件'}`;
    case 'shell_sftp_write': return `写入 ${str('path') || '文件'}`;
    case 'shell_sftp_list': return `浏览 ${str('path') || '.'}`;
    case 'shell_read_terminal': return '读取终端输出';
    case 'shell_send_terminal': return `发送到终端：${str('command').replace(/\s+/g, ' ') || '…'}`;
    case 'shell_server_stats': return '获取主机监控指标';
    case 'shell_list_hosts': return '查询已保存主机';
    case 'shell_list_sessions': return '查询活动会话';
    case 'shell_connect': return `连接 ${str('host') || '主机'}`;
    case 'shell_tunnels': return `端口转发 (${str('action') || 'list'})`;
    default: return tool;
  }
}

function formatToolStepBody(tool: string, output?: string, error?: string): string {
  if (error) return error;
  if (!output) return '(无输出)';
  if (tool === 'shell_exec') {
    try {
      const p = JSON.parse(output) as { stdout?: string; stderr?: string; exitCode?: number; durationMs?: number };
      if (p && typeof p === 'object' && ('stdout' in p || 'stderr' in p || 'exitCode' in p)) {
        const parts: string[] = [];
        const out = (p.stdout || '').replace(/\r?\n$/, '');
        const err = (p.stderr || '').replace(/\r?\n$/, '');
        if (out) parts.push(out);
        if (err) parts.push(`[stderr]\n${err}`);
        const code = typeof p.exitCode === 'number' ? p.exitCode : 0;
        const dur = typeof p.durationMs === 'number' ? ` · ${p.durationMs}ms` : '';
        if (code !== 0 || parts.length === 0) parts.push(`[exit ${code}${dur}]`);
        return parts.join('\n');
      }
    } catch {
      const unesc = (s: string) => s.replace(/\\n/g, '\n').replace(/\\r/g, '').replace(/\\t/g, '\t').replace(/\\"/g, '"').replace(/\\\\/g, '\\');
      const mOut = output.match(/"stdout"\s*:\s*"((?:\\.|[^"\\])*)/);
      const mErr = output.match(/"stderr"\s*:\s*"((?:\\.|[^"\\])*)/);
      const parts = [mOut?.[1] && unesc(mOut[1]).replace(/\r?\n$/, ''), mErr?.[1] && `[stderr]\n${unesc(mErr[1]).replace(/\r?\n$/, '')}`].filter(Boolean);
      if (parts.length > 0) return `${parts.join('\n')}\n…(已截断预览)`;
    }
  }
  return output;
}

function formatPermissionLabel(mode?: PermissionMode): string {
  if (mode === 'readonly') return '只读模式';
  if (mode === 'smart') return '智能确认';
  if (mode === 'ask_all') return '逐次确认';
  return '自动放行';
}

function renderLightMarkdown(
  text: string,
  hasActiveSession: boolean,
  onCopy: (code: string) => void,
  onRun: (code: string) => void,
): React.ReactNode {
  const nodes: React.ReactNode[] = [];
  const fenceRe = /```([a-zA-Z0-9_+-]*)\n?([\s\S]*?)```/g;
  let lastIdx = 0;
  let match: RegExpExecArray | null;
  let k = 0;

  const renderCode = (s: string) =>
    s.split(/(`[^`\n]+`)/g).map((tok, i) =>
      tok.startsWith('`') && tok.endsWith('`') && tok.length > 2 ? (
        <code key={i} className="ai-inline-code">{tok.slice(1, -1)}</code>
      ) : (
        <React.Fragment key={i}>{tok}</React.Fragment>
      ),
    );

  const renderInline = (s: string) =>
    s.split(/(\*\*[^*\n]+\*\*|`[^`\n]+`)/g).map((tok, i) => {
      if (tok.startsWith('`') && tok.endsWith('`') && tok.length > 2) {
        return <code key={i} className="ai-inline-code">{tok.slice(1, -1)}</code>;
      }
      if (tok.startsWith('**') && tok.endsWith('**') && tok.length > 4) {
        return <strong key={i}>{renderCode(tok.slice(2, -2))}</strong>;
      }
      return <React.Fragment key={i}>{tok}</React.Fragment>;
    });

  const pushProse = (prose: string) => {
    for (const block of prose.split(/\n{2,}/)) {
      const trimmed = block.trim();
      if (trimmed) nodes.push(<p key={`p-${k++}`} className="ai-md-p">{renderInline(trimmed)}</p>);
    }
  };

  while ((match = fenceRe.exec(text)) !== null) {
    if (match.index > lastIdx) pushProse(text.slice(lastIdx, match.index));
    const lang = match[1]?.trim() || 'shell';
    const code = match[2].replace(/\r?\n$/, '');
    nodes.push(
      <div key={`c-${k++}`} className="ai-code-block">
        <div className="ai-code-head">
          <span className="mono">{lang}</span>
          <div className="ai-code-actions">
            <button type="button" className="ai-code-btn" onClick={() => onCopy(code)}>
              <Copy size={11} />
              复制
            </button>
            {hasActiveSession && code.trim() && (
              <button type="button" className="ai-code-btn is-run" onClick={() => onRun(code.trim())}>
                <Play size={10.5} />
                运行
              </button>
            )}
          </div>
        </div>
        <pre className="ai-code-pre"><code>{code}</code></pre>
      </div>,
    );
    lastIdx = fenceRe.lastIndex;
  }
  if (lastIdx < text.length) pushProse(text.slice(lastIdx));
  return <div className="ai-md">{nodes}</div>;
}

export const AIDrawer: React.FC<AIDrawerProps> = ({
  configVersion = 0,
  activeSessionId,
  activeSessionName,
  isLocalSession = false,
  hasActiveSession,
  permissionMode,
  initialExplainText,
  onClearInitialExplain,
  onClose,
  onOpenAISettings,
  onOpenAgentSecurity,
  onInsertCommand,
  onToast,
}) => {
  const [config, setConfig] = useState<AIConfigView | null>(null);
  const [configLoaded, setConfigLoaded] = useState(false);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [prompt, setPrompt] = useState('');
  const [attachedSelection, setAttachedSelection] = useState('');
  const [running, setRunning] = useState(false);

  const messagesRef = useRef(messages);
  messagesRef.current = messages;
  const activeAssistantIdRef = useRef<string | null>(null);
  const lastHandledExplainRef = useRef('');
  const threadRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    let alive = true;
    void getApi().AIConfig()
      .then((c) => { if (alive) { setConfig(c); setConfigLoaded(true); } })
      .catch(() => { if (alive) setConfigLoaded(true); });
    return () => { alive = false; };
  }, [configVersion]);

  useEffect(() => onDesktopEvent<[AgentStepEvent]>('ai:agent_step', (ev) => {
    const targetId = activeAssistantIdRef.current;
    if (!targetId) return;
    setMessages((prev) => prev.map((msg) => {
      if (msg.id !== targetId) return msg;
      const steps = msg.steps ? [...msg.steps] : [];
      if (ev.kind === 'tool_call') {
        const tool = ev.tool || 'tool';
        steps.push({
          id: `${ev.step}-${steps.length}-${Date.now()}`,
          step: ev.step,
          tool,
          label: formatToolStepLabel(tool, ev.arguments),
          status: 'running',
          expanded: false,
        });
        return { ...msg, steps };
      }
      if (ev.kind === 'tool_result') {
        let idx = -1;
        for (let i = steps.length - 1; i >= 0; i--) {
          if (steps[i].status === 'running' && (!ev.tool || steps[i].tool === ev.tool)) {
            idx = i;
            break;
          }
        }
        if (idx === -1) {
          for (let i = steps.length - 1; i >= 0; i--) {
            if (steps[i].status === 'running') {
              idx = i;
              break;
            }
          }
        }
        if (idx >= 0) {
          const hasErr = Boolean(ev.error);
          steps[idx] = { ...steps[idx], status: hasErr ? 'error' : 'done', output: ev.output, error: ev.error, redactedCount: ev.redactedCount, expanded: hasErr };
        }
        return { ...msg, steps };
      }
      if (ev.kind === 'final' && ev.output) return { ...msg, content: ev.output };
      return msg;
    }));
  }), []);

  useEffect(() => {
    const el = threadRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages, running]);

  useEffect(() => {
    const ta = textareaRef.current;
    if (!ta) return;
    ta.style.height = 'auto';
    ta.style.height = `${Math.min(112, Math.max(36, ta.scrollHeight))}px`;
  }, [prompt]);

  const aiReady = Boolean(config?.ready || config?.model || (config?.mode === 'cli' && config?.cliCommand));
  const aiDisplayLabel = config?.mode === 'cli'
    ? `CLI · ${config.cliCommand || '未设置'}${config.cliModel ? ` (${config.cliModel})` : ''}`
    : config?.model || '未配置模型';

  const executeAgentTurn = useCallback(async (assistantId: string, goal: string, history: AgentTurn[]) => {
    activeAssistantIdRef.current = assistantId;
    setRunning(true);
    try {
      const finalText = await getApi().RunAIAgent(activeSessionId || '', goal, history);
      setMessages((prev) => prev.map((m) => m.id === assistantId ? {
        ...m,
        running: false,
        error: undefined,
        content: finalText || m.content || '已完成执行。',
        steps: m.steps?.map((s) => (s.status === 'running' ? { ...s, status: 'done' } : s)),
      } : m));
    } catch (e) {
      const errText = errorMessage(e);
      setMessages((prev) => prev.map((m) => m.id === assistantId ? {
        ...m,
        running: false,
        error: errText,
        steps: m.steps?.map((s) => (s.status === 'running' ? { ...s, status: 'error', expanded: true } : s)),
      } : m));
    } finally {
      if (activeAssistantIdRef.current === assistantId) activeAssistantIdRef.current = null;
      setRunning(false);
    }
  }, [activeSessionId]);

  const sendMessage = useCallback(async (rawText: string, rawSelection?: string) => {
    if (running) return;
    const selection = truncateSelection(rawSelection ?? attachedSelection);
    const userText = rawText.trim();
    if (!userText && !selection) return;
    if (config && !aiReady) {
      if (selection) setAttachedSelection(selection);
      onOpenAISettings();
      return;
    }

    const displayText = userText || '分析并排查这段终端输出的问题';
    const goal = selection ? `${displayText}\n\n终端选区内容：\n\`\`\`\n${selection}\n\`\`\`` : displayText;
    const history: AgentTurn[] = messagesRef.current
      .filter((m) => !m.running && !m.error && m.content.trim() !== '')
      .slice(-6)
      .map((m) => ({ role: m.role, content: m.content }));

    const now = Date.now();
    const assistantId = `a-${now + 1}`;
    setMessages((prev) => [
      ...prev,
      { id: `u-${now}`, role: 'user', content: displayText, selectionLines: selection ? countLines(selection) : undefined },
      { id: assistantId, role: 'assistant', content: '', steps: [], running: true, retryGoal: goal, retryHistory: history },
    ]);
    setPrompt('');
    setAttachedSelection('');
    await executeAgentTurn(assistantId, goal, history);
  }, [aiReady, attachedSelection, config, executeAgentTurn, onOpenAISettings, running]);

  useEffect(() => {
    if (!initialExplainText) {
      lastHandledExplainRef.current = '';
      return;
    }
    if (!configLoaded || lastHandledExplainRef.current === initialExplainText) return;
    lastHandledExplainRef.current = initialExplainText;
    const truncated = truncateSelection(initialExplainText);
    onClearInitialExplain();
    if (aiReady && !running) void sendMessage('分析并排查这段终端输出的问题', truncated);
    else setAttachedSelection(truncated);
  }, [aiReady, configLoaded, initialExplainText, onClearInitialExplain, running, sendMessage]);

  const toggleStep = (msgId: string, stepId: string) => {
    setMessages((prev) => prev.map((m) => m.id === msgId
      ? { ...m, steps: m.steps?.map((s) => (s.id === stepId ? { ...s, expanded: !s.expanded } : s)) }
      : m));
  };

  const targetText = hasActiveSession
    ? (isLocalSession ? `当前目标 · ${activeSessionName || '本机终端'} · 本机` : `当前目标 · ${activeSessionName || 'SSH 会话'}`)
    : '未连接会话 · 可直接提问或指定已保存主机';
  const quickChips = isLocalSession ? LOCAL_QUICK_CHIPS : SSH_QUICK_CHIPS;
  const attachedLines = countLines(attachedSelection);
  const canSend = Boolean(prompt.trim() || attachedSelection.trim());

  return (
    <section id="aiDrawer" className="right-drawer ai-drawer" aria-label="AI 助手">
      <div className="drawer-header">
        <div className="drawer-title">
          <Sparkles size={15} strokeWidth={1.8} style={{ color: 'var(--purple)' }} />
          <span>AI 助手</span>
        </div>
        <div style={{ display: 'flex', gap: 2 }}>
          <button
            type="button"
            className="icon-btn"
            title="新建对话"
            disabled={messages.length === 0 || running}
            onClick={() => { setMessages([]); setAttachedSelection(''); }}
          >
            <RotateCcw size={14} />
          </button>
          <button type="button" className="icon-btn" title="安全与 MCP 设置" onClick={onOpenAgentSecurity}>
            <Shield size={14} />
          </button>
          <button id="aiSettingsBtn" type="button" className="icon-btn" title="配置 AI 模型" onClick={onOpenAISettings}>
            <Settings size={14} />
          </button>
          <button type="button" className="icon-btn" title="关闭" onClick={onClose}>
            <X size={14} />
          </button>
        </div>
      </div>

      <div className="ai-target-bar" title={targetText}>
        <span className={`ai-target-dot ${hasActiveSession ? 'is-live' : ''}`} />
        <span className="ai-target-text">{targetText}</span>
      </div>

      {config && !aiReady && (
        <div className="ai-setup-banner">
          <div className="soft-card" style={{ padding: '9px 11px', flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 12, color: 'var(--ink-secondary)' }}>先配置 API 或本地 CLI 即可使用</span>
            <button type="button" className="btn btn-primary btn-sm" onClick={onOpenAISettings}>去配置</button>
          </div>
        </div>
      )}

      <div ref={threadRef} className="ai-thread">
        {messages.length === 0 ? (
          <div className="ai-empty-state">
            <p className="ai-empty-hint">发送需求即自动在当前终端排查与执行，危险操作会先征得确认。</p>
            <div className="ai-quick-chips">
              {quickChips.map((chip) => (
                <button key={chip} type="button" className="ai-quick-chip" disabled={running} onClick={() => void sendMessage(chip)}>
                  {chip}
                </button>
              ))}
            </div>
          </div>
        ) : (
          messages.map((msg) => msg.role === 'user' ? (
            <div key={msg.id} className="ai-msg-user">
              {msg.selectionLines ? (
                <span className="ai-msg-sel-tag"><Terminal size={11} />终端选区 · {msg.selectionLines} 行</span>
              ) : null}
              <div className="ai-msg-user-bubble">{msg.content}</div>
            </div>
          ) : (
            <div key={msg.id} className="ai-msg-assistant">
              {msg.steps && msg.steps.length > 0 && (
                <div className="ai-steps-list">
                  {msg.steps.map((st) => (
                    <div key={st.id} className={`ai-step-item ${st.status === 'error' ? 'is-error' : ''}`}>
                      <button type="button" className="ai-step-row" title={st.label} onClick={() => toggleStep(msg.id, st.id)}>
                        <span className={`ai-step-icon is-${st.status}`}>
                          {st.status === 'running' ? <Loader2 size={12} className="spin" /> : st.status === 'error' ? <AlertCircle size={12} /> : <Check size={12} />}
                        </span>
                        <span className="ai-step-label mono">{st.label}</span>
                        {(st.redactedCount ?? 0) > 0 && <span className="ai-step-badge">脱敏 {st.redactedCount}</span>}
                        <ChevronRight size={12} className={`ai-step-chevron ${st.expanded ? 'is-open' : ''}`} />
                      </button>
                      {st.expanded && st.status !== 'running' && (
                        <pre className={`ai-step-body mono ${st.error ? 'is-error' : ''}`}>{formatToolStepBody(st.tool, st.output, st.error)}</pre>
                      )}
                    </div>
                  ))}
                </div>
              )}

              {msg.content && renderLightMarkdown(
                msg.content,
                hasActiveSession,
                (code) => { void copyToClipboard(code); onToast('已复制代码'); },
                (code) => onInsertCommand(code, true),
              )}

              {msg.running && (
                <div className="ai-running-row"><Loader2 size={12.5} className="spin" /><span>思考与执行中…</span></div>
              )}

              {msg.error && (
                <div className="ai-error-row">
                  <span>{msg.error}</span>
                  <button
                    type="button"
                    className="btn btn-sm btn-ghost"
                    disabled={running}
                    onClick={() => {
                      if (running || !msg.retryGoal) return;
                      setMessages((prev) => prev.map((m) => m.id === msg.id ? { ...m, running: true, error: undefined, content: '', steps: [] } : m));
                      void executeAgentTurn(msg.id, msg.retryGoal, msg.retryHistory || []);
                    }}
                  >
                    重试
                  </button>
                </div>
              )}
            </div>
          ))
        )}
      </div>

      <div className="ai-composer">
        {attachedLines > 0 && (
          <div className="ai-attach-chip">
            <Terminal size={11.5} />
            <span>已附终端选区 · {attachedLines} 行</span>
            <button type="button" className="ai-attach-clear" aria-label="移除终端选区" onClick={() => setAttachedSelection('')}>
              <X size={11} />
            </button>
          </div>
        )}
        <textarea
          ref={textareaRef}
          id="aiPromptInput"
          className="ai-composer-input"
          rows={1}
          placeholder="输入需求按 Enter 直接执行（Shift+Enter 换行）"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing && e.keyCode !== 229) {
              e.preventDefault();
              if (!running) void sendMessage(prompt);
            }
          }}
        />
        <div className="ai-composer-bar">
          <div className="ai-meta-pills">
            <button type="button" className="ai-meta-pill mono" title={`当前模型：${aiDisplayLabel}（点击切换）`} onClick={onOpenAISettings}>
              {aiDisplayLabel}
            </button>
            <button type="button" className="ai-meta-pill" title="安全执行策略（点击设置）" onClick={onOpenAgentSecurity}>
              {formatPermissionLabel(permissionMode)}
            </button>
          </div>
          <button
            id="aiSubmitBtn"
            type="button"
            className={`btn btn-sm ai-send-btn ${running ? 'btn-danger-solid' : 'btn-primary'}`}
            disabled={!running && !canSend}
            title={running ? '停止执行' : '发送并执行 (Enter)'}
            aria-label={running ? '停止执行' : '发送并执行'}
            onClick={() => (running ? void getApi().CancelAI().catch(() => {}) : void sendMessage(prompt))}
          >
            {running ? (<><Square size={10.5} fill="currentColor" />停止</>) : <ArrowUp size={14} strokeWidth={2.2} />}
          </button>
        </div>
      </div>
    </section>
  );
};
