import React from 'react';
import { AlertTriangle, Bot, Check, ShieldAlert, ShieldCheck, X } from 'lucide-react';
import type { AgentApprovalRequest } from '../types/shell';

interface AgentApprovalDialogProps {
  request: AgentApprovalRequest;
  onResolve: (decision: 'allow_once' | 'allow_session' | 'deny') => void;
}

export const AgentApprovalDialog: React.FC<AgentApprovalDialogProps> = ({ request, onResolve }) => {
  const isDanger = request.risk === 'danger';
  const sourceLabel =
    request.source === 'mcp'
      ? `外部 MCP Agent · ${request.clientName}`
      : request.source === 'http'
        ? `外部 HTTP Agent · ${request.clientName}`
        : 'Shell 内置 AI Agent';

  return (
    <div id="agentApprovalDialog" className="modal-backdrop">
      <div className="modal-sheet" role="dialog" aria-modal="true" style={{ maxWidth: 510 }}>
        <div className="modal-header">
          <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
            {isDanger ? (
              <ShieldAlert size={18} style={{ color: 'var(--red)' }} />
            ) : (
              <AlertTriangle size={18} style={{ color: 'var(--orange)' }} />
            )}
            <h2>{isDanger ? '高危操作审批' : 'Agent 操作确认'}</h2>
          </div>
          <span className={`pill-badge ${isDanger ? 'danger' : 'caution'}`}>
            {isDanger ? '高危' : '需确认'}
          </span>
        </div>

        <div className="modal-body">
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', fontSize: 12, color: 'var(--ink-secondary)' }}>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
              <Bot size={13} />
              {sourceLabel}
            </span>
            <span className="mono" style={{ fontWeight: 600, color: 'var(--ink-primary)' }}>
              {request.host || '未指定主机'} · {request.tool}
            </span>
          </div>

          <div className="code-block" style={{ maxHeight: 140, overflowY: 'auto' }}>
            {request.summary}
          </div>

          <div style={{ fontSize: 12.5, color: 'var(--ink-secondary)' }}>
            <b>拦截原因：</b>
            {request.reason}
          </div>

          {request.segments && request.segments.length > 0 && (
            <div className="soft-card" style={{ padding: '8px 10px', gap: 5 }}>
              <div style={{ fontSize: 11.5, fontWeight: 600, color: 'var(--ink-secondary)' }}>
                命令语法拆分审计 ({request.segments.length} 个子命令)
              </div>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 5 }}>
                {request.segments.map((seg, i) => (
                  <span
                    key={i}
                    className={`pill-badge ${seg.risk === 'danger' ? 'danger' : seg.risk === 'caution' ? 'caution' : 'safe'} mono`}
                    title={seg.reason || seg.raw}
                  >
                    {seg.wrapped ? 'sudo ' : ''}
                    {seg.program || seg.raw}
                    {seg.hasRedir ? ' >file' : ''}
                  </span>
                ))}
              </div>
            </div>
          )}
        </div>

        <div className="modal-footer">
          <button type="button" className="btn" onClick={() => onResolve('deny')}>
            <X size={13} />
            拒绝
          </button>
          {request.canRememberSession && !isDanger && (
            <button type="button" className="btn" onClick={() => onResolve('allow_session')}>
              <ShieldCheck size={13} />
              本次会话信任此类操作
            </button>
          )}
          <button
            type="button"
            className={`btn ${isDanger ? 'btn-danger-solid' : 'btn-primary'}`}
            onClick={() => onResolve('allow_once')}
          >
            <Check size={13} />
            允许一次
          </button>
        </div>
      </div>
    </div>
  );
};
