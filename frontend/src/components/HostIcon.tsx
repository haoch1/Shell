import React from 'react';
import {
  siAlpinelinux,
  siArchlinux,
  siCentos,
  siDebian,
  siDocker,
  siFedora,
  siLinux,
  siRockylinux,
  siUbuntu,
} from 'simple-icons';
import type { HostIconId, Profile, ProfileColor } from '../types/shell';
import { profileColorHex } from '../lib/themes';

interface BrandTheme {
  lightFg: string;
  darkFg: string;
  lightBg: string;
  darkBg: string;
  lightRing: string;
  darkRing: string;
}

const BRAND_THEMES: Record<Exclude<HostIconId, ''>, BrandTheme> = {
  ubuntu: {
    lightFg: '#E95420',
    darkFg: '#FF8A5B',
    lightBg: '#FFF2EC',
    darkBg: '#362925',
    lightRing: 'rgba(233, 84, 32, 0.22)',
    darkRing: 'rgba(255, 138, 91, 0.34)',
  },
  debian: {
    lightFg: '#D70A53',
    darkFg: '#FF6B9A',
    lightBg: '#FDF0F4',
    darkBg: '#36252E',
    lightRing: 'rgba(215, 10, 83, 0.20)',
    darkRing: 'rgba(255, 107, 154, 0.34)',
  },
  centos: {
    lightFg: '#932279',
    darkFg: '#E28FFF',
    lightBg: '#F9EFF7',
    darkBg: '#31273A',
    lightRing: 'rgba(147, 34, 121, 0.20)',
    darkRing: 'rgba(226, 143, 255, 0.32)',
  },
  rocky: {
    lightFg: '#0EA472',
    darkFg: '#3EE2A5',
    lightBg: '#ECFBF5',
    darkBg: '#20342F',
    lightRing: 'rgba(14, 164, 114, 0.22)',
    darkRing: 'rgba(62, 226, 165, 0.32)',
  },
  alpine: {
    lightFg: '#0D6B99',
    darkFg: '#56CCF2',
    lightBg: '#EDF7FC',
    darkBg: '#20303B',
    lightRing: 'rgba(13, 107, 153, 0.22)',
    darkRing: 'rgba(86, 204, 242, 0.32)',
  },
  arch: {
    lightFg: '#1793D1',
    darkFg: '#56CCF2',
    lightBg: '#ECF7FD',
    darkBg: '#20313D',
    lightRing: 'rgba(23, 147, 209, 0.22)',
    darkRing: 'rgba(86, 204, 242, 0.32)',
  },
  fedora: {
    lightFg: '#3C6EB4',
    darkFg: '#74B3FF',
    lightBg: '#EFF5FC',
    darkBg: '#242F42',
    lightRing: 'rgba(60, 110, 180, 0.22)',
    darkRing: 'rgba(116, 179, 255, 0.32)',
  },
  linux: {
    lightFg: '#CA8A04',
    darkFg: '#FCD34D',
    lightBg: '#FEF9EB',
    darkBg: '#363022',
    lightRing: 'rgba(202, 138, 4, 0.22)',
    darkRing: 'rgba(252, 211, 77, 0.32)',
  },
  windows: {
    lightFg: '#0078D4',
    darkFg: '#56CCF2',
    lightBg: '#EDF6FE',
    darkBg: '#203140',
    lightRing: 'rgba(0, 120, 212, 0.22)',
    darkRing: 'rgba(86, 204, 242, 0.32)',
  },
  docker: {
    lightFg: '#1D8CE0',
    darkFg: '#56CCF2',
    lightBg: '#EDF7FE',
    darkBg: '#203240',
    lightRing: 'rgba(29, 140, 224, 0.22)',
    darkRing: 'rgba(86, 204, 242, 0.32)',
  },
};

