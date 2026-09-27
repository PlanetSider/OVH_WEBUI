# 任务进度

> 最后更新：QQ Bot v2 通道替换（手工 AppID/AppSecret）

## 总览

| 阶段 | 状态 | 说明 |
|------|------|------|
| P0 monorepo 脚手架 | ✅ | backend + frontend + docs + docker |
| P1 Go 后端迁入 | ✅ | `github.com/ovh-webui/server` |
| P2 线上巡检 | ❌ 已取消 | 前后端已移除（ADR-003） |
| P3 前端 API 对齐 | ✅ | **统一** `lib/http.ts`（axios）；`api.ts` facade |
| P4 单元测试 | ✅ | catalog / numconv |
| P5 烟测 | ✅ | 仅 ns529169 约束；inspection 断言 404 |
| P6 交接文档 | ✅ | docs/handover/*（无巡检残留误导） |
| P7 **ovh/web 全量移植** | ✅ | hooks + server-control + vps-control + 全页面 |
| P8 TypeScript 零错误 | ✅ | `tsc -b` EXIT 0 |
| P9 Config Sniper | ❌ | **完全下线**（ADR-004） |
| P10 全功能测试 | ✅ | **54/54 PASS** |
| P11 缺口补全 | ✅ | 订单 / 询价 / 订阅 PUT |
| P12 HTTP 统一 | ✅ | 单一 axios 传输层 + backendUrl |
| P13 文档清理 | ✅ | inspection 文档改为「已取消」说明 |

## P14 QQ Bot v2 通道替换（✅ 本地实现与验证完成）

- QQ 客户端：`backend/internal/qqbot`，支持 token 缓存、过期前 60 秒刷新、HTTP 200 token `code` 错误、OpenAPI `err_code`、Gateway WebSocket Identify/READY/心跳和断线重连。
- 设置：手填 AppID/AppSecret；AppSecret 通过 AES-GCM 配置链路加密；设置响应不回传密钥。
- 路由：用户接收完整通知；群聊/频道仅接收独服和 VPS 上下架监控提醒；仅群聊/频道时普通通知策略性抑制。
- 微信：删除二维码、轮询、命令、快捷下单和发送运行时；保留 `weixin_*` 表、历史数据、store/types 和密文迁移。
- 验证：QQ 专项测试（含 Gateway 握手/重连）、`go test ./... -count=1`、`npx tsc -b`、`npm run build` 已通过；`go test -race` 受当前 Windows ThreadSanitizer 地址空间错误限制，未使用真实 QQ 凭据发送外部消息。


```
/  /servers  /queue  /history
/monitor  /vps-monitor
/server-control  /vps-control  /performance
/account  /contact-change  /settings  /logs  /telegram-order
```

### 访问

- 前端：http://127.0.0.1:8080
- 后端：http://127.0.0.1:19998
- 登录 API Key：`<api-secret-from-env>`
- 一键：`scripts/start-dev.ps1`

## 切片日志

### 统一 HTTP + 文档清理（2026-07-11）

1. `src/lib/http.ts`：axios + backendUrl + apiRequest + 鉴权/账户注入  
2. `api-client.ts` → 兼容 re-export；`api.ts` 仅业务 facade  
3. AuthGate verify 走 `resolveAbsoluteUrl`  
4. 清理 README / handover 中 inspection 误导路径  
5. `package.json` test:unit:backend 去掉 inspection  

### 全功能测试（2026-07-11）

- 脚本：`scripts/full_functional_test.py`
- 报告：`docs/handover/FULL-TEST-RESULTS.md`
- **Config Sniper / 巡检：404 验证**
- **未测**：暂停/关机/重启/重装/删除/终止

## 可选后续

1. CI 流水线  
2. 嵌入式 UI `-tags ui` 可选打包  
3. PWA / 安装到主屏  
4. ContactChangePage 统一 Token Dialog  
