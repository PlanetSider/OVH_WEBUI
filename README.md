# OVH_WEBUI

OVH 独服与 VPS 的自托管控制台，提供服务器目录、可用性监控、抢购队列、多账户管理、已购服务器控制，以及 Telegram / 飞书 / QQ Bot v2 通知和交互卡片。

项目采用 **一个前后端一体镜像**：React 前端构建后嵌入 Go 二进制，由同一个应用容器提供页面和 API。HTTPS 可按需交给已有的反向代理处理。

## 功能

- OVH 独服目录、机房可用性和配置价格查询
- 抢购队列、快速下单、自动重试、历史记录
- 独服补货监控和 VPS 监控
- Telegram 通知、Webhook、文本下单和一键入队
- Telegram、飞书与 QQ Bot v2 通知；用户目标接收完整通知，群聊/频道目标仅接收独服和 VPS 上下架监控提醒
- 飞书通知、交互卡片、可用性配置聚合和一键入队
- 飞书回调要求 Encrypt Key 进行请求签名校验；Verification Token 可作为额外身份校验
- QQ Bot v2 使用手填 AppID/AppSecret；支持用户 OpenID、群聊 OpenID 和频道 ID 目标
- 多 OVH 账户、默认账户切换、账户凭据验证和账户状态查询
- 独服电源、重装、硬件、IPMI、网络、IP、防护、续费等控制
- VPS 电源、快照、重装和任务控制
- 运行日志、系统指标、队列和监控历史

已移除或废弃：线上巡检、Config Sniper。

## 生产部署

### 架构

```text
HTTPS 反向代理
        |
        v
127.0.0.1:19998（Compose 仅绑定本机）
        |
        v
  ghcr.io/planetsider/ovh-webui@sha256:<reviewed-digest>
  React UI + Go API
        |
        v
宿主机 ./data:/data
SQLite / 日志 / 缓存
```

项目自身只构建一个镜像；生产部署必须把镜像变量设为经过审核的不可变 digest：

```text
ghcr.io/planetsider/ovh-webui@sha256:<reviewed-digest>
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
# 从发布页或可信构建记录取得摘要后填写，不要使用 latest
sed -i "s#^OVH_WEBUI_IMAGE=.*#OVH_WEBUI_IMAGE=ghcr.io/planetsider/ovh-webui@sha256:<reviewed-digest>#" .env
mkdir -p data
# 镜像以 UID 100 的非 root 用户运行，确保运行时目录可写
sudo chown -R 100:100 data
```

编辑 `.env`，至少设置：

```dotenv
API_SECRET_KEY=替换为同时含大写、小写、数字且至少 8 位的随机密钥
OVH_WEBUI_IMAGE=ghcr.io/planetsider/ovh-webui@sha256:<reviewed-digest>
TG_WEBHOOK_SECRET=可选的随机密钥（配置 Telegram Webhook 时必须设置）
TG_WEBHOOK_SECRET_OPTIONAL=false
# 可选但生产建议设置：首次飞书绑定允许的 open_id，逗号分隔
FEISHU_ALLOWED_OPEN_IDS=
# 可选：敏感凭据 AES-GCM 密钥；未设置时首次启动自动写入 data/config.key
OVH_DB_KEY=
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

通过反向代理访问 `https://你的域名`；仅在部署主机本地调试时使用 `http://127.0.0.1:19998`，然后使用 `API_SECRET_KEY` 登录。

根目录 `.env` 只供 Docker Compose 使用；本地直接运行 Go 后端时使用 `backend/.env`，可通过 `scripts/init-first-run.ps1` 或 `scripts/init-first-run.sh` 生成。两者的 `API_SECRET_KEY` 应保持一致，便于前端和烟测登录。

### 更新

服务器上的 `.env` 和 `./data` 数据目录不会被代码更新覆盖：

```bash
cd /opt/ovh-webui
git pull --ff-only
docker compose pull
docker compose up -d
docker compose ps
```

更新前先将 `.env` 中的 `OVH_WEBUI_IMAGE` 改为新的、经过审核的完整 `sha256` digest；不要改回 `latest`。

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

- `latest`：`main` 分支发布标签，仅用于发布流程，不用于生产 Compose
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

发送 `/reboot` 可通过内联按钮依次选择 OVH 账户和独立服务器，并在二次确认后发送重启命令。仅配置一个默认账户时会直接显示服务器列表。

Telegram 与飞书私聊共用以下命令语义（QQ Bot v2 当前仅用于通知）：

