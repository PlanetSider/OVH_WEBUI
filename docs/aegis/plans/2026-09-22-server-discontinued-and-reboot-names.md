# 服务器停售状态与重启名称修复实施计划

## 目标

在 `OVH_WEBUI` 中为抢购队列和服务器监控增加基于成功整点目录刷新的停售状态：型号连续缺失满 1 小时后显示红色“停售”、将该型号的自动检查降为每小时一次并发送通知；型号重新出现后恢复原频率并发送重新上架通知。同时修复 `/reboot` 交互使用服务器控制页可见名称的问题。

## Aegis Visibility

本任务横跨目录刷新、SQLite 持久化、队列/监控调度、通知 outbox 和前端消费者；保留状态来源和迁移兼容边界，才能避免只改标签而后台仍高频请求或重启后重复通知。

## 已批准范围

- 只使用成功完成的完整服务器目录刷新推进停售计时；刷新失败不改变状态。
- 追踪中的 `planCode` 首次缺失后，连续缺失满 3600 秒才进入停售。
- 不删除、不暂停用户任务；仅对停售型号降频。
- 抢购保留原 `retryInterval`，停售时由运行时有效间隔覆盖为 3600 秒。
- 监控只对停售订阅跳过 5 秒轮询，按每小时到期一次检查；其他订阅保持 5 秒。
- 恢复时清除停售状态、恢复原频率，并为抢购/监控分别发送一次恢复通知。
- 状态周期和通知事件必须可持久化、可重启恢复、可去重。
- reboot 名称优先使用账户别名，其次使用服务器 `commercialRange`，最后使用 OVH 名称；有可用名称时不显示“未设置自定义名称 #n”。

## 当前权威与变更边界

- 目录刷新 owner：`backend/internal/handlers/server_catalog_refresh.go`。
- 任务状态 owner：`types.QueueItem`、`monitor.Subscription` 及其 SQLite 映射。
- 通知 owner：现有 `monitor` notification outbox；不新增旁路发送器。
- 目录缺失起始时间 owner：SQLite `kv` 中新增的停售追踪记录；任务上的 `discontinued` 与节流时间是面向执行/界面的持久状态。
- reboot 名称 owner：`server_aliases`；`commercialRange` 作为已有服务器控制列表信息的兼容显示回退。

## Change Necessity

配置或仅修改前端不足以实现跨整点刷新、重启恢复、后台降频和通知去重，必须修改状态模型、迁移、调度和 outbox。最小代码边界是：队列/监控状态字段与映射、目录刷新状态转换、已有 outbox 新事件类型、两个前端行组件和 reboot 服务器选择构造。

## Ripple Signal Triage

- **Persistence/schema**：queue 与 monitor 表增加默认值为正常状态的列；旧数据库通过幂等迁移补列。
- **Producer/consumer**：目录刷新产生状态转换，队列处理器、监控循环和 HTTP/前端消费该状态。
- **Notification contract**：新增 outbox kind，沿用现有渠道快照、重试、死信和去重机制。
- **Fallback/source-of-truth**：reboot 不再把“技术名称以 ns 开头”作为唯一无名称判断，改为 alias → commercialRange → OVH name 的单一显示优先级。

## TDD Route

- Mode: `off`
- Decision: `skipped`
- Authority: 当前会话已明确 TDD off；不要求先写失败测试。
- Test posture: 先实现最小状态转换与持久化，再用针对性 Go 回归测试、前端类型检查、ESLint 和 Vite 构建验证。

## 实施任务

### 1. 扩展状态模型与 SQLite 兼容迁移

修改：
- `backend/internal/types/types.go`
- `backend/internal/db/schema.sql`
- `backend/internal/db/db.go`
- `backend/internal/db/queue.go`
- `backend/internal/db/monitor.go`

内容：
- `QueueItem` 增加 `Discontinued bool`。
- 监控订阅增加 `Discontinued bool` 和 `DiscontinuedNextCheckAt float64`。
- queue 增加 `discontinued INTEGER NOT NULL DEFAULT 0`；monitor_subscriptions 增加停售和下一次检查时间列。
- 所有 SELECT/INSERT/UPSERT/全表替换和转换函数保持旧数据兼容。
- 增加 `kv` 追踪结构的编码/读取测试，确保缺失字段默认正常。

### 2. 实现成功目录刷新后的停售状态转换

修改：
- `backend/internal/handlers/server_catalog_refresh.go`
- 必要时新增同包的 `server_catalog_discontinued.go`
- `backend/internal/handlers/server_catalog_refresh_test.go`

