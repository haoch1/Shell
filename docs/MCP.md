# MCP 协议与外部 Agent 接入文档

Shell 提供两套统一受控的 AI 协作方式，共享同一套结构化命令权限引擎、自动脱敏流水线、桌面审批弹窗与审计日志：

1. **外部 AI Agent 调用 Shell**：通过标准 **Model Context Protocol (MCP `2025-03-26`)**（支持 Streamable HTTP 与 stdio 两种传输）或本地 REST API，接入 Antigravity、Claude Code、Codex CLI、Gemini CLI、Cursor、Windsurf 或自定义脚本。
2. **内置 AI 助手**：在 **偏好设置 → AI 模型** 中支持配置自定义 OpenAI 兼容 API（DeepSeek、OpenAI、Ollama、OpenRouter 等）或直接调用本机 AI CLI（`claude`、`codex`、`gemini` 或自定义命令），提供「写命令 / 查报错 / 自动运维」三种模式。

---

## 1. 启用与客户端接入

1. 打开 **偏好设置 → Agent 与安全 → 外部接入 (MCP)**。
2. 开启 **允许外部 AI 工具连接 Shell**。
3. 在「客户端接入」卡片中切换 **JSON 配置 / Claude Code / Codex / Gemini CLI**，点击一键复制对应配置或命令行。

### 方式 A：主流 AI CLI 一键注册

```powershell
# Claude Code (Streamable HTTP)
claude mcp add --transport http shell http://127.0.0.1:17822/mcp --header "Authorization: Bearer <令牌>"

# Codex CLI (Stdio 桥接)
codex mcp add shell -- "C:\Path\To\Shell.exe" --mcp

# Gemini CLI (Streamable HTTP)
gemini mcp add --transport http shell http://127.0.0.1:17822/mcp --header "Authorization: Bearer <令牌>"
```

### 方式 B：JSON 配置（Streamable HTTP）

适用于支持 MCP `2025-03-26` Streamable HTTP 的客户端（Antigravity、Cursor、Claude Code 等）：

```json
{
  "mcpServers": {
    "shell": {
      "url": "http://127.0.0.1:17822/mcp",
      "headers": {
        "Authorization": "Bearer <从偏好设置复制的访问令牌>"
      }
    }
  }
}
```

### 方式 C：Stdio 模式（`Shell.exe --mcp`）

适用于通过本地子进程标准输入/输出通信的 MCP 客户端。`Shell.exe --mcp` 会自动发现正在运行的 Shell 桌面进程并读取经 DPAPI 保护的访问令牌：

```json
{
  "mcpServers": {
    "shell": {
      "command": "C:\\Path\\To\\Shell.exe",
      "args": ["--mcp"]
    }
  }
}
```

### 方式 D：本地 REST API

```bash
# 查看可用工具列表
curl -H "Authorization: Bearer <TOKEN>" http://127.0.0.1:17822/api/v1/tools

# 调用工具
curl -X POST http://127.0.0.1:17822/api/v1/call \
  -H "Authorization: Bearer <TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{"name":"shell_exec","arguments":{"command":"uptime && free -h"}}'
```

---

## 2. 内置 MCP 工具列表

| 工具名称 | 功能说明 | 默认策略（智能确认模式） |
| :--- | :--- | :--- |
| `shell_list_hosts` | 列出允许 Agent 访问的已保存 SSH 主机（不含凭据） | 自动放行 |
| `shell_list_sessions` | 列出当前已建立的 SSH 会话（`sessionId`、主机、延迟） | 自动放行 |
| `shell_connect` | 在 Shell 中建立并打开指定主机的 SSH 标签页 | 弹窗确认 |
| `shell_exec` | 在复用 SSH 通道上执行命令并返回 `stdout` / `stderr` / `exitCode` | 只读自动放行；写操作与高危弹窗确认 |
| `shell_read_terminal` | 读取交互式终端近期输出（已去除 ANSI 转义并脱敏） | 自动放行 |
| `shell_send_terminal` | 向当前交互式终端写入并执行命令 | 只读自动放行；写操作与高危弹窗确认 |
| `shell_sftp_list` | 通过 SFTP 列出远端目录内容 | 自动放行（拦截敏感路径） |
| `shell_sftp_read` | 通过 SFTP 读取远端文本文件（自动脱敏，拦截敏感路径） | 自动放行（拦截敏感路径） |
| `shell_sftp_write` | 通过 SFTP 写入远端文本文件 | 弹窗确认 |
| `shell_server_stats` | 获取远端主机 CPU、内存、磁盘、负载与网络吞吐状态 | 自动放行 |
| `shell_tunnels` | 查询、启动或停止本地/远程/SOCKS5 端口转发规则 | 查询自动放行；启停弹窗确认 |

---

## 3. 权限治理策略（`禁止 > 审批 > 允许`）

命令策略引擎对每条执行命令进行结构化拆解，识别 `&&`、`||`、`;`、`|`、换行、`$(...)` 与反引号子命令、输出重定向（`>` / `>>`）及 `sudo` / `env` 包装前缀，并逐段独立评估风险等级：

| 权限模式 | 行为说明 |
| :--- | :--- |
| **只读 (`readonly`)** | 仅放行经验证的只读查询与文件读取，自动拒绝所有变更、写入与高危命令 |
| **智能确认 (`smart`)** | 只读查询与白名单规则自动放行；涉及修改、重定向、未知或高危操作时弹出审批对话框 |
| **每次确认 (`ask_all`)** | 所有命令执行与文件操作均须人工点击确认 |
| **自动放行 (`trusted`)** | 非禁止规则内的常规操作自动放行；高危破坏性命令（如 `rm -rf`、`mkfs`、关机）仍需人工审批 |

- **单主机权限覆盖**：在 **编辑主机 → 高级 → Agent 访问权限** 中可按主机单独设置为「跟随全局 / 仅只读 / 允许 / 禁止」；设为禁止的主机对 Agent 完全不可见。
- **SFTP 敏感路径保护**：默认拦截 `/etc/shadow`、`*/.ssh/id_*`、`/proc/*/environ`、`*/.aws/credentials`、`*/.kube/config` 等路径，可在「高级规则与测试」中自定义。

---

## 4. 自动文本脱敏与审计记录

所有返回给外部 Agent 或 AI 模型的内容均先经过本地脱敏引擎清洗：

- **密钥与凭据**：PEM 私钥块、AWS（`AKIA...`）、GitHub（`ghp_` / `github_pat_`）、GitLab、Slack、OpenAI / Anthropic（`sk-...`）、Stripe、Google API、阿里云（`LTAI...`）、腾讯云（`AKID...`）、HuggingFace、JWT、HTTP `Authorization` / `Cookie` 头、数据库连接串密码、`.env` 敏感键值及命令行密码参数（自动保留 `ssh -p 2222` 等正常端口参数）。
- **个人隐私信息（PII）**：电子邮箱、11 位手机号、通过校验和验证的 18 位居民身份证号与通过 Luhn 校验的银行卡号。
- **可选规则与在线验证**：支持可选 IPv4 匿名化（保留 `127.0.0.1`）、自定义脱敏正则与豁免白名单，可在 **偏好设置 → Agent 与安全 → 安全与脱敏 → 高级规则与测试** 中即时测试。
- **审计记录**：每次调用均按 JSONL 格式追加写入 `%LOCALAPPDATA%\Shell\audit.jsonl`（摘要在落盘前同样经过脱敏处理），可在 **操作记录** 页面实时查看与清空。
