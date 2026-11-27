# 版本变更记录

本项目遵循 [语义化版本 2.0.0](https://semver.org/lang/zh-CN/) 规范。

## [1.1.0] - 2026-11-27

### 终端配色与视觉升级
- **强光高可读深色模式与深浅色同步终端配色**：采用接近 GitHub Dark Dimmed / VS Code Modern 的高对比石墨深色层级（强光下依然清晰可辨），新增「跟随界面外观」默认模式与成对商业级终端配色（Termius Dark/Light、Tokyo Night/Day、Catppuccin Mocha/Latte、Rosé Pine/Dawn、Nord Dark/Light、Dracula、GitHub Light、Solarized Light），切换浅色/深色外观时终端与界面自动协调同步，侧栏底部新增一键深浅色切换按钮。
- **官方品牌发行版图标**：集成 `simple-icons` 官方矢量图标（Ubuntu、Debian、CentOS、Rocky、Alpine、Arch、Fedora、Linux、Docker 及 Windows），为深色与浅色模式分别校准高亮与背景层级。
- **整体组件与字号比例优化**：调优标题栏、侧边栏、主机卡片、输入框、按钮、偏好设置与状态栏的字号与内边距，显著提升 Windows 高分屏阅读舒适度。

### 网络代理、跳板机与交互修复
- **系统代理 / 直连 / 多级 SSH 跳板机**：新增纯 Go Windows 系统代理检测（自动读取注册表 `ProxyEnable` / `ProxyServer` / `ProxyOverride` 绕过列表及环境变量），支持在「偏好设置 → 连接与指纹」配置全局默认网络（直连 / 系统代理 / SOCKS5 / HTTP），并可在主机「跳板与代理」页按单机覆盖直连、系统代理、自定义代理或最多 5 级 SSH 跳板链路。
- **多平台导入自动识别跳板与代理**：导入 Netcatty、OpenSSH、MobaXterm、Xshell、PuTTY、SecureCRT、Tabby、Electerm、WindTerm 等配置时自动解析 `Proxy` 与 `ProxyJump`（支持按别名/IP 关联已有主机、多级链式跳板及内联 `user@host:port` 自动建档关联）。
- **编辑主机免重输密码**：编辑已有主机资料时自动预填已加密保存的凭据（支持切换明文查看），点击「仅保存」立即持久化 DPAPI 凭据，修改 IP / 端口时默认沿用原密码不再强制重输。
- **智能 IPv6 / 长域名展示与侧栏排序**：将长 IPv6 地址按段中段缩略为 `2406:da18…4e21 :58222` 形式，悬停显示完整地址并支持侧栏悬浮一键复制；左侧主机栏新增排序按钮（默认顺序 / 名称 A→Z / Z→A / 最近使用 / 主机地址），并修复新导入未连接主机误入「最近使用」列表的问题。
- **终端底部留白与窗口控制细节**：采用独立外层留白容器避免终端末行提示符贴底，优化终端滚动条样式；标题栏最大化/还原按钮随窗口状态实时切换标准单框与双框图标；系统托盘右键精简为「断开全部会话」与「退出 Shell」。

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
