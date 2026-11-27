# 版本变更记录

本项目遵循 [语义化版本 2.0.0](https://semver.org/lang/zh-CN/) 规范。

## [1.0.0] - 2026-11-27

Shell 首个正式发行版本，基于 Go 1.26、Wails v2 (WebView2)、React 19 与 xterm.js v6 构建。

### SSH、网络与系统集成
- **低延迟核心 SSH 引擎**：采用纯 Go 本机回环二进制 WebSocket 终端数据流（空闲零延迟回显 + 突发输出帧合并）、`TCP_NODELAY` 与快速回退拨号，支持密码、私钥、Windows OpenSSH Agent、键盘交互式 2FA、多级跳板机（`ProxyJump`）、SSH Agent 转发（`ForwardAgent`）、心跳保活（KeepAlive）、RTT 延迟监控及 SHA-256 主机指纹校验。
- **前置网络代理**：支持按主机独立配置 SOCKS5 与 HTTP `CONNECT` 出站代理（含跳板链路）。
- **端口转发管理器**：支持本地转发（`-L`）、远程转发（`-R`）与动态 SOCKS5 代理（`-D`），实时显示连接数与字节流量，支持随主机自动启动。
- **Windows 原生系统托盘与窗口持久化**：纯 Go 实现 `Shell_NotifyIconW` 系统托盘（最小化至托盘与精简右键菜单），并自动保存与恢复窗口尺寸及最大化状态。

### 终端、文件与资产管理
- **现代终端工作区**：支持多标签页（右键复制会话 / 分屏 / 关闭其他）、递归水平/垂直分屏、广播输入、`Ctrl+1..9` / `Ctrl+Tab` 切换、正则搜索（`Ctrl+Shift+F`）、OSC 7 工作目录追踪、可选 WebGL 渲染及 9 套终端配色。
- **SFTP 文件管理与远程编辑**：提供面包屑导航、拖放上传、传输进度队列、新建目录/文件、重命名、`chmod` 权限修改及内置远程文本编辑器。
- **零休眠服务器状态监控**：复用 SSH 通道无休眠实时采集远端 Linux 的 CPU、内存、磁盘、负载与网速，并自动识别远程系统发行版矢量图标。
- **通用 SSH 配置导入**：自动识别 Netcatty、Xshell、MobaXterm、Termius、Tabby、Electerm、SecureCRT、PuTTY/KiTTY、WindTerm、FinalShell、OpenSSH `config` 及通用 CSV/JSON/纯文本，保留原始分组并加密保存导入凭据。
- **批量管理、密钥与加密保险库**：支持侧边栏主机与 `known_hosts` 批量删除、发行版图标与标记色、带变量占位符的命令片段库、Ed25519 / RSA-4096 密钥生成与公钥一键部署，以及 `AES-256-GCM` + `scrypt` 加密备份导入导出。

### AI 助手、MCP 协议与安全治理
- **内置 AI 助手（API + 本地 CLI）**：支持自定义 OpenAI 兼容 API（DeepSeek、OpenAI、Ollama、OpenRouter 等）与本地 AI CLI（Claude Code / Codex CLI / Gemini CLI / 自定义命令），提供「写命令 / 查报错 / 自动运维」三种模式。
- **标准 MCP Server (`2025-03-26`)**：提供 Streamable HTTP（`127.0.0.1:17822/mcp`）、本地 REST API 及 `Shell.exe --mcp` stdio 桥接模式，内置 11 个标准运维工具与 Claude Code / Codex / Gemini CLI 一键接入命令，支持 DPAPI 加密 Bearer 令牌鉴权与 `Origin` 防重绑定校验。
- **命令权限引擎、自动脱敏与审计**：支持结构化 Shell 语法拆分与 `禁止 > 审批 > 允许` 策略判定、按主机独立权限控制、SFTP 敏感路径拦截、密钥与个人隐私自动脱敏及本地 JSONL 审计日志。
