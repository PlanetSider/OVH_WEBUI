# QQ 下单与监控提醒界面

## Goal
在抢购导航中新增 QQ 下单页面；让 Telegram、飞书、QQ 三个下单页面的“命令参考”完整覆盖当前机器人命令及中文别名；当系统设置允许独服监控自动付款时，把“自动购买（付款）”放入“提醒方式”网格中，显示在“有货时自动下单”右侧。

## Approved Scope
- QQ 下单页面采用网页直接执行并返回结果，不向 QQ 发送网页执行结果。
- 复用现有 `TelegramOrderPage` 表单和命令分发语义，扩展渠道为 `qq`。
- 新增 `/api/qq/quick-order`，复用现有 `buildTelegramCommandArgs` 和 `dispatchBotCommand`，不新增订单数据模型或业务 owner。
- QQ 页面放在飞书下单下方；同步桌面侧栏、移动端更多菜单、顶部标题和路由。
- 监控 `autoPay` 字段、设置开关和后端校验保持不变，仅调整 `MonitorSubscriptionDialog` 的布局。

## Baseline / Owners
- 前端共用下单 owner：`src/pages/TelegramOrderPage.tsx`；`FeishuOrderPage.tsx` 是薄包装。
- 下单 API owner：`backend/internal/handlers/telegram_quick_order.go` 和 `backend/main.go` 路由。
- 命令语义 owner：`backend/internal/handlers/telegram_commands.go`、`backend/internal/telegram/commands.go`。
- 监控付款展示 owner：`src/components/common/MonitorSubscriptionDialog.tsx`；系统开关读取 `useSettings().data?.monitorAutoPayEnabled`。
- 导航 owner：`src/App.tsx`、`src/components/layout/AppSidebar.tsx`、`src/components/layout/MobileBottomNav.tsx`、`src/components/layout/TopBar.tsx`。

## Change Necessity / Ripple
现有页面没有 QQ 路由或 QQ quick-order API，且共用命令参考缺少 `/start`、`/help`、`/order`、`/pay` 和中文别名；仅改文案无法提供可用 QQ 页面，因此必须扩展现有渠道 owner和一个同构 HTTP endpoint。API 请求体和返回格式沿用已有 quick-order，不改变 Telegram/飞书消费者。监控只做同一表单网格的条件布局调整，保持持久化字段兼容。

## TDD Route
`auto -> strict`：新增公共 API、跨渠道 UI、共享命令参考和监控行为显示，先补后端 handler 测试/前端类型检查，再实现并运行完整回归。项目未要求严格 RED/GREEN，采用最小可验证实现与既有测试模式。

## Tasks
1. 抽取/扩展下单页面的渠道模型、QQ API 调用、QQ 状态文案和完整共享命令参考；新增 QQ 页面包装和路由/导航入口。
2. 增加 QQ quick-order handler 与路由，复用当前参数构造和共享命令分发；补充成功、参数错误和未知模式测试。
3. 调整独服监控弹窗提醒网格：开关允许时显示付款卡片在自动下单右侧，关闭时保持自动下单跨列；不改 payload。
4. 运行后端测试、前端类型检查、构建和补丁检查，审查 TG/飞书兼容性。
5. 提交并推送单一功能提交，回读 HEAD、远端和工作区状态。

## Compatibility / Retirement
- Telegram/飞书现有路由和 API 保持原路径与返回格式；QQ 新增路径，不迁移或删除旧路径。
- `autoPay` 旧字段和未开启设置时的行为保留；原独立付款卡片布局被同功能网格卡片替代，旧视觉路径不再保留。

## Verification
- `go test ./... -count=1`
- `go vet ./...`
- `npx tsc -b`
- `npm run build`
- `git diff --check`
- 重点检查三个下单路由、完整命令参考、QQ 导航入口和监控付款卡片响应式布局。

## Risks
- 未使用真实 QQ/Telegram/飞书凭据发送外部消息。
- 未执行真实 OVH 下单或支付；测试只验证参数校验和业务分发路径。
- 前端构建可能保留既有 chunk 大小提示。
