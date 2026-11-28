import React, { useEffect, useState } from 'react';
import { ArrowLeftRight, Copy, Plus, Trash2, X } from 'lucide-react';
import type { Profile, SessionRecord, TunnelMode, TunnelRule, TunnelStatus } from '../types/shell';
import { copyToClipboard, errorMessage, getApi } from '../lib/bridge';
import { formatBytes, randomId } from '../lib/utils';
import { Checkbox } from './ui/Checkbox';

interface TunnelsDrawerProps {
  session: SessionRecord;
  profile?: Profile;
  onClose: () => void;
  onSaveProfileTunnels: (profile: Profile, tunnels: TunnelRule[]) => Promise<void>;
  onTunnelsChanged: (count: number) => void;
  onToast: (msg: string) => void;
}

export const TunnelsDrawer: React.FC<TunnelsDrawerProps> = ({
  session,
  profile,
  onClose,
  onSaveProfileTunnels,
  onTunnelsChanged,
  onToast,
}) => {
  const [live, setLive] = useState<TunnelStatus[]>([]);
  const [adding, setAdding] = useState(false);
  const [err, setErr] = useState('');
  const [form, setForm] = useState<{
    name: string;
    mode: TunnelMode;
    bindHost: string;
    bindPort: string;
    targetHost: string;
    targetPort: string;
    autoStart: boolean;
  }>({
    name: '',
    mode: 'local',
    bindHost: '127.0.0.1',
    bindPort: '8080',
    targetHost: '127.0.0.1',
    targetPort: '3000',
    autoStart: false,
  });

  const refresh = async () => {
    try {
      const list = await getApi().ListTunnels(session.id);
      setLive(list || []);
      onTunnelsChanged((list || []).filter((t) => t.active).length);
    } catch {
      setLive([]);
    }
  };

  useEffect(() => {
    void refresh();
    const id = window.setInterval(() => void refresh(), 2500);
    return () => window.clearInterval(id);
  }, [session.id]);

  const savedRules = profile?.tunnels ?? [];
  const mergedIds = new Set<string>([...savedRules.map((r) => r.id), ...live.map((l) => l.id)]);
  const items = [...mergedIds].map((id) => {
    const saved = savedRules.find((r) => r.id === id);
    const active = live.find((l) => l.id === id);
    return {
      rule: saved ?? active!,
      status: active,
      isSaved: Boolean(saved),
    };
  });

  const toggleRule = async (rule: TunnelRule, currentlyActive: boolean) => {
    setErr('');
    try {
      if (currentlyActive) {
        await getApi().StopTunnel(session.id, rule.id);
        onToast(`已停止转发「${rule.name}」`);
      } else {
        await getApi().StartTunnel(session.id, rule);
        onToast(`已启动转发「${rule.name}」`);
      }
      await refresh();
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const createRule = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr('');
    const bp = Number(form.bindPort);
    const tp = form.mode === 'dynamic' ? 0 : Number(form.targetPort);
    if (!Number.isInteger(bp) || bp < 1 || bp > 65535) {
      setErr('请输入有效的监听端口 (1-65535)');
      return;
    }
    if (form.mode !== 'dynamic' && (!Number.isInteger(tp) || tp < 1 || tp > 65535)) {
      setErr('请输入有效的目标端口 (1-65535)');
      return;
    }
    const rule: TunnelRule = {
      id: randomId(),
      name:
        form.name.trim() ||
        (form.mode === 'dynamic'
          ? `SOCKS5 :${bp}`
          : `${form.mode === 'local' ? 'L' : 'R'} :${bp} -> ${form.targetHost}:${tp}`),
      mode: form.mode,
      bindHost: form.bindHost.trim() || '127.0.0.1',
      bindPort: bp,
      targetHost: form.mode === 'dynamic' ? '' : form.targetHost.trim() || '127.0.0.1',
      targetPort: tp,
      autoStart: form.autoStart,
    };
    try {
      await getApi().StartTunnel(session.id, rule);
      if (profile?.id) {
        await onSaveProfileTunnels(profile, [...savedRules, rule]);
      }
      setAdding(false);
      setForm({ ...form, name: '' });
      onToast(`已启动转发「${rule.name}」`);
      await refresh();
    } catch (ex) {
      setErr(errorMessage(ex));
    }
  };

  const removeRule = async (id: string) => {
    await getApi().StopTunnel(session.id, id).catch(() => {});
    if (profile?.id) {
      await onSaveProfileTunnels(
        profile,
        savedRules.filter((r) => r.id !== id),
      );
    }
    await refresh();
  };

  return (
    <section className="right-drawer" aria-label="端口转发管理">
      <div className="drawer-header">
        <div className="drawer-title">
          <ArrowLeftRight size={15} strokeWidth={1.8} />
          <span>端口转发隧道</span>
        </div>
        <div style={{ display: 'flex', gap: 2 }}>
          <button type="button" className="icon-btn" title="新建转发规则" onClick={() => setAdding((v) => !v)}>
            <Plus size={15} />
          </button>
          <button type="button" className="icon-btn" title="关闭" onClick={onClose}>
            <X size={14} />
          </button>
        </div>
      </div>

      <div className="drawer-body">
        <div className="card-list">
          {err && <p className="error-text">{err}</p>}

          {adding && (
            <form className="soft-card" onSubmit={createRule}>
              <div className="segmented">
                {(['local', 'remote', 'dynamic'] as TunnelMode[]).map((m) => (
                  <button
                    key={m}
                    type="button"
                    className={form.mode === m ? 'active' : ''}
                    onClick={() => setForm({ ...form, mode: m })}
                  >
                    {m === 'local' ? '本地 -L' : m === 'remote' ? '远端 -R' : 'SOCKS5 -D'}
                  </button>
                ))}
              </div>

              <div className="field-group">
                <label className="field-label">名称</label>
                <input
                  className="input input-sm"
                  placeholder="MySQL / 内网代理"
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
              </div>

              <div className="field-row">
                <div className="field-group grow">
                  <label className="field-label">监听地址</label>
                  <input
                    className="input input-sm mono"
                    value={form.bindHost}
                    onChange={(e) => setForm({ ...form, bindHost: e.target.value })}
                  />
                </div>
                <div className="field-group" style={{ width: 92 }}>
                  <label className="field-label">监听端口</label>
                  <input
                    className="input input-sm mono"
                    value={form.bindPort}
                    onChange={(e) => setForm({ ...form, bindPort: e.target.value })}
                  />
                </div>
              </div>

              {form.mode !== 'dynamic' && (
                <div className="field-row">
                  <div className="field-group grow">
                    <label className="field-label">目标主机</label>
                    <input
                      className="input input-sm mono"
                      value={form.targetHost}
                      onChange={(e) => setForm({ ...form, targetHost: e.target.value })}
                    />
                  </div>
                  <div className="field-group" style={{ width: 92 }}>
                    <label className="field-label">目标端口</label>
                    <input
                      className="input input-sm mono"
                      value={form.targetPort}
                      onChange={(e) => setForm({ ...form, targetPort: e.target.value })}
                    />
                  </div>
                </div>
              )}

              {profile?.id && (
                <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, cursor: 'pointer' }}>
                  <Checkbox
                    checked={form.autoStart}
                    onChange={(e) => setForm({ ...form, autoStart: e.target.checked })}
                  />
                  <span>连接此主机时自动开启</span>
                </label>
              )}

              <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6 }}>
                <button type="button" className="btn btn-sm" onClick={() => setAdding(false)}>
                  取消
                </button>
                <button type="submit" className="btn btn-primary btn-sm">
                  保存并启动
                </button>
              </div>
            </form>
          )}

          {items.length === 0 && !adding && (
            <div style={{ padding: '28px 12px', textAlign: 'center', color: 'var(--ink-tertiary)', fontSize: 12.5 }}>
              暂无端口转发规则
            </div>
          )}

          {items.map(({ rule, status }) => {
            const active = Boolean(status?.active);
            const endpoint = `${rule.bindHost}:${rule.bindPort}`;
            return (
              <div key={rule.id} className="soft-card">
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 7, minWidth: 0 }}>
                    <span className={`tab-dot ${active ? 'connected' : 'closed'}`} />
                    <span style={{ fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {rule.name}
                    </span>
                    <span className="pill-badge neutral">
                      {rule.mode === 'local' ? '-L' : rule.mode === 'remote' ? '-R' : '-D'}
                    </span>
                  </div>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={active}
                    className="apple-switch"
                    onClick={() => void toggleRule(rule, active)}
                  />
                </div>

                <div className="mono" style={{ fontSize: 11.5, color: 'var(--ink-secondary)' }}>
                  {rule.mode === 'dynamic'
                    ? `SOCKS5 ${endpoint}`
                    : `${endpoint} → ${rule.targetHost}:${rule.targetPort}`}
                </div>

                {status && (
                  <div
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      fontSize: 11,
                      color: 'var(--ink-tertiary)',
                    }}
                  >
                    <span className="mono">
                      连接 {status.connections} · ↑{formatBytes(status.bytesOut)} ↓{formatBytes(status.bytesIn)}
                    </span>
                    <div style={{ display: 'flex', gap: 2 }}>
                      <button
                        type="button"
                        className="host-action-btn"
                        title="复制监听地址"
                        onClick={() => {
                          void copyToClipboard(endpoint);
                          onToast(`已复制 ${endpoint}`);
                        }}
                      >
                        <Copy size={12} />
                      </button>
                      <button
                        type="button"
                        className="host-action-btn"
                        title="删除规则"
                        style={{ color: 'var(--red)' }}
                        onClick={() => void removeRule(rule.id)}
                      >
                        <Trash2 size={12} />
                      </button>
                    </div>
                  </div>
                )}
                {!status && (
                  <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                    <button
                      type="button"
                      className="host-action-btn"
                      title="删除规则"
                      style={{ color: 'var(--red)' }}
                      onClick={() => void removeRule(rule.id)}
                    >
                      <Trash2 size={12} />
                    </button>
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </section>
  );
};
