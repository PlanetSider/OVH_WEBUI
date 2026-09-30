# OVH Region Currency and Exchange Rates

## Scope and acceptance

Implement the approved OVH_WEBUI feature set: classify CA/US/IE accounts; display CA and US amounts in USD and IE amounts in EUR; configure either FreeExchangeRateApi or Exchange Rate API with backend-only secrets; refresh EUR/CNY and USD/CNY at Asia/Shanghai midnight; switch between account-native/original display and CNY display; and use Frankfurter historical rates for dated history/bills without falling back to today's rate.

Acceptance requires preserving raw OVH amounts and currencies, keeping existing non-CA/US/IE account values readable, never returning API keys, retaining existing whole-settings merge semantics, exposing an explicit exchange status, and showing unavailable historical values when the dated lookup cannot be completed.

## Owners and boundaries

- `backend/internal/types` and `backend/internal/config`: persisted encrypted exchange configuration and cached rate/status fields.
- `backend/internal/exchange`: provider adapters, rate normalization, cross-rate conversion, current-rate cache, historical-rate cache, and Asia/Shanghai refresh loop. This package must not import handlers.
- `backend/internal/handlers/settings.go` and route setup: merge/validate exchange patches and expose redacted status.
- `backend/main.go`: lifecycle wiring for the refresh loop.
- `src/hooks/ovh/use-settings.ts`, `src/pages/SettingsPage.tsx`, `src/lib/currency.ts`, and price/history consumers: typed settings and consistent display formatting.

## TDD route

- Mode: `off`.
- Decision: `skipped`.
- Authority: repository/runtime configuration (`Aegis TDD mode: off`), not a user request for strict TDD.
- Verification posture: add focused regression tests where contracts are introduced, then run the existing Go test suite, TypeScript build check, and frontend production build.

## Execution tasks

1. Read current config, settings, account, price, history, and scheduler contracts; record only compatible additions.
2. Add typed exchange config and redacted status fields; add provider HTTP adapters and tests using `httptest` for response variants and failures.
3. Add service methods for current-rate refresh, CAD/USD cross-rate calculation, dated Frankfurter lookup/cache, and a cancellable midnight loop; wire startup/shutdown and settings save validation.
4. Add account-region helpers and a frontend settings panel with provider-specific key inputs, status, and a disabled CNY switch until effective rates exist.
5. Replace price formatting at all identified consumers with shared raw-to-display helpers while preserving source values and historical unavailable states.
6. Run `go test ./...`, `npx tsc -b`, and `npm run build`; fix regressions and update handover progress only for completed slices.

## Compatibility and retirement

Existing OVH source prices remain authoritative and are never overwritten. Existing settings fields and non-CA/US/IE zones remain supported. Existing raw price formatting remains the fallback when exchange data is missing. The new exchange service becomes the sole conversion owner; page-local exchange calls or client-side secret handling are not introduced.

## Risks and verification

Provider payloads differ, so adapters must isolate endpoint/JSON parsing and reject incomplete rates. Config writes must preserve omitted/blank secrets. Current-rate failures must leave the last known successful cache intact and mark status error. Historical failures must return an unavailable result, not current-day data. Verify these cases in focused tests and by inspecting the final diff before completion.

## Execution route

Inline execution in the current workspace. The work spans shared contracts and persistence, so coordination overhead for independent agents does not outweigh maintaining one coherent API contract. User confirmation required: no; scope and workspace are already approved.
