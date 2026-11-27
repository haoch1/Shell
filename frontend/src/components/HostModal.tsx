import React, { useEffect, useState } from 'react';
import { FolderOpen, Trash2, X } from 'lucide-react';
import type { AuthMode, HostAgentAccess, HostIconId, Profile, ProfileColor, ProxyType } from '../types/shell';
import { PROFILE_COLORS, profileColorHex } from '../lib/themes';
import { errorMessage, getApi } from '../lib/bridge';
import { HOST_ICONS, inferHostIcon, renderHostGlyph } from './HostIcon';

interface HostModalProps {
  profile: Profile | null;
  allProfiles: Profile[];
  existingGroups: string[];
  onClose: () => void;
  onSave: (p: Profile, secret: string, remember: boolean, andConnect: boolean) => Promise<void>;
  onDelete?: (p: Profile) => void;
}

export const HostModal: React.FC<HostModalProps> = ({
  profile,
  allProfiles,
  existingGroups,
  onClose,
  onSave,
  onDelete,
}) => {
  const [tab, setTab] = useState<'basic' | 'advanced'>('basic');
  const [name, setName] = useState(profile?.name ?? '');
  const [host, setHost] = useState(profile?.host ?? '');
  const [port, setPort] = useState(String(profile?.port ?? 22));
  const [user, setUser] = useState(profile?.user ?? 'root');
  const [auth, setAuth] = useState<AuthMode>(profile?.auth ?? 'password');
  const [keyPath, setKeyPath] = useState(profile?.keyPath ?? '');
  const [secret, setSecret] = useState('');
  const [remember, setRemember] = useState(Boolean(profile?.remembered ?? true));
  const [group, setGroup] = useState(profile?.group ?? '');
  const [tags, setTags] = useState((profile?.tags ?? []).join(', '));
  const [color, setColor] = useState<ProfileColor>(profile?.color ?? '');
  const [icon, setIcon] = useState<HostIconId>(profile?.icon ?? '');
  const [pinned, setPinned] = useState(Boolean(profile?.pinned));
  const [jumpHostId, setJumpHostId] = useState(profile?.jumpHostId ?? '');
  const [proxyType, setProxyType] = useState<ProxyType>(profile?.proxyType ?? '');
  const [proxyHost, setProxyHost] = useState(profile?.proxyHost ?? '');
  const [proxyPort, setProxyPort] = useState(String(profile?.proxyPort || 7890));
  const [proxyUser, setProxyUser] = useState(profile?.proxyUser ?? '');
  const [agentAccess, setAgentAccess] = useState<HostAgentAccess>(profile?.agentAccess ?? '');
  const [forwardAgent, setForwardAgent] = useState(Boolean(profile?.forwardAgent));
  const [keepAliveSec, setKeepAliveSec] = useState(String(profile?.keepAliveSec ?? 30));
  const [startupCommand, setStartupCommand] = useState(profile?.startupCommand ?? '');
  const [notes, setNotes] = useState(profile?.notes ?? '');
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');

  const identityChanged =
    Boolean(profile?.remembered) &&
    (host.trim() !== profile?.host ||
      Number(port) !== profile?.port ||
      user.trim() !== profile?.user ||
      auth !== profile?.auth ||
      (auth === 'key' && keyPath.trim() !== (profile?.keyPath ?? '')));

  useEffect(() => {
    if (identityChanged) setRemember(false);
  }, [identityChanged]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const pickKey = async () => {
    try {
      const p = await getApi().PickKey();
      if (p) setKeyPath(p);
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const submit = async (andConnect: boolean) => {
    if (saving) return;
    const pNum = Number(port || 22);
    if (!host.trim() || !user.trim() || !Number.isInteger(pNum) || pNum < 1 || pNum > 65535) {
      setErr('请填写有效的主机地址、用户名与端口 (1-65535)。');
      return;
    }
    if (auth === 'key' && !keyPath.trim()) {
      setErr('私钥认证需要选择或填写私钥文件路径。');
      return;
    }
    setSaving(true);
    setErr('');
    try {
      const payload: Profile = {
        id: profile?.id ?? '',
        name: name.trim(),
        host: host.trim(),
        port: pNum,
        user: user.trim(),
        auth,
        keyPath: auth === 'key' ? keyPath.trim() : '',
        group: group.trim(),
        tags: tags
          .split(/[,，]/)
          .map((t) => t.trim())
          .filter(Boolean),
        color,
        icon,
        pinned,
        jumpHostId,
        proxyType,
        proxyHost: proxyType ? proxyHost.trim() : '',
        proxyPort: proxyType ? Number(proxyPort) || 0 : 0,
        proxyUser: proxyType ? proxyUser.trim() : '',
        agentAccess,
        forwardAgent,
        keepAliveSec: Math.max(0, Math.min(3600, Number(keepAliveSec) || 0)),
        startupCommand: startupCommand.trim(),
        notes: notes.trim(),
        tunnels: profile?.tunnels,
        lastUsedAt: profile?.lastUsedAt,
        remembered: profile?.remembered,
      };
      await onSave(payload, secret, auth !== 'agent' && remember, andConnect);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setSaving(false);
    }
  };

  const jumpCandidates = allProfiles.filter((p) => p.id !== profile?.id);

  return (
    <div id="connectDialog" className="modal-backdrop" onClick={onClose}>
      <form
        id="connectionForm"
        className="modal-sheet"
        autoComplete="off"
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => {
          e.preventDefault();
          void submit(true);
        }}
      >
        <div className="modal-header">
          <h2 id="profileTitle">{profile?.id ? `编辑 · ${profile.name}` : '新建 SSH 主机'}</h2>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <div className="segmented">
              <button type="button" className={tab === 'basic' ? 'active' : ''} onClick={() => setTab('basic')}>
                连接
              </button>
              <button type="button" className={tab === 'advanced' ? 'active' : ''} onClick={() => setTab('advanced')}>
                高级
              </button>
            </div>
            <button type="button" data-close className="icon-btn" aria-label="关闭" onClick={onClose}>
              <X size={15} />
            </button>
          </div>
        </div>

        <div className="modal-body">
          {tab === 'basic' ? (
            <>
              <div className="field-row">
                <div className="field-group grow">
                  <label className="field-label" htmlFor="address">主机地址</label>
                  <input
                    id="address"
                    className="input mono"
                    autoFocus={!profile?.host}
                    placeholder="prod.example.com 或 10.0.0.8"
                    value={host}
                    onChange={(e) => setHost(e.target.value)}
                    required
                  />
                </div>
                <div className="field-group" style={{ width: 92 }}>
                  <label className="field-label" htmlFor="port">端口</label>
                  <input
                    id="port"
                    className="input mono"
                    value={port}
                    onChange={(e) => setPort(e.target.value)}
                  />
                </div>
              </div>

              <div className="field-row">
                <div className="field-group grow">
                  <label className="field-label" htmlFor="login">用户名</label>
                  <input
                    id="login"
                    className="input mono"
                    placeholder="root / ubuntu / deploy"
                    value={user}
                    onChange={(e) => setUser(e.target.value)}
                    required
                  />
                </div>
                <div className="field-group" style={{ flex: '0 0 215px' }}>
                  <label className="field-label">认证方式</label>
                  <div id="authMode" className="segmented" style={{ width: '100%' }}>
                    {(['password', 'key', 'agent'] as AuthMode[]).map((m) => (
                      <button
                        key={m}
                        type="button"
                        data-auth={m}
                        className={auth === m ? 'active' : ''}
                        onClick={() => {
                          setAuth(m);
                          setSecret('');
                        }}
                      >
                        {m === 'password' ? '密码' : m === 'key' ? '私钥' : 'Agent'}
                      </button>
                    ))}
                  </div>
                </div>
              </div>

              {auth === 'password' && (
                <div className="field-group">
                  <label className="field-label" htmlFor="password">密码</label>
                  <input
                    id="password"
                    type="password"
                    className="input"
                    autoComplete="off"
                    placeholder={
                      profile?.remembered && !identityChanged
                        ? '已加密保存，留空沿用'
                        : '可留空（键盘交互 / 2FA）'
                    }
                    value={secret}
                    onChange={(e) => setSecret(e.target.value)}
                  />
                </div>
              )}

              {auth === 'key' && (
                <>
                  <div className="field-row">
                    <div className="field-group grow">
                      <label className="field-label" htmlFor="keyPath">私钥文件路径</label>
                      <input
                        id="keyPath"
                        className="input mono"
                        placeholder="C:\Users\...\id_ed25519"
                        value={keyPath}
                        onChange={(e) => setKeyPath(e.target.value)}
                      />
                    </div>
                    <button id="chooseKey" type="button" className="btn" onClick={() => void pickKey()}>
                      <FolderOpen size={14} />
                      浏览
                    </button>
                  </div>
                  <div className="field-group">
                    <label className="field-label" htmlFor="passphrase">私钥口令</label>
                    <input
                      id="passphrase"
                      type="password"
                      className="input"
                      autoComplete="off"
                      placeholder={
                        profile?.remembered && !identityChanged ? '已加密保存，留空沿用' : '输入私钥解锁口令'
                      }
                      value={secret}
                      onChange={(e) => setSecret(e.target.value)}
                    />
                  </div>
                </>
              )}

              {auth === 'agent' && (
                <div className="soft-card" style={{ fontSize: 12, color: 'var(--ink-secondary)' }}>
                  将使用本机 Windows OpenSSH Agent（<span className="mono">\\.\pipe\openssh-ssh-agent</span>）中已加载的密钥进行免密认证。
                </div>
              )}

              {auth !== 'agent' && (
                <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, cursor: 'pointer' }}>
                  <input
                    id="remember"
                    type="checkbox"
                    checked={remember}
                    onChange={(e) => setRemember(e.target.checked)}
                  />
                  <span>记住凭据</span>
                </label>
              )}

              <div className="field-row">
                <div className="field-group grow">
                  <label className="field-label" htmlFor="alias">显示名称</label>
                  <input
                    id="alias"
                    className="input"
                    placeholder="例如：东京生产节点（可选）"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                  />
                </div>
                <div className="field-group" style={{ width: 150 }}>
                  <label className="field-label" htmlFor="hostGroup">分组</label>
                  <input
                    id="hostGroup"
                    className="input"
                    list="shell-groups-datalist"
                    placeholder="生产 / 测试"
                    value={group}
                    onChange={(e) => setGroup(e.target.value)}
                  />
                  <datalist id="shell-groups-datalist">
                    {existingGroups.map((g) => (
                      <option key={g} value={g} />
                    ))}
                  </datalist>
                </div>
              </div>

              <div className="field-group">
                <span className="field-label">系统图标</span>
                <div className="icon-picker-row">
                  {HOST_ICONS.map((item) => {
                    const active = icon === item.id;
                    const glyph = item.id || inferHostIcon({ name, host, group });
                    const tint = item.id ? item.brandColor : profileColorHex(color);
                    return (
                      <button
                        key={item.id || 'auto'}
                        type="button"
                        className={`icon-choice-btn ${active ? 'selected' : ''}`}
                        style={{ '--host-accent': tint } as React.CSSProperties}
                        title={item.label}
                        onClick={() => setIcon(item.id)}
                      >
                        {item.id === '' ? (
                          <span style={{ fontSize: 10.5, fontWeight: 600 }}>自动</span>
                        ) : (
                          renderHostGlyph(glyph, 15)
                        )}
                      </button>
                    );
                  })}
                </div>
              </div>

              <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between' }}>
                <div className="field-group">
                  <span className="field-label">标记颜色</span>
                  <div className="color-picker-row">
                    {PROFILE_COLORS.map((c) => (
                      <button
                        key={c.id || 'default'}
                        type="button"
                        className={`color-swatch ${color === c.id ? 'selected' : ''}`}
                        style={{ background: c.hex }}
                        title={c.label}
                        onClick={() => setColor(c.id)}
                      />
                    ))}
                  </div>
                </div>
                <label style={{ display: 'flex', alignItems: 'center', gap: 7, fontSize: 12.5, cursor: 'pointer', height: 22 }}>
                  <input type="checkbox" checked={pinned} onChange={(e) => setPinned(e.target.checked)} />
                  置顶显示
                </label>
              </div>
            </>
          ) : (
            <>
              <div className="field-group">
                <label className="field-label">跳板机</label>
                <select className="select" value={jumpHostId} onChange={(e) => setJumpHostId(e.target.value)}>
                  <option value="">直连</option>
                  {jumpCandidates.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name} ({p.user}@{p.host}:{p.port})
                    </option>
                  ))}
                </select>
              </div>

              <div className="field-group">
                <label className="field-label">网络代理</label>
                <div className="field-row">
                  <select
                    className="select"
                    style={{ width: 112 }}
                    value={proxyType}
                    onChange={(e) => setProxyType(e.target.value as ProxyType)}
                  >
                    <option value="">无</option>
                    <option value="socks5">SOCKS5</option>
                    <option value="http">HTTP</option>
                  </select>
                  {proxyType && (
                    <>
                      <input
                        className="input grow mono"
                        placeholder="127.0.0.1"
                        value={proxyHost}
                        onChange={(e) => setProxyHost(e.target.value)}
                      />
                      <input
                        className="input mono"
                        style={{ width: 82 }}
                        placeholder="7890"
                        value={proxyPort}
                        onChange={(e) => setProxyPort(e.target.value)}
                      />
                      <input
                        className="input mono"
                        style={{ width: 92 }}
                        placeholder="用户名"
                        value={proxyUser}
                        onChange={(e) => setProxyUser(e.target.value)}
                      />
                    </>
                  )}
                </div>
              </div>

              <div className="field-group">
                <label className="field-label">Agent 访问权限</label>
                <div className="segmented" style={{ width: '100%' }}>
                  {(
                    [
                      { id: '', label: '跟随全局' },
                      { id: 'readonly', label: '仅只读' },
                      { id: 'allow', label: '允许' },
                      { id: 'deny', label: '禁止' },
                    ] as { id: HostAgentAccess; label: string }[]
                  ).map((opt) => (
                    <button
                      key={opt.id || 'inherit'}
                      type="button"
                      className={agentAccess === opt.id ? 'active' : ''}
                      onClick={() => setAgentAccess(opt.id)}
                    >
                      {opt.label}
                    </button>
                  ))}
                </div>
              </div>

              <div className="field-row">
                <div className="field-group grow">
                  <label className="field-label">标签</label>
                  <input
                    className="input"
                    placeholder="k8s, gpu, ap-northeast"
                    value={tags}
                    onChange={(e) => setTags(e.target.value)}
                  />
                </div>
                <div className="field-group" style={{ width: 140 }}>
                  <label className="field-label">心跳保活 (秒)</label>
                  <input
                    className="input mono"
                    type="number"
                    min={0}
                    max={3600}
                    value={keepAliveSec}
                    onChange={(e) => setKeepAliveSec(e.target.value)}
                  />
                </div>
              </div>

              <div className="setting-row" style={{ padding: '4px 0' }}>
                <div style={{ fontWeight: 500, fontSize: 13 }}>SSH Agent 转发</div>
                <button
                  type="button"
                  role="switch"
                  aria-checked={forwardAgent}
                  className="apple-switch accent"
                  onClick={() => setForwardAgent((v) => !v)}
                />
              </div>

              <div className="field-group">
                <label className="field-label">启动命令</label>
                <input
                  className="input mono"
                  placeholder="tmux attach || tmux"
                  value={startupCommand}
                  onChange={(e) => setStartupCommand(e.target.value)}
                />
              </div>

              <div className="field-group">
                <label className="field-label">备注</label>
                <textarea
                  className="textarea"
                  rows={2}
                  style={{ resize: 'none' }}
                  placeholder="可选备注"
                  value={notes}
                  onChange={(e) => setNotes(e.target.value)}
                />
              </div>
            </>
          )}

          {err && <p id="profileError" className="error-text">{err}</p>}
        </div>

        <div className="modal-footer">
          {profile?.id && onDelete && (
            <button
              id="deleteProfile"
              type="button"
              className="btn btn-danger"
              style={{ marginRight: 'auto' }}
              onClick={() => onDelete(profile)}
            >
              <Trash2 size={13} />
              删除
            </button>
          )}
          <button id="saveOnly" type="button" className="btn" style={{ minWidth: 74 }} disabled={saving} onClick={() => void submit(false)}>
            仅保存
          </button>
          <button id="connect" type="submit" className="btn btn-primary" style={{ minWidth: 96 }} disabled={saving}>
            {saving ? '处理中…' : '保存并连接'}
          </button>
        </div>
      </form>
    </div>
  );
};
