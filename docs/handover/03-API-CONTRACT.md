# API 契约摘要

## 鉴权

- 启动时 `API_SECRET_KEY` 支持 32 字节随机值编码成的 64 位十六进制密钥（推荐由初始化脚本或 `openssl rand -hex 32` 生成）；其他格式保持 8–256 个字符且同时含英文大写、小写和数字。长度/字符格式校验不能验证随机性，生产密钥必须使用密码学安全随机源生成。
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

### Bot 抢购列表

- Bot 命令 `/list`（中文别名 `/列表`）只读列出全部 OVH 账户状态为 `pending` 或 `running` 的抢购队列；排除暂停、完成、失败、停售和 `ProxyGuardPaused` 任务。按账户、型号、选项和自动付款聚合，显示各机房数量、规格和单台价格；无任务回复“当前没有开启的抢购任务。”，长结果分条回复当前会话。
- `/list` 沿用 Telegram/飞书授权和 QQ 私聊白名单；QQ群继续只允许 `/stock`、`/price`、`/库存`、`/价格`，拒绝 `/list` 和 `/列表`，不披露任务列表。命令只读队列并使用公开 catalog 的单台未税价格；总价为单台首月月费加安装费，缺失时显示 `暂不可用`。价格查询共用 5 秒超时，超时不会省略任务或数量；不创建购物车、不下单、不发送通知广播。

### 定时任务播报

- `GET/POST /api/settings` 支持 `taskBroadcastEnabled`、`taskBroadcastTime`（北京时间 `HH:mm`）、`taskBroadcastQueueEnabled`、`taskBroadcastMonitorEnabled`、`taskBroadcastVpsEnabled` 和 `taskBroadcastReportEnabled`。
- 定时总开关默认关闭；四个内容开关分别控制抢购任务、独服监控、VPS 监控和按账号统计的“抢购战报”。未提供字段的部分更新保留旧值，时间非法返回 `400`。
- 每次服务启动都会独立播报当前三类运行任务一次，不受定时总开关和四个内容开关影响；启动播报不包含“抢购战报”。每日定时播报按北京时间执行，战报统计最近 24 小时成功下单并按 OVH 账号列出未支付/已支付订单数和金额。
- 任务消息分别发送，标题后只有一个空行；`型号`、`Plan Code`、`月费`、`安装费`、`总价` 为独立字段，后三项紧跟在 `数据中心` 后，`自动付款` 为最后一行。价格使用含税公开 catalog 拆分，缺失时显示 `暂不可用`。

- `POST /api/feishu/events`：事件订阅与绑定账户
- `POST /api/feishu/card-action`：交互卡片回调
- `GET/DELETE /api/feishu/binding`
- `POST /api/feishu/test-card`
- 基础通知只需 `feishuAppId` 与 `feishuAppSecret`；HTTP 事件/卡片回调必须配置 `feishuEncryptKey`，可额外配置 `feishuVerificationToken` 做身份校验

### QQ Bot v2

- `GET/POST /api/settings`：配置 `qqAppId`、`qqAppSecret`、`qqNotificationsEnabled`、`qqUserOpenIds`、`qqGroupOpenIds`、`qqChannelTargets`；读取响应只返回 `qqAppSecretConfigured`，不返回 AppSecret 或 access token
- `POST /api/qq/test`：向所有已配置 QQ 目标发送测试消息；要求 `X-API-Key`
- `POST /api/qq/registration/start`：创建 QQ 官方扫码绑定会话，服务端向 `q.qq.com/lite/create_bind_task` 请求任务；响应返回 `sessionId`、官方二维码 URL、`expiresIn` 和 `interval`，不返回绑定密钥或 AppSecret
- `GET /api/qq/registration/:sessionId`：服务端轮询 `q.qq.com/lite/poll_bind_result`；完成后服务端使用 AES-256-GCM 解密并保存 AppID/AppSecret，将扫码者 `user_openid` 去重加入管理员白名单并启用 QQ 通知。响应只返回 `appId`、`appSecretConfigured`、`userOpenId` 和绑定状态，不返回 AppSecret
- `POST /api/qq/quick-order`：QQ 下单页面直接执行与 QQ Bot 相同的 `/stock`、`/queue`、`/buy`、`/monitor`、`/price` 命令语义；要求完整 API 请求签名，不向 QQ 发送网页执行回执。请求体沿用 `{ "mode": "stock", "planCode": "24ska01", "datacenter": "gra", "quantity": 1, "options": [] }` 字段；响应返回 `success`、`message`/`error`、`mode` 和生成的 `command`。
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
