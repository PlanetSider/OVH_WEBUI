# OVH_WEBUI

OVH 独服与 VPS 的自托管控制台，提供服务器目录、可用性监控、抢购队列、多账户管理、已购服务器控制，以及 Telegram / 飞书通知和交互卡片。

项目采用 **一个前后端一体镜像**：React 前端构建后嵌入 Go 二进制，由同一个应用容器提供页面和 API。HTTPS 可按需交给已有的反向代理处理。

## 功能

- OVH 独服目录、机房可用性和配置价格查询
- 抢购队列、快速下单、自动重试、历史记录
- 独服补货监控和 VPS 监控
- Telegram 通知、Webhook、文本下单和一键入队
- 飞书通知、交互卡片、可用性配置聚合和一键入队
- 飞书基础配置只需 App ID 和 App Secret；Token / Encrypt Key 为回调安全项
- 多 OVH 账户、默认账户切换、账户凭据验证和账户状态查询
- 独服电源、重装、硬件、IPMI、网络、IP、防护、续费等控制
- VPS 电源、快照、重装和任务控制
- 运行日志、系统指标、队列和监控历史

已移除或废弃：线上巡检、Config Sniper。

## 生产部署

### 架构

```text
Internet :19998（或由外部反向代理转发）
        |
        v
  ghcr.io/planetsider/ovh-webui
  React UI + Go API
        |
        v
  宿主机 ./data:/data
  SQLite / 日志 / 缓存
```

项目自身只构建一个镜像：

```text
ghcr.io/planetsider/ovh-webui:latest
```

### 首次部署

要求：Linux、Docker Engine、Docker Compose v2。需要公网 HTTPS 时，请在容器前配置已有的反向代理。

```bash
sudo mkdir -p /opt/ovh-webui
sudo chown -R "$USER":"$USER" /opt/ovh-webui
git clone https://github.com/PlanetSider/OVH_WEBUI.git /opt/ovh-webui
cd /opt/ovh-webui

cp .env.example .env
sed -i "s/^API_SECRET_KEY=.*/API_SECRET_KEY=$(openssl rand -hex 32)/" .env
mkdir -p data
```

编辑 `.env`，至少设置：

```dotenv
API_SECRET_KEY=替换为强随机密钥
TG_WEBHOOK_SECRET=可选的随机密钥
TG_WEBHOOK_SECRET_OPTIONAL=false
```

如果 GHCR 镜像是私有的，先登录：

```bash
docker login ghcr.io
```

启动：

```bash
docker compose pull
docker compose up -d
docker compose ps
```

访问 `http://服务器IP:19998`，使用 `API_SECRET_KEY` 登录，然后在设置页添加 OVH 账户。

### 更新

服务器上的 `.env` 和 `./data` 数据目录不会被代码更新覆盖：

```bash
cd /opt/ovh-webui
git pull --ff-only
docker compose pull
docker compose up -d
docker compose ps
```

Compose 默认使用 `ghcr.io/planetsider/ovh-webui:latest`。

### GitHub 自动构建

`.github/workflows/publish-images.yml` 在以下情况运行：

- 推送到 `main`
- 推送 `v*` 格式的版本标签
- 在 GitHub Actions 页面手动运行

工作流会执行：

```text
构建 React 前端
    -> 复制到 backend/web
    -> go build -tags ui
    -> 打包为一个运行镜像
    -> 发布 linux/amd64 和 linux/arm64
```

镜像标签包括：

- `latest`：`main` 分支
- `sha-<commit>`：每次构建
- `vX.Y.Z`：版本标签

工作流只负责构建和发布镜像，不会自动连接生产服务器。服务器更新仍需执行 `pull` 和 `up -d`，或另行配置部署密钥和远程发布流程。

首次发布后，可以在 GitHub 仓库的 **Packages** 页面将镜像设置为 Public。私有镜像需要服务器执行 `docker login ghcr.io`。

## 通知配置

### Telegram

在设置页填写 Bot Token 和 Chat ID，然后注册 Webhook：

```text
https://你的域名/api/telegram/webhook
```

Telegram Webhook 路径免 `X-API-Key`，但使用 Telegram Secret Token 校验来源。

### 飞书

在设置页填写：

