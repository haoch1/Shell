import type {
  AgentConfig,
  AgentStatusView,
  AIConfigInput,
  AIConfigView,
  AIResult,
  AppInfoView,
  AppSettings,
  AuditEntry,
  DetectedAICLI,
  ImportedHost,
  ImportPreviewResult,
  ImportSummary,
  KnownHostEntry,
  LocalKeyInfo,
  PolicyConfig,
  PolicyEvaluation,
  Profile,
  RedactionConfig,
  RedactResult,
  RemoteEntry,
  ServerStats,
  Snippet,
  SystemProxyInfo,
  TunnelRule,
  TunnelStatus,
  UpdateInfo,
} from '../types/shell';

interface GoAppBindings {
  AppInfo(): Promise<AppInfoView>;
  OpenDataDir(): Promise<void>;
  CheckForUpdate(): Promise<UpdateInfo>;
  DownloadAndApplyUpdate(): Promise<void>;
  TermWSURL(sessionId: string): Promise<string>;
  CancelVisibleConnect(profileId: string): Promise<void>;
  Profiles(): Promise<Profile[]>;
  SaveProfile(p: Profile): Promise<Profile>;
  GetProfileSecret(id: string): Promise<string>;
  SaveProfileWithSecret(p: Profile, secret: string, remember: boolean, keepExisting: boolean): Promise<Profile>;
  DuplicateProfile(id: string): Promise<Profile>;
  DeleteProfile(id: string): Promise<void>;
  DeleteProfiles(ids: string[]): Promise<void>;
  ClearRecentHosts(): Promise<void>;
  ForgetSecret(id: string): Promise<void>;
  PickKey(): Promise<string>;

  PickAndParseImportFiles(): Promise<ImportPreviewResult>;
  ParseImportFilePaths(paths: string[]): Promise<ImportPreviewResult>;
  DiscoverLocalSSHConfigImport(): Promise<ImportPreviewResult>;
  CommitImportedHosts(entries: ImportedHost[], overrideGroup: string): Promise<Profile[]>;

  Connect(id: string, p: Profile, secret: string, remember: boolean, cols: number, rows: number): Promise<void>;
  Disconnect(id: string): Promise<void>;
  Input(id: string, b64: string): Promise<void>;
  Resize(id: string, cols: number, rows: number): Promise<void>;
  ConfirmHost(id: string, accept: boolean): Promise<void>;
  AnswerChallenge(id: string, answers: string[], accept: boolean): Promise<void>;
  FetchStats(id: string): Promise<ServerStats>;
  InstallPublicKey(id: string, pubKey: string): Promise<void>;
  SaveTranscript(defaultName: string, content: string): Promise<string>;

  RealPath(id: string, dir: string): Promise<string>;
  ListFiles(id: string, dir: string): Promise<RemoteEntry[]>;
  PickUploadFiles(): Promise<string[]>;
  UploadLocalPaths(id: string, dir: string, paths: string[], overwrite: boolean): Promise<string[]>;
  UploadFile(id: string, dir: string): Promise<string>;
  DownloadFile(id: string, remotePath: string, overwrite: boolean): Promise<string>;
  CancelTransfer(transferId: string): Promise<void>;
  MakeDir(id: string, remoteDir: string): Promise<void>;
  CreateRemoteFile(id: string, remotePath: string): Promise<void>;
  RenameRemote(id: string, oldPath: string, newPath: string): Promise<void>;
  DeleteRemote(id: string, targetPath: string): Promise<void>;
  ChmodRemote(id: string, remotePath: string, mode: number): Promise<void>;
  ReadRemoteText(id: string, remotePath: string): Promise<string>;
  WriteRemoteText(id: string, remotePath: string, content: string): Promise<void>;

  StartTunnel(id: string, spec: TunnelRule): Promise<TunnelStatus>;
  StopTunnel(id: string, tunnelId: string): Promise<void>;
  ListTunnels(id: string): Promise<TunnelStatus[]>;

  Snippets(): Promise<Snippet[]>;
  SaveSnippet(s: Snippet): Promise<Snippet>;
  DeleteSnippet(id: string): Promise<void>;

