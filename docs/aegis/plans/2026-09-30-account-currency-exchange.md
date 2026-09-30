# OVH Account Currency and Exchange Rates

## Goal
Implement the approved account-region currency rules and exchange-rate display workflow in `OVH_WEBUI` without changing purchase charges or exposing provider keys.

## Architecture
The Go backend is the canonical owner for provider adapters, encrypted configuration, current rates, historical-rate lookup, and conversion metadata. The React frontend owns settings controls and uses backend-provided conversion state for consistent display formatting. Existing raw OVH amounts remain stored and available for fallback.

## Scope and compatibility
- CA and US accounts display in USD; IE accounts display in EUR. Existing non-CA/US/IE records remain readable.
- CA catalog values returned in CAD are converted to USD for display through stored CAD/CNY and USD/CNY rates; order APIs continue receiving their original OVH values.
- Primary providers are FreeExchangeRateApi and Exchange Rate API, selected in settings with provider-specific keys. Keys are stored server-side and redacted from GET responses.
- Once a provider succeeds, the backend refreshes EUR/CNY and USD/CNY daily at Asia/Shanghai midnight and on configuration activation. The CNY display mode is disabled until usable rates exist.
- Frankfurter is used for date-specific historical bill/history conversions in CNY mode. Missing historical data reports an unavailable conversion and never substitutes today's rate.
- Existing settings, account, and raw-price API behavior remains compatible.

## Change necessity and owners
A no-code/config-only change cannot provide encrypted key persistence, scheduled refresh, provider normalization, historical caching, or cross-page display consistency. The minimum code boundary is backend config/types/provider/rate service and handlers, plus the existing frontend settings hook/page and shared display helpers consumed by price/history pages.

## TDD route
Mode: off. Decision: skipped. Authority: repository Aegis TDD mode is off and the user did not request strict TDD. Verification still includes focused backend tests for provider parsing, conversion, persistence/redaction, and historical fallback, plus TypeScript/build checks.

## Tasks
1. Add backend exchange settings fields, provider adapters, rate persistence, refresh loop, historical lookup/cache, conversion endpoints, and tests; keep secrets redacted.
2. Add account-region target-currency mapping and exchange settings controls with disabled CNY mode until backend readiness.
3. Route all existing price/order/history presentation through shared display conversion without changing raw charge/request values.
4. Run Go tests, frontend typecheck, lint/build where available, and fix regressions.

## Verification and stop conditions
Required evidence: `go test ./...`, `npx tsc -b`, and `npm run build` from `OVH_WEBUI`; focused UI/API checks when available. Stop and report if provider contracts cannot be resolved from official/local evidence, or if an existing API contract would require destructive migration. No live credentials are committed.

## Retirement
The current raw-only formatter remains as the explicit fallback for unavailable rates and for non-CNY mode. It is not removed until every display consumer has a tested conversion path and historical unavailable states are represented.
