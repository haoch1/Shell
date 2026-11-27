import React, { useEffect, useState } from 'react';
import {
  AlertTriangle,
  BookmarkPlus,
  Bot,
  CheckCircle2,
  Copy,
  CornerDownLeft,
  Play,
  Send,
  Settings,
  Shield,
  ShieldAlert,
  Sparkles,
  Square,
  Wrench,
  X,
} from 'lucide-react';
import type { AgentStepEvent, AIConfigView, AIResult } from '../types/shell';
import { copyToClipboard, errorMessage, getApi, onDesktopEvent } from '../lib/bridge';

interface AIDrawerProps {
  configVersion?: number;
  activeSessionId?: string;
  hasActiveSession: boolean;
  osHint?: string;
  initialExplainText?: string;
  onClearInitialExplain: () => void;
  onClose: () => void;
  onOpenAISettings: () => void;
  onOpenAgentSecurity: () => void;
  onInsertCommand: (cmd: string, execute: boolean) => void;
  onSaveAsSnippet: (cmd: string, desc: string) => void;
  onToast: (msg: string) => void;
}

const QUICK_PROMPTS = [
  '查看占用 8080 端口的进程',
  '查找 /var/log 下超过 100MB 的文件',
  '查看最近 50 条 SSH 登录失败记录',
  '统计当前目录各子文件夹体积并排序',
];

const AGENT_PROMPTS = [
  '巡检当前服务器的 CPU、内存、磁盘占用及异常 systemd 服务',
  '排查 Docker 运行状态与最近报错日志，给出修复建议',
  '检查监听端口与 Nginx 配置是否存在语法错误',
];

interface HistoryItem {
  id: string;
  mode: 'command' | 'explain';
  prompt: string;
  result?: AIResult;
  explanationText?: string;
}

