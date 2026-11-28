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

  useEffect(() => {
    if (!showPopover) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setShowPopover(false);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [showPopover]);

  if (!session) {
    return null;
  }

  const memPct = stats && stats.memTotalMB > 0 ? Math.round((stats.memUsedMB / stats.memTotalMB) * 100) : 0;
  const swapTotalMB = stats?.swapTotalMB ?? 0;
  const swapUsedMB = stats?.swapUsedMB ?? 0;
  const swapPct = swapTotalMB > 0 ? Math.min(100, Math.max(0, Math.round((swapUsedMB / swapTotalMB) * 100))) : 0;
  const latency = session.latencyMs ?? stats?.latencyMs;
  const clampPct = (pct: number) => Math.min(100, Math.max(0, pct));

  const barTone = (pct: number) => (pct >= 85 ? 'high' : pct >= 65 ? 'warn' : '');
  const metricColor = (pct: number): string | undefined =>
    pct >= 85 ? 'var(--red)' : pct >= 70 ? 'var(--orange)' : undefined;
  const latencyColor = (ms: number): string | undefined =>
    ms >= 600 ? 'var(--red)' : ms >= 250 ? 'var(--orange)' : undefined;
  const displayName =
    session.profile.name && (!hideHostIp || session.profile.name !== session.profile.host)
      ? session.profile.name
      : session.profile.user || 'SSH 主机';

  const rawHostname = (stats?.hostname ?? '').trim();
  const hasRealHostname = Boolean(rawHostname) && !/^localhost(\.localdomain)?$/i.test(rawHostname);
  const popoverTitle = displayName;
  const popoverTitleTooltip =
    !hideHostIp && hasRealHostname && rawHostname !== displayName
      ? `${displayName} (${rawHostname})`
      : displayName;

  const osLabel = (stats?.os || 'Linux').trim();
  const rawKernel = (stats?.kernel || '').trim();
  const cleanKernel = /linux/i.test(osLabel) ? rawKernel.replace(/^linux\s+/i, '') : rawKernel;
  const osSubtitle = cleanKernel ? `${osLabel} · ${cleanKernel}` : osLabel;
  const effectiveLatency = latency ?? stats?.latencyMs ?? 0;

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
            className={`telemetry-trigger${showPopover ? ' active' : ''}`}
            onClick={() => setShowPopover((v) => !v)}
            aria-expanded={showPopover}
            title="查看实时服务器监控详情"
          >
            <span className="telemetry-stat">
              <span className="telemetry-k">CPU</span>
              <span className="telemetry-v" style={{ color: metricColor(stats.cpuPercent) }}>
                {stats.cpuPercent.toFixed(0)}%
              </span>
            </span>
            <span className="telemetry-stat">
              <span className="telemetry-k">MEM</span>
              <span className="telemetry-v" style={{ color: metricColor(memPct) }}>
                {memPct}%
              </span>
            </span>
            <span className="telemetry-stat">
              <span className="telemetry-k">DISK</span>
              <span className="telemetry-v" style={{ color: metricColor(stats.diskPercent) }}>
                {stats.diskPercent}%
              </span>
            </span>
            {netRate && (
              <>
                <span className="telemetry-sep" aria-hidden="true" />
                <span className="telemetry-net">
                  ↓{formatBytes(netRate.rxPerSec)}/s ↑{formatBytes(netRate.txPerSec)}/s
                </span>
              </>
            )}
          </button>
        )}
      </div>

      {showPopover && stats && (
        <>
          <div style={{ position: 'fixed', inset: 0, zIndex: 29 }} onClick={() => setShowPopover(false)} />
          <div className="telemetry-popover">
            <div className="telemetry-head">
              <div className="telemetry-head-top">
                <div className="telemetry-title" title={popoverTitleTooltip}>
                  {popoverTitle}
                </div>
                <span className="pill-badge safe">在线</span>
              </div>
              <div className="telemetry-os" title={osSubtitle}>
                {osSubtitle}
              </div>
            </div>

            <div className="telemetry-kpi-grid">
              <div className="telemetry-kpi-cell">
                <span className="telemetry-kpi-label">运行时间</span>
                <span className="telemetry-kpi-val">{formatDuration(stats.uptimeSec)}</span>
              </div>
              <div className="telemetry-kpi-cell">
                <span className="telemetry-kpi-label">进程数</span>
                <span className="telemetry-kpi-val">
                  {(stats.processes ?? 0) > 0 ? String(stats.processes) : '—'}
                </span>
              </div>
              <div className="telemetry-kpi-cell">
                <span className="telemetry-kpi-label">往返延迟</span>
                <span className="telemetry-kpi-val" style={{ color: latencyColor(effectiveLatency) }}>
                  {effectiveLatency} ms
                </span>
              </div>
            </div>

            <div className="telemetry-gauges">
              <div className="telemetry-gauge">
                <div className="telemetry-gauge-head">
                  <span className="telemetry-gauge-label">
                    <Cpu size={12} />
                    <span>CPU ({stats.cpuCores} 核)</span>
                  </span>
                  <span className="telemetry-gauge-val">{stats.cpuPercent.toFixed(1)}%</span>
                </div>
                <div className="metric-bar-track">
                  <div
                    className={`metric-bar-fill ${barTone(stats.cpuPercent)}`}
                    style={{ width: `${clampPct(stats.cpuPercent)}%` }}
                  />
                </div>
              </div>

              <div className="telemetry-gauge">
                <div className="telemetry-gauge-head">
                  <span className="telemetry-gauge-label">
                    <MemoryStick size={12} />
                    <span>内存</span>
                  </span>
                  <span className="telemetry-gauge-val">
                    {(stats.memUsedMB / 1024).toFixed(1)} / {(stats.memTotalMB / 1024).toFixed(1)} GB ({memPct}%)
                  </span>
                </div>
                <div className="metric-bar-track">
                  <div
                    className={`metric-bar-fill ${barTone(memPct)}`}
                    style={{ width: `${clampPct(memPct)}%` }}
                  />
                </div>
              </div>

              {swapTotalMB > 0 && (
                <div className="telemetry-gauge">
                  <div className="telemetry-gauge-head">
                    <span className="telemetry-gauge-label">
                      <MemoryStick size={12} />
                      <span>Swap</span>
                    </span>
                    <span className="telemetry-gauge-val">
                      {(swapUsedMB / 1024).toFixed(1)} / {(swapTotalMB / 1024).toFixed(1)} GB ({swapPct}%)
                    </span>
                  </div>
                  <div className="metric-bar-track">
                    <div
                      className={`metric-bar-fill ${barTone(swapPct)}`}
                      style={{ width: `${clampPct(swapPct)}%` }}
                    />
                  </div>
                </div>
              )}

              <div className="telemetry-gauge">
                <div className="telemetry-gauge-head">
                  <span className="telemetry-gauge-label">
                    <HardDrive size={12} />
                    <span>根分区 /</span>
                  </span>
                  <span className="telemetry-gauge-val">
                    {stats.diskUsedGB.toFixed(1)} / {stats.diskTotalGB.toFixed(1)} GB ({stats.diskPercent}%)
                  </span>
                </div>
                <div className="metric-bar-track">
                  <div
                    className={`metric-bar-fill ${barTone(stats.diskPercent)}`}
                    style={{ width: `${clampPct(stats.diskPercent)}%` }}
                  />
                </div>
              </div>
            </div>

            <div className="telemetry-foot">
              <div className="telemetry-foot-row">
                <span className="telemetry-foot-label">
                  <Activity size={12} />
                  <span>系统负载 (1/5/15m)</span>
                </span>
                <span className="telemetry-foot-val">
                  {stats.load1 || '-'} / {stats.load5 || '-'} / {stats.load15 || '-'}
                </span>
              </div>
              <div className="telemetry-foot-row">
                <span className="telemetry-foot-label">
                  <Network size={12} />
                  <span>累计流量</span>
                </span>
                <span className="telemetry-foot-val">
                  ↓ {formatBytes(stats.netRxBytes)} · ↑ {formatBytes(stats.netTxBytes)}
                </span>
              </div>
            </div>
          </div>
        </>
      )}
    </footer>
  );
};
