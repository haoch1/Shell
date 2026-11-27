# 安全策略

## 安全架构说明

Shell 采用**本地优先、最小权限**的安全设计：

### 1. 凭据与密钥存储
- **Windows DPAPI 加密（`CryptProtectData`）**：所有已保存的 SSH 密码、私钥解锁口令、AI API 密钥与本地 MCP Bearer 认证令牌，均绑定当前 Windows 系统用户加密存储于 `%LOCALAPPDATA%\Shell\*.dpapi`。明文凭据不会写入 `profiles.json` 或 `settings.json`。
- **凭据目标绑定**：已保存的 SSH 凭据与目标连接标识 `(id, host, port, user, auth, keyPath)` 强绑定；当主机地址、端口或用户名发生变更时，旧凭据自动失效并清除，防止凭据被重放到非预期目标。
- **加密备份保险库**：包含凭据的导出备份强制要求用户设置口令，使用 **AES-256-GCM** 算法与 **scrypt** 密钥派生（`N=32768, r=8, p=1`）。

### 2. SSH 传输与主机指纹校验
- **严格主机指纹验证**：首次连接目标主机时展示服务端 **SHA-256** 指纹并由用户确认，通过 Windows 独占文件锁（`LockFileEx`）原子写入 `%LOCALAPPDATA%\Shell\known_hosts`。若远端主机密钥发生非预期变化，握手立即中止。
- **纯 Go 加密实现**：SSH 传输、SFTP、密钥生成（Ed25519 / RSA-4096）及 SOCKS5 代理均基于 `golang.org/x/crypto/ssh` 实现，不依赖外部命令行进程。

### 3. AI Agent、MCP 桥接与脱敏机制
- **仅回环监听与防 DNS 重绑定**：本地 MCP / REST 服务仅绑定 `127.0.0.1`，校验 HTTP 请求 `Origin` 头（非法来源返回 `403 Forbidden`），并对 Bearer 令牌执行常量时间比对。
- **结构化命令策略引擎（`禁止 > 审批 > 允许`）**：将复合命令（`&&`、`||`、`;`、`|`、`$()`、反引号、输出重定向、`sudo` 包装）解析为独立子命令逐段评估。高危破坏性命令（如 `rm -rf`、`mkfs`、`dd of=/dev/`、`curl | sh`、`shutdown` 等）及自定义禁止规则无法通过拼接安全命令绕过。
- **按主机隔离与敏感路径保护**：支持按单台主机覆盖 Agent 权限（跟随全局 / 仅只读 / 允许 / 禁止）；禁止访问的主机不会出现在 Agent 主机列表中。SFTP 读取与写入默认拦截 `/etc/shadow`、`*/.ssh/id_*`、`/proc/*/environ`、`*/.aws/credentials` 等敏感路径。
- **自动文本脱敏**：在终端输出、命令结果、SFTP 文件内容或选中日志发送至 AI 模型或外部 Agent 前，自动识别并替换 PEM 私钥、主流云服务与 SaaS 令牌（AWS、阿里云、腾讯云、GitHub、GitLab、OpenAI、Anthropic、Stripe、Slack、GCP、JWT）、HTTP Authorization/Cookie 头、数据库连接串密码、`.env` 敏感键值、邮箱、手机号、校验和验证的身份证号及 Luhn 银行卡号。
- **本地审计记录**：所有外部 MCP 与内置 AI Agent 的调用来源、客户端名称、目标主机、脱敏后摘要、风险等级、审批结果、脱敏数量及耗时均持久化记录于 `%LOCALAPPDATA%\Shell\audit.jsonl`。

### 4. 外部 AI 接口约束
- 远程 AI 服务地址强制要求使用 **HTTPS**（仅本机回环地址如 Ollama / LM Studio 允许 `http://`），并禁用 HTTP 重定向以防 API 密钥泄露。

## 安全漏洞报告

如发现安全漏洞，请**勿**在公开 Issue 中披露细节。请通过 [GitHub Security Advisories](https://github.com/haoch1/Shell/security/advisories/new) 私密提交复现步骤与影响说明，我们将在核实后尽快修复并发布安全更新。
