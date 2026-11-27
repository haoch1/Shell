export interface FormattedConnectError {
  title: string;
  detail?: string;
  canEditProfile: boolean;
}

const MAX_DETAIL_LEN = 240;

const REDUNDANT_PREFIXES: RegExp[] = [
  /^error:\s*/i,
  /^连接失败[：:]\s*/,
  /^SSH\s*握手失败[：:]\s*/i,
  /^ssh:\s*handshake failed:\s*/i,
];

function cleanDetail(raw: string): string | undefined {
  let text = String(raw ?? '')
    .replace(/[\r\n\t]+/g, ' ')
    .replace(/\s{2,}/g, ' ')
    .trim();
  if (!text) return undefined;

  let changed = true;
  while (changed) {
    changed = false;
    for (const prefix of REDUNDANT_PREFIXES) {
      const stripped = text.replace(prefix, '').trim();
      if (stripped && stripped !== text) {
        text = stripped;
        changed = true;
      }
    }
  }

  if (text.length > MAX_DETAIL_LEN) {
    text = `${text.slice(0, MAX_DETAIL_LEN - 1).trimEnd()}…`;
  }
  return text || undefined;
}

function buildResult(title: string, detail: string | undefined, canEditProfile: boolean): FormattedConnectError {
  const normalizedDetail = detail && detail !== title ? detail : undefined;
  return normalizedDetail ? { title, detail: normalizedDetail, canEditProfile } : { title, canEditProfile };
}

export function formatConnectError(raw: string): FormattedConnectError {
  const source = String(raw ?? '').trim();
  const lower = source.toLowerCase();
  const detail = cleanDetail(source);

  if (!source) {
    return { title: '连接失败', canEditProfile: true };
  }

  // 1. Host key / fingerprint verification failures (managed in known_hosts, not profile credentials)
  if (
    lower.includes('knownhosts') ||
    lower.includes('known_hosts') ||
    lower.includes('host key') ||
    lower.includes('hostkey') ||
    source.includes('指纹') ||
    source.includes('拒绝信任')
  ) {
    return buildResult('主机指纹校验未通过', detail, false);
  }

  // 2. ProxyJump / bastion failures (checked before generic network/auth errors so jump issues stay obvious)
  if (source.includes('跳板机') || /\bjump\b/.test(lower)) {
    return buildResult('跳板机连接失败', detail, true);
  }

  // 3. SOCKS5 / HTTP proxy failures (checked before generic connection refused / timeout)
  if (lower.includes('proxy') || lower.includes('socks') || source.includes('代理')) {
    return buildResult('代理连接失败：请检查代理配置', detail, true);
  }

  // 4. Authentication failures (password, private key, passphrase, interactive auth)
  if (
    lower.includes('unable to authenticate') ||
    lower.includes('no supported methods remain') ||
    lower.includes('permission denied') ||
    lower.includes('passphrase') ||
    lower.includes('authentication fail') ||
    lower.includes('auth failed') ||
    source.includes('私钥') ||
    source.includes('密钥') ||
    source.includes('密码') ||
    source.includes('口令') ||
    source.includes('认证已取消') ||
    source.includes('认证方式')
  ) {
    return buildResult('身份认证失败：密码或密钥不正确', detail, true);
  }

  // 5. Connection / handshake timeout
  if (
    lower.includes('i/o timeout') ||
    lower.includes('deadline exceeded') ||
    lower.includes('timed out') ||
    lower.includes('timeout') ||
    source.includes('超时')
  ) {
    return buildResult('连接超时：目标主机无响应', detail, true);
  }

  // 6. TCP connection refused
  if (
    lower.includes('connection refused') ||
    lower.includes('actively refused') ||
    lower.includes('econnrefused') ||
    source.includes('拒绝连接')
  ) {
    return buildResult('连接被拒绝：请检查 SSH 端口或防火墙', detail, true);
  }

  // 7. DNS / hostname resolution failure
  if (
    lower.includes('no such host') ||
    lower.includes('lookup ') ||
    lower.includes('getaddrinfo') ||
    source.includes('域名解析')
  ) {
    return buildResult('域名解析失败：无法解析主机地址', detail, true);
  }

  // 8. Network / route unreachable
  if (
    lower.includes('network is unreachable') ||
    lower.includes('no route to host') ||
    lower.includes('host is unreachable') ||
    lower.includes('unreachable network') ||
    lower.includes('unreachable host') ||
    lower.includes('enetunreach') ||
    lower.includes('ehostunreach')
  ) {
    return buildResult('网络不可达：请检查网络或代理设置', detail, true);
  }

  // 9. Generic fallback
  return buildResult('连接失败', detail, true);
}
