# P1-02 与 P2/P3 修复计划

## Aegis Visibility
本次修复同时触及 webhook 幂等、请求边界、日志持久化、后台 goroutine、SQLite 账户恢复和迁移标识符；需要明确每个现有 owner、兼容边界和回归证据，避免只加局部 guard 而保留旧 fail-open 路径。

## 需求与验收
来源：上一轮只读 Go 后端审计报告，以及用户“修复 P1-02、P2、P3”的直接授权。

范围：
- P1-02：Telegram/Feishu 事件幂等缺失 ID 或 SQLite 失败时 fail-closed，并通过 503 让平台重试。
- P2：敏感日志与文件权限、Logger 旧快照覆盖、订单详情后台任务生命周期、installOSLocks/DeletedTaskIDs 回收、JSON 解析错误、请求/响应大小上限、账户删除与 checkout_attempts 一致性、CORS 限制、请求时间戳强制检查。
- P3：上游错误客户端脱敏、订单映射次级错误显式计数、Feishu/目录后台任务生命周期收敛、迁移标识符白名单。

非目标：不改变正常队列数量语义、不把 `MaxRetries == 0` 改成有限重试、不删除真实持久化业务数据、不暴露或轮换任何现有秘密、不引入新的外部 API。

验收：
- P1-02 的 DB 错误、DB 缺失、事件 ID 缺失均不会进入副作用业务路径。
- 所有审计列出的忽略 JSON 解析点均改为错误响应或有明确的空 body 语义。
- 日志写入按最新快照串行化，临时文件为 0600。
- 订单补全、目录预热和 Feishu 连接具备父 context/停止等待边界。
- 账户存在任意 checkout_attempt 时删除被阻止；队列 tombstone 和按服务锁有可验证回收路径。
- 外部 body 有上限，客户端不再收到原始上游响应细节。
- 迁移标识符必须来自内部白名单。

## Owners / 文件边界
- 事件幂等：`backend/internal/db/telegram_security.go`、`backend/internal/handlers/telegram.go`、`backend/internal/handlers/feishu.go`。
- 日志/存储：`backend/internal/logger/logger.go`、`backend/internal/storage/storage.go`、`backend/internal/telegram/telegram.go`。
- 生命周期：`backend/main.go`、`backend/internal/purchase/purchase.go`、`backend/internal/catalog/region_cache.go`、Feishu long connection manager。
- 队列/账户：`backend/internal/handlers/queue.go`、`backend/internal/handlers/server_control_basic.go`、`backend/internal/app/app.go`、`backend/internal/db/accounts.go`。
- 输入/响应/错误：`backend/internal/handlers` 全部 JSON 绑定点、`backend/internal/handlers/order_mapping.go`、目录/Telegram HTTP 调用点。
- 鉴权/CORS/迁移：`backend/internal/auth/middleware.go`、`backend/main.go`、`backend/internal/db/db.go`。

## TDD Route
mode: auto；decision: skipped（用户未要求严格测试优先；现有测试较多，采用每个行为补充最小回归测试并进行全量验证）；authority: 用户直接修复授权；verification: 定向测试、`gofmt`、`go test -count=1 ./...`、`go vet ./...`，若工具链允许再跑 `go test -race ./...`。

## 执行任务
1. 先修事件幂等 owner 和相关测试，确认 fail-closed。
2. 修日志、文件权限、请求/响应限制和上游错误边界。
3. 修后台任务生命周期，确保停机顺序为停止接收、停止循环、等待辅助任务、保存状态。
4. 修队列 tombstone/按服务锁回收及账户 checkout 恢复删除保护。
5. 集中处理所有忽略 JSON 绑定点，保留有明确空 body 合约的例外。
6. 修 CORS、请求时间戳和迁移标识符校验。
7. 运行格式化、定向测试、全量测试、vet；审阅 diff 与旧 fail-open 路径残留。

## 兼容与退休
- 兼容保留 HTTP 200 的平台重复事件响应；仅数据库暂不可用改为 503。
- 保留正常数量扇出和无限普通队列重试语义。
- retire：所有事件幂等 DB 错误继续执行业务的旧路径、所有忽略 JSON 解析的旧路径、Logger 无序刷盘路径。
- 持久化数据删除采用 confirmation-first 边界：本次只增加删除保护和回收判断，不执行历史/checkout 真实数据清理。

## 风险与验证
- Go race detector 可能仍受本机缺少 GCC 限制；需明确报告未覆盖。
- Feishu SDK 是否真正响应 context 需要通过其接口和停止行为验证；若不能，只能保留有界等待并记录残余风险。
- 大规模 JSON 绑定替换后需编译所有 handler，避免返回路径遗漏。
