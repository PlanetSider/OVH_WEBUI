# 统一消息渠道账户状态与通知健康度

- 日期：2026-09-24
- 状态：设计已确认，待实施
- 范围：Telegram、飞书、微信消息渠道；WebUI 只读展示；不改变服务器控制/VPS 资源级账户选择

## Goal

建立一个跨消息渠道的唯一运行时 OVH 账户状态。Telegram、飞书、微信的账户切换写入同一个 `active_account_id`，其它渠道读取时立即使用同一账户；WebUI 展示该渠道账户，但服务器控制和 VPS 页面继续保持自己的资源级账户选择。同时增加统一的通知渠道健康度接口和 Telegram Webhook 专用状态接口。

## Architecture

### 规范状态

- `ovh_accounts.is_default` 继续表示没有显式选择时的兜底账户。
- `kv.active_account_id` 保存消息渠道共享的运行时账户状态，并带来源渠道和更新时间。
- `State.ResolveChannelAccount()` 是消息渠道读取账户的唯一 owner，返回账户及 `active/default/none` 来源。
- `State.SetChannelActiveAccount(accountID, source)` 是消息渠道切换的唯一写入 owner；Telegram、飞书、微信 handler 不直接写 KV 或 `is_default`。
- 队列、历史、监控订阅、服务器控制、VPS 控制和已生成通知中的 `account_id` 保持原有快照语义，不随 active account 漂移。

### 通知状态

- `GET /api/notify/channels` 聚合 active account、Telegram、飞书、微信的入站传输状态和出站 outbox 状态。
- `GET /api/accounts/active` 返回脱敏的共享渠道账户摘要，供 WebUI 展示。
- `GET /api/telegram/status` 返回 Webhook 语义，不引入 poller offset。
- 出站最近成功/失败由新的持久化渠道统计 owner 维护；pending 从 `notification_outbox` 当前剩余渠道快照计算。
- 入站状态由各传输 owner 维护：Telegram Webhook、飞书 Webhook/WebSocket、微信 iLink 长轮询。

## Tech Stack

- Go/Gin backend
- SQLite/sqlx persistence with existing `kv` and `notification_outbox`
- React/TypeScript/Vite frontend with TanStack Query
- Existing Telegram Webhook, Feishu Webhook/WebSocket, and Weixin iLink polling implementations

## Baseline / Authority Refs

- `backend/internal/db/schema.sql`: `kv`, `ovh_accounts`, `notification_outbox` schema and fallback semantics.
- `backend/internal/db/accounts.go`: default-account mutation and account deletion transaction.
- `backend/internal/app/app.go`: `State.FindAccount`, account mutation guards, notification retry state.
- `backend/internal/telegram/commands.go`, `backend/internal/telegram/enqueue.go`: current Telegram default-account resolution.
- `backend/internal/handlers/account_commands.go`: current global default account switch flow.
- `backend/internal/handlers/feishu.go`, `backend/internal/handlers/feishu_long_connection.go`: Feishu identity, card actions, and mutually exclusive transport modes.
- `backend/internal/handlers/weixin_commands.go`, `backend/internal/weixin/manager.go`: Weixin command path, sender authorization, and polling status.
- `backend/internal/monitor/delivery.go`, `backend/internal/monitor/outbox.go`, `backend/internal/db/notification.go`: channel selection, outbox delivery, retry and remaining-channel semantics.
- `src/hooks/ovh/use-accounts.ts`, `src/pages/SettingsPage.tsx`, `src/components/common/AccountSelect.tsx`, `src/components/common/ActiveAccountSync.tsx`: current frontend account semantics and notification settings UI.
- User-approved design in the current conversation: shared message-channel active account; WebUI resource selectors remain independent; WebUI displays channel-selected account.

No repository ADR or product baseline exists before this plan. `docs/aegis/BASELINE-GOVERNANCE.md` is method-pack workspace governance only and is not treated as product authority.

## Compatibility Boundary

