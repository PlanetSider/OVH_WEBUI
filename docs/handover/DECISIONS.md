# 架构决策记录 (ADR)

## ADR-001：后端以 Go 为准，不用 Python 重构

- **状态**：Accepted  
- **背景**：OVH-BUY 为 Flask 单文件巨石；ovh/server 已模块化且覆盖服务器/VPS 控制。  
- **决策**：`OVH_WEBUI/backend` = 从 `ovh/server` 迁入并改 module 为 `github.com/ovh-webui/server`。  
- **后果**：Config Sniper 暂缺；需按 Go 契约改前端。

## ADR-002：前端保留 OVH_WEBUI 视觉，不整站换 ovh/web

- **状态**：Accepted  
- **背景**：OVH_WEBUI 已有 dashboard 布局与终端风组件。  
- **决策**：路由/UI 以 OVH_WEBUI 为准；API/能力对齐 Go。  
- **后果**：server-control 细节组件可逐步从 ovh/web 移植。

## ADR-003：线上巡检取消

- **状态**：Accepted — **功能已移除**  
- **背景**：产品不需要独立巡检模块。  
- **决策**：删除 `/api/inspection/*`、前端页面与导航；测试与运维改用服务器控制只读接口。

## ADR-004：配置绑定狙击 (config-sniper) 完全下线

- **状态**：Accepted — **永久下线**（非二期）  
- **背景**：Go 后端已删除 config_sniper 业务表与路由；产品不再提供该能力。  
- **决策**：不迁移、不重建、前端不暴露任何入口。  
- **后果**：历史 DB 列/注释可保留兼容；业务与文档均视为废弃功能。

## ADR-005：前后端分离 + Docker 单镜像

- **状态**：Accepted  
- **背景**：开发效率与嵌入式 UI 二选一。  
- **决策**：开发环境使用 Vite 代理；Docker 生产镜像使用 `-tags ui` 将前端嵌入 Go 二进制。
- **后果**：Compose 只需启动一个应用容器，不再依赖独立前端或 nginx 容器。

## ADR-006：统一前端 HTTP 为 axios（lib/http.ts）

- **状态**：Accepted  
- **背景**：曾并存 `api-client`（fetch）与 `http`（axios），错误处理 / `backendUrl` 不一致。  
- **决策**：
  - **唯一传输层**：`src/lib/http.ts`（axios + 鉴权 + 账户注入 + `backendUrl`）
  - **业务 facade**：`src/lib/api.ts`（语义化方法，底层 `apiRequest`）
  - **`api-client.ts`**：仅 re-export，禁止新增逻辑  
- **后果**：hooks 继续 `import { api } from "@/lib/http"`；旧页可用 `import { api } from "@/lib/api"`。

## ADR-007：通知主路径从微信迁移到 QQ Bot v2

- **状态**：Accepted — 本地代码与协议测试已验证，真实 QQ 凭据外部发送待部署时验证
- **背景**：原微信 iLink 二维码/长轮询/命令路径不再作为当前通知产品能力；QQ Bot v2 官方接口支持用户、群聊和频道消息发送。用户明确选择手填 AppID/AppSecret，不需要二维码扫描或自动回填。
- **决策**：以 `backend/internal/qqbot` 作为 QQ 唯一发送 owner。`AppSecret` 复用 `secret.Cipher` 加密配置；access token 仅进程内缓存并在过期前 60 秒刷新。设置 API 提供 `POST /api/qq/test`，不回传密钥。
- **路由**：普通订单、目录、抢购、代理和新服务器通知只发送 QQ 用户目标；独服/VPS 上架和下架监控通知发送用户、群聊和频道目标。没有用户目标但有群聊/频道目标时，普通消息视为策略性抑制，不无限重试。
- **退役**：删除微信二维码登录、长轮询、命令、快捷下单、状态和测试运行时及其 UI/API 入口。
- **保留**：`weixin_credentials`、`weixin_sync_state`、`weixin_context_tokens`、`weixin_seen_messages`、`weixin_runtime_locks` 及历史 rows 不删除；保留 store/types 和启动时密文迁移作为历史/迁移兼容边界。
- **后果**：旧微信 outbox 渠道快照规范化为 `qq`；旧微信管理 API 返回 404。QQ 频道发送仍受 QQ Bot 权限、在线状态、WebSocket/平台规则和频控约束。
