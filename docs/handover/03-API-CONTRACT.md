# API 契约摘要

## 鉴权

- Header：`X-API-Key: <API_SECRET_KEY>`
- 可选：`X-Request-Time`（毫秒时间戳，偏差 >5 分钟拒绝）
- 白名单免鉴权：`/health`, `/api/health`, `/api/version`, `/api/telegram/webhook`, `/api/feishu/events`, `/api/feishu/card-action` 等

## 多账户

- 控制类接口支持 `?account=<accountId>`
- 前端：`localStorage.ovh_active_server_control_account_id`
- 空 account → 默认账户

## 核心分组

### 系统

- `GET /health`
- `GET /api/stats`
- `GET /api/system/metrics`
- `GET /api/logs` / `DELETE` / `POST /flush`

### 账户

- `GET/POST /api/accounts`
- `PUT/DELETE /api/accounts/:id`
- `POST /api/accounts/:id/set-default`
- `POST /api/accounts/:id/verify`
- `GET /api/accounts/status`：并发验证全部 OVH 账户并返回 `/me` 基础资料
- `GET /api/ovh/account/info|bills|refunds|credit-balance|email-history|sub-accounts`
- `GET /api/ovh/contact-change-requests` + accept/refuse/resend-email

### 抢购

- `GET/POST /api/queue` · `DELETE /api/queue/:id` · `DELETE /api/queue/clear`
- `PUT /api/queue/:id/status`
- `POST /api/queue/quick-order`
- `GET/DELETE /api/purchase-history`

### 监控

- `/api/monitor/*` 独服
- `/api/vps-monitor/*` VPS
- 监控通知支持 Telegram 或飞书；飞书可用性通知按内存/存储配置聚合，并提供卡片入队按钮

### 飞书

- `POST /api/feishu/events`：事件订阅与绑定账户
- `POST /api/feishu/card-action`：交互卡片回调
- `GET/DELETE /api/feishu/binding`
- `POST /api/feishu/test-card`
- 基础发送配置只需 `feishuAppId` 与 `feishuAppSecret`；`feishuVerificationToken`、`feishuEncryptKey` 为事件回调安全项，使用回调/按钮交互时建议配置

### 服务器控制

- 前缀：`/api/server-control`
- 摘要：`GET /:serviceName/summary`
- 列表：`GET /list`
- 电源/重装/硬件/网络/IPMI/防火墙/BackupFTP/engagement/mitigation/...

### VPS 控制

- 前缀：`/api/vps-control`

### 已下线（返回 404）

| 前缀 | 说明 |
|------|------|
| `/api/inspection/*` | 线上巡检已取消（ADR-003） |
| `/api/config-sniper/*` | Config Sniper 已下线（ADR-004） |

运维只读请用：`/api/server-control/:serviceName/*`（hardware / serviceinfo / ips 等）。

## 错误格式

```json
{ "error": "...", "message": "...", "code": "NO_API_KEY" }
```

业务层常见：

```json
{ "success": false, "error": "..." }
```