- Existing `POST /api/accounts/:id/set-default` remains and only changes the fallback `isDefault` account.
- Existing `isDefault` response field and resource-level account selectors remain compatible.
- Existing Telegram Webhook remains the only Telegram inbound transport; no `getUpdates` poller is added.
- Feishu keeps its existing mutually exclusive `webhook` / `long_connection` modes.
- Weixin keeps iLink long polling; no artificial Webhook mode is introduced.
- Existing queue/history/monitor/VPS account IDs are immutable snapshots.
- Existing notification outbox event payloads and channel-removal retry behavior remain compatible.
- Existing authentication and per-channel actor authorization remain required for account switching.

## Requirement Ready Check

- Requirement source: user-approved unified shared active-account behavior and channel-health requirements.
- Scenarios: switch from each channel; observe same account in other channels/WebUI; fallback after deletion; independent WebUI resource selection; channel delivery and inbound failure visibility.
- Acceptance evidence: backend unit/handler/database tests, frontend type check/build, full Go tests and race-focused tests, endpoint response assertions, and manual/static review of direct default-account call sites.
- Decision: ready.

## Change Necessity

- User-visible need: current Telegram/Feishu/Weixin paths can resolve different account semantics, and WebUI cannot show which account message channels are using; channel health is fragmented.
- No-change option: documentation alone cannot synchronize runtime account state or expose durable delivery metrics.
- Why code is necessary: the behavior requires a canonical persisted state, transactional account lifecycle handling, shared handler owner, new API contracts, transport snapshots, and UI query/rendering.
- Minimum change boundary: account-state owner, channel handlers/status aggregation, outbox delivery statistics, routes, focused frontend hooks/UI, and regression tests.
- Decision: code-change.

## Ripple Signal Triage

- Canonical owner: `State.ResolveChannelAccount` / `State.SetChannelActiveAccount` for channel account state; DB methods for atomic persistence; monitor outbox owner for delivery statistics; transport owners for inbound snapshots; handlers for API aggregation.
- Source-of-truth risk: do not create per-channel active-account keys; do not use `isDefault` as the runtime channel state.
- Contract risk: add `/api/accounts/active`, `/api/notify/channels`, and `/api/telegram/status` response contracts without exposing credentials or poller-only fields.
- Persistence risk: account deletion and active-state clearing must be atomic; channel statistics must survive restart.
- Consumer impact: Telegram, Feishu, Weixin command paths and SettingsPage change; resource-level WebUI selectors and business snapshots do not.
- Retirement track: remove direct message-channel use of `telegram.DefaultAccountID`/global `switchDefaultAccount`; retain `isDefault` only for fallback and explicit account-management API.

## TDD Route

- Mode: `off`
- Decision: `skipped` for strict TDD
- Authority: current session Aegis setting; user did not request test-first TDD.
- Test posture: add focused regression tests alongside implementation and run verification-before-completion; do not prescribe RED/GREEN ceremony.
- Verification: full Go tests, targeted race tests, `go vet`, TypeScript check, frontend build, API contract assertions, and direct-call-site audit.

## Plan Pressure Test

- Owner / contract / retirement: one account-state owner and one delivery-statistics owner; old per-channel/default paths are explicitly retained only for fallback or retired from message-channel callers.
- Architecture integrity: reuse `kv`, account mutation guards, existing outbox, and existing transport managers; do not add a poller or a second account state.
- Verification scope: covers persistence, deletion races, authorization, cross-channel resolution, status aggregation, outbox retry metrics, and UI compatibility.
- Task executability: tasks are ordered by persistence owner, channel consumers, status aggregation, frontend, then verification.
- Pressure result: proceed.

## Implementation Tasks

### 1. Add the canonical shared channel-account owner

**Files / owners**

- `backend/internal/types/types.go` for persisted/runtime response types.
- `backend/internal/db/channel_account.go` (new) and `backend/internal/db/kv.go` as needed for atomic read/write helpers.
- `backend/internal/app/channel_account.go` (new) for `State` resolution and mutation owner.
- `backend/internal/db/accounts.go` and `backend/internal/app/app.go` for deletion and account-mutation integration.
- `backend/internal/db/*channel_account*_test.go`, `backend/internal/app/*channel_account*_test.go`.