| 命令 | 作用 |
|------|------|
| `/order` | 查看最近一个月订单 |
| `/order <数量>` | 查看最近数量条订单，数量范围 1–100 |
| `/order unpaid` | 查看最近一个月未付款订单 |
| `/pay <订单号>` | 使用当前 OVH 账户的默认支付方式支付订单 |

订单回复包含订单时间、订单号、状态、含税价格，以及 OVH 返回的到期时间和订单/PDF 链接（存在时）。`/pay` 只使用 API 返回的默认支付方式；账户没有可用默认方式时不会尝试其他方式。命令也支持中文别名：`/开始`、`/帮助`、`/账户`、`/切换账户`、`/重启`、`/库存`、`/抢购`、`/下单`、`/监控`、`/价格`、`/订单`、`/支付`。

### 飞书

在设置页填写：

| 配置 | 是否必填 | 说明 |
|------|----------|------|
| App ID | 必填 | 基础消息和卡片发送 |
| App Secret | 必填 | 基础消息和卡片发送 |
| Verification Token | 可选 | 事件身份附加校验；建议与 Encrypt Key 一起配置 |
| Encrypt Key | 必填（使用 HTTP 回调时） | 请求签名、时间戳新鲜度和加密事件解密 |

回调地址：

```text
https://你的域名/api/feishu/events
https://你的域名/api/feishu/card-action
```

基础通知需要 App ID 和 App Secret；使用 HTTP 事件/卡片回调时必须配置 Encrypt Key。首次私聊绑定还应在 `FEISHU_ALLOWED_OPEN_IDS` 中列出允许的 `open_id`，已有绑定不会被其他用户覆盖。

飞书私聊同样支持 `/reboot` 交互卡片；服务器按钮会显示数据中心国旗、三字母机房代码和自定义名称。

### QQ Bot v2

在设置页进入「QQ 机器人」，手动填写 QQ 开放平台的 `AppID` 和 `AppSecret`，再按目标类型填写 ID：

| 目标 | 字段 | 通知范围 |
|------|------|----------|
| 用户 | `qqUserOpenIds` | 完整通知；同时作为私聊管理员白名单，可执行全部机器人命令和中文别名 |
| 群聊 | `qqGroupOpenIds` | 仅接受 `/stock`、`/price`、`/库存`、`/价格`；同时接收独服/VPS 监控上架、下架提醒 |
| 频道 | `qqChannelTargets` 的 `channelId`（可选 `guildId`） | 仅独服/VPS 监控上架、下架提醒 |

QQ Gateway 交互命令要求机器人订阅私聊/群聊事件。管理员私聊支持现有完整命令集，包括 `/order`、`/pay`、账户切换、`/reboot` 和全部中文别名；群聊只处理库存和价格命令。群聊通常需要在 QQ 中 `@` 机器人，QQ 平台投递的消息会回复到原用户或群聊并引用原消息。QQ 不提供普通 QQ 号码给 Bot API，管理员身份必须填写 `user_openid`。`/reboot` 在 QQ 中使用编号选择以及 `/reboot confirm`、`/reboot cancel` 文本确认。

后端通过 `POST /api/qq/test` 发送配置测试消息，所有 QQ 管理接口均要求 `X-API-Key`。`AppSecret` 由现有 AES-GCM 配置加密链路保存，设置 API 只返回 `qqAppSecretConfigured`，不会返回密钥或 access token。当前版本不提供二维码扫描或自动回填参数；频道发送使用 QQ 官方 `/channels/{channel_id}/messages` 接口，客户端会按官方要求先建立 Gateway WebSocket、完成 Identify/READY 并维持心跳，同时受机器人权限、在线状态和频控限制。

微信运行时登录、命令和下单入口已退役；历史微信 SQLite 表和数据仍保留，仅用于迁移/历史兼容，不作为当前通知通道。

## 本地开发

### Windows

依赖：Go 1.25、Node.js 20、npm。

```powershell
.\scripts\init-first-run.ps1
npm install
```

推荐一键启动（后端会在 `19998` 端口后台运行，Vite 前端运行在 `8080`）：

```powershell
npm run dev:all
```

停止后台 Go 服务：

```powershell
.\scripts\start-backend.ps1 -Stop
```

启动后端：

```powershell
npm run backend
```

另开终端启动前端：

```powershell
npm run dev
```

开发地址通常为 `http://127.0.0.1:8080`。前端开发模式通过 API 代理访问后端；默认 Go 开发构建只提供 API，不嵌入前端。

如需直接调试 Go 进程，也可以执行 `cd backend; go run .`。后端重编译使用 `npm run backend:rebuild`。

### Linux / macOS

```bash
chmod +x scripts/init-first-run.sh
./scripts/init-first-run.sh
npm install
```

在一个终端启动后端，另开终端启动前端：

