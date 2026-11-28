import React, { useEffect, useState } from 'react';
import { Activity, Bot, Cpu, HardDrive, MemoryStick, Network } from 'lucide-react';
import type { AgentStatusView, ServerStats, SessionRecord } from '../types/shell';
import { formatBytes, formatDuration, formatHostDisplay } from '../lib/utils';

interface StatusBarProps {
  session?: SessionRecord;
  jumpHostName?: string;
  stats?: ServerStats;
  netRate?: { rxPerSec: number; txPerSec: number };
  showTelemetry: boolean;
  hideHostIp?: boolean;
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
  hideHostIp = false,
  activeTunnelsCount,
  agentStatus,
  onOpenTunnels,
  onOpenAgentSettings,
}) => {
  const [showPopover, setShowPopover] = useState(false);

  useEffect(() => {
    if (!session || session.state !== 'connected') {
      setShowPopover(false);
    }
  }, [session?.id, session?.state]);

  if (!session) {
    return null;
  }

  const memPct = stats && stats.memTotalMB > 0 ? Math.round((stats.memUsedMB / stats.memTotalMB) * 100) : 0;
  const swapTotalMB = stats?.swapTotalMB ?? 0;
  const swapUsedMB = stats?.swapUsedMB ?? 0;
  const swapPct = swapTotalMB > 0 ? Math.min(100, Math.max(0, Math.round((swapUsedMB / swapTotalMB) * 100))) : 0;
  const latency = session.latencyMs ?? stats?.latencyMs;

  const barTone = (pct: number) => (pct >= 85 ? 'high' : pct >= 65 ? 'warn' : '');
  const metricColor = (pct: number): string | undefined =>
    pct >= 85 ? 'var(--red)' : pct >= 70 ? 'var(--orange)' : undefined;
  const latencyColor = (ms: number): string | undefined =>
    ms >= 600 ? 'var(--red)' : ms >= 250 ? 'var(--orange)' : undefined;
  const displayName =
    session.profile.name && (!hideHostIp || session.profile.name !== session.profile.host)
      ? session.profile.name
      : session.profile.user || 'SSH 主机';

  return (
    <footer className="statusbar">
      <div className="statusbar-left">
        <span
          className="status-chip"
          title={
            hideHostIp
              ? displayName
              : `${session.profile.user}@${session.profile.host}:${session.profile.port}`
          }
        >
          <span className={`tab-dot ${session.state}`} />
          <span
            className={`status-host-text ${hideHostIp ? '' : 'mono'}`}
            data-ip-hidden={hideHostIp ? 'true' : undefined}
          >
            {hideHostIp
              ? displayName
              : `${session.profile.user}@${formatHostDisplay(session.profile.host, session.profile.port)}`}
          </span>
        </span>
        {jumpHostName && (
          <span className="pill-badge neutral" style={{ fontSize: 11, height: 18, lineHeight: 1 }}>
            via {jumpHostName}
          </span>
        )}
        {latency !== undefined && session.state === 'connected' && (
          <span className="status-chip mono" style={{ color: latencyColor(latency) }}>
            {latency} ms
          </span>
        )}
        {activeTunnelsCount > 0 && (
          <button
            type="button"
            className="telemetry-trigger"
            style={{ color: 'var(--green)' }}
            onClick={onOpenTunnels}
          >
            {activeTunnelsCount} 转发
          </button>
        )}
        {agentStatus?.running && (
          <button
            type="button"
            className="telemetry-trigger"
            style={{ color: 'var(--accent)', gap: 4 }}
            onClick={onOpenAgentSettings}
            title="本地 MCP Agent 服务运行中，点击管理权限与执行方式"
          >
            <Bot size={12} />
            <span>MCP :{agentStatus.port}</span>
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
            <span style={{ color: metricColor(stats.cpuPercent) }}>CPU {stats.cpuPercent.toFixed(0)}%</span>
            <span style={{ color: metricColor(memPct) }}>MEM {memPct}%</span>
            <span style={{ color: metricColor(stats.diskPercent) }}>DISK {stats.diskPercent}%</span>
            {netRate && (
              <span>
                ↓{formatBytes(netRate.rxPerSec)}/s ↑{formatBytes(netRate.txPerSec)}/s
              </span>
            )}
          </button>
        )}
      </div>

      {showPopover && stats && (
        <>
          <div style={{ position: 'fixed', inset: 0, zIndex: 29 }} onClick={() => setShowPopover(false)} />
          <div className="telemetry-popover">
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <div style={{ fontWeight: 600, fontSize: 13 }}>
                {stats.hostname || (hideHostIp ? displayName : formatHostDisplay(session.profile.host))}
              </div>
              <span className="pill-badge safe">在线</span>
            </div>
            <div style={{ fontSize: 11.5, color: 'var(--ink-secondary)' }}>
              {stats.os || 'Linux'} · {stats.kernel}
            </div>
            <div style={{ fontSize: 11.5, color: 'var(--ink-tertiary)' }} className="mono">
              运行 {formatDuration(stats.uptimeSec)} · 负载 {stats.load1 || '-'} / {stats.load5 || '-'} /{' '}
              {stats.load15 || '-'}
              {(stats.processes ?? 0) > 0 && ` · ${stats.processes} 进程`}
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

            {swapTotalMB > 0 && (
              <div className="field-group">
                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11.5 }}>
                  <span className="status-chip"><MemoryStick size={12} />Swap</span>
                  <span className="mono">
                    {(swapUsedMB / 1024).toFixed(1)} / {(swapTotalMB / 1024).toFixed(1)} GB ({swapPct}%)
                  </span>
                </div>
                <div className="metric-bar-track">
                  <div className={`metric-bar-fill ${barTone(swapPct)}`} style={{ width: `${swapPct}%` }} />
                </div>
              </div>
            )}

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