export const AIDrawer: React.FC<AIDrawerProps> = ({
  configVersion = 0,
  activeSessionId,
  hasActiveSession,
  osHint,
  initialExplainText,
  onClearInitialExplain,
  onClose,
  onOpenAISettings,
  onOpenAgentSecurity,
  onInsertCommand,
  onSaveAsSnippet,
  onToast,
}) => {
  const [mode, setMode] = useState<'command' | 'explain' | 'agent'>('command');
  const [config, setConfig] = useState<AIConfigView | null>(null);
  const [prompt, setPrompt] = useState('');
  const [explainContext, setExplainContext] = useState('');
  const [running, setRunning] = useState(false);
  const [streamText, setStreamText] = useState('');
  const [agentSteps, setAgentSteps] = useState<AgentStepEvent[]>([]);
  const [error, setError] = useState('');
  const [history, setHistory] = useState<HistoryItem[]>([]);

  useEffect(() => {
    void getApi()
      .AIConfig()
      .then(setConfig)
      .catch(() => {});
  }, [configVersion]);

  useEffect(() => {
    if (initialExplainText) {
      setMode('explain');
      setExplainContext(initialExplainText);
      onClearInitialExplain();
    }
  }, [initialExplainText, onClearInitialExplain]);

  useEffect(() => {
    const off1 = onDesktopEvent<[string]>('ai:delta', (delta) => {
      setStreamText((prev) => prev + delta);
    });
    const off2 = onDesktopEvent<[AgentStepEvent]>('ai:agent_step', (ev) => {
      setAgentSteps((prev) => [...prev, ev]);
    });
    return () => {
      off1();
      off2();
    };
  }, []);

  const aiReady = Boolean(config?.ready || config?.model || (config?.mode === 'cli' && config?.cliCommand));
  const aiDisplayLabel =
    config?.mode === 'cli'
      ? `CLI · ${config.cliCommand || '未设置'}${config.cliModel ? ` (${config.cliModel})` : ''}`
      : config?.model || '未配置模型';

  const run = async (overridePrompt?: string) => {
    const q = (overridePrompt ?? prompt).trim();
    if (running) {
      await getApi().CancelAI().catch(() => {});
      return;
    }
    if ((mode === 'command' || mode === 'agent') && !q) return;
    if (mode === 'explain' && !q && !explainContext.trim()) return;
    if (config && !aiReady) {
      onOpenAISettings();
      return;
    }
    setRunning(true);
    setError('');
    setStreamText('');
    if (mode === 'agent') setAgentSteps([]);
    try {
      if (mode === 'command') {
        const res = await getApi().SuggestCommand(q, osHint || '');
        setHistory((prev) => [{ id: String(Date.now()), mode: 'command', prompt: q, result: res }, ...prev.slice(0, 14)]);
        setPrompt('');
      } else if (mode === 'explain') {
        const text = await getApi().ExplainTerminal(explainContext, q);
        setHistory((prev) => [
          {
            id: String(Date.now()),
            mode: 'explain',
            prompt: q || '分析选中的终端输出',
            explanationText: text,
          },
          ...prev.slice(0, 14),
        ]);
        setPrompt('');
      } else {
        await getApi().RunAIAgent(activeSessionId || '', q);
        setPrompt('');
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setRunning(false);
      setStreamText('');
    }
  };

  const riskBadge = (risk?: string) => {
    if (risk === 'danger') {
      return (
        <span className="pill-badge danger">
          <ShieldAlert size={11} />
          高危
        </span>
      );
    }
    if (risk === 'caution') {
      return (
        <span className="pill-badge caution">
          <AlertTriangle size={11} />
          需确认
        </span>
      );
    }
    return (
      <span className="pill-badge safe">
        <CheckCircle2 size={11} />
        安全
      </span>
    );
  };

  return (
    <section id="aiDrawer" className="right-drawer" aria-label="AI 运维助手">
      <div className="drawer-header">
        <div className="drawer-title">
          <Sparkles size={15} strokeWidth={1.8} style={{ color: 'var(--purple)' }} />
          <span>AI 助手</span>
        </div>
        <div style={{ display: 'flex', gap: 2 }}>
          <button
            type="button"
            className="icon-btn"
            title="安全与 MCP 设置"
            onClick={onOpenAgentSecurity}
          >
            <Shield size={14} />
          </button>
          <button
            id="aiSettingsBtn"
            type="button"
            className="icon-btn"
            title="配置模型 API"
            onClick={onOpenAISettings}
          >
            <Settings size={14} />
          </button>
          <button type="button" className="icon-btn" title="关闭" onClick={onClose}>
            <X size={14} />
          </button>
        </div>
      </div>

      <div className="sftp-toolbar">
        <div className="segmented" style={{ width: '100%' }}>
          <button type="button" className={mode === 'command' ? 'active' : ''} onClick={() => setMode('command')}>
            写命令
          </button>
          <button type="button" className={mode === 'explain' ? 'active' : ''} onClick={() => setMode('explain')}>
            查报错
          </button>
          <button type="button" className={mode === 'agent' ? 'active' : ''} onClick={() => setMode('agent')}>
            自动运维
          </button>
        </div>

        {config && !aiReady && (
          <div
            className="soft-card"
            style={{ padding: '10px 12px', flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}
          >
            <span style={{ fontSize: 12, color: 'var(--ink-secondary)' }}>先配置 API 或本地 CLI 即可使用</span>
            <button type="button" className="btn btn-primary btn-sm" onClick={onOpenAISettings}>
              去配置
            </button>
          </div>
        )}

        {mode === 'explain' && (
          <div className="field-group">
            <label className="field-label">日志片段</label>
            <textarea
              className="textarea mono"
              rows={3}
              style={{ fontSize: 11.5, resize: 'none' }}
              placeholder="粘贴终端报错或选中输出…"
              value={explainContext}
              onChange={(e) => setExplainContext(e.target.value)}
            />
          </div>
        )}

        <div className="field-group">
          <textarea
            id="aiPromptInput"
            className="textarea"
            rows={2}
            style={{ resize: 'none' }}
            placeholder={
              mode === 'command'
                ? '告诉 AI 你想做什么，例如：查看 8080 端口占用'
                : mode === 'explain'
                  ? '补充问题（可选）'
                  : '告诉 AI 帮你排查或执行什么任务'
            }
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={(e) => {
              if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                e.preventDefault();
                void run();
              }
            }}
          />
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <span className="mono" style={{ fontSize: 11, color: 'var(--ink-tertiary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {aiDisplayLabel}
            </span>
            <button
              id="aiSubmitBtn"
              type="button"
              className={`btn btn-sm ${running ? 'btn-danger' : 'btn-primary'}`}
              onClick={() => void run()}
            >
              {running ? (
                <>
                  <Square size={11} />
                  停止
                </>
              ) : (
                <>
                  {mode === 'agent' ? <Bot size={12} /> : <Send size={12} />}
                  {mode === 'agent' ? '执行任务' : '生成'}
                </>
              )}
            </button>
          </div>
        </div>
        {error && <p className="error-text">{error}</p>}
      </div>

      <div className="drawer-body">
        <div className="card-list">
          {mode === 'agent' ? (
            <>
              {agentSteps.length === 0 && !running && (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6, paddingTop: 4 }}>
                  <div style={{ fontSize: 11.5, color: 'var(--ink-tertiary)', paddingLeft: 2 }}>
                    示例
                  </div>
                  {AGENT_PROMPTS.map((q) => (
                    <button
                      key={q}
                      type="button"
                      className="soft-card"
                      style={{ textAlign: 'left', cursor: 'pointer', padding: '8px 11px', fontSize: 12 }}
                      onClick={() => {
                        setPrompt(q);
                        if (aiReady && hasActiveSession) void run(q);
                      }}
                    >
                      {q}
                    </button>
                  ))}
                </div>
              )}

              {agentSteps.map((st, idx) => (
                <div key={idx} className="soft-card">
                  {st.kind === 'tool_call' && (
                    <>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, fontWeight: 600 }}>
                        <Wrench size={12} style={{ color: 'var(--accent)' }} />
                        <span>步骤 {st.step} · 调用 {st.tool}</span>
                      </div>
                      {st.arguments && <div className="code-block" style={{ fontSize: 11 }}>{st.arguments}</div>}
                    </>
                  )}
                  {st.kind === 'tool_result' && (
                    <>
                      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6 }}>
                        <span style={{ fontSize: 11.5, fontWeight: 500, color: 'var(--ink-secondary)' }}>
                          {st.tool} 返回
                        </span>
                        <div style={{ display: 'flex', gap: 4 }}>
                          {st.risk && riskBadge(st.risk)}
                          {(st.redactedCount ?? 0) > 0 && (
                            <span className="pill-badge accent">脱敏 {st.redactedCount} 处</span>
                          )}
                        </div>
                      </div>
                      {st.error ? (
                        <p className="error-text">{st.error}</p>
                      ) : (
                        st.output && (
                          <div className="code-block" style={{ maxHeight: 150, overflowY: 'auto', fontSize: 11 }}>
                            {st.output}
                          </div>
                        )
                      )}
                    </>
                  )}
                  {(st.kind === 'thought' || st.kind === 'final') && (
                    <div
                      style={{
                        fontSize: 12.5,
                        color: 'var(--ink-primary)',
                        whiteSpace: 'pre-wrap',
                        lineHeight: 1.55,
                        userSelect: 'text',
                      }}
                    >
                      {st.kind === 'final' && (
                        <div style={{ fontSize: 11.5, fontWeight: 600, color: 'var(--green)', marginBottom: 4 }}>
                          执行总结
                        </div>
                      )}
                      {st.output}
                    </div>
                  )}
                </div>
              ))}
              {running && (
                <div className="soft-card">
                  <span style={{ fontSize: 12, color: 'var(--purple)', fontWeight: 500 }}>Agent 正在分析与执行…</span>
                </div>
              )}
            </>
          ) : (
            <>
              {running && (
                <div className="soft-card">
                  <div style={{ fontSize: 12, color: 'var(--purple)', fontWeight: 500 }}>正在思考…</div>
                  {streamText && (
                    <div className="code-block" style={{ fontSize: 11.5, whiteSpace: 'pre-wrap' }}>
                      {streamText}
                    </div>
                  )}
                </div>
              )}

              {history.length === 0 && !running && mode === 'command' && (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6, paddingTop: 4 }}>
                  <div style={{ fontSize: 11.5, color: 'var(--ink-tertiary)', paddingLeft: 2 }}>快捷示例</div>
                  {QUICK_PROMPTS.map((q) => (
                    <button
                      key={q}
                      type="button"
                      className="soft-card"
                      style={{ textAlign: 'left', cursor: 'pointer', padding: '8px 11px', fontSize: 12 }}
                      onClick={() => {
                        setPrompt(q);
                        if (aiReady) void run(q);
                      }}
                    >
                      {q}
                    </button>
                  ))}
                </div>
              )}

              {history.map((item) => (
                <div key={item.id} className="soft-card">
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                    <span style={{ fontSize: 12, color: 'var(--ink-secondary)', fontWeight: 500 }}>
                      {item.prompt}
                    </span>
                    {item.result && riskBadge(item.result.risk)}
                  </div>

                  {item.result ? (
                    <>
                      <div className="code-block">{item.result.command}</div>
                      {item.result.explanation && (
                        <div style={{ fontSize: 12, color: 'var(--ink-secondary)', lineHeight: 1.5 }}>
                          {item.result.explanation}
                        </div>
                      )}
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 5 }}>
                        <button
                          type="button"
                          className="btn btn-sm"
                          disabled={!hasActiveSession}
                          onClick={() => onInsertCommand(item.result!.command, false)}
                        >
                          <CornerDownLeft size={12} />
                          插入
                        </button>
                        <button
                          type="button"
                          className={`btn btn-sm ${item.result.risk === 'danger' ? 'btn-danger' : 'btn-primary'}`}
                          disabled={!hasActiveSession}
                          onClick={() => onInsertCommand(item.result!.command, true)}
                        >
                          <Play size={11} />
                          执行
                        </button>
                        <button
                          type="button"
                          className="btn btn-sm btn-ghost"
                          onClick={() => {
                            void copyToClipboard(item.result!.command);
                            onToast('已复制命令');
                          }}
                        >
                          <Copy size={12} />
                          复制
                        </button>
                        <button
                          type="button"
                          className="btn btn-sm btn-ghost"
                          onClick={() => onSaveAsSnippet(item.result!.command, item.prompt)}
                        >
                          <BookmarkPlus size={12} />
                          存为片段
                        </button>
                      </div>
                    </>
                  ) : (
                    <div
                      style={{
                        fontSize: 12.5,
                        color: 'var(--ink-primary)',
                        whiteSpace: 'pre-wrap',
                        lineHeight: 1.55,
                        userSelect: 'text',
                      }}
                    >
                      {item.explanationText}
                    </div>
                  )}
                </div>
              ))}
            </>
          )}
        </div>
      </div>
    </section>
  );
};
