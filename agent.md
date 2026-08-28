# OVH_WEBUI 开发协作规范

本文是本仓库的开发与自动化协作说明。修改代码前先阅读与目标模块对应的文档，并保持改动和现有架构一致。

## 项目范围

OVH_WEBUI 是一个前后端一体的 OVH 自托管控制台：

- `src/` 是 React 18 + TypeScript + Vite 前端。
- `backend/` 是 Go 1.25 + Gin API、目录查询、监控、队列、通知和服务器控制实现。
- 前端通过 Vite 构建后复制到 `backend/web`，使用 `go build -tags ui` 嵌入 Go 二进制。
- 生产运行时只依赖一个监听 `19998` 端口的容器，持久化数据位于 `/data`（本地开发对应 `backend/data/`）。

同级的 `OVH/` 是旧版独立仓库，不属于本项目的源码或构建流程；本文件只约束 `OVH_WEBUI/`。

## 目录与模块边界

- `backend/internal/handlers/`：HTTP 路由处理器，按资源拆分；新增 API 时同步更新路由、鉴权和 API 合同文档。
- `backend/internal/app/`：共享运行时状态及服务生命周期。
- `backend/internal/db/`：SQLite 持久化；运行期主数据必须通过数据库访问，避免重新引入 JSON 主读写。
- `backend/internal/catalog/`、`price/`：目录标准化与价格计算。
- `backend/internal/purchase/`、`monitor/`、`vps/`：下单、独服/VPS 监控及其后台循环。
- `backend/internal/telegram/`、`feishu` 处理器、`backend/internal/weixin/`：通知和交互通道。
- `src/pages/`、`src/components/`、`src/lib/`：页面、可复用 UI 和 API 客户端。
- `scripts/`：初始化、启动和烟测脚本；优先复用脚本，不要复制一套临时流程。
- `docs/`：部署、安全、架构、API 合同和交接记录。

## 本地开发

环境要求：Node.js 20、npm、Go 1.25；Docker 用于验证生产镜像。

首次初始化（会生成本地密钥并创建运行目录）：

```powershell
.\scripts\init-first-run.ps1
npm install
```

Windows 推荐启动方式：

```powershell
npm run dev:all
```

也可以分别运行后端和前端：

```powershell
npm run backend       # 启动/复用 127.0.0.1:19998
npm run dev           # Vite，通常为 127.0.0.1:8080
```

后端需要重编译时使用 `npm run backend:rebuild`。Linux/macOS 可直接执行 `scripts/init-first-run.sh`，再在 `backend/` 中运行 `go run .`，另开终端运行 `npm run dev`。

## 验证要求

提交前根据改动范围执行相应检查：

```powershell
npm run lint
npm run build
npm run test:unit:backend
```

涉及 HTTP 路由、鉴权、账户、监控、通知或下单流程时，先启动后端，再运行：

```powershell
$env:API_SECRET_KEY="与 backend/.env 一致的密钥"
python scripts/smoke_test.py
```

需要完整流程验证时运行 `npm run test:full`；该脚本可能访问真实 OVH API，必须只使用用户明确提供的测试账户和目标。只读改动至少完成 lint/build；Go 逻辑改动应补充或运行对应 `go test`。

### 验证记录与环境限制

每次验证应记录实际执行的命令、结果和环境限制，不要把“未执行”写成“通过”。截至 2026-08-29，本项目已完成以下验证（含 Git 提交状态）：

- `npm exec tsc -- --noEmit --pretty false`：通过。
- 使用不加载受限 `vite.config.ts` 临时文件的 Vite API 完成生产构建：通过；构建输出中的本地资源引用已检查。标准 `npm run build` 若因当前工作区无法写入 `vite.config.ts.timestamp-*.mjs` 而失败，应标记为环境权限限制，而不是代码构建失败。
- `npm ls --depth=0 --all`：通过，未发现缺失或 invalid 依赖。
- `scripts/*.py` 使用 `compile()` 做无 `.pyc` 语法检查：通过；PowerShell 初始化、后端启动和开发启动脚本解析：通过。
- `git diff --check`：通过。
- 历史 Git 提交与推送：提交 `25b66aa fix: harden monitoring purchase and account workflows` 已推送至 `origin/main`。
- 使用免安装 Go 1.25 执行 `go test ./... -count=1`：全部后端测试包通过；项目脚本 `npm run test:unit:backend` 也通过。
- 执行 `go build .`：Go 编译完成，但当前 Windows 环境拒绝写入默认输出 `backend/server.exe`；改用可写临时路径执行 `go build -o D:\Codex\OVH\server-go-build-test.exe .` 通过，临时文件已清理。
- `.env`、`backend/.env`、`backend/data/` 和 SQLite 文件不得进入 Git；验证时只检查文件名、忽略规则和跟踪状态，不输出真实密钥或账户信息。

