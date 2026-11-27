import React from 'react';
import type { HostIconId, Profile, ProfileColor } from '../types/shell';
import { profileColorHex } from '../lib/themes';

export const HOST_ICONS: { id: HostIconId; label: string; brandColor: string }[] = [
  { id: '', label: '自动', brandColor: '#8e8e93' },
  { id: 'ubuntu', label: 'Ubuntu', brandColor: '#E95420' },
  { id: 'debian', label: 'Debian', brandColor: '#D70A53' },
  { id: 'centos', label: 'CentOS', brandColor: '#932279' },
  { id: 'rocky', label: 'Rocky', brandColor: '#10B981' },
  { id: 'alpine', label: 'Alpine', brandColor: '#0D597F' },
  { id: 'arch', label: 'Arch', brandColor: '#1793D1' },
  { id: 'fedora', label: 'Fedora', brandColor: '#3C6EB4' },
  { id: 'linux', label: 'Linux', brandColor: '#D97706' },
  { id: 'windows', label: 'Windows', brandColor: '#0078D4' },
  { id: 'docker', label: 'Docker', brandColor: '#2496ED' },
];

const BRAND_COLORS: Record<Exclude<HostIconId, ''>, string> = {
  ubuntu: '#E95420',
  debian: '#D70A53',
  centos: '#932279',
  rocky: '#10B981',
  alpine: '#0D597F',
  arch: '#1793D1',
  fedora: '#3C6EB4',
  linux: '#D97706',
  windows: '#0078D4',
  docker: '#2496ED',
};

const VALID_OS_ICONS = new Set<string>(Object.keys(BRAND_COLORS));

export function detectOSIconFromText(text: string): Exclude<HostIconId, ''> | '' {
  const s = (text || '').toLowerCase();
  if (!s) return '';
  if (s.includes('ubuntu')) return 'ubuntu';
  if (s.includes('debian') || s.includes('raspbian') || s.includes('kali')) return 'debian';
  if (s.includes('centos') || s.includes('red hat') || s.includes('rhel')) return 'centos';
  if (s.includes('rocky') || s.includes('alma')) return 'rocky';
  if (s.includes('alpine')) return 'alpine';
  if (s.includes('arch') || s.includes('manjaro')) return 'arch';
  if (s.includes('fedora')) return 'fedora';
  if (s.includes('docker') || s.includes('k8s') || s.includes('container')) return 'docker';
  if (s.includes('windows') || s.includes('win10') || s.includes('win11')) return 'windows';
  if (s.includes('linux') || s.includes('suse') || s.includes('freebsd')) return 'linux';
  return '';
}

export function inferHostIcon(profile?: Pick<Profile, 'icon' | 'name' | 'host' | 'group' | 'tags'>): Exclude<HostIconId, ''> {
  if (profile?.icon && VALID_OS_ICONS.has(profile.icon)) {
    return profile.icon as Exclude<HostIconId, ''>;
  }
  const hay = `${profile?.name ?? ''} ${profile?.host ?? ''} ${profile?.group ?? ''} ${(profile?.tags ?? []).join(' ')}`;
  return detectOSIconFromText(hay) || 'linux';
}

