# API 契约摘要

## 鉴权

- 受保护 API 必须携带：`X-API-Key: <API_SECRET_KEY>`、`X-Request-Time: <Unix milliseconds>`、`X-Request-Nonce: <unique nonce>`、`X-Request-Signature: <HMAC-SHA256 hex>`
- 签名原文为 `METHOD\nEscapedPath?RawQuery\nTimestamp\nNonce\nBody`，其中 Body 是实际发送的原始字节；时间戳偏差超过 5 分钟或重复 nonce 会被拒绝
- 白名单免 API 鉴权：`/health`, `/api/health`, `/api/version`, `/api/version/check-update`, `/api/telegram/webhook`, `/api/feishu/events`, `/api/feishu/card-action`；Webhook 必须通过各自协议校验

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

### 服务器询价

- `POST /api/servers/:planCode/price`：请求体可传 `accountId`、`datacenter`、`options`；未传 `accountId` 时使用默认账户。仅创建临时购物车询价，不下单。
- 成功响应的 `price.prices.withTax` 为购物车含税总价，`price.prices.currencyCode` 为 OVH 返回的币种，`price.duration` 为基础商品实际计价周期（如 `P1M`/`P12M`）。默认 `P1M/default` 被 OVH 拒绝时才查询该购物车的 Eco 计价并最多重试一次；非月付周期不可按月费展示。

### 抢购

- `GET/POST /api/queue` · `DELETE /api/queue/:id` · `DELETE /api/queue/clear`
- `PUT /api/queue/:id/status`
- `POST /api/queue/quick-order`
- `GET/DELETE /api/purchase-history`

### 监控

- `/api/monitor/*` 独服
- `/api/vps-monitor/*` VPS
- 监控通知支持 Telegram、飞书或 QQ；飞书可用性通知按内存/存储配置聚合并提供卡片入队按钮。QQ 用户目标接收完整通知；群聊和频道目标只接收独服/VPS 上架、下架监控提醒。
- 独服监控（/api/monitor/*）支持有货变化后的自动下单；VPS 监控（/api/vps-monitor/*）仅发送库存通知，不支持自动下单。
- 创建或更新 VPS 订阅不接受 autoOrder、quantity、autoOrderAccountId 字段；旧数据库中的 auto_order_account_id 仅为兼容保留列，不再使用。

### 飞书

- `POST /api/feishu/events`：事件订阅与绑定账户
- `POST /api/feishu/card-action`：交互卡片回调
- `GET/DELETE /api/feishu/binding`
- `POST /api/feishu/test-card`
- 基础通知只需 `feishuAppId` 与 `feishuAppSecret`；HTTP 事件/卡片回调必须配置 `feishuEncryptKey`，可额外配置 `feishuVerificationToken` 做身份校验

### QQ Bot v2

- `GET/POST /api/settings`：配置 `qqAppId`、`qqAppSecret`、`qqNotificationsEnabled`、`qqUserOpenIds`、`qqGroupOpenIds`、`qqChannelTargets`；读取响应只返回 `qqAppSecretConfigured`，不返回 AppSecret 或 access token
- `POST /api/qq/test`：向所有已配置 QQ 目标发送测试消息；要求 `X-API-Key`
- Token：后端调用 `POST https://api.bot.qq.com/app/getAppAccessToken`，使用 `Authorization: QQBot <access_token>`；token 只在服务端内存缓存并在过期前 60 秒刷新
- 发送端点：用户 `/v2/users/{user_openid}/messages`、群聊 `/v2/groups/{group_openid}/messages` 使用 `{ "msg_type": 0, "content": "..." }`；命令回复额外携带原消息 `msg_id`；频道 `/channels/{channel_id}/messages` 使用 `content` 字段。频道发送和入站命令均按 QQ 官方协议建立 `/gateway` WebSocket，完成 `QQBot <access_token>` Identify、READY 和心跳保持在线。
- 入站命令：Gateway 订阅 `C2C_MESSAGE_CREATE`、`GROUP_AT_MESSAGE_CREATE`/`GROUP_MESSAGE_CREATE`。`qqUserOpenIds` 是私聊管理员白名单，管理员可执行完整命令；`qqGroupOpenIds` 是群聊白名单，群聊只允许 `/stock`、`/price`、`/库存`、`/价格`。消息回复引用原 `msg_id`；QQ 不向 Bot API 提供普通 QQ 号码，只使用 OpenID。
- 路由：普通通知只发用户；独服/VPS 上架和下架监控通知发用户、群聊和频道。仅配置群聊/频道时普通通知视为策略性抑制，不进入无限重试
- 当前版本手动填写 AppID/AppSecret，不提供二维码扫描或自动回填。微信登录、命令和通知 API 已退役；微信 SQLite 历史表和数据保留用于迁移/历史兼容

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
