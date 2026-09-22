# 上游可靠性与安全能力选择性移植计划

## 目标

从 `coolci/OVH_WEBUI` 选择性移植以下能力到当前 `PlanetSider/OVH_WEBUI`：

1. `ParseTS` 时间解析，兼容无时区 `NowISO` 与 RFC3339。
2. 启动加载失败保护，禁止空内存快照覆盖无法读取的 SQLite 表。
3. 临时错误分类，仅应用于库存查询、购物车准备等可安全重试阶段；保留 checkout 不确定性隔离。
4. 订单支付状态后台轮询，接入当前 history 与通知 outbox。
5. 按账户代理隔离与 `proxyguard`，接入当前多账户 `ovh.Factory`。
6. AES-GCM 凭据加密，兼容旧明文数据库、`.dbkey` 和环境变量密钥。

## Aegis Visibility

这是跨模块高风险移植，不采用上游 `main` 整体合并。当前项目已拥有独立的停售 tracker、通知 outbox、checkout attempt 恢复和 Docker 单镜像部署；这些是不可回退的 canonical owner。上游代码只能作为行为参考，必须在本地 owner 中重写。

## Change Necessity

仅复制文件会覆盖当前多账户、通知、停售和订单不确定性边界，因此必须修改 State、SQLite 映射、OVH client 工厂、purchase/monitor 生命周期和启动路由。安全能力还会增加 schema 迁移、密钥来源和代理故障状态等持久化边界。

## TDD Route

- Mode: `off`
- Decision: `skipped`
- Authority: 当前会话明确 TDD off。
- Test posture: 不要求先写 RED；每个 seam 先补定向回归测试或实现后立即验证，最终运行 Go 单元/竞态测试（若工具链可用）、TypeScript、Vite 和 diff 检查。

## Canonical owners and preserved boundaries

- 时间：`backend/internal/types/types.go`，供全后端解析使用；不删除 `NowISO`。
- 启动加载安全：`app.State.LoadAll` 与 `Save*`；失败表状态保留在 State，不用空列表推断成功。
- 队列/监控变更：`MutateQueue`、`MutateHistory`、`EnqueueMonitorOrders`、`Monitor.MutateSubscriptions`；不引入旁路整表写入。
- checkout：`checkout_attempts`、`CommitPurchaseSuccess*` 和 `DeletedTaskIDs`；checkout 结果不确定时禁止自动重试。
- 通知：现有 monitor notification outbox；订单支付轮询只创建幂等 outbox，不直接发送新渠道。
- 账户和 OVH 请求：`types.OVHAccount`、`handlers/accounts.go`、`ovh.Factory`；代理配置按账户保存并由 Factory 构造 transport。
- 凭据：`config.Store`/账户 DB 读取边界；密钥优先环境变量，其次旧 `.dbkey`，最后受保护写入配置文件，迁移必须幂等。
- 部署：继续使用现有 Docker 单镜像；不引入上游在线自更新。

## 实施任务

### 1. 时间解析与测试

- 在 `types` 增加公开 `NowISOLayout`、`ParseTS(string) (time.Time, bool)`。
- 接受 RFC3339Nano、RFC3339、无时区微秒和无时区秒格式。
- 将队列排序、监控通知耗时、订单时间节流等自有时间解析调用改为 `ParseTS`。
- 增加格式兼容、非法值和本地时区行为测试。

### 2. 启动加载失败保护

- `State` 增加按表 `loadFailed` 及锁，记录 accounts/queue/history/servers/monitor/vps 等读取错误。
- `LoadAll` 和 `Monitor.LoadFromDB` 区分“成功读取空表”和“读取失败”。
- 所有整表 `SaveQueue/SaveHistory/SaveServers/SaveAll` 和必要的 monitor/vps 保存入口在对应表失败时返回错误，不执行覆盖。
- 启动安全状态保持关闭，直到关键状态成功恢复；HTTP 健康/状态输出包含可诊断信息但不泄露凭据。
- 增加“加载失败后保存被拒绝、成功空表仍可保存、监控加载失败不清空订阅”的测试。

### 3. 可安全重试的临时错误分类

- 新增 purchase 内部错误分类：429、408、409、499、5xx、网络超时/连接失败视为 transient；明确业务 4xx 视为 definitive。
- 只在库存查询、配置/价格查询、购物车创建、购物车配置等尚未产生不可逆 checkout 的阶段使用重试预算/退避。
- checkout 请求继续使用现有 `checkoutFailureIsDefinitive` 与 checkout attempt 隔离：5xx/超时/409 不能自动二次 checkout。
- 日志明确阶段和重试原因，失败历史不把 transient 误报为永久业务失败。
- 增加阶段分类、退避、429/5xx/timeout 和 checkout 不确定性回归测试。

