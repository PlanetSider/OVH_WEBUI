# 运行手册

## 前置

- Go 1.22+
- Node 20+
- Python 3（可选烟测）

## 首次部署

```powershell
.\scripts\init-first-run.ps1
# 全新数据：
.\scripts\init-first-run.ps1 -Fresh
```

会生成 `backend/.env`（随机 `API_SECRET_KEY`）与空 `data/`。

## 本地后端

```powershell
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
cd backend
go run .
# :19998 ，自动加载 .env
```

## 本地前端

```powershell
cd ..
npm install
npm run dev
# http://127.0.0.1:8080
```

登录使用 init 打印的 API 密钥。可选在根目录 `.env.local`（gitignore）：

```
VITE_DEV_API_KEY=<同 backend API_SECRET_KEY>
```

仅开发环境会预填，生产包不会。

## 配置 OVH

仅前端「设置 → OVH 账户」或仓库签名脚本访问账户接口。受保护 API 除 `X-API-Key` 外还必须带时间戳、唯一 nonce 和绑定原始请求体的 HMAC 签名，直接复制只带 API key 的 curl 会返回 401。可用烟测先验证连接：

```powershell
$env:API_SECRET_KEY = "与 backend/.env 一致的密钥"
python scripts/smoke_test.py
```

脚本按 `METHOD\nEscapedPath?RawQuery\nTimestamp\nNonce\nBody` 生成 HMAC-SHA256 hex 签名。

## Docker / Linux 生产

见 [DEPLOY.md](../DEPLOY.md)。

```bash
chmod +x scripts/docker-deploy.sh
./scripts/docker-deploy.sh
```

## 环境变量速查

| 变量 | 含义 | 默认 |
|------|------|------|
| PORT | 端口 | 19998 |
| API_SECRET_KEY | 网关密钥 | **必填** |
| TG_WEBHOOK_SECRET | Telegram Webhook 密钥；为空时首次启动随机生成并落盘 | 空时自动生成并落盘 |
| TG_WEBHOOK_SECRET_OPTIONAL | 必须为 false；不允许关闭 Webhook 密钥校验 | false |
| DATA_DIR | 数据目录 | data（Compose 绑定 `./data`） |

**已废弃（勿再配置）**：`INSPECTION_ALLOWLIST`、`ALLOW_FULL_INSPECTION`。

## 重新初始化

```powershell
.\scripts\init-first-run.ps1 -Fresh -ForceEnv
```

旧 `data/` 与 `.env` 会带时间戳备份。
