# Browser Request Signing Compatibility

## Intent And Evidence

- User reports `window.crypto.randomUUID is not a function` during login.
- Baseline: main at c15438d; existing unrelated tsconfig.app.tsbuildinfo modification must remain untouched.
- In-memory execution of the actual HTTP client with getRandomValues but no randomUUID reproduces the exact error before any network request.
- Supplying only randomUUID with no subtle instead fails with `Cannot read properties of undefined (reading 'importKey')`.
- Both the AuthGate header builder and Axios interceptor directly generate nonces. They share one HMAC implementation but duplicate signing-header assembly.
- Browser secure-context/API exposure is the recurrence condition; the actual user's scheme/browser is unknown. The prior startup validator repair remains valid and does not address this distinct frontend failure.

## Decision And Boundary

- Change Necessity: code-change. HTTPS configuration is a workaround, but the supported browser client must not assume randomUUID/subtle exist. Repair the existing transport/signing owner rather than the login component or backend enforcement.
- PatchShape: canonical-owner compatibility repair in src/lib/http.ts. Preserve METHOD/path/query/timestamp/nonce/raw-body signing and all four authentication headers.
- Ripple: AuthGate and every signed Axios request. Verify both consumers, JSON/raw bytes, encoded paths, account/query injection and negative authentication controls.
- Minimality: sufficient repair. Generate 128-bit nonces with getRandomValues, retain native HMAC when available, use @noble/hashes only when subtle is absent, and reuse buildRequestAuthHeaders from the interceptor. No hand-written hash implementation or weak randomness.
- Owner fit: edit-in-place, local-fix-without-new-responsibility. Existing transport remains the sole protocol owner. One private nonce helper and one HMAC capability branch replace unsafe assumptions; no new production subsystem.
- Preserve native HMAC errors instead of silently falling back after runtime rejection. A browser without a secure random source must fail closed before dispatch.
- TDD Route: Mode off, Decision skipped; diagnostic reproduction and post-change regression, not a strict RED/GREEN gate.
- Risk: Medium (shared authentication transport and one pinned, zero-runtime-dependency crypto library).
- npm/package-lock.json is the Docker production dependency source. Avoid unrelated dependency updates or historical lockfile churn.
- Do not read or change real environment secrets/data or execute OVH, purchase or notification operations.
- Compatibility does not encrypt HTTP traffic. Production still requires HTTPS; the API key is sent in X-API-Key and stored locally as before.

## Acceptance

1. Login headers and Axios signing work with full Web Crypto, no randomUUID, and no randomUUID/subtle.
2. Signatures match Node HMAC-SHA256 for UTF-8, binary bodies and queries; nonces are fresh cryptographic random values.
3. No random source rejects before dispatch; native HMAC errors propagate.
4. TypeScript, production build, focused regressions and existing authentication enforcement pass.
5. Isolated Go backend accepts fixture signed GET /api/stats in restricted-crypto mode and rejects wrong keys, tampering, expiry and replay. No real OVH credentials or user data.
6. Report executed evidence separately from actual remote-browser/deployment verification.

## Verification And Reproduction

- `npm run test:crypto` bundles the real HTTP client using Vite's existing esbuild dependency, with a synthetic key and in-memory Axios adapters. No network request is dispatched.
- Fifteen requests cover full Web Crypto, missing randomUUID, and missing randomUUID/subtle; each mode exercises login headers, UTF-8 and binary-view bodies, Axios login, and injected account/query serialization. All signatures match Node HMAC-SHA256. Long keys/bodies, nonce uniqueness, absent secure randomness, and native importKey/sign failures are also checked.
- The same fifteen browser fixtures pass the real Go authentication middleware via httptest. Each request also checks wrong keys, query/body tampering, expired timestamps, unsigned requests, and exact-request replay: 105 status/code checks in total.
- TypeScript and production build passed against the local dependencies and an isolated fresh `npm ci --ignore-scripts` installation; the fresh install also passed `npm run test:crypto`. The full Go suite passed after adding the opt-in integration regression. Production build retains the existing large-chunk warning.
- Targeted transport lint reports the pre-existing `no-constant-binary-expression` at src/lib/http.ts:136 (HEAD:134). The committed baseline produces the same error; this repair leaves that unrelated expression unchanged.
- The existing tsconfig.app.tsbuildinfo modification remains outside the repair scope. Real environment files, credentials and runtime data were not read or changed.
- No remote deployment, actual user's browser, or Docker image rollout was verified. A built artifact containing this change must be deployed before the remote page benefits; production still requires HTTPS.

Run the optional browser-to-Go integration from the repository root in PowerShell:

```powershell
$env:OVH_BROWSER_SIGNING_FIXTURES = node scripts/test-http-signing.mjs --fixtures
try {
    go -C backend test ./internal/auth -run '^TestBrowserSigningFixtures$' -count=1
} finally {
    $env:OVH_BROWSER_SIGNING_FIXTURES = $null
}
```

The fixture environment variable contains only a synthetic test key. Ordinary Go tests skip this opt-in case and do not require Node or frontend dependencies.