  GetSettings(): Promise<AppSettings>;
  SaveSettings(s: AppSettings): Promise<AppSettings>;
  UpdatePreferences(changes: Partial<AppSettings>): Promise<AppSettings>;
  DetectSystemProxy(): Promise<SystemProxyInfo>;

  KnownHosts(): Promise<KnownHostEntry[]>;
  RemoveKnownHost(line: number): Promise<void>;
  RemoveKnownHosts(lines: number[]): Promise<void>;
  ClearKnownHosts(): Promise<void>;

  LocalKeys(): Promise<LocalKeyInfo[]>;
  GenerateSSHKey(name: string, algo: string, passphrase: string, comment: string): Promise<LocalKeyInfo>;

  ExportBackup(includeSecrets: boolean, passphrase: string): Promise<string>;
  ImportBackup(passphrase: string): Promise<ImportSummary>;

  AIConfig(): Promise<AIConfigView>;
  DetectAICLIs(): Promise<DetectedAICLI[]>;
  SaveAIConfig(cfg: AIConfigInput): Promise<void>;
  TestAIConfig(cfg: AIConfigInput): Promise<void>;
  ConfigureAI(endpoint: string, model: string, key: string, keep: boolean): Promise<void>;
  TestAI(endpoint: string, model: string, key: string, keep: boolean): Promise<void>;
  CancelAI(): Promise<void>;
  Suggest(request: string): Promise<string>;
  SuggestCommand(request: string, osHint: string): Promise<AIResult>;
  ExplainTerminal(selectedText: string, question: string): Promise<string>;
  RunAIAgent(sessionId: string, goal: string): Promise<string>;

  GetAgentStatus(): Promise<AgentStatusView>;
  SaveAgentConfig(cfg: AgentConfig): Promise<AgentStatusView>;
  RotateAgentToken(): Promise<string>;
  TestRedaction(sample: string, cfg: RedactionConfig): Promise<RedactResult>;
  TestCommandPolicy(command: string, cfg: PolicyConfig, hostAccess: string): Promise<PolicyEvaluation>;
  ResolveAgentApproval(id: string, decision: string): Promise<void>;
  AuditLogs(limit: number): Promise<AuditEntry[]>;
  ClearAuditLogs(): Promise<void>;
}

interface WailsRuntime {
  EventsOn(event: string, cb: (...args: unknown[]) => void): () => void;
  ClipboardGetText(): Promise<string>;
  ClipboardSetText(text: string): Promise<boolean>;
  WindowMinimise(): void;
  WindowToggleMaximise(): void;
  WindowIsMaximised(): Promise<boolean>;
  BrowserOpenURL(url: string): void;
  Quit(): void;
}

declare global {
  interface Window {
    go?: { main?: { App?: GoAppBindings } };
    runtime?: WailsRuntime;
  }
}

export function getApi(): GoAppBindings {
  const api = window.go?.main?.App;
  if (!api) {
    throw new Error('桌面桥接不可用，请从 Shell.exe 启动应用。');
  }
  return api;
}

export function hasDesktopBridge(): boolean {
  return Boolean(window.go?.main?.App && window.runtime);
}

export function getRuntime(): WailsRuntime | undefined {
  return window.runtime;
}

export function onDesktopEvent<T extends unknown[]>(event: string, handler: (...args: T) => void): () => void {
  const rt = window.runtime;
  if (!rt?.EventsOn) return () => {};
  return rt.EventsOn(event, (...args: unknown[]) => handler(...(args as T)));
}

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return String(err ?? '发生未知错误');
}

export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    const rt = window.runtime;
    if (rt?.ClipboardSetText) {
      return await rt.ClipboardSetText(text);
    }
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

export async function readFromClipboard(): Promise<string> {
  try {
    const rt = window.runtime;
    if (rt?.ClipboardGetText) {
      return await rt.ClipboardGetText();
    }
    return await navigator.clipboard.readText();
  } catch {
    return '';
  }
}

export function openExternalURL(url: string): void {
  const rt = window.runtime;
  if (rt?.BrowserOpenURL) {
    rt.BrowserOpenURL(url);
  } else {
    window.open(url, '_blank', 'noopener,noreferrer');
  }
}