### 4. 订单支付状态轮询

- 检查当前 history schema 与 OVH `/me/order/{id}/status` 响应，新增 `OrderStatusLoop` 或等价 purchase owner。
- 仅轮询有 order ID、未达到终态且距上次查询超过最小间隔的历史记录；后台周期受控，避免用户手动刷新造成 OVH 限流。
- 状态更新通过 `MutateHistory`/专用 DB 更新原子发布，记录订单状态和时间，不覆盖停售/通知字段。
- 状态变化创建稳定 event key 的 outbox 事件，复用当前 Telegram/飞书/微信渠道和重试/死信。
- 启动和优雅关闭接入 main context；增加节流、终态、不确定响应、持久化失败测试。

### 5. 代理隔离与 proxyguard

- 在账户 schema 增加 `proxy_url`、`fingerprint` 默认空字段并迁移；API 返回代理脱敏值，创建/更新校验 http/https/socks5/socks5h URL。
- 新增 `netfp` transport builder：无代理直连；配置代理时绝不静默回退直连；指纹仅使用可验证的 HTTP/TLS/UA 选项。
- `ovh.Factory.ClientFor` 按账户缓存独立 client/transport，Invalidate 时释放旧 transport；公开目录请求使用默认账户代理但失败可诊断。
- 新增 `proxyguard` 状态机：连续代理链路失败时暂停该账户任务/自动下单并通知；恢复后允许人工或健康检查解除。不得静默删除队列/监控任务。
- 代理测试接口只返回出口 IP/健康状态/脱敏 URL，不能返回凭据；增加代理解析、无回退、账户隔离、故障熔断和恢复测试。

### 6. 凭据加密与兼容迁移

- 新增 `secret` 包使用 AES-GCM；密文带版本前缀并随机 nonce。
- 密钥解析顺序：`OVH_DB_KEY` → 现有 `.dbkey` → 受保护配置文件写入的新密钥；密钥不可用时启动失败或进入明确安全降级，不能静默把加密值当空凭据。
- 对 `ovh_accounts` 的 app key/secret/consumer key、kv config 敏感字段和 Telegram/飞书/微信 token 做读写加解密；API 响应保持现有脱敏契约。
- 明文旧库首次启动幂等迁移为密文；迁移失败不得覆盖原明文；错误日志不得包含密钥。
- 增加旧明文、已加密、错误密钥、密钥轮换/环境变量优先级和迁移幂等测试。

### 7. 集成与验收

- 保留并验证停售 tracker、`Discontinued` queue/monitor 字段、catalog status outbox、reboot 名称回退、飞书/微信路由和当前 Docker 单镜像。
- 运行 `go test ./backend/internal/...`、`go test -race ./backend/internal/...`（工具链可用时），前端 TypeScript、目标 ESLint、Vite build。
- 运行 SQLite 迁移/旧数据回读测试和无代理/代理配置测试。
- 运行 `git diff --check`、查看提交文件和工作树；不自动 push，除非用户另行明确要求。

## 风险、退休与不可合并项

- 不整体合并上游 `main`。
- 不引入上游在线自更新、Caddy/Nginx 双部署边界、作者水印或删除当前页面的变更。
- `config.Store` 中的明文 fallback 仅作为兼容读取路径，成功迁移并验证后不再写明文；保留旧 `.dbkey` 读取直到明确迁移窗口结束。
- 代理故障只暂停受影响账户的自动动作，不删除任务、不改变停售状态。
- checkout 不确定性永远优先于 transient 重试；该边界不能被通用重试 helper 覆盖。

## Verification exit criteria

- 时间解析测试通过，所有接入调用不再错误解析无时区 `NowISO`。
- 启动读失败时没有任何整表覆盖写入；正常空表仍可持久化。
- transient 阶段可重试，checkout 不确定阶段只隔离、不重复 checkout。
- 支付轮询遵守节流并能在终态停止，通知事件幂等。
- 两个账户代理 transport 独立，代理失败不直连泄漏，proxyguard 状态可观测。
- 加密迁移兼容旧库且错误密钥不会静默破坏凭据。
- 现有停售/通知/reboot/多账户/Docker 契约回归通过。
