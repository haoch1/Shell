import React, { useEffect, useState } from 'react';
import { Activity, Bot, Cpu, HardDrive, MemoryStick, Network } from 'lucide-react';
import type { AgentStatusView, ServerStats, SessionRecord } from '../types/shell';
import { formatBytes, formatDuration } from '../lib/utils';

interface StatusBarProps {
  session?: SessionRecord;
  jumpHostName?: string;
  stats?: ServerStats;
  netRate?: { rxPerSec: number; txPerSec: number };
  showTelemetry: boolean;
  activeTunnelsCount: number;
  agentStatus?: AgentStatusView | null;
  onOpenTunnels: () => void;
  onOpenAgentSettings: () => void;
}

export const StatusBar: React.FC<StatusBarProps> = ({
  session,
  jumpHostName,
  stats,
  netRate,
  showTelemetry,
  activeTunnelsCount,
  agentStatus,
  onOpenTunnels,
  onOpenAgentSettings,
}) => {
  const [now, setNow] = useState(() => Date.now());
  const [showPopover, setShowPopover] = useState(false);

  useEffect(() => {
    if (!session || session.state !== 'connected') {
      setShowPopover(false);
      return;
    }
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [session?.id, session?.state]);

  if (!session) {
    return null;
  }

  const memPct = stats && stats.memTotalMB > 0 ? Math.round((stats.memUsedMB / stats.memTotalMB) * 100) : 0;
  const elapsed = session.state === 'connected' ? Math.max(0, (now - session.startedAt) / 1000) : 0;
  const latency = session.latencyMs ?? stats?.latencyMs;

  const barTone = (pct: number) => (pct >= 85 ? 'high' : pct >= 65 ? 'warn' : '');

  return (
    <footer className="statusbar">
      <div className="statusbar-left">
        <span className="status-chip">
          <span className={`tab-dot ${session.state}`} />
          <span className="mono">
            {session.profile.user}@{session.profile.host}
            {session.profile.port !== 22 ? `:${session.profile.port}` : ''}
          </span>
        </span>
        {jumpHostName && (
          <span className="pill-badge neutral" style={{ fontSize: 10.5 }}>
            via {jumpHostName}
          </span>
        )}
        {latency !== undefined && session.state === 'connected' && (
          <span className="status-chip mono">{latency} ms</span>
        )}
        {session.state === 'connected' && (
          <span className="status-chip mono">{formatDuration(elapsed)}</span>
        )}
        {activeTunnelsCount > 0 && (
          <button
            type="button"
            className="telemetry-trigger"
            style={{ color: 'var(--green)' }}
            onClick={onOpenTunnels}
          >
            {activeTunnelsCount} 转发运行中
          </button>
        )}
        {agentStatus?.running && (
          <button
            type="button"
            className="telemetry-trigger"
            style={{ color: 'var(--accent)' }}
            onClick={onOpenAgentSettings}
            title="本地 MCP Agent 服务运行中，点击管理权限与脱敏"
          >
            <Bot size={11} />
            MCP :{agentStatus.port}
          </button>
        )}
      </div>

      <div className="statusbar-right">
        {showTelemetry && session.state === 'connected' && stats && (
          <button
            type="button"
            className="telemetry-trigger"
            onClick={() => setShowPopover((v) => !v)}
            title="查看实时服务器监控详情"
          >
            <span>CPU {stats.cpuPercent.toFixed(0)}%</span>
            <span>MEM {memPct}%</span>
            <span>DISK {stats.diskPercent}%</span>
            {netRate && (
              <span>
                ↓{formatBytes(netRate.rxPerSec)}/s ↑{formatBytes(netRate.txPerSec)}/s
              </span>
            )}
          </button>
        )}
        <span className="mono" style={{ opacity: 0.75 }}>
          {session.cols}×{session.rows}
        </span>
      </div>

      {showPopover && stats && (
        <>
          <div style={{ position: 'fixed', inset: 0, zIndex: 29 }} onClick={() => setShowPopover(false)} />
          <div className="telemetry-popover">
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <div style={{ fontWeight: 600, fontSize: 13 }}>{stats.hostname || session.profile.host}</div>
              <span className="pill-badge safe">在线</span>
            </div>
            <div style={{ fontSize: 11.5, color: 'var(--ink-secondary)' }}>
              {stats.os || 'Linux'} · {stats.kernel}
            </div>
            <div style={{ fontSize: 11.5, color: 'var(--ink-tertiary)' }} className="mono">
              运行 {formatDuration(stats.uptimeSec)} · 负载 {stats.load1 || '-'} / {stats.load5 || '-'} /{' '}
              {stats.load15 || '-'}
            </div>

            <div className="field-group">
              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11.5 }}>
                <span className="status-chip"><Cpu size={12} />CPU ({stats.cpuCores} 核)</span>
                <span className="mono">{stats.cpuPercent.toFixed(1)}%</span>
              </div>
              <div className="metric-bar-track">
                <div className={`metric-bar-fill ${barTone(stats.cpuPercent)}`} style={{ width: `${stats.cpuPercent}%` }} />
              </div>
            </div>

            <div className="field-group">
              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11.5 }}>
                <span className="status-chip"><MemoryStick size={12} />内存</span>
                <span className="mono">
                  {(stats.memUsedMB / 1024).toFixed(1)} / {(stats.memTotalMB / 1024).toFixed(1)} GB ({memPct}%)
                </span>
              </div>
              <div className="metric-bar-track">
                <div className={`metric-bar-fill ${barTone(memPct)}`} style={{ width: `${memPct}%` }} />
              </div>
            </div>

            <div className="field-group">
              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11.5 }}>
                <span className="status-chip"><HardDrive size={12} />根分区 /</span>
                <span className="mono">
                  {stats.diskUsedGB.toFixed(1)} / {stats.diskTotalGB.toFixed(1)} GB ({stats.diskPercent}%)
                </span>
              </div>
              <div className="metric-bar-track">
                <div className={`metric-bar-fill ${barTone(stats.diskPercent)}`} style={{ width: `${stats.diskPercent}%` }} />
              </div>
            </div>

            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11.5, color: 'var(--ink-secondary)' }}>
              <span className="status-chip"><Network size={12} />累计流量</span>
              <span className="mono">
                ↓ {formatBytes(stats.netRxBytes)} · ↑ {formatBytes(stats.netTxBytes)}
              </span>
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11.5, color: 'var(--ink-secondary)' }}>
              <span className="status-chip"><Activity size={12} />往返延迟</span>
              <span className="mono">{latency ?? stats.latencyMs} ms</span>
            </div>
          </div>
        </>
      )}
    </footer>
  );
};
