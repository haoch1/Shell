import React from 'react';
import {
  Box,
  Cloud,
  Cpu,
  Database,
  Globe,
  HardDrive,
  Server,
  Shield,
  Terminal,
} from 'lucide-react';
import type { HostIconId, Profile, ProfileColor } from '../types/shell';
import { profileColorHex } from '../lib/themes';

export const HOST_ICONS: { id: HostIconId; label: string }[] = [
  { id: '', label: '自动' },
  { id: 'server', label: '服务器' },
  { id: 'linux', label: '终端' },
  { id: 'cloud', label: '云主机' },
  { id: 'database', label: '数据库' },
  { id: 'gpu', label: '算力' },
  { id: 'docker', label: '容器' },
  { id: 'bastion', label: '堡垒机' },
  { id: 'globe', label: '节点' },
  { id: 'harddrive', label: '存储' },
];

export function inferHostIcon(profile?: Pick<Profile, 'icon' | 'name' | 'host' | 'group' | 'tags'>): Exclude<HostIconId, ''> {
  if (profile?.icon) return profile.icon;
  const hay = `${profile?.name ?? ''} ${profile?.host ?? ''} ${profile?.group ?? ''} ${(profile?.tags ?? []).join(' ')}`.toLowerCase();
  if (/\b(gpu|cuda|a100|h100|rtx|ml|ai|train)\b/.test(hay)) return 'gpu';
  if (/\b(bastion|jump|gateway|gw|proxy|vpn)\b|跳板|堡垒/.test(hay)) return 'bastion';
  if (/\b(db|sql|mysql|postgres|pg|redis|mongo|clickhouse|es)\b|数据库/.test(hay)) return 'database';
  if (/\b(docker|k8s|kube|pod|container)\b|容器/.test(hay)) return 'docker';
  if (/\b(nas|minio|s3|storage|backup|disk)\b|存储|备份/.test(hay)) return 'harddrive';
  if (/\b(aws|gcp|azure|aliyun|tencent|cloud|ecs|ec2)\b|云/.test(hay)) return 'cloud';
  if (/\b(edge|cdn|tokyo|hk|sg|us|eu|node|api|web)\b|节点|生产/.test(hay)) return 'globe';
  return 'server';
}

export function renderHostGlyph(icon: Exclude<HostIconId, ''>, size: number) {
  const props = { size, strokeWidth: 1.85 };
  switch (icon) {
    case 'linux':
      return <Terminal {...props} />;
    case 'docker':
      return <Box {...props} />;
    case 'database':
      return <Database {...props} />;
    case 'cloud':
      return <Cloud {...props} />;
    case 'gpu':
      return <Cpu {...props} />;
    case 'bastion':
      return <Shield {...props} />;
    case 'globe':
      return <Globe {...props} />;
    case 'harddrive':
      return <HardDrive {...props} />;
    default:
      return <Server {...props} />;
  }
}

interface HostIconProps {
  profile?: Pick<Profile, 'icon' | 'color' | 'name' | 'host' | 'group' | 'tags'>;
  iconOverride?: HostIconId;
  colorOverride?: ProfileColor;
  size?: 'sm' | 'md' | 'lg';
  isLive?: boolean;
}

export const HostIcon: React.FC<HostIconProps> = ({
  profile,
  iconOverride,
  colorOverride,
  size = 'md',
  isLive = false,
}) => {
  const resolvedIcon = iconOverride !== undefined ? (iconOverride || inferHostIcon(profile)) : inferHostIcon(profile);
  const hex = profileColorHex(colorOverride ?? profile?.color);
  const glyphSize = size === 'sm' ? 12 : size === 'lg' ? 16 : 14;

  return (
    <span
      className={`host-icon-badge size-${size}`}
      style={{ '--host-accent': hex } as React.CSSProperties}
    >
      {renderHostGlyph(resolvedIcon, glyphSize)}
      {isLive && <span className="host-icon-live" title="在线会话" />}
    </span>
  );
};
