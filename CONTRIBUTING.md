# 参与贡献

欢迎向 **Shell** 提交 Issue、功能改进与 Pull Request。

## 开发环境要求

- **操作系统**：Windows 10 / 11（x64），需安装 Microsoft Edge WebView2 Runtime
- **Go**：`1.26+`（设置 `CGO_ENABLED=0`）
- **Node.js**：`22+`（支持 `npm` 或 `bun`）

## 目录结构

```text
Shell/
├── main.go                     # 入口：Wails v2 桌面窗口与 --mcp stdio 代理
├── app.go                      # 桌面端绑定接口、会话管理与生命周期
├── tray_windows.go             # 纯 Go Windows 原生系统托盘实现
├── agent_runtime.go            # MCP / AI 工具调度、审批弹窗与通用导入绑定
├── cmd/genrsrc/                # 纯 Go Windows 图标与 .syso 资源生成器
├── internal/
│   ├── local/                  # 主机配置、DPAPI 凭据、导入器、片段、密钥、保险库、AI 客户端
│   ├── remote/                 # SSH、ProxyJump、SOCKS5/HTTP 代理、SFTP、端口转发、状态监控
│   ├── security/               # 敏感信息脱敏、结构化命令权限引擎、审计日志
│   └── agentbridge/            # MCP Streamable HTTP 服务端、REST API 与 stdio 桥接
├── frontend/                   # React 19 + TypeScript + Vite 6 + xterm.js v6 前端
└── .github/                    # GitHub Actions 构建发布流水线与校验脚本
```

## 本地构建与验证流程

```powershell
# 1. 安装依赖并构建前端
cd frontend
npm ci
npm run build
cd ..

# 2. 运行全部 Go 单元测试与集成测试
$env:CGO_ENABLED = '0'
go test -v -count=1 -tags desktop,production,wv2runtime.download ./...

# 3. 编译 Windows x64 便携可执行文件与发行包
./.github/scripts/package-windows.ps1

# 4. 运行原生 WebView2 启动校验与前端 UI 冒烟测试
node .github/scripts/smoke-windows.mjs
```

## 开发规范

- **保持轻量化**：严格保持 `CGO_ENABLED=0`，避免引入重型第三方运行时依赖。
- **凭据与安全红线**：禁止以明文记录或向 AI/MCP 输出凭据与敏感信息；任何新增脱敏或权限策略规则均须补充 `internal/security/` 单元测试。
- **提交前校验**：提交 PR 前请确认 `npm run typecheck`、`go test ./...` 与 `smoke-windows.mjs` 全部通过。
- **提交信息规范**：建议使用清晰的中文常规提交格式，如 `feat: ...`、`fix: ...`、`refactor: ...`、`docs: ...`。