**Changes**

- Persist `active_account_id` metadata in `kv` without changing `ovh_accounts.is_default`.
- Validate target account inside the same write transaction as the active-state update.
- Return a copied, credential-redacted account summary plus source, selected-by, and timestamp.
- Resolve active account first, then default, then none.
- Make deleting the active account clear the active key in the same database transaction; existing default promotion remains the fallback path.
- Cover missing key, invalid key, account rename/update, concurrent set/delete behavior, and empty-account behavior.

**Compatibility / retirement**

- Preserve `FindAccount("")` for generic default-account consumers.
- Introduce a clearly named channel resolver rather than silently changing all default-account semantics.

### 2. Route all message-channel account reads and writes through the owner

**Files / owners**

- `backend/internal/telegram/commands.go`, `backend/internal/telegram/enqueue.go`.
- `backend/internal/handlers/telegram_commands.go`, `telegram_quick_order.go`, `telegram.go`, `account_commands.go`, `telegram_account.go`.
- `backend/internal/handlers/feishu.go`, `account_commands.go`.
- `backend/internal/handlers/weixin_commands.go`.
- Existing Telegram/Feishu/Weixin handler tests and new account-switch tests.

**Changes**

- Replace message-channel fallback reads that call `DefaultAccountID` or `FindAccount("")` with the shared resolver.
- Keep explicit account references (`@1`, `@zone`, `@all`) and task/account snapshots unchanged; explicit references override the shared current account where already supported.
- Make Telegram `/account switch` and its callback call the shared setter.
- Make Feishu account cards call the shared setter after existing bound-recipient authorization.
- Implement Weixin text account listing and selection (`/account`, `/account 1`, `/account switch 1`) using the same setter and sender authorization.
- Return the resolved shared account in confirmations, including name and zone.
- Ensure a channel switch does not broadcast recursive notifications; other channels observe the shared state on their next command.

**Retirement**

- Remove the old Telegram-only/global-default switch behavior from message-channel paths.
- Keep account-management `set-default` as fallback-only behavior.

### 3. Add durable per-channel delivery statistics

**Files / owners**

- `backend/internal/db/schema.sql` and `backend/internal/db/db.go` for the new stats table/migration.
- `backend/internal/db/notification_stats.go` (new) and tests.
- `backend/internal/types/types.go` for stats types.
- `backend/internal/monitor/outbox.go`, `delivery.go`, and `monitor.go`/dispatch callers.

**Changes**

- Add one row per channel for last attempt, last success, last failure, last error, consecutive failures, and update time.
- Update stats after each concrete channel send result, including failures that remain in outbox and payload errors that enter dead-letter storage.
- Add a DB aggregate for pending events by channel using the existing remaining-channel snapshot; do not duplicate mutable counters as a second source of truth.
- Expose `pending` and a documented `retrying` interpretation (pending work with recorded failure/backoff), without claiming an in-flight network request.
- Preserve current per-channel outbox removal and retry backoff behavior.

### 4. Add inbound transport snapshots and unified status handlers

**Files / owners**

- `backend/internal/telegram/webhook_status.go` (new) and `backend/internal/handlers/telegram.go`.
- `backend/internal/handlers/feishu.go`, `feishu_long_connection.go` for Webhook/WebSocket receive/process/error timestamps and snapshots.
- `backend/internal/weixin/types.go`, `weixin/manager.go` for receive/process timestamps and existing polling status.
- `backend/internal/handlers/notify_channels.go` (new) for aggregation.
- `backend/main.go` for route registration.
- Targeted handler/status tests.

**Changes**

- Record Telegram Webhook received, processed, failed, and last update/pending data without introducing poller fields.
- Record Feishu mode, running/connected state, latest event/process/error data for both mutually exclusive modes.
- Extend Weixin status with received/processed timestamps while retaining polling, lease, and last-poll semantics.
- Add `GET /api/telegram/status` using the shared Telegram snapshot and Telegram `getWebhookInfo` data where available.
- Add `GET /api/notify/channels`, combining active account, normalized inbound transport state, persisted delivery stats, and pending counts.
- Redact tokens, secrets, credentials, and message bodies.
- Return stable empty/null fields when a channel is disabled or unconfigured rather than failing the entire aggregate response.

