# Docker Compose 部署

项目发布的是前后端一体镜像，Compose 只启动一个应用容器。容器自带 React 页面、Go API、SQLite 所需目录和健康检查，不需要额外的前端、后端或网关容器。

## 首次部署

要求：Linux、Docker Engine、Docker Compose v2。

```bash
mkdir -p /opt/ovh-webui
cd /opt/ovh-webui
git clone https://github.com/PlanetSider/OVH_WEBUI.git .
cp .env.example .env
sed -i "s/^API_SECRET_KEY=.*/API_SECRET_KEY=$(openssl rand -hex 32)/" .env
mkdir -p data
# 镜像使用非 root 用户运行；Linux 主机首次部署时将 data 目录交给容器用户
sudo chown -R 100:100 data
docker compose pull
docker compose up -d
docker compose ps
```

打开 `http://服务器IP:19998`，使用 `.env` 中的 `API_SECRET_KEY` 登录。

如果 GHCR 镜像是私有的，先执行 `docker login ghcr.io`。

## 配置

```dotenv
API_SECRET_KEY=强随机密钥
TG_WEBHOOK_SECRET=可选的随机密钥
TG_WEBHOOK_SECRET_OPTIONAL=false
```

容器内部和宿主机都使用 `19998` 端口。需要 HTTPS 时，在容器前使用已有的反向代理，并将流量转发到该端口；应用本身不申请证书。

数据直接绑定到项目目录的 `./data`（Linux 首次部署需确保容器用户可写）：

- SQLite 数据库和 OVH 账户凭据：`/data`
- 日志：`/data/logs`
- 缓存：`/data/cache`

不要提交 `.env`、OVH 凭据、Telegram Token 或飞书 App Secret。

## 更新与运维

```bash
cd /opt/ovh-webui
git pull --ff-only
docker compose pull
docker compose up -d
docker compose ps
```

```bash
# 查看日志
docker compose logs -f --tail=200 ovh-webui

# 重启
docker compose restart ovh-webui

# 健康检查
curl -fsS http://127.0.0.1:19998/health

# 停止
docker compose down
```

## Telegram / 飞书回调

应用回调地址使用反向代理提供的 HTTPS 域名：

```text
https://你的域名/api/telegram/webhook
https://你的域名/api/feishu/events
https://你的域名/api/feishu/card-action
```

反向代理需要保留 `/api/*` 和 `/health` 路径，并将请求转发到应用容器的 `19998` 端口。