| 配置 | 是否必填 | 说明 |
|------|----------|------|
| App ID | 必填 | 基础消息和卡片发送 |
| App Secret | 必填 | 基础消息和卡片发送 |
| Verification Token | 可选 | 事件订阅和回调安全校验 |
| Encrypt Key | 可选 | 加密事件解密和请求签名校验 |

回调地址：

```text
https://你的域名/api/feishu/events
https://你的域名/api/feishu/card-action
```

基础通知只需要 App ID 和 App Secret。需要事件绑定账户或卡片按钮交互时，按飞书应用后台的事件订阅配置填写安全项。

## 本地开发

### Windows

依赖：Go 1.25、Node.js 20、npm。

```powershell
.\\scripts\\init-first-run.ps1
npm install
```

启动后端：

```powershell
cd backend
go run .
```

另开终端启动前端：

```powershell
npm run dev
```

开发地址通常为 `http://127.0.0.1:8080`。前端开发模式通过 API 代理访问后端；默认 Go 开发构建只提供 API，不嵌入前端。

### Linux / macOS

```bash
chmod +x scripts/init-first-run.sh
./scripts/init-first-run.sh
npm install
cd backend && go run .
```

另开终端执行：

```bash
npm run dev
```

### 本地生产构建

构建一个本地前后端一体镜像：

```bash
docker build -f Dockerfile -t ovh-webui:local .
```

生产和本地 Compose 都使用根目录的单镜像 `docker-compose.yml`；镜像已包含前端和 API，不需要额外的前端、后端或网关容器。

## 配置与数据

| 内容 | 位置 |
|------|------|
| 网关 API 密钥 | `.env` 的 `API_SECRET_KEY` |
| OVH 账户凭据 | SQLite `/data`，通过 WebUI 添加 |
| 队列、历史、监控订阅 | SQLite `/data` |
| 日志和缓存 | `/data/logs`、`/data/cache` |
| Docker 数据目录 | `./data` 绑定到容器 `/data` |

不要把以下内容提交到 Git：`.env`、`backend/.env`、`backend/data/`、OVH 凭据、Telegram Token、飞书 App Secret。

## 常用运维命令

```bash
# 查看服务状态
docker compose ps

# 查看应用日志
docker compose logs -f --tail=200 ovh-webui

# 重启应用
docker compose restart ovh-webui

# 停止服务
docker compose down

# 健康检查
curl -fsS http://127.0.0.1:19998/health
```

默认端口：

| 端口 | 用途 |
|------|------|
| `19998` | 应用页面、API 和健康检查 |

## API 入口

除健康检查、Webhook 等白名单路径外，API 需要请求头：

```http
X-API-Key: <API_SECRET_KEY>
```

主要接口：

- `/api/accounts`：账户管理；`/api/accounts/status`：账户状态查询
- `/api/servers`、`/api/catalog`、`/api/availability`：服务器目录与可用性
- `/api/queue`、`/api/queue/quick-order`：抢购队列
- `/api/monitor/*`：独服监控
- `/api/vps-monitor/*`：VPS 监控
- `/api/server-control/*`：已购独服控制
- `/api/vps-control/*`：已购 VPS 控制
- `/api/feishu/*`：飞书绑定、事件和卡片
- `/api/telegram/*`：Telegram Webhook、命令和下单

完整接口说明见 [`docs/handover/03-API-CONTRACT.md`](./docs/handover/03-API-CONTRACT.md)。

## 目录

```text
OVH_WEBUI/
├── backend/                         # Go API、监控、队列和 OVH 客户端
├── src/                             # React 前端
├── Dockerfile                       # 前后端一体镜像
├── docker-compose.yml               # 单镜像部署
├── .github/workflows/                # GitHub Actions 镜像发布
├── scripts/                          # 初始化、开发和烟测脚本
└── docs/                             # 部署、安全和架构文档
```

## 文档

- [`docs/DEPLOY.md`](./docs/DEPLOY.md)：Linux、镜像和运维说明
- [`docs/SECURITY.md`](./docs/SECURITY.md)：密钥与数据安全
- [`docs/handover/00-INDEX.md`](./docs/handover/00-INDEX.md)：架构、API、迁移和测试记录

## 许可证与声明

自用运维工具。调用 OVH 官方 API 时请遵守 OVH 服务条款和当地数据保护要求。本项目不保证库存可用性或抢购成功率。