### 5. Expose the shared account and status in WebUI

**Files / owners**

- `src/hooks/ovh/use-accounts.ts` for active-account types/query.
- `src/hooks/ovh/use-settings.ts` or a focused notification hook for channel-status query.
- `src/lib/query.ts` for query keys.
- `src/pages/SettingsPage.tsx` for read-only channel-account and channel-health display.
- `src/components/common/AccountSelect.tsx` and `ActiveAccountSync.tsx` only if needed to preserve explicit resource-level semantics; do not bind them to channel active state.

**Changes**

- Add read hooks for `/api/accounts/active` and `/api/notify/channels`.
- Render “消息渠道当前账户” with name, zone, source, selecting channel, and update time.
- Render compact per-channel transport and delivery indicators, including pending and last error.
- Update copy that currently says Telegram/Feishu use the same default account to describe the shared channel active account and default fallback accurately.
- Keep server-control/VPS selectors and their localStorage behavior independent.
- Invalidate/refetch status after account deletion, settings save, focus regain, and normal short polling interval; no WebSocket/SSE.

### 6. Verification, review, and compatibility audit

**Tests**

- DB/app tests for atomic set/resolve/delete/fallback behavior and concurrent mutation.
- Handler tests for Telegram, Feishu, and Weixin account switching, authorization rejection, invalid account IDs, and shared-state visibility.
- Status endpoint tests for redaction, disabled/unconfigured channels, webhook versus long-connection versus long-polling fields, and pending/delivery statistics.
- Outbox tests for success/failure stats, pending channel aggregation, retries, dead letters, and restart-safe persisted last-result data.
- Frontend TypeScript check and build.

**Commands**

```text
go test ./... -count=1
go test -race ./internal/db ./internal/app ./internal/handlers ./internal/monitor ./internal/telegram ./internal/weixin -count=1
go vet ./...
npx tsc --noEmit -p tsconfig.app.json
npm run build
git diff --check
```

Use the repository's configured clang compiler if CGO is required, as in the previous verification run. Existing npm/Vite warnings are non-blocking unless they become errors.

**Manual/static checks**

- Search message-channel callers to confirm no direct `DefaultAccountID` or global default switch remains in active-account paths.
- Confirm resource-level WebUI account selectors still send their explicit account IDs.
- Confirm Telegram Webhook remains the only Telegram inbound mode and no `getUpdates` loop is started.
- Confirm all changed API responses omit credentials and secrets.

## Risks and Residual Boundaries

- The shared active account is intentionally global across message channels; two authorized operators switching concurrently use last successful write wins. The response timestamp/source makes this visible, but it is not per-user isolation.
- Existing outbox rows do not contain an account snapshot for every notification kind; this change must not retroactively rewrite their payloads. Any future account-specific notification semantics need a separate contract.
- Feishu WebSocket SDK callbacks and Webhook handlers have different lifecycle hooks; status timestamps may be less precise for SDK-level disconnects than for application events.
- Pending counts are current remaining-channel work, not a total historical delivery count.
- Generic arbitrary outbound Webhook and Telegram polling fallback remain out of scope.

## Retirement / Completion Boundary

- Retire direct per-channel/default account resolution from message command paths.
- Deliberately retain `isDefault`, `FindAccount("")`, and resource-level selectors for fallback and non-channel operations.
- Do not remove existing endpoint names or migrate Telegram to polling.
- Completion requires all approved scenarios, tests, API redaction checks, and frontend build evidence; a successful compile alone is insufficient.

## Execution Route

- Decision: inline
- Evidence: persistence transactions, shared account owner, and channel status aggregation have sequential dependencies and shared mutable state; delegation would add coordination cost and race risk.
- Fallback: continue in the current workspace in ordered task slices if a subagent route becomes unavailable.
- User confirmation required: no — approved design and scope are explicit.
