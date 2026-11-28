import React, { useEffect, useState } from 'react';
import { ShieldAlert, ShieldCheck, X } from 'lucide-react';
import type { ChallengeRequest, HostVerifyRequest } from '../types/shell';

interface VerifyDialogProps {
  request: HostVerifyRequest;
  onDecision: (accept: boolean) => void;
}

export const VerifyHostDialog: React.FC<VerifyDialogProps> = ({ request, onDecision }) => {
  const isChanged = Boolean(request.changed);
  return (
    <div id="trustDialog" className="modal-backdrop">
      <div className="modal-sheet" style={{ maxWidth: 460 }}>
        <div className="modal-header">
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <ShieldAlert size={18} style={{ color: isChanged ? 'var(--red)' : 'var(--orange)' }} />
            <h2>{isChanged ? '主机指纹已变更' : '验证主机指纹'}</h2>
          </div>
          <button type="button" className="icon-btn" aria-label="关闭" onClick={() => onDecision(false)}>
            <X size={15} />
          </button>
        </div>
        <div className="modal-body">
          <p style={{ margin: 0, fontSize: 12.5, lineHeight: 1.55, color: isChanged ? 'var(--red)' : 'var(--ink-secondary)' }}>
            {isChanged
              ? '此主机的 SSH 指纹与本地记录不一致。若近期重装系统或更换主机密钥，请核对新指纹后再更新；否则可能存在中间人劫持风险。'
              : '首次连接到此主机，请与服务器管理员核对以下 SHA-256 指纹：'}
          </p>
          <div className="soft-card">
            <div style={{ fontWeight: 600, fontSize: 13 }} className="mono">
              {request.host}
            </div>
            <div style={{ fontSize: 11.5, color: 'var(--ink-secondary)' }}>{request.algorithm}</div>
            {isChanged && request.previousFingerprint && (
              <div className="field-group" style={{ gap: 3 }}>
                <span className="field-label">原有指纹</span>
                <div className="code-block" style={{ opacity: 0.72 }}>{request.previousFingerprint}</div>
              </div>
            )}
            <div className="field-group" style={{ gap: 3 }}>
              {isChanged && <span className="field-label">最新指纹</span>}
              <div className="code-block">{request.fingerprint}</div>
            </div>
          </div>
        </div>
        <div className="modal-footer">
          <button type="button" className="btn" autoFocus={isChanged} onClick={() => onDecision(false)}>
            拒绝连接
          </button>
          <button
            type="button"
            className={`btn ${isChanged ? 'btn-danger-solid' : 'btn-primary'}`}
            onClick={() => onDecision(true)}
          >
            <ShieldCheck size={14} />
            {isChanged ? '更新指纹并继续' : '信任并继续'}
          </button>
        </div>
      </div>
    </div>
  );
};

interface ChallengeDialogProps {
  request: ChallengeRequest;
  onSubmit: (answers: string[], accept: boolean) => void;
}

export const ChallengeDialog: React.FC<ChallengeDialogProps> = ({ request, onSubmit }) => {
  const [answers, setAnswers] = useState<string[]>(() => request.questions.map(() => ''));

  useEffect(() => {
    setAnswers(request.questions.map(() => ''));
  }, [request]);

  return (
    <div id="challengeDialog" className="modal-backdrop">
      <form
        className="modal-sheet"
        style={{ maxWidth: 440 }}
        autoComplete="off"
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit(answers, true);
        }}
      >
        <div className="modal-header">
          <h2>交互式身份验证</h2>
          <button type="button" className="icon-btn" aria-label="关闭" onClick={() => onSubmit([], false)}>
            <X size={15} />
          </button>
        </div>
        <div className="modal-body">
          <div className="mono" style={{ fontSize: 12.5, fontWeight: 600 }}>
            {request.user}@{request.host}
          </div>
          {request.instruction && (
            <p style={{ margin: 0, fontSize: 12.5, color: 'var(--ink-secondary)', whiteSpace: 'pre-wrap' }}>
              {request.instruction}
            </p>
          )}
          {request.questions.map((q, idx) => (
            <div key={idx} className="field-group">
              <label className="field-label">{q}</label>
              <input
                className="input"
                autoFocus={idx === 0}
                type={request.echo[idx] ? 'text' : 'password'}
                value={answers[idx] ?? ''}
                onChange={(e) => {
                  const next = [...answers];
                  next[idx] = e.target.value;
                  setAnswers(next);
                }}
              />
            </div>
          ))}
        </div>
        <div className="modal-footer">
          <button type="button" className="btn" onClick={() => onSubmit([], false)}>
            取消
          </button>
          <button type="submit" className="btn btn-primary">
            继续
          </button>
        </div>
      </form>
    </div>
  );
};