export function renderHostGlyph(icon: Exclude<HostIconId, ''>, size: number) {
  switch (icon) {
    case 'ubuntu':
      // Classic Ubuntu Circle of Friends
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <circle cx="12" cy="12" r="6.2" stroke="currentColor" strokeWidth="2.1" strokeDasharray="7.6 5.4" strokeDashoffset="1.2" />
          <circle cx="4.6" cy="12" r="2.2" fill="currentColor" />
          <circle cx="15.8" cy="5.7" r="2.2" fill="currentColor" />
          <circle cx="15.8" cy="18.3" r="2.2" fill="currentColor" />
        </svg>
      );
    case 'debian':
      // Iconic Debian swirl spiral
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path
            d="M14.2 14.5c-1.8 1.2-4.3.7-5.4-1.1-1.3-2.1-.6-5 1.5-6.2 2.5-1.5 5.9-.5 7.2 2.1 1.6 3.1.3 7.1-2.8 8.7-3.8 1.9-8.6.2-10.3-3.7C2.6 10 4.8 4.9 9.2 3.4c4.9-1.6 10.3 1.1 11.7 6"
            stroke="currentColor"
            strokeWidth="2.1"
            strokeLinecap="round"
          />
          <circle cx="12.2" cy="11.2" r="1.4" fill="currentColor" />
        </svg>
      );
    case 'centos':
      // CentOS geometric diamond grid
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <rect x="4" y="4" width="6.5" height="6.5" rx="1.2" stroke="currentColor" strokeWidth="1.9" />
          <rect x="13.5" y="4" width="6.5" height="6.5" rx="1.2" stroke="currentColor" strokeWidth="1.9" />
          <rect x="4" y="13.5" width="6.5" height="6.5" rx="1.2" stroke="currentColor" strokeWidth="1.9" />
          <rect x="13.5" y="13.5" width="6.5" height="6.5" rx="1.2" stroke="currentColor" strokeWidth="1.9" />
          <path d="M12 2.5L15 5.5M12 21.5L9 18.5M2.5 12L5.5 9M21.5 12L18.5 15" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
        </svg>
      );
    case 'rocky':
      // Rocky Linux circular mountain emblem
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <circle cx="12" cy="12" r="8.8" stroke="currentColor" strokeWidth="2" />
          <path d="M6.2 17.5L12.6 10.4L16.2 14L19 10.8" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case 'alpine':
      // Alpine Linux twin peaks
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path d="M2.5 17.5L9.2 6.5L14.2 14.5L16.8 10.5L21.5 17.5H2.5Z" stroke="currentColor" strokeWidth="2" strokeLinejoin="round" />
        </svg>
      );
    case 'arch':
      // Iconic Arch Linux pointed chevron
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path
            d="M12 3L4.2 20.5C6.8 18.8 9.2 17.8 10.6 17.6C10.3 16.5 10.7 15.1 12 15.1C13.3 15.1 13.7 16.5 13.4 17.6C14.8 17.8 17.2 18.8 19.8 20.5L12 3Z"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinejoin="round"
          />
        </svg>
      );
    case 'fedora':
      // Fedora rounded badge + f-loop
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path d="M12 3C7.03 3 3 7.03 3 12V19.2C3 20.2 3.8 21 4.8 21H12C16.97 21 21 16.97 21 12C21 7.03 16.97 3 12 3Z" stroke="currentColor" strokeWidth="1.9" />
          <path d="M15.2 9.2A2.2 2.2 0 0 0 11 10.2V14.8A2.2 2.2 0 1 1 8.8 12.6H13.5" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" />
        </svg>
      );
    case 'windows':
      // Crisp 4-pane Windows logo
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
          <rect x="3.5" y="3.5" width="7.6" height="7.6" rx="1.2" />
          <rect x="12.9" y="3.5" width="7.6" height="7.6" rx="1.2" />
          <rect x="3.5" y="12.9" width="7.6" height="7.6" rx="1.2" />
          <rect x="12.9" y="12.9" width="7.6" height="7.6" rx="1.2" />
        </svg>
      );
    case 'docker':
      // Docker container blocks
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <rect x="5" y="8.5" width="3.2" height="3" rx="0.5" fill="currentColor" />
          <rect x="9.2" y="8.5" width="3.2" height="3" rx="0.5" fill="currentColor" />
          <rect x="13.4" y="8.5" width="3.2" height="3" rx="0.5" fill="currentColor" />
          <rect x="9.2" y="4.8" width="3.2" height="3" rx="0.5" fill="currentColor" />
          <path d="M3 13.2H18.5C19.6 13.2 20.8 12.5 21.4 11.4C21 15.8 17.8 19 12.2 19C6.8 19 3.5 16.5 3 13.2Z" stroke="currentColor" strokeWidth="1.9" strokeLinejoin="round" />
        </svg>
      );
    default:
      // Linux Tux penguin silhouette
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path
            d="M12 3.2C9.5 3.2 8.2 5 8.2 7.6C8.2 9.2 7.2 10.8 6.4 12.6C5.6 14.3 5.8 16.8 7.5 18.2C8.7 19.2 10.2 19.6 12 19.6C13.8 19.6 15.3 19.2 16.5 18.2C18.2 16.8 18.4 14.3 17.6 12.6C16.8 10.8 15.8 9.2 15.8 7.6C15.8 5 14.5 3.2 12 3.2Z"
            stroke="currentColor"
            strokeWidth="1.9"
          />
          <circle cx="10.4" cy="7.2" r="0.9" fill="currentColor" />
          <circle cx="13.6" cy="7.2" r="0.9" fill="currentColor" />
          <path d="M10.7 9.4L12 10.4L13.3 9.4" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
          <path d="M8.5 19.8L6.5 20.6M15.5 19.8L17.5 20.6" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" />
        </svg>
      );
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
  const customColor = colorOverride ?? profile?.color;
  // Use authentic OS distro color by default unless user explicitly picked a custom color tag
  const hex = customColor ? profileColorHex(customColor) : BRAND_COLORS[resolvedIcon];
  const glyphSize = size === 'sm' ? 13 : size === 'lg' ? 18 : 16;

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
