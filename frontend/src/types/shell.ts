export type AuthMode = 'password' | 'key' | 'agent';
export type ProfileColor = '' | 'blue' | 'green' | 'orange' | 'red' | 'purple' | 'teal' | 'gray';
export type HostIconId =
  | ''
  | 'ubuntu'
  | 'debian'
  | 'centos'
  | 'rocky'
  | 'alpine'
  | 'arch'
  | 'fedora'
  | 'linux'
  | 'windows'
  | 'docker';
export type TunnelMode = 'local' | 'remote' | 'dynamic';
export type ProxyType = '' | 'direct' | 'system' | 'socks5' | 'http';
export type GlobalProxyMode = 'direct' | 'system' | 'socks5' | 'http';
export type HostAgentAccess = '' | 'readonly' | 'allow' | 'deny';

export interface SystemProxyInfo {
  enabled: boolean;
  protocol?: string;
  host?: string;
  port?: number;
  user?: string;
  bypass?: string;
  source?: string;
  summary?: string;
}
export type PermissionMode = 'readonly' | 'smart' | 'ask_all' | 'trusted';

export interface TunnelRule {
  id: string;
  name: string;
  mode: TunnelMode;
  bindHost: string;
  bindPort: number;
  targetHost?: string;
  targetPort?: number;
  autoStart?: boolean;
}

export interface TunnelStatus extends TunnelRule {
  active: boolean;
  connections: number;
  bytesIn: number;
  bytesOut: number;
  error?: string;
}

export interface Profile {
  id: string;
  name: string;
  group?: string;
  tags?: string[];
  color?: ProfileColor;
  icon?: HostIconId;
  pinned?: boolean;
  host: string;
  port: number;
  user: string;
  auth: AuthMode;
  keyPath?: string;
  jumpHostId?: string;
  proxyType?: ProxyType;
  proxyHost?: string;
  proxyPort?: number;
  proxyUser?: string;
  forwardAgent?: boolean;
  keepAliveSec?: number;
  startupCommand?: string;
  notes?: string;
  agentAccess?: HostAgentAccess;
  tunnels?: TunnelRule[];
  lastUsedAt?: number;
  remembered?: boolean;
}

export interface ImportedHost {
  name: string;
  host: string;
  port: number;
  user: string;
  auth: string;
  keyPath?: string;
  group?: string;
  proxyType?: string;
  proxyHost?: string;
  proxyPort?: number;
  proxyJump?: string;
  forwardAgent?: boolean;
  keepAliveSec?: number;
  notes?: string;
  sourceFormat: string;
  alreadySaved: boolean;
}

export interface ImportPreviewResult {
  detectedFormat: string;
  hosts: ImportedHost[];
  warnings?: string[];
}

export type HostExportFormat = 'openssh' | 'putty';

export interface HostExportResult {
  path: string;
  count: number;
  warnings?: string[];
}

export interface RemoteEntry {
  name: string;
  path: string;
  isDir: boolean;
  isSymlink: boolean;
  size: number;
  mode: number;
  perm: string;
  modTime: number;
}

export interface TransferProgress {
  transferId: string;
  sessionId: string;
  name: string;
  direction: 'upload' | 'download';
  transferred: number;
  total: number;
  status: 'running' | 'done' | 'error' | 'canceled';
  error?: string;
}

export interface ServerStats {
  hostname: string;
  os: string;
  kernel: string;
  uptimeSec: number;
  load1: string;
  load5: string;
  load15: string;
  cpuCores: number;
  cpuPercent: number;
  memTotalMB: number;
  memUsedMB: number;
  diskTotalGB: number;
  diskUsedGB: number;
  diskPercent: number;
  netRxBytes: number;
  netTxBytes: number;
  latencyMs: number;
  collectedAt: number;
}

export interface Snippet {
  id: string;
  name: string;
  category: string;
  command: string;
  description?: string;
  pinned?: boolean;
}

export interface AppSettings {
  theme: 'light' | 'dark' | 'system';
  terminalTheme: string;
  fontFamily: string;
  fontSize: number;
  lineHeight: number;
  cursorStyle: 'bar' | 'block' | 'underline';
  cursorBlink: boolean;
  scrollback: number;
  copyOnSelect: boolean;
  rightClickPaste: boolean;
  showTelemetry: boolean;
  webglEnabled: boolean;
  defaultKeepAlive: number;
  proxyMode?: GlobalProxyMode;
  proxyHost?: string;
  proxyPort?: number;
  proxyUser?: string;
  confirmOnClose: boolean;
  closeToTray: boolean;
  autoCheckUpdate?: boolean;
  hideHostIp?: boolean;
}

export interface KnownHostEntry {
  line: number;
  hosts: string[];
  keyType: string;
  fingerprint: string;
}

export interface LocalKeyInfo {
  name: string;
  path: string;
  publicPath: string;
  keyType: string;
  fingerprint: string;
  publicKey: string;
  hasPublic: boolean;
  encrypted: boolean;
}

