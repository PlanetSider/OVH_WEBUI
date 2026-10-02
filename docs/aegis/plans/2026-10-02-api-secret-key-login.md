# API Secret Key Login Repair

## Necessity And Boundary

The initialization scripts and deployment instructions generate 32 random bytes encoded as 64 hexadecimal characters. Startup validation rejects this supported format unless it contains uppercase, lowercase, and digits. Metadata-only inspection confirmed the local keys use the generated format, and neither local service was listening. This establishes a startup defect, not the cause of an uninspected remote browser failure.

Decision: code-change. Changing user secrets alone would leave the recurring generator/validator mismatch in place. Repair the existing validator and Linux initialization assignment; do not weaken request authentication or replace user configuration.

## TaskStartSnapshot

- Repository: D:\Codex\OVH\OVH_WEBUI
- Branch and baseline: main at f1eee63, aligned with origin/main.
- Existing unrelated change: tsconfig.app.tsbuildinfo; preserve it.
- Risk: Medium, because this changes authentication configuration validation.
- ArchitectureReviewRequired: false. Keep the signature protocol, whitelist, time window, replay protection, storage, and account boundaries.
- TDD Mode: off. Use focused regression verification without an automatic strict-TDD gate.
- Do not emit real secrets, overwrite .env files, modify user data, or execute OVH operations.

## Owner And Compatibility

- Canonical owner: backend/internal/auth/middleware.go, ValidateAPIKeyStrength.
- Patch shape: extend the existing validation contract to its documented machine-generated format; no downstream bypass or second validator.
- Preserve: 8-to-256-character mixed-case-and-digit keys and all protected-request checks.
- Rebind: generator comments and deployment examples to the explicit supported 64-character hexadecimal format.
- Retire: the Linux initializer's dependency on an obsolete literal placeholder.
- Minimality: sufficient owner-level repair; no new dependency or production subsystem.
- Topology: the generator/validator contract mismatch recurs independently of the obsolete shell placeholder. The placeholder defect is a separately confirmed fresh-Linux initialization failure. Their simultaneous role in the original browser incident is unknown.

## Plan And Acceptance

1. Extend validation to exactly 64 hexadecimal characters; keep existing validation for other keys and test length/format boundaries.
2. Replace the current API_SECRET_KEY assignment in Linux environment templates, regardless of placeholder text. Explicitly read UTF-8 templates and environments in the Windows initializer. Ordinary reruns must preserve existing environments.
3. Align environment examples, deployment instructions, and the API contract.
4. Verify focused and full Go tests, isolated initialization runs, and isolated signed login, including rejection of wrong keys, missing signatures, expired timestamps, tampering, and replay.
5. Report browser/deployment evidence separately. No real credentials or existing runtime data may be changed.

## Additional Confirmed Defect

Windows PowerShell 5.1 reads BOM-less UTF-8 templates using its legacy default encoding. For the current root template, that produced 12 lines and zero API_SECRET_KEY assignments instead of 21 lines and one assignment. Explicit UTF-8 reads in the existing initializer correct this independent first-run defect. Existing environments are not rewritten by an ordinary rerun.

## Verification Results

- Focused Go authentication tests passed, including generated keys, same-length wrong keys, length/format limits, and exact request replay with the REQUEST_REPLAYED code.
- Full backend tests (`go test ./... -count=1`) and static checks (`go vet ./...`) passed.
- Bash syntax and Windows PowerShell parser checks passed.
- Both initializers passed fresh initialization and ordinary rerun tests in minimal temporary fixtures. Root/backend keys matched the hex64 format. Environment files and a data marker remained unchanged on rerun. Bash execution used Git Bash on Windows; native Linux/macOS execution was not tested.
- A compiled backend started with a randomly generated fixture key and fresh temporary storage. The actual frontend buildRequestAuthHeaders function and Axios interceptor both fetched GET /api/stats successfully (200).
- Live negative controls returned 401 with NO_API_KEY, INVALID_API_KEY, NO_REQUEST_SIGNATURE, TIMESTAMP_EXPIRED, INVALID_REQUEST_SIGNATURE, and REQUEST_REPLAYED as appropriate.
- User root/backend environments remained unchanged. No real OVH accounts, purchases, or notifications were used.
- Browser UI and the user's remote deployment were not verified. Browser signing still requires HTTPS or localhost. The local startup-format defect does not establish the root cause of an unobserved remote login failure.
- Existing unrelated compiler-cache changes were preserved; no commit or push was performed for this repair.