```bash
cd backend && go run .
```

另开终端执行：

```bash
npm run dev
```

前端开发服务器为 `http://127.0.0.1:8080`，会将 `/api` 和 `/health` 代理到 `http://127.0.0.1:19998`。如后端地址不同，可设置 `VITE_API_PROXY`。

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
| 队列、历史、监控订阅、微信历史表 | SQLite `/data` |
| 日志和缓存 | `/data/logs`、`/data/cache` |
| Docker 数据目录 | `./data` 绑定到容器 `/data` |

直接运行后端时，`DATA_DIR` 默认是 `backend/data`；Docker Compose 使用项目根目录的 `./data`。两种模式不要共用同一个 SQLite 文件，避免并发写入。

不要把以下内容提交到 Git：`.env`、`backend/.env`、`backend/data/`、OVH 凭据、Telegram Token、飞书 App Secret、QQ AppSecret 或 access token。微信历史表中的旧密文同样不应导出。

开发/测试脚本支持以下环境变量：

| 变量 | 用途 |
|------|------|
| `VITE_API_PROXY` | Vite 开发服务器代理的后端地址 |
| `SMOKE_BASE` | 烟测/全功能测试的服务地址，默认 `http://127.0.0.1:19998` |
| `API_SECRET_KEY` | 烟测请求使用的访问密钥 |
| `OVH_APP_KEY`、`OVH_APP_SECRET`、`OVH_CONSUMER_KEY` | 烟测在无账户时可选创建测试账户 |
| `SMOKE_ALLOWED_SERVER` | 全功能测试可选的只读目标服务器 |

## 本地验证

提交前按改动范围运行：

```powershell
npm run lint
npm run build
npm run test:unit:backend
```

涉及 API、鉴权、账户、监控或通知时，先启动后端并设置与 `backend/.env` 一致的密钥：

```powershell
$env:API_SECRET_KEY="与 backend/.env 一致的密钥"
python scripts/smoke_test.py
```

`npm run test:full` 会执行更完整的只读接口检查；它可能访问 OVH API，只能使用明确授权的测试账户和目标，脚本不会执行重启、重装、删除、终止等破坏性操作。

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

除健康检查、版本检查和 Webhook 等白名单路径外，API 需要以下请求头。官方前端和仓库内烟测会自动生成这些值；脚本客户端必须按 `METHOD\nEscapedPath?RawQuery\nTimestamp\nNonce\nBody` 生成 HMAC-SHA256 hex 签名：

```http
X-API-Key: <API_SECRET_KEY>
X-Request-Time: <Unix milliseconds>
X-Request-Nonce: <unique nonce>
X-Request-Signature: <HMAC-SHA256 hex>
```

时间戳允许偏差最多 5 分钟，同一 nonce 只能使用一次。

主要接口：

- `/api/accounts`：账户管理；`/api/accounts/status`：账户状态查询
- `/api/servers`、`/api/catalog`、`/api/availability`：服务器目录与可用性
- `/api/realtime-availability`、`/api/preadded-servers`：实时可用性和预添加服务器
- `/api/cache/*`、`/api/system/metrics`、`/api/version/*`：缓存、系统指标和版本信息
- `/api/queue`、`/api/queue/quick-order`：抢购队列
- `/api/monitor/*`：独服监控
- `/api/vps-monitor/*`：VPS 监控
- `/api/server-control/*`：已购独服控制
- `/api/vps-control/*`：已购 VPS 控制
- `/api/feishu/*`：飞书绑定、事件和卡片
- `/api/telegram/*`：Telegram Webhook、命令和下单
- `/api/qq/test`：QQ Bot v2 测试通知
- 微信历史表仍由启动迁移代码维护，但不再提供微信登录、命令或通知 API

后台在运行主机的每个整点并行刷新完整服务器目录和实时可用性批次。预添加服务器只使用同一批次在线获取的区域实时可用性与区域公开目录进行比对；完整服务器目录独立提交，不参与或覆盖该批次。任一刷新失败时保留对应的上一份成功数据。

完整接口说明见 [`docs/handover/03-API-CONTRACT.md`](./docs/handover/03-API-CONTRACT.md)。

## 目录

```text
OVH_WEBUI/
├── backend/                         # Go API、监控、队列和 OVH 客户端
├── src/                             # React 前端
├── agent.md                         # 开发协作与验证约定
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
- [`agent.md`](./agent.md)：面向自动化协作和日常开发的项目约定

## 许可证与声明

自用运维工具。调用 OVH 官方 API 时请遵守 OVH 服务条款和当地数据保护要求。本项目不保证库存可用性或抢购成功率。
