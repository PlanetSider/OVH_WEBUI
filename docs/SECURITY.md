# 安全与隐私

## 切勿提交到 Git

| 路径 / 内容 | 原因 |
|-------------|------|
| `backend/.env` | `API_SECRET_KEY` 等 |
| 根目录 `.env` | Docker 编排密钥 |
| `backend/data/` | SQLite 中含 OVH 账户 AK/AS/CK、队列、历史 |
| OVH App Key / Secret / Consumer Key | 可操作账户资源 |
| Telegram Bot Token | 可冒用 Bot |
| QQ Bot AppSecret / access token | 可冒用 QQ 机器人；access token 仅允许存在服务端内存 |
| 真实独服主机名（公开仓库） | 资产情报（可选脱敏） |

`.gitignore` 已覆盖 `.env` 与 `backend/data/`。

QQ Bot v2 的 `AppSecret` 通过 `secret.Cipher` AES-GCM 加密保存在配置中；`GET /api/settings` 只返回 `qqAppSecretConfigured`，`POST /api/qq/test` 及设置写入均要求带请求签名的 `X-API-Key`。access token 只存在服务端进程内存，错误和日志不得输出 AppSecret 或 token。

微信运行时入口已退役，但 `weixin_credentials`、`weixin_sync_state`、`weixin_context_tokens`、`weixin_seen_messages`、`weixin_runtime_locks` 历史表和数据不删除；启动时只保留必要的历史密文迁移。生产环境应限制 `/data` 的文件权限并纳入加密备份策略。

## 首次部署

```powershell
.\scripts\init-first-run.ps1          # 生成随机 API_SECRET_KEY
.\scripts\init-first-run.ps1 -Fresh   # 并清空 runtime 数据（会备份）
```

登录页使用脚本打印的密钥；在 UI 中添加 OVH 账户，**不要**把 AK/AS/CK 写进仓库或示例文件。

## 已下线 / 无效配置

- `INSPECTION_ALLOWLIST` / `ALLOW_FULL_INSPECTION`：巡检已删除，配置无效，样板中已移除。

## 烟测

仅通过环境变量注入密钥与可选目标机：

```powershell
$env:API_SECRET_KEY="..."
$env:OVH_APP_KEY="..."
$env:OVH_APP_SECRET="..."
$env:OVH_CONSUMER_KEY="..."
$env:SMOKE_ALLOWED_SERVER="..."   # 可选，写操作测试约束
python scripts/smoke_test.py
```

脚本内**无**硬编码 OVH 密钥或固定生产主机名。

## 前端

- 生产构建不会预填 API Key。
- 开发可选：`VITE_DEV_API_KEY` 写在**本地** `.env.local`（gitignore）。