内容：
- 用 `planCode` 建立当前完整目录索引。
- 从 queue 和 monitor 当前快照收集被追踪型号。
- 在 `kv` 保存每个型号的 `missingSince`、最近名称和状态周期标识。
- 只有成功刷新后推进计时：首次缺失记录起点，满 1 小时生成停售转换；目录恢复生成恢复转换。
- 通过现有 `MutateQueue`、监控订阅变更/持久化路径发布任务状态；不得因刷新失败清除旧状态。
- 同一型号在同一状态周期只生成一次停售/恢复转换；无关联任务的 tracker 项可清理。

### 3. 接入抢购和监控降频

修改：
- `backend/internal/purchase/queue_processor.go`
- `backend/internal/purchase/queue_processor_test.go`
- `backend/internal/monitor/types.go`
- `backend/internal/monitor/persist.go`
- `backend/internal/monitor/loop.go`
- 相关 monitor 测试

内容：
- 抢购 `effectiveRetryInterval` 在 `Discontinued` 时返回 3600，恢复后自然使用用户原始 `RetryInterval`。
- 监控循环仅跳过尚未到 `DiscontinuedNextCheckAt` 的停售订阅；到期后允许一次检查并重新安排下一小时。
- 订阅 clone、状态复制、数据库保存/加载覆盖新增字段。
- 编辑配置时不意外清除同一 `planCode` 的停售状态；切换到新型号时按新型号状态重新判定。

### 4. 接入停售/恢复通知 outbox

修改：
- `backend/internal/monitor/types.go`
- `backend/internal/monitor/outbox.go`
- `backend/internal/monitor/outbox_test.go` 或新增针对性测试
- `backend/internal/handlers/server_catalog_refresh.go`

内容：
- 新增停售/恢复通知 kind 和 payload：模式（抢购/监控）、planCode、serverName、周期标识、恢复标志。
- 复用 `NotificationTargetChannels`、现有 outbox 重试/渠道关闭处理和死信路径。
- event key 包含模式、planCode 和状态周期，避免重复发送；通知失败可重试。
- 文案包含“正在抢购/监控的名称（planCode）已停售/重新上架”，并说明当前/恢复后的频率。

### 5. 前端显示停售与节流状态

修改：
- `src/hooks/ovh/use-queue.ts`
- `src/hooks/ovh/use-monitor.ts`
- `src/pages/QueuePage.tsx`
- `src/pages/MonitorPage.tsx`

内容：
- 类型补充后端新增字段。
- 抢购行和监控行增加红色 `停售` Chip；停售期间显示“每小时检查”。
- 抢购原有暂停/恢复、编辑、删除行为保持不变。
- 仅新增状态展示，不在前端复制停售判定。

### 6. 修复 reboot 选择名称

修改：
- `backend/internal/handlers/reboot_commands.go`
- `backend/internal/handlers/reboot_commands_test.go`

内容：
- 服务器选择结构保留/读取 `commercialRange`。
- 显示名称优先级：数据库别名 → `commercialRange` → OVH `name` → 最终编号回退。
- 保留服务名作为实际重启参数，不把显示名写回 OVH。
- 增加无别名但有 commercialRange、别名存在和全部字段为空的测试。

## 验证

- `go test ./backend/internal/...`（若环境没有 Go，记录工具缺失并运行可用的局部静态验证）。
- 重点测试：
  - 缺失不足一小时不转换；满一小时只转换一次；刷新失败不转换；恢复只转换一次。
  - queue/monitor 状态持久化往返及旧数据库默认值。
  - queue effective interval 和 monitor 下一次检查时间。
  - outbox 事件 key 去重、渠道重试和 payload 文案。
  - reboot 名称优先级。
- 前端：本地 TypeScript、ESLint、Vite build。
- `git diff --check`，并在提交前检查只包含本任务文件。

## 风险与保留项

- 老数据库不会知道应用升级前已经缺失多久；首次成功刷新后从当前时间开始计时，这是保守且可解释的兼容行为。
- 目录 API 返回空列表仍按现有逻辑视为刷新失败，不触发批量停售。
- 运行时更新 queue 与 monitor 状态仍通过现有各自持久化 owner；通知 event key 保证重复执行不重复插入，但状态转换与 outbox 写入的跨资源原子性需要测试确认。

## Retirement Track

- 不删除现有队列、监控或通知路径。
- 退休的是 reboot 中“仅凭 ns 前缀判定无可见名称”的旧责任；服务名仍作为重启调用参数保留。
- 新停售 owner 稳定后，不增加第二套 API/前端判定逻辑；恢复条件由成功目录刷新唯一负责。

## 执行路线

`inline`：各任务共享状态模型和锁/持久化边界，分散委派会增加协调和冲突成本。