export interface ImportSummary {
  profiles: number;
  snippets: number;
  secrets: number;
}

export type AIProviderMode = 'api' | 'cli';

export interface DetectedAICLI {
  id: string;
  name: string;
  command: string;
  installed: boolean;
  path?: string;
}

export interface AIConfigView {
  mode: AIProviderMode;
  endpoint: string;
  model: string;
  hasKey: boolean;
  cliCommand?: string;
  cliArgs?: string;
  cliModel?: string;
  ready: boolean;
}

export interface AIConfigInput {
  mode: AIProviderMode;
  endpoint: string;
  model: string;
  apiKey: string;
  keepKey: boolean;
  cliCommand: string;
  cliArgs: string;
  cliModel: string;
}

export interface AIResult {
  command: string;
  explanation: string;
  risk: 'safe' | 'caution' | 'danger';
}

export interface PolicyConfig {
  mode: PermissionMode;
  allowPatterns?: string[];
  denyPatterns?: string[];
  deniedPaths?: string[];
}

export interface RedactionConfig {
  enabled: boolean;
  maskSecrets: boolean;
  maskPII: boolean;
  maskIPs: boolean;
  customPatterns?: string[];
  allowlist?: string[];
}

export interface AgentConfig {
  enabled: boolean;
  port: number;
  execMode?: 'silent' | 'visible';
  policy: PolicyConfig;
  redaction: RedactionConfig;
}

export interface AgentStatusView extends AgentConfig {
  running: boolean;
  token: string;
  exePath: string;
}

export interface RedactResult {
  text: string;
  count: number;
  hits: Record<string, number>;
}

export interface CommandSegment {
  raw: string;
  program: string;
  args: string[];
  wrapped: boolean;
  hasRedir: boolean;
  risk: 'safe' | 'caution' | 'danger';
  reason?: string;
}

export interface PolicyEvaluation {
  decision: 'allow' | 'ask' | 'deny';
  risk: 'safe' | 'caution' | 'danger';
  reason: string;
  segments?: CommandSegment[];
}

export interface AuditEntry {
  id: string;
  timestamp: number;
  source: string;
  clientName: string;
  host: string;
  sessionId?: string;
  tool: string;
  summary: string;
  risk: 'safe' | 'caution' | 'danger';
  decision: string;
  redactedCount: number;
  durationMs: number;
  phases?: { sessionMs?: number; approvalMs?: number; remoteMs?: number };
  ok: boolean;
  error?: string;
}

export interface AgentApprovalRequest {
  id: string;
  source: string;
  clientName: string;
  host: string;
  sessionId?: string;
  tool: string;
  summary: string;
  risk: 'safe' | 'caution' | 'danger';
  reason: string;
  segments?: CommandSegment[];
  canRememberSession: boolean;
}

export interface AgentStepEvent {
  step: number;
  kind: 'thought' | 'tool_call' | 'tool_result' | 'final' | 'error';
  tool?: string;
  arguments?: string;
  output?: string;
  risk?: string;
  decision?: string;
  redactedCount?: number;
  error?: string;
}

export interface AppInfoView {
  version: string;
  dataDir: string;
}

export interface UpdateInfo {
  currentVersion: string;
  latestVersion: string;
  hasUpdate: boolean;
  releaseUrl: string;
  notes: string;
  publishedAt?: string;
  assetName?: string;
  assetSize?: number;
  downloadUrl?: string;
  checksumsUrl?: string;
}

export interface UpdateProgress {
  phase: 'checking' | 'downloading' | 'verifying' | 'applying' | 'restarting' | 'error';
  downloaded: number;
  total: number;
  percent: number;
  message: string;
}

export interface HostVerifyRequest {
  id: string;
  host: string;
  algorithm: string;
  fingerprint: string;
}

export interface ChallengeRequest {
  id: string;
  host: string;
  user: string;
  instruction: string;
  questions: string[];
  echo: boolean[];
}

export type SessionState = 'connecting' | 'connected' | 'closed';

export interface SessionRecord {
  id: string;
  profile: Profile;
  state: SessionState;
  startedAt: number;
  latencyMs?: number;
  dir: string;
  cols: number;
  rows: number;
  endMessage?: string;
  secret?: string;
  remember?: boolean;
}

export type SplitNode =
  | { type: 'leaf'; paneId: string; sessionId: string }
  | {
      type: 'split';
      id: string;
      direction: 'horizontal' | 'vertical';
      ratio: number;
      first: SplitNode;
      second: SplitNode;
    };

export interface WorkspaceTab {
  id: string;
  title: string;
  color?: ProfileColor;
  root: SplitNode;
  activePaneId: string;
  broadcast: boolean;
}

export type DrawerMode = 'none' | 'sftp' | 'tunnels' | 'snippets' | 'ai';