export const HOST_ICONS: { id: HostIconId; label: string; brandColor: string; darkColor: string }[] = [
  { id: '', label: '自动', brandColor: '#6e6e73', darkColor: '#a1a1a6' },
  { id: 'ubuntu', label: 'Ubuntu', brandColor: BRAND_THEMES.ubuntu.lightFg, darkColor: BRAND_THEMES.ubuntu.darkFg },
  { id: 'debian', label: 'Debian', brandColor: BRAND_THEMES.debian.lightFg, darkColor: BRAND_THEMES.debian.darkFg },
  { id: 'centos', label: 'CentOS', brandColor: BRAND_THEMES.centos.lightFg, darkColor: BRAND_THEMES.centos.darkFg },
  { id: 'rocky', label: 'Rocky', brandColor: BRAND_THEMES.rocky.lightFg, darkColor: BRAND_THEMES.rocky.darkFg },
  { id: 'alpine', label: 'Alpine', brandColor: BRAND_THEMES.alpine.lightFg, darkColor: BRAND_THEMES.alpine.darkFg },
  { id: 'arch', label: 'Arch', brandColor: BRAND_THEMES.arch.lightFg, darkColor: BRAND_THEMES.arch.darkFg },
  { id: 'fedora', label: 'Fedora', brandColor: BRAND_THEMES.fedora.lightFg, darkColor: BRAND_THEMES.fedora.darkFg },
  { id: 'linux', label: 'Linux', brandColor: BRAND_THEMES.linux.lightFg, darkColor: BRAND_THEMES.linux.darkFg },
  { id: 'windows', label: 'Windows', brandColor: BRAND_THEMES.windows.lightFg, darkColor: BRAND_THEMES.windows.darkFg },
  { id: 'docker', label: 'Docker', brandColor: BRAND_THEMES.docker.lightFg, darkColor: BRAND_THEMES.docker.darkFg },
];

const SIMPLE_ICON_PATHS: Partial<Record<Exclude<HostIconId, ''>, string>> = {
  ubuntu: siUbuntu.path,
  debian: siDebian.path,
  centos: siCentos.path,
  rocky: siRockylinux.path,
  alpine: siAlpinelinux.path,
  arch: siArchlinux.path,
  fedora: siFedora.path,
  linux: siLinux.path,
  docker: siDocker.path,
};

const VALID_OS_ICONS = new Set<string>(Object.keys(BRAND_THEMES));

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
  const path = SIMPLE_ICON_PATHS[icon];
  if (path) {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
        <path d={path} />
      </svg>
    );
  }
  // Windows 11 crisp 4-pane logo
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <rect x="2.5" y="2.5" width="8.7" height="8.7" rx="1.1" />
      <rect x="12.8" y="2.5" width="8.7" height="8.7" rx="1.1" />
      <rect x="2.5" y="12.8" width="8.7" height="8.7" rx="1.1" />
      <rect x="12.8" y="12.8" width="8.7" height="8.7" rx="1.1" />
    </svg>
  );
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
  const theme = BRAND_THEMES[resolvedIcon];
  const glyphSize = size === 'sm' ? 13 : size === 'lg' ? 18 : 16;

  const customHex = customColor ? profileColorHex(customColor) : '';
  const styleVars = (customHex
    ? {
        '--host-fg-light': customHex,
        '--host-fg-dark': customHex,
        '--host-bg-light': `color-mix(in srgb, ${customHex} 12%, #ffffff)`,
        '--host-bg-dark': `color-mix(in srgb, ${customHex} 18%, #1d1e22)`,
        '--host-ring-light': `color-mix(in srgb, ${customHex} 24%, transparent)`,
        '--host-ring-dark': `color-mix(in srgb, ${customHex} 32%, transparent)`,
      }
    : {
        '--host-fg-light': theme.lightFg,
        '--host-fg-dark': theme.darkFg,
        '--host-bg-light': theme.lightBg,
        '--host-bg-dark': theme.darkBg,
        '--host-ring-light': theme.lightRing,
        '--host-ring-dark': theme.darkRing,
      }) as React.CSSProperties;

  return (
    <span className={`host-icon-badge size-${size}`} style={styleVars}>
      {renderHostGlyph(resolvedIcon, glyphSize)}
      {isLive && <span className="host-icon-live" title="在线会话" />}
    </span>
  );
};
