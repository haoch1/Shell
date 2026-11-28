import React, { useEffect, useState } from 'react';
import {
  ChevronRight,
  Copy,
  RefreshCw,
} from 'lucide-react';
import type {
  AgentConfig,
  AgentStatusView,
  AuditEntry,
  PermissionMode,
  PolicyEvaluation,
  RedactResult,
} from '../types/shell';
import { copyToClipboard, errorMessage, getApi } from '../lib/bridge';

interface AgentSecurityTabProps {
  onStatusChanged: (st: AgentStatusView) => void;
  onToast: (msg: string) => void;
}

const DEFAULT_SAMPLE = `DATABASE_URL=postgres://admin:S3cr3tPass!@10.0.1.25:5432/prod
OPENAI_API_KEY=sk-proj-abc1234567890XYZ9876543210
Contact: ops@company.com / 13812345678`;

const DECISION_LABEL: Record<string, string> = {
  'auto-allow': '已放行',
  'user-allow-once': '已确认',
  'user-allow-session': '会话放行',
  'policy-deny': '策略拦截',
  'user-deny': '已拒绝',
};

export const AgentSecurityTab: React.FC<AgentSecurityTabProps> = ({ onStatusChanged, onToast }) => {
  const [sub, setSub] = useState<'mcp' | 'guard' | 'audit'>('mcp');
  const [mcpClient, setMcpClient] = useState<'json' | 'claude' | 'codex' | 'antigravity'>('json');
  const [status, setStatus] = useState<AgentStatusView | null>(null);
  const [portInput, setPortInput] = useState('17822');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  // Advanced rules state (tucked cleanly inside collapsible fold)
  const [allowText, setAllowText] = useState('');
  const [denyText, setDenyText] = useState('');
  const [deniedPathsText, setDeniedPathsText] = useState('');
  const [customRegexText, setCustomRegexText] = useState('');
  const [allowRegexText, setAllowRegexText] = useState('');

  // Testers
  const [policyCmd, setPolicyCmd] = useState('ls -la && rm -rf /tmp/test');
  const [policyEval, setPolicyEval] = useState<PolicyEvaluation | null>(null);
  const [redactSample, setRedactSample] = useState(DEFAULT_SAMPLE);
  const [redactOut, setRedactOut] = useState<RedactResult | null>(null);

  // Audit logs
  const [logs, setLogs] = useState<AuditEntry[]>([]);

  const load = async () => {
    setErr('');
    try {
      const st = await getApi().GetAgentStatus();
      setStatus(st);
      setPortInput(String(st.port || 17822));
      setAllowText((st.policy.allowPatterns ?? []).join('\n'));
      setDenyText((st.policy.denyPatterns ?? []).join('\n'));
      setDeniedPathsText((st.policy.deniedPaths ?? []).join('\n'));
      setCustomRegexText((st.redaction.customPatterns ?? []).join('\n'));
      setAllowRegexText((st.redaction.allowlist ?? []).join('\n'));
      onStatusChanged(st);
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const rotateToken = async () => {
    setBusy(true);
    setErr('');
    try {
      await getApi().RotateAgentToken();
      await load();
      onToast('已重置访问令牌');
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const loadAudit = async () => {
    const items = await getApi().AuditLogs(100).catch(() => []);
    setLogs(items || []);
  };

  useEffect(() => {
    void load();
    void loadAudit();
  }, []);

  const saveConfig = async (next: AgentConfig, notice?: string) => {
    setBusy(true);
    setErr('');
    try {
      const updated = await getApi().SaveAgentConfig(next);
      setStatus(updated);
      onStatusChanged(updated);
      if (notice) onToast(notice);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  if (!status) {
    if (err) {
      return (
        <div className="pref-group-pad">
          <p className="error-text" role="alert">{err}</p>
          <div className="pref-row-actions">
            <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void load()}>重试</button>
            {err.includes('请重置访问令牌') && (
              <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void rotateToken()}>重置访问令牌</button>
            )}
          </div>
        </div>
      );
    }
    return <div style={{ color: 'var(--ink-tertiary)', padding: 20 }}>加载中…</div>;
  }

  const splitLines = (s: string) =>
    s
      .split(/\r?\n/)
      .map((l) => l.trim())
      .filter(Boolean);

  const mcpHttpConfig = JSON.stringify(
    {
      mcpServers: {
        shell: {
          url: `http://127.0.0.1:${status.port}/mcp`,
          headers: {
            Authorization: `Bearer ${status.token}`,
          },
        },
      },
    },
    null,
    2,
  );

  const mcpStdioConfig = JSON.stringify(
    {
      mcpServers: {
        shell: {
          command: status.exePath || 'Shell.exe',
          args: ['--mcp'],
        },
      },
    },
    null,
    2,
  );

  const exeQuoted = `"${status.exePath || 'Shell.exe'}"`;
  const claudeCmd = `claude mcp add --transport http shell http://127.0.0.1:${status.port}/mcp --header "Authorization: Bearer ${status.token}"`;
  const codexCmd = `codex mcp add shell -- ${exeQuoted} --mcp`;
  const agyCmd = `agy mcp add --type http --header "Authorization: Bearer ${status.token}" shell http://127.0.0.1:${status.port}/mcp`;

  const activeSnippet =
    mcpClient === 'claude'
      ? claudeCmd
      : mcpClient === 'codex'
        ? codexCmd
        : mcpClient === 'antigravity'
          ? agyCmd
          : mcpHttpConfig;

  const runPolicyTest = async () => {
    const res = await getApi().TestCommandPolicy(
      policyCmd,
      {
        ...status.policy,
        allowPatterns: splitLines(allowText),
        denyPatterns: splitLines(denyText),
        deniedPaths: splitLines(deniedPathsText),
      },
      '',
    );
    setPolicyEval(res);
  };

  const runRedactTest = async () => {
    const res = await getApi().TestRedaction(redactSample, {
      ...status.redaction,
      customPatterns: splitLines(customRegexText),
      allowlist: splitLines(allowRegexText),
    });
    setRedactOut(res);
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <div className="segmented" style={{ width: '100%' }}>
        <button type="button" className={sub === 'mcp' ? 'active' : ''} onClick={() => setSub('mcp')}>
          外部接入 (MCP)
        </button>
        <button type="button" className={sub === 'guard' ? 'active' : ''} onClick={() => setSub('guard')}>
          安全与脱敏
        </button>
        <button
          type="button"
          className={sub === 'audit' ? 'active' : ''}
          onClick={() => {
            setSub('audit');
            void loadAudit();
          }}
        >
          操作记录
        </button>
      </div>

      {err && <p className="error-text">{err}</p>}

      {sub === 'mcp' && (
        <>
          <div className="pref-group-pad">
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
              <div style={{ fontWeight: 600, display: 'flex', alignItems: 'center', gap: 7 }}>
                <span>允许外部 AI 工具连接 Shell</span>
                {status.running && <span className="pill-badge safe mono">:{status.port}</span>}
                {status.enabled && !status.running && (
                  <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void saveConfig(status, 'MCP 服务已开启')}>重新启动</button>
                )}
              </div>
              <button
                id="mcpEnableSwitch"
                type="button"
                role="switch"
                aria-checked={status.enabled}
                disabled={busy}
                className="apple-switch accent"
                onClick={() =>
                  void saveConfig(
                    {
                      enabled: !status.enabled,
                      port: Number(portInput) || 17822,
                      execMode: status.execMode || 'silent',
                      policy: status.policy,
                      redaction: status.redaction,
                    },
                    !status.enabled ? 'MCP 服务已开启' : 'MCP 服务已关闭',
                  )
                }
              />
            </div>

            <div className="pref-row" style={{ padding: '6px 0 2px', minHeight: 36, borderBottom: 'none' }}>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div className="pref-row-label" style={{ fontSize: 12.5 }}>MCP 执行方式</div>
                <div className="pref-row-sub">
                  {(status.execMode || 'silent') === 'visible'
                    ? '自动唤起并切到对应主机终端，实时回显 Agent 执行过程'
                    : '后台静默直连执行，托盘驻留期间无需弹窗即可自动响应'}
                </div>
              </div>
              <div className="segmented" style={{ flexShrink: 0 }}>
                {(
                  [
                    { id: 'silent', label: '后台静默' },
                    { id: 'visible', label: '前台终端可视' },
                  ] as const
                ).map((m) => (
                  <button
                    key={m.id}
                    type="button"
                    className={(status.execMode || 'silent') === m.id ? 'active' : ''}
                    onClick={() =>
                      void saveConfig(
                        {
                          enabled: status.enabled,
                          port: Number(portInput) || status.port || 17822,
                          execMode: m.id,
                          policy: status.policy,
                          redaction: status.redaction,
                        },
                        m.id === 'visible' ? '已切换为前台终端可视执行' : '已切换为后台静默执行',
                      )
                    }
                  >
                    {m.label}
                  </button>
                ))}
              </div>
            </div>

            <div className="field-row" style={{ marginTop: 2 }}>
              <div className="field-group" style={{ width: 108 }}>
                <label className="field-label">端口</label>
                <input
                  className="input input-sm mono"
                  type="number"
                  min={1024}
                  max={65535}
                  value={portInput}
                  onChange={(e) => setPortInput(e.target.value)}
                  onBlur={() => {
                    const p = Number(portInput);
                    if (p >= 1024 && p <= 65535 && p !== status.port) {
                      void saveConfig({ ...status, port: p }, `端口已更新为 ${p}`);
                    }
                  }}
                />
              </div>

              <div className="field-group grow">
                <label className="field-label" htmlFor="mcpTokenInput">访问令牌</label>
                <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                  <input
                    id="mcpTokenInput"
                    className="input input-sm mono"
                    readOnly
                    type="text"
                    value={status.token}
                  />
                  <button
                    type="button"
                    className="btn btn-sm btn-icon-sm"
                    title="复制令牌"
                    aria-label="复制令牌"
                    onClick={() => {
                      void copyToClipboard(status.token);
                      onToast('已复制令牌');
                    }}
                  >
                    <Copy size={13} />
                  </button>
                  <button
                    type="button"
                    className="btn btn-sm btn-icon-sm"
                    title="重置令牌"
                    aria-label="重置令牌"
                    disabled={busy}
                    onClick={() => void rotateToken()}
                  >
                    <RefreshCw size={13} />
                  </button>
                </div>
              </div>
            </div>
          </div>

          <div className="pref-group-pad">
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
              <div style={{ fontWeight: 600, fontSize: 13 }}>客户端接入</div>
              <div className="pref-row-actions">
                <button
                  type="button"
                  className="btn btn-primary btn-sm btn-pref-action"
                  onClick={() => {
                    void copyToClipboard(activeSnippet);
                    onToast(mcpClient === 'json' ? '已复制 HTTP 配置' : '已复制 CLI 接入命令');
                  }}
                >
                  {mcpClient === 'json' ? '复制 HTTP 配置' : '复制命令'}
                </button>
                {mcpClient === 'json' && (
                  <button
                    type="button"
                    className="btn btn-sm btn-pref-action"
                    onClick={() => {
                      void copyToClipboard(mcpStdioConfig);
                      onToast('已复制 Stdio 配置');
                    }}
                  >
                    复制 Stdio
                  </button>
                )}
              </div>
            </div>
            <div className="segmented" style={{ width: '100%' }}>
              {(
                [
                  { id: 'json', label: 'JSON 配置' },
                  { id: 'claude', label: 'Claude Code' },
                  { id: 'codex', label: 'Codex' },
                  { id: 'antigravity', label: 'Antigravity' },
                ] as const
              ).map((c) => (
                <button
                  key={c.id}
                  type="button"
                  className={mcpClient === c.id ? 'active' : ''}
                  onClick={() => setMcpClient(c.id)}
                >
                  {c.label}
                </button>
              ))}
            </div>
            <div className="code-block" style={{ fontSize: 11.5, whiteSpace: mcpClient === 'json' ? 'pre' : 'pre-wrap', overflowX: 'auto' }}>
              {activeSnippet}
            </div>
          </div>
        </>
      )}

      {sub === 'guard' && (
        <>
          <div className="pref-group-pad">
            <div style={{ fontWeight: 600, fontSize: 13 }}>命令执行权限</div>
            <div className="segmented" style={{ width: '100%' }}>
              {(
                [
                  { id: 'readonly', label: '只读' },
                  { id: 'smart', label: '智能确认' },
                  { id: 'ask_all', label: '每次确认' },
                  { id: 'trusted', label: '自动放行' },
                ] as { id: PermissionMode; label: string }[]
              ).map((m) => (
                <button
                  key={m.id}
                  type="button"
                  className={status.policy.mode === m.id ? 'active' : ''}
                  onClick={() =>
                    void saveConfig(
                      { ...status, policy: { ...status.policy, mode: m.id } },
                      `已切换为${m.label}`,
                    )
                  }
                >
                  {m.label}
                </button>
              ))}
            </div>
          </div>

          <div className="pref-group">
            {[
              { key: 'enabled' as const, title: '自动脱敏敏感信息' },
              { key: 'maskSecrets' as const, title: '保护密钥、令牌与密码' },
              { key: 'maskPII' as const, title: '保护邮箱、手机与证件号' },
              { key: 'maskIPs' as const, title: '隐藏 IPv4 地址' },
            ].map((item) => (
              <div key={item.key} className="pref-row">
                <span className="pref-row-label">{item.title}</span>
                <button
                  type="button"
                  role="switch"
                  aria-checked={status.redaction[item.key]}
                  className="apple-switch accent"
                  onClick={() =>
                    void saveConfig(
                      {
                        ...status,
                        redaction: { ...status.redaction, [item.key]: !status.redaction[item.key] },
                      },
                      '脱敏设置已保存',
                    )
                  }
                />
              </div>
            ))}
          </div>

          <details id="advancedGuardFold" className="advanced-fold">
            <summary>
              <ChevronRight size={13} className="fold-chevron" />
              <span>高级规则与测试</span>
            </summary>
            <div className="advanced-fold-body">
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
                <div className="field-group">
                  <label className="field-label">允许命令规则</label>
                  <textarea
                    className="textarea mono"
                    rows={2}
                    style={{ fontSize: 11.5, resize: 'none' }}
                    placeholder="kubectl get *"
                    value={allowText}
                    onChange={(e) => setAllowText(e.target.value)}
                  />
                </div>
                <div className="field-group">
                  <label className="field-label">禁止命令规则</label>
                  <textarea
                    className="textarea mono"
                    rows={2}
                    style={{ fontSize: 11.5, resize: 'none' }}
                    placeholder="rm -rf *"
                    value={denyText}
                    onChange={(e) => setDenyText(e.target.value)}
                  />
                </div>
              </div>

              <div className="field-group">
                <label className="field-label">SFTP 禁止路径</label>
                <textarea
                  className="textarea mono"
                  rows={2}
                  style={{ fontSize: 11.5, resize: 'none' }}
                  value={deniedPathsText}
                  onChange={(e) => setDeniedPathsText(e.target.value)}
                />
              </div>

              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
                <div className="field-group">
                  <label className="field-label">自定义脱敏正则</label>
                  <textarea
                    className="textarea mono"
                    rows={2}
                    style={{ fontSize: 11.5, resize: 'none' }}
                    placeholder="internal_tok_[a-z0-9]{16}"
                    value={customRegexText}
                    onChange={(e) => setCustomRegexText(e.target.value)}
                  />
                </div>
                <div className="field-group">
                  <label className="field-label">脱敏豁免白名单</label>
                  <textarea
                    className="textarea mono"
                    rows={2}
                    style={{ fontSize: 11.5, resize: 'none' }}
                    placeholder="EXAMPLE_KEY"
                    value={allowRegexText}
                    onChange={(e) => setAllowRegexText(e.target.value)}
                  />
                </div>
              </div>

              <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                <button
                  type="button"
                  className="btn btn-primary btn-sm btn-key-action"
                  disabled={busy}
                  onClick={() =>
                    void saveConfig(
                      {
                        ...status,
                        policy: {
                          ...status.policy,
                          allowPatterns: splitLines(allowText),
                          denyPatterns: splitLines(denyText),
                          deniedPaths: splitLines(deniedPathsText),
                        },
                        redaction: {
                          ...status.redaction,
                          customPatterns: splitLines(customRegexText),
                          allowlist: splitLines(allowRegexText),
                        },
                      },
                      '高级规则已保存',
                    )
                  }
                >
                  保存规则
                </button>
              </div>

              <div className="soft-card">
                <div style={{ fontWeight: 600, fontSize: 12 }}>命令权限测试</div>
                <div style={{ display: 'flex', gap: 6 }}>
                  <input
                    className="input input-sm mono"
                    value={policyCmd}
                    onChange={(e) => setPolicyCmd(e.target.value)}
                    placeholder="输入命令检测…"
                  />
                  <button type="button" className="btn btn-sm btn-key-action" onClick={() => void runPolicyTest()}>
                    检测
                  </button>
                </div>
                {policyEval && (
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', fontSize: 12 }}>
                    <span
                      className={`pill-badge ${
                        policyEval.decision === 'allow' ? 'safe' : policyEval.decision === 'deny' ? 'danger' : 'caution'
                      }`}
                    >
                      {policyEval.decision === 'allow' ? '放行' : policyEval.decision === 'deny' ? '拦截' : '需确认'}
                    </span>
                    <span style={{ color: 'var(--ink-secondary)' }}>{policyEval.reason}</span>
                  </div>
                )}
              </div>

              <div className="soft-card">
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                  <span style={{ fontWeight: 600, fontSize: 12 }}>脱敏测试</span>
                  <button id="runRedactTestBtn" type="button" className="btn btn-sm btn-key-action" onClick={() => void runRedactTest()}>
                    运行测试
                  </button>
                </div>
                <textarea
                  className="textarea mono"
                  rows={3}
                  style={{ fontSize: 11, resize: 'none' }}
                  value={redactSample}
                  onChange={(e) => setRedactSample(e.target.value)}
                />
                {redactOut && (
                  <>
                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 5, alignItems: 'center' }}>
                      <span className="pill-badge safe">脱敏 {redactOut.count} 处</span>
                      {Object.entries(redactOut.hits || {}).map(([k, v]) => (
                        <span key={k} className="pill-badge neutral mono">
                          {k}: {v}
                        </span>
                      ))}
                    </div>
                    <div className="code-block" style={{ fontSize: 11 }}>{redactOut.text}</div>
                  </>
                )}
              </div>
            </div>
          </details>
        </>
      )}

      {sub === 'audit' && (
        <>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 12, color: 'var(--ink-secondary)' }}>
              最近 {logs.length} 条记录
            </span>
            <div className="pref-row-actions">
              <button type="button" className="btn btn-sm btn-key-action" onClick={() => void loadAudit()}>
                刷新
              </button>
              <button
                type="button"
                className="btn btn-sm btn-danger btn-key-action"
                onClick={async () => {
                  await getApi().ClearAuditLogs();
                  setLogs([]);
                  onToast('已清空记录');
                }}
              >
                清空
              </button>
            </div>
          </div>

          {logs.length === 0 ? (
            <div className="soft-card" style={{ textAlign: 'center', padding: 24, color: 'var(--ink-tertiary)' }}>
              暂无操作记录
            </div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, maxHeight: 370, overflowY: 'auto' }}>
              {logs.map((entry) => (
                <div key={entry.id} className="soft-card" style={{ padding: '8px 11px', gap: 4 }}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 5, flexWrap: 'wrap' }}>
                      <span className="pill-badge neutral">{entry.clientName || entry.source}</span>
                      <span className="mono" style={{ fontWeight: 600, fontSize: 12 }}>{entry.tool}</span>
                      {entry.host && <span style={{ fontSize: 11.5, color: 'var(--ink-secondary)' }}>@{entry.host}</span>}
                    </div>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                      <span
                        className={`pill-badge ${
                          entry.decision.includes('deny') ? 'danger' : entry.risk === 'caution' ? 'caution' : 'safe'
                        }`}
                      >
                        {DECISION_LABEL[entry.decision] || entry.decision}
                      </span>
                      {entry.redactedCount > 0 && (
                        <span className="pill-badge accent">脱敏 {entry.redactedCount}</span>
                      )}
                    </div>
                  </div>
                  <div className="mono" style={{ fontSize: 11.5, color: 'var(--ink-primary)', overflowWrap: 'anywhere' }}>
                    {entry.summary}
                  </div>
                  <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 8, fontSize: 11, color: 'var(--ink-tertiary)' }}>
                    <span>{new Date(entry.timestamp * 1000).toLocaleString()}</span>
                    {entry.phases ? (
                      <details style={{ textAlign: 'right' }}>
                        <summary className="mono" style={{ cursor: 'pointer' }}>总计 {entry.durationMs} ms</summary>
                        {entry.phases.sessionMs != null && <div>会话准备 {entry.phases.sessionMs} ms</div>}
                        {entry.phases.approvalMs != null && <div>工具审批 {entry.phases.approvalMs} ms</div>}
                        {entry.phases.remoteMs != null && <div>远端命令往返 {entry.phases.remoteMs} ms</div>}
                      </details>
                    ) : (
                      <span className="mono">{entry.durationMs} ms</span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
};