以下项目可能因环境而无法得出有效结论，必须明确记录原因：

- Go 标准构建输出：当前 Windows 环境执行 `go build .` 时无法创建默认 `backend/server.exe`；显式输出到可写路径已通过，换用允许写入可执行文件的环境后可补跑标准命令。
- 后端未运行（`127.0.0.1:19998` 连接被拒绝）时，`smoke_test.py`/`full_functional_test.py` 只能记录为未完成，不能判定接口失败。启动后端且获得明确测试账户授权后，才可执行真实 API 烟测。
- 未安装 Docker 时，不执行 `docker compose config`、`docker build` 或容器健康检查；Docker 可用后再补验。
- npm registry 审计接口不可达时，`npm audit` 结果只能记为网络/registry 限制，不能推断“无漏洞”。
- `npm run lint` 的历史 `any`、Hook 依赖、`require()` 等问题应单独记录；除非用户要求，不为验证任务扩大成无关的大规模重构。

监控、通知和抢购流程验证仍以静态审查及单元测试为主：不得调用真实抢购、重装、电源、网络或其他有副作用的 OVH 接口，除非用户在当前任务中明确授权。通知 outbox 当前为至少一次投递语义，多实例或进程崩溃场景可能重复通知，这是已知架构限制，不能在报告中表述为“恰好一次”。

### 当前审查待办与已知限制

以下事项仍需在具备对应工具、服务或授权的环境中补验；不要将其误报为已通过：

- Go 后端：完整 `go test ./... -count=1` 已通过；标准 `go build .` 的默认输出仍受 Windows 权限限制，详见上方验证记录。
- 后端运行时：当前未启动 `127.0.0.1:19998`，因此 smoke/full functional 测试尚未完成；启动后端并取得明确测试账户授权后再执行。
- 标准前端构建：`npm run build` 曾受工作区无法写入 Vite 临时文件限制；替代构建已通过，换用可写环境后应补跑标准命令。
- ESLint：`npm run lint` 当前仍有 216 项历史问题（191 error、25 warning），主要涉及 `any`、Hook 依赖、`require()` 等；除非用户明确要求，不扩大为无关重构。
- Docker 与依赖审计：Docker 未安装；npm registry 审计接口不可达，不能据此推断镜像或依赖安全结论。
- 账户删除、PurchaseServer、队列公平性和监控恢复路径：当前 Go 单元测试已完成；真实 OVH 抢购、重装、电源、网络等副作用操作不得自动执行。
- 通知 outbox：当前保证至少一次投递，多进程或进程崩溃后可能重复通知；若要求跨进程去重/恰好一次效果，需要单独设计 claim/lease、幂等键和状态迁移。
- OVH SDK：`CallAPIWithContext` 在正式请求前可能先发起无 context 的 `/auth/time` 请求，取消语义验证时需保留这一限制。

## 修改约定

- 保持 TypeScript、Go 和现有 ESLint/格式风格；优先小范围修改，不做无关重构。
- 新增或修改 API 时同时更新 `docs/handover/03-API-CONTRACT.md`，并确认 `X-API-Key` 白名单、请求时间戳和错误响应语义没有被绕过。
- 涉及 SQLite schema、迁移或持久化字段时，检查启动迁移、旧数据兼容和并发访问；不要直接修改用户的 `backend/data/`。
- 账户相关后台任务必须保留账户隔离语义；目录/实时可用性刷新仍由主刷新账户负责，队列和监控按账户运行。
- 下单、重装、电源、网络等操作属于有副作用的 API：默认只做单元测试和只读检查，真实调用前必须获得明确授权。
- 前端 API 调用集中复用现有客户端和查询模式；不要在组件中硬编码密钥、生产地址或账户凭据。
- UI 变更需同时考虑登录态、加载/错误/空状态、桌面和窄屏布局。

## 安全与数据

绝不提交或输出以下内容：`backend/.env`、根目录 `.env`、`backend/data/`、SQLite 数据库、OVH Application/Secret/Consumer Key、Telegram Token、飞书 App Secret、微信 iLink Bot Token 以及真实服务器主机名。使用 `.env.example` 和环境变量传递配置，日志和测试输出也要脱敏。

除 `/health` 等白名单外，API 默认要求 `X-API-Key`。Webhook 的例外必须保留来源校验（例如 Telegram Secret Token），不能为了方便关闭鉴权。

## 部署与文档

本地镜像验证：

```powershell
docker build -f Dockerfile -t ovh-webui:local .
docker compose config
```

生产更新遵循 `docker compose pull`、`docker compose up -d`、`docker compose ps`，不要覆盖宿主机 `.env` 或 `data/`。架构、安全和运维行为变化要同步更新 `README.md`、`docs/DEPLOY.md`、`docs/SECURITY.md` 或对应交接文档。
