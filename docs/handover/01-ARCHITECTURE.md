# 架构说明

## 总览

```
OVH_WEBUI/
├── backend/                 # Go 服务 (module: github.com/ovh-webui/server)
│   ├── main.go              # 路由装配、后台队列/监控
│   └── internal/
│       ├── app/             # 运行时 State（账户/队列/缓存）
│       ├── auth/            # X-API-Key 中间件
│       ├── catalog/         # 目录标准化 + 存储解析
│       ├── config/          # KV 配置
│       ├── db/              # SQLite 持久化
│       ├── handlers/        # HTTP handlers（按域拆分）
│       ├── monitor/         # 独服可用性监控
│       ├── ovh/             # OVH client 工厂（多账户）
│       ├── purchase/        # 抢购队列处理器
│       ├── qqbot/            # QQ Bot v2 token 缓存与三类目标发送
│       ├── telegram/         # TG 通知/下单
│       ├── types/             # 共享 DTO
│       ├── weixin/            # 历史 store/types 与密文迁移兼容，不参与运行时通知
│       └── vps/               # VPS 相关
├── src/                     # React 前端
│   ├── components/          # layout / dashboard / server-control / vps-control / ui
│   ├── hooks/ovh/           # React Query hooks（主路径）
│   ├── hooks/useApi.ts      # 旧 facade hooks（走 lib/api）
│   ├── lib/http.ts          # ★ 统一 axios 传输层
│   ├── lib/api.ts           # 业务 API facade（底层 → http）
│   ├── lib/api-client.ts    # 兼容 re-export → http
│   └── pages/               # 路由页面
├── scripts/                 # smoke / full_functional_test
├── docs/handover/           # 交接记忆
├── Dockerfile               # 前后端一体镜像
└── docker-compose.yml       # 单镜像部署
```

## 设计原则

| 原则 | 实践 |
|------|------|
| 模块化 | 后端按 package 分域；handlers 不直接写业务 SQL |
| 多账户 | `ovh_accounts` + `ClientFor(accountID)` |
| 安全默认 | API Key 鉴权；写操作需显式确认 |
| 可测试 | 纯逻辑包（catalog、numconv）优先 TDD |
| 前端可替换 | 所有能力经 `/api/*`；Vite 开发代理同源 |
| 单一传输层 | 仅 `lib/http.ts` 发 HTTP（axios + backendUrl） |

## 前端 HTTP 分层

| 层 | 文件 | 职责 |
|----|------|------|
| 传输 | `lib/http.ts` | axios 实例、`apiRequest`、鉴权/账户/backendUrl |
| 业务 facade | `lib/api.ts` | `api.getStats()` 等语义化方法 |
| Hooks | `hooks/ovh/*` | React Query，直接用 axios `api` |
| 兼容 | `lib/api-client.ts` | re-export http，勿新增逻辑 |

## 运行时

- 默认端口：`19998`
- 数据：项目目录 `./data`（SQLite + logs/cache），Compose 直接绑定到容器 `/data`
- 队列处理器：启动时 `go purchase.ProcessQueueLoop`
- 监控：有订阅时自动 Start
- 数据刷新：运行主机每个整点并行刷新完整服务器目录与实时可用性批次。预增服务器仅使用当批在线获取的区域实时可用性和区域公开目录比对并原子保存；完整目录独立更新内存缓存与 SQLite，失败时各自保留上一份成功数据。

## 通知通道责任

- `monitor/feishu.go` 暴露统一的 QQ 普通通知和 QQ 监控通知 helper；`qqbot.Client` 是 QQ Bot v2 的唯一发送 owner。
- 普通通知（订单状态、购买成功、目录/代理事件、新服务器）只调用 `SendDefault*`，因此只到 QQ 用户 OpenID。
- 独服和 VPS 的上架/下架可用性提醒调用 `SendMonitor*`，发送到用户、群聊和频道目标。
- QQ AppSecret 通过 `secret.Cipher` 加密，access token 只存在进程内；设置响应只返回已配置布尔值。
- `internal/weixin/store.go` 与历史 `weixin_*` 表保留用于迁移/历史兼容；二维码、轮询、命令和主动发送运行时已删除。
## 前端路由

| 路径 | 页面 |
|------|------|
| `/` | 仪表盘 |
| `/servers` | 可购服务器列表 |
| `/queue` `/history` | 抢购队列/历史 |
| `/monitor` `/vps-monitor` | 库存监控 |
| `/server-control` `/vps-control` | 已购独服 / VPS 控制 |
| `/performance` | 流量/性能 |
| `/account` `/contact-change` | 账户与联系人 |
| `/settings` `/logs` | 设置与日志 |
| `/telegram-order` | TG 下单辅助 |

> ~~`/inspection`~~ 已删除（ADR-003）。
