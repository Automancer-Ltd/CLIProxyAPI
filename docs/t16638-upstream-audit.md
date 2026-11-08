# Hub v8.0.60 preparation (t16638)

The estate additions are rebased onto v8.0.60. **This prepares a build; it does not authorize or perform the production cutover.** m6 was not contacted or changed. The cutover is held behind journal-safe failover/rollback work, Tracker t16644.

## Baseline and method

The fork head inspected was `1421b638`. Its version string says v8.0.44, but its actual upstream ancestor is `f7331b5197730226829cab32c31fb4ce546ef8fc`, tagged **v8.0.50**. Target v8.0.60 is `91e0dee8`. There are **111 missing commits: 91 non-merge patches and 20 merges**, covering v8.0.51–60. A version-label comparison overstates the missing releases.

Read the upstream GitHub release notes v8.0.45–60, inventoried all 111 commits, compared paths against the fork delta, and inspected the security and integration patches below. The appendix records every commit and every same-file overlap, including changes that merge cleanly. This is a source/dependency catch-up audit, not a penetration test or a claim that the software has no undiscovered vulnerabilities. Commit severity below is this review's qualitative assessment, conditional on the described feature; upstream did not supply CVSS or CVE ratings for these patches.

Sources: [upstream releases](https://github.com/router-for-me/CLIProxyAPI/releases), [complete comparison](https://github.com/router-for-me/CLIProxyAPI/compare/f7331b5197730226829cab32c31fb4ce546ef8fc...v8.0.60), and the linked commits in the inventory.

## Outstanding security fixes and hardening

| Severity | Commits | Effect and exposure |
| --- | --- | --- |
| High, conditional | `3698b159`, `d96b1941` | Model streams are owned by both plugin and instance; wrong-owner callbacks are rejected and teardown cannot race a new registration. Protects stream confidentiality/control if plugins have separate trust. Does not sandbox native plugins. |
| Medium | `04208efa` | Plugin refresh preserves operator-disabled auth state; avoids unintended account reactivation. |
| Medium | `68cd7572`, `5f38d557` | Durable Codex refresh claims and receipts prevent reuse after an uncertain request, including timeout, redirect, 429/5xx and persistence failures. Native Codex remains usable during separate SIWC recovery. Credential integrity/availability; not an identified credential-exfiltration fix. New `.codex-refresh` state changes failover and downgrade requirements. |
| Medium, integrity hardening | `d3899771`, `482efbf5`, `0559fa3c` | Targeted OAuth re-login validates identity/provider/organisation and retains provider hints, metadata and adoption identity. Reduces wrong-account replacement; no demonstrated unauthenticated bypass is asserted. |
| Medium, conditional | `dde469d3` | Unix plugin library preflight runs in a subprocess to contain dlopen/initialisation crashes. A trusted native plugin still executes arbitrary native code once loaded; this is not a security sandbox. |
| Medium, conditional | `8a82bed5` | Transport-cache mutex unlocks after a builder panic and purged entries retire, preventing a panic from wedging future requests/reusing retired pools. |
| Medium, availability | `0a94b2ae`, `69a032de` | Bounds deferred error-body buffering, avoids success-body buffering and synchronises websocket disconnect logging. Limits retention/races under concurrent or adversarial responses. |
| Medium, conditional | `6e103599` | Aborts hot reload if plugin quiesce fails and coordinates usage replay/delivery. Avoids overlapping plugin execution and lost or duplicated delivery at reload. |
| Medium/Low, availability and account control | `95f125ff`, `7b4c64b0`, `e5827065` | Skips refresh without renewal credentials; retains disabled-credential backoff and stops rejected-grant refresh spin. Medium if repeated refresh harms account availability; otherwise low operational hardening. |
| Low, isolation/config integrity | `8643ea56` | Connection-pool settings participate in credential deduplication/index identity, so differently configured entries are not silently conflated. Check persisted pins/indexes at cutover. |
| Low | `a5795047` | Finalises and cleans up request logging on panic; preserves audit/error evidence. |
| Low | `f2e255f7`, `b5b724d0` | Clears consumed usage queue references and reuses content-addressed plugin shadow files; reduces memory/disk retention and reload races. |
| Low, hardening | `ea0b4712` | Claude state becomes 32 random bytes/base64url and OAuth scopes align with provider requirements. Previous state was already 16 cryptographically random bytes (128 bits); this review does **not** claim a broken CSRF defence was fixed. Scope changes require operator awareness. |

Related correctness changes with security/availability implications, rather than separate proven vulnerabilities:

- `585f97de` carries context and headers through alternate-target availability, important to per-request restrictions/pins.
- `1d4e0549`, `1c537211` preserve terminal request errors and unsupported capability responses without cooling down or refreshing healthy accounts.
- `e25b7daf`, `c3938b72`, `03a8743c`, `508ac2bc`, `be691624` classify provider policy/request faults and preserve error evidence. Devin's embedded `exec_command`/`write_stdin` wording is rewritten for provider compatibility; this is **not** a shell-injection fix. These changes do not bypass provider policy.
- `f3762c64`, `f49bd5d9`, `0e02765c` move Antigravity discovery, clone parsed model IDs and retain nil/empty distinction. Its redirect refusal and 8 MiB response bound remain; the fork's ownership transport guard had to be restored at the new call site.
- `91e0dee8` prevents SSE keepalives splitting a partially written line during image streams. “Injection” in the title describes framing; no attacker-controlled content injection has been established.
- `7c627ffb` adds the Antigravity aicode OAuth scope: a permission expansion for compatibility, not a security fix.
- `6e6afab9` and `78a9cc9a` replace JSON/base64 libraries and update moego. No security advisory is claimed for that migration; immutable/no-copy invariants remain tested.

Already present in the actual base, so **not missing**: `477605dc` unauthenticated descriptor exhaustion; `ad8bdd3f` zstd decompression bounds; `a6b4477b` Home websocket access control; `86d951cd`/`ae65e26d` private response metadata stripping; `4cb98e3c` websocket deferred-request retention; `b7442218` recycled Gin-context cross-request state; `a96e640d` quarantine during manual refresh; `d65840fb` xAI credential filename identity hashing.

## Dependency advisories found while preparing the build

The task-local Go 1.26.10 toolchain archive was checked against Go's published SHA-256. `govulncheck` v1.9.0 found two reachable advisories in the initial candidate; both are fixed in this branch:

| Advisory | Publisher severity | Change | Qualification |
| --- | --- | --- | --- |
| [GHSA-c9gr-gmg4-2vqv](https://github.com/andybalholm/brotli/security/advisories/GHSA-c9gr-gmg4-2vqv), GO-2026-6888 | Low | brotli 1.0.6 → **1.2.7** | Publisher describes malformed-input panic/infinite loop (DoS). Go's database instead describes excessive allocation under the same alias CVE-2026-111497. The descriptions disagree; both identify 1.2.7 as fixed. No RCE claim for this Go implementation. |
| [GHSA-fpmh-frq8-7rj8](https://github.com/klauspost/compress/security/advisories/GHSA-fpmh-frq8-7rj8), GO-2026-6865 / CVE-2026-116591 | High | klauspost/compress 1.18.7 → **1.20.2** | Crafted two-block zstd input can panic. Static reachability is not proof every scanner trace is attacker reachable in this deployment. |

After these updates `govulncheck -show verbose ./...` exits 0: **0 affected vulnerabilities; 0 in imported packages; 1 module-only advisory** (GO-2026-5932, unmaintained `golang.org/x/crypto/openpgp`, which is not imported or called). Existing newer fork x/crypto, x/net, x/sync, x/sys and x/text versions were retained rather than downgraded to upstream versions. This scan covers the root module/default Linux build, not nested example modules, plugin binaries or every optional build tag.

## Conflicts with estate additions and resolution

Eight original textual conflicts: `go.sum`, management `auth_files_provider_oauth.go` and `auth_files_v8.go`, `internal/auth/xai/xai.go`, `internal/config/config_v8.go`, `internal/runtime/executor/codex_executor_auth_test.go`, `sdk/cliproxy/antigravity_models.go`, and `sdk/cliproxy/auth/conductor_lifecycle.go`.

| Upstream change | Estate overlap | Resolution/check |
| --- | --- | --- |
| Targeted OAuth recovery/provider metadata (`d3899771`, `482efbf5`, `0559fa3c`) | Shared-authority enrollment guard, recovery adoption, pinned account identity | Keep upstream target/identity checks and metadata with fork enrollment guard. Retain nested-target/plugin-verification tests; remove obsolete test asserting Claude cannot target a login because upstream now supports it. Full management suite includes mismatch/recovery cases. |
| Refresh lifecycle/backoff (`95f125ff`, `7b4c64b0`, `e5827065`, `04208efa`) | Holds, disabled accounts, authority generation/control fields | Preserve upstream failure counters and fork authority metadata/control state. Existing conductor/authority tests cover selection and fencing. |
| Codex refresh journal (`68cd7572`, `5f38d557`) | Native credential files, standby mirror/handback, old-binary rollback; dormant shared-authority path | Keep native journal path. Existing opt-in shared authority still uses its own durable dispatch/spool path; do not activate it as part of this release. Native failover omission is filed as t16644; production cutover must wait. |
| Antigravity extraction (`f3762c64`, `f49bd5d9`, `0e02765c`) | `GuardHubTransport` previously wrapped discovery inside the service | Add optional transport decorator to the SDK request and install ownership guard at the new call site. New regression test allows one request, revokes ownership, then proves no second network dispatch. Preserve redirects/response bound. |
| Local KV store (`ec14dc47`, `e6e5708e`) | v8 `antigravity-keys` mapping, portable auth dir | Keep both `server.kv-store` and `access.antigravity-keys`; no live config rewrite. New KV defaults may create auth-dir/kv.db, which contains session-date state and is not a credential grant. |
| Selector/terminal-request semantics (`585f97de`, `1d4e0549`, `1c537211` and policy-error commits) | Consume-first, holds, provider restrictions, account pins, one-model gate | Retain fork selectors and guards while passing request context/headers and upstream terminal-error semantics; existing auth suite exercises these boundaries. |
| Credential pool indexing (`8643ea56`) | Stored auth indexes/pins and allowance identities | Retain upstream expanded pool identity and fork account pins. Interactive cutover must compare account/pin inventory, not silently repin an account. |
| Usage extraction/queue and request logging (`6d7e6a3b`, `fb30a9e8`, `f208c908`, `f2e255f7`, logging fixes) | Allowance page, usage refresh and lane usage accounting | Preserve fork dashboard, quota/credit display and lane events; use upstream extraction and queue cleanup. Usage listing checked on spare port; provider data remains fixture-only. |
| JSON/base64 migration (`6e6afab9`, `78a9cc9a`) | Fork-only allowance/provider handlers and websocket coverage test | Migrate remaining fork gjson imports to ojson and remaining standard base64 imports to moego/base64; tidy root module and preserve no-copy tests. |
| New dependency versions/imports | Fork's newer security dependencies, xAI/Codex tests | Keep newer x/* versions; fix two compression advisories above; regenerate sums with Go tidy. |

All remaining same-file overlaps (including provider executors, websocket sessions, handlers and service lifecycle that merge cleanly) are enumerated in the appendix. The fork dashboard/consume-first/pin/hold implementation is carried over, not redesigned. To retain both histories, the branch contains the squashed fork delta on v8.0.60 and then an ancestry merge of old fork main whose tree is unchanged. Land with a **merge commit**, not squash, so the next upstream comparison recognises v8.0.60.

## Validation and limits

Initial integration revision: `82d600f983f52f153e0ff447793ac67ba9f96171`. The final artifact revision and checksums are recorded on t16638 and in the attached `validation-receipt.json`. Follow-ups finish the base64 migration, correct upstream test fixtures, and fix root-module CI package selection.

- Built `./cmd/server` using Go 1.26.10 on auto-hst1, with v8.0.60-estate and candidate revision embedded.
- Targeted integration suites: `sdk/cliproxy`, `sdk/auth`, management, and `internal/hubauth` pass, including the new discovery ownership regression.
- Full root-module test suite passes: `go test -p 4 -count=1 ./...` (exit 0). `go vet -p 4 ./...` passes after removing an unused local `sync.Once` and its blank-identifier value copy in an upstream pluginhost test (Go 1.26 copylock warning). CI also exposed a mock CONNECT tunnel discarding Hijack-buffered TLS bytes. A temporary timing probe reproduced the exact TLS/unknown-refresh failure three times before the fix and passed three times when the copy used the buffered reader. The timing probe was removed; both mock proxy helpers retain the buffered reader. No refresh assertion was weakened. An early run falsely reported stale no-copy allowlist entries because the checkout root was named `.hub-work` and the source scanner skips dot directories; moving it to `hub-work` fixes the harness input without changing the test. A test run interrupted by ancestry-merge markers was discarded and rerun on the clean tree.
- Live isolated process on auto-hst1 **127.0.0.1:8395**, `--local-model`, empty private auth directory, discovery/TLS disabled, public fixture keys only: health 200; readiness `serving:true`; embedded `/hub` 200 HTML; keyed model list 200; wrong client key 401; keyed empty Messages request 400; management usage 200 with accounts/observed_at/strategy/usage_refresh; no management key 401. Process stopped and owned listener confirmed closed.
- No production tokens, provider inference or refresh were exercised. No m6 config, service, files or live revision were read or changed. No macOS runtime/codesign proof was performed; the interactive session must provide it.

See [the cutover plan](t16638-cutover-plan.md) for exact sequence, existing commands and the mandatory rollback prerequisite.

## Complete upstream inventory and fork path overlaps

Every non-merge patch has its intersecting fork paths listed. Same-file overlap is a review prompt, not a claim of a semantic conflict. Merges contain no independent fix in this inventory.

- [7c627ffb](https://github.com/router-for-me/CLIProxyAPI/commit/7c627ffbdb4acff7107e05e4416e45c15a003d50) — fix(auth/antigravity): request aicode OAuth scope
  Fork path overlap: none.
- [d9a688e6](https://github.com/router-for-me/CLIProxyAPI/commit/d9a688e6155720897433abb0972949b30235849f) — chore(meta): match Muse Code 1.4.14 client headers
  Fork path overlap: `internal/runtime/executor/meta_executor.go`.
- [b147b8d2](https://github.com/router-for-me/CLIProxyAPI/commit/b147b8d2105352a3e90ef82faf448135908ad5dd) — fix(meta): include Muse namespace in tool-bearing requests
  Fork path overlap: `internal/runtime/executor/meta_executor.go`.
- [f0ddc38f](https://github.com/router-for-me/CLIProxyAPI/commit/f0ddc38f2921a653830b704c288b49651e3197d2) — fix(xai): support Grok Imagine Video 1.5 Lite
  Fork path overlap: `config.example.yaml`.
- [95f125ff](https://github.com/router-for-me/CLIProxyAPI/commit/95f125ffd04c2ab086ca3f8137c0a9ab185a3237) — fix(auth): skip automatic Codex refresh without renewal credentials
  Fork path overlap: `sdk/cliproxy/auth/conductor_refresh.go`.
- [7b4c64b0](https://github.com/router-for-me/CLIProxyAPI/commit/7b4c64b006875fdbbf3bd2e77fa558573ecbdaf3) — fix(auth): preserve disabled credential refresh backoff
  Fork path overlap: `sdk/cliproxy/auth/conductor_lifecycle.go`.
- [c69cdd93](https://github.com/router-for-me/CLIProxyAPI/commit/c69cdd933d1928561f3c1e437e1443f6b6573b6f) — fix(xai): preserve Lite resolution and catalog fallback
  Fork path overlap: `config.example.yaml`.
- [9513c2d5](https://github.com/router-for-me/CLIProxyAPI/commit/9513c2d5dc8574022df84725a99d5d0ac45c20ff) — docs(auth): explain Codex refresh token key requirement
  Fork path overlap: `sdk/cliproxy/auth/conductor_refresh.go`.
- [8a82bed5](https://github.com/router-for-me/CLIProxyAPI/commit/8a82bed5825ec776683d77c44b2b7dfa5c034e56) — fix(helps): release TransportCache mutex on builder panic and retire purged entries
  Fork path overlap: none.
- [3698b159](https://github.com/router-for-me/CLIProxyAPI/commit/3698b1597aba1e52a54a39a536f3e32fe77379df) — fix(pluginhost): scope model execution streams to the owning plugin and instance
  Fork path overlap: none.
- [8643ea56](https://github.com/router-for-me/CLIProxyAPI/commit/8643ea56d520617c7c3e436d4eb2f847238604b9) — fix(config): include connection-pool settings in credential deduplication keys
  Fork path overlap: `sdk/cliproxy/auth/types.go`.
- [a5795047](https://github.com/router-for-me/CLIProxyAPI/commit/a5795047bfb4b32371a80cdf94b520f3054c436f) — fix(api/middleware): finalize request logging on handler panic
  Fork path overlap: `internal/api/middleware/request_logging_test.go`, `internal/api/server.go`.
- [ce39bc62](https://github.com/router-for-me/CLIProxyAPI/commit/ce39bc620f65173575aa9b049b9954276124ba6e) — feat(pluginstore): support plugin locales normalization and management passthrough
  Fork path overlap: none.
- [24f97440](https://github.com/router-for-me/CLIProxyAPI/commit/24f97440138d99f6704be0336f8f66968dec067d) — Merge pull request #7143 from router-for-me/fix/codex-no-refresh-token-spin (merge).
- [53ff2f9e](https://github.com/router-for-me/CLIProxyAPI/commit/53ff2f9e7478464007690edf4392b05231a2264a) — feat(pluginhost): expose per-entry name and logo for multi-entry OAuth plugins
  Fork path overlap: none.
- [ec06c3cb](https://github.com/router-for-me/CLIProxyAPI/commit/ec06c3cb8bac53c7577772fd6272db8c70a2a93d) — fix(translator): normalize numeric schema constraints when converting parameters (#7149)
  Fork path overlap: none.
- [4448b96e](https://github.com/router-for-me/CLIProxyAPI/commit/4448b96e4c6e6bdfa69346d5c9ecee282b1644ac) — Merge pull request #7113 from zoidypuh/pr/muse-1414-client-headers (merge).
- [00b76771](https://github.com/router-for-me/CLIProxyAPI/commit/00b7677102fa6e3b1a2c2fa90c13458beaffd1b0) — fix(translator): preserve developer roles in responses to chat completions
  Fork path overlap: none.
- [3ac38498](https://github.com/router-for-me/CLIProxyAPI/commit/3ac384983ea2f2dcc8f198ab8fbca0a661036c5b) — fix(gemini): preserve parallel responses tool output batches across developer notices
  Fork path overlap: none.
- [7cad1cb7](https://github.com/router-for-me/CLIProxyAPI/commit/7cad1cb7df48c6c5ffb7ff0b34ef8198806115bc) — Merge pull request #7142 from gabrielrinaldi/fix/grok-video-1.5-lite (merge).
- [c888ff72](https://github.com/router-for-me/CLIProxyAPI/commit/c888ff72d46280202b51a5f635059ccd2dbb22e6) — fix(translator): normalize omitted and function roles in Gemini ingress translators
  Fork path overlap: none.
- [06ec5eac](https://github.com/router-for-me/CLIProxyAPI/commit/06ec5eac72d0e317d4b2c6bbb2da2728e0aa2639) — Merge pull request #7187 from sususu98/fix/gemini-omitted-content-role-7186 (merge).
- [32e3fd55](https://github.com/router-for-me/CLIProxyAPI/commit/32e3fd550d4cc4155fce8fd4d96b5840f36d48fb) — Merge pull request #7164 from sususu98/fix/transport-cache-panic-unlock-7162 (merge).
- [ef04e83a](https://github.com/router-for-me/CLIProxyAPI/commit/ef04e83ae9e22ebd9d8e1ed8d8db0976e2db543f) — Merge pull request #7165 from sususu98/fix/credential-dedup-connection-pool-7163 (merge).
- [f31d60aa](https://github.com/router-for-me/CLIProxyAPI/commit/f31d60aadb86d300cde7815ee97197357ae30fcc) — Merge pull request #7166 from sususu98/fix/request-logging-finalize-on-panic-7161 (merge).
- [533687e4](https://github.com/router-for-me/CLIProxyAPI/commit/533687e41899288ca4913363293f6980cd4f239d) — Merge pull request #6315 from SomSamantray/fix/antigravity-aicode-scope (merge).
- [d96b1941](https://github.com/router-for-me/CLIProxyAPI/commit/d96b19416846c6dafc88ad3307b16aab061255ea) — fix(pluginhost): reject model stream registration after concurrent teardown
  Fork path overlap: none.
- [d7af3711](https://github.com/router-for-me/CLIProxyAPI/commit/d7af371173ab046c291a873afec6ed6dd5019815) — Merge pull request #7167 from sususu98/fix/pluginhost-model-stream-ownership-7159 (merge).
- [b1a3e50b](https://github.com/router-for-me/CLIProxyAPI/commit/b1a3e50bdc3370dd92c508462a9343a675c27d9d) — docs: remove OpenLux sponsorship from READMEs
  Fork path overlap: none.
- [be2563ff](https://github.com/router-for-me/CLIProxyAPI/commit/be2563ff74a40979a9aadc59d0b24cedc6ba2e81) — docs: update Xcode sponsorship copy
  Fork path overlap: none.
- [6e6afab9](https://github.com/router-for-me/CLIProxyAPI/commit/6e6afab99a1939525ecb6bb1f0873c8e7a9df877) — refactor: migrate gjson, sjson, and base64 to MoeForge/moego
  Fork path overlap: `go.mod`, `go.sum`, `internal/api/handlers/management/auth_files.go`, `internal/auth/xai/xai.go`, `internal/runtime/executor/antigravity_executor.go`, `internal/runtime/executor/codex_executor_auth_test.go`, `internal/runtime/executor/codex_websockets_connection.go`, `internal/runtime/executor/codex_websockets_duplex.go`, `internal/runtime/executor/codex_websockets_execute.go`, `internal/runtime/executor/codex_websockets_executor.go`, `internal/runtime/executor/codex_websockets_executor_test.go`, `internal/runtime/executor/codex_websockets_session.go`, `internal/runtime/executor/codex_websockets_stream.go`, `internal/runtime/executor/devin_executor.go`, `internal/runtime/executor/meta_executor.go`, `internal/runtime/executor/xai_websockets_executor.go`, `sdk/api/handlers/claude/code_handlers.go`, `sdk/api/handlers/handlers.go`, `sdk/api/handlers/openai/openai_handlers.go`, `sdk/api/handlers/openai/openai_responses_interrupt_test.go`, `sdk/api/handlers/openai/openai_responses_websocket.go`, `sdk/cliproxy/auth/conductor_home_execution.go`, `sdk/cliproxy/auth/types.go`.
- [e5827065](https://github.com/router-for-me/CLIProxyAPI/commit/e582706557b22a4deeaa640f7d465432f7d91a0c) — fix(auth): prevent auto-refresh spin for disabled credentials with rejected refresh tokens
  Fork path overlap: `sdk/cliproxy/auth/conductor_refresh.go`.
- [0130d6f3](https://github.com/router-for-me/CLIProxyAPI/commit/0130d6f38c91e9bb3980484b8a2ef83be71c0a01) — fix(claude): keep Fable-only 429 rejections model-scoped when 5h headers are omitted
  Fork path overlap: none.
- [e25b7daf](https://github.com/router-for-me/CLIProxyAPI/commit/e25b7dafa0ea64cb91850d846707f7da731de91e) — fix(devin): sanitize embedded exec tool phrases and stop retrying request-caused 403 errors
  Fork path overlap: none.
- [24b04548](https://github.com/router-for-me/CLIProxyAPI/commit/24b0454811d263af2a0facb6b478a03d5d27b582) — fix(antigravity): map safety and filter finish reasons to refusal in claude response translator
  Fork path overlap: none.
- [d3899771](https://github.com/router-for-me/CLIProxyAPI/commit/d38997717dd38a328bca0b2b0ce3fe8e41a84c8d) — fix(management): honor targeted OAuth re-login across all built-in providers
  Fork path overlap: `internal/api/handlers/management/auth_files.go`, `internal/api/handlers/management/auth_files_fields.go`, `internal/api/handlers/management/auth_files_provider_oauth.go`, `internal/api/handlers/management/auth_files_v8.go`, `sdk/cliproxy/auth/types.go`.
- [a1eeb60e](https://github.com/router-for-me/CLIProxyAPI/commit/a1eeb60ec3f70dd20da774e7ea667e2d0bfceaea) — fix(codex): disable supports_image_detail_original for non-codex catalog routes
  Fork path overlap: none.
- [9acf81d3](https://github.com/router-for-me/CLIProxyAPI/commit/9acf81d3a6732f541bb40d0247f8ea46b5aa3ef6) — fix(claude): drop replayed web search citations when search results are missing
  Fork path overlap: none.
- [dde469d3](https://github.com/router-for-me/CLIProxyAPI/commit/dde469d3c2cf7f7ee4dce9b519b0b087b7132dbc) — fix(pluginhost): isolate unix plugin dlopen crashes with a preflight subprocess probe
  Fork path overlap: none.
- [54778bd3](https://github.com/router-for-me/CLIProxyAPI/commit/54778bd30de531eddaab41884bb124029a0f0751) — fix(agents): enforce usage of moego libraries for JSON and Base64 operations
  Fork path overlap: `AGENTS.md`.
- [6d4102f3](https://github.com/router-for-me/CLIProxyAPI/commit/6d4102f33f1037988330d2383a950bc6ffa1001f) — feat(config): support native-entrypoints configuration for Claude Code clients
  Fork path overlap: `config.example.yaml`, `internal/config/config_types.go`.
- [04208efa](https://github.com/router-for-me/CLIProxyAPI/commit/04208efa8b52767e228570dac90c93dde5e25fcb) — fix(pluginhost): preserve disabled auth state across plugin refreshes
  Fork path overlap: none.
- [be864305](https://github.com/router-for-me/CLIProxyAPI/commit/be8643051801879ac5b7147604b1d8db4bae73a6) — fix(translator/gemini): nest Claude tool_result images inside functionResponse.parts
  Fork path overlap: none.
- [b3442f65](https://github.com/router-for-me/CLIProxyAPI/commit/b3442f6544c57b29ba809cfcba7733b0729022a6) — fix(devin): keep sealed reasoning items sent back via the Responses API
  Fork path overlap: none.
- [193debb8](https://github.com/router-for-me/CLIProxyAPI/commit/193debb81d92a72313662e99008fc37e4c3e6a80) — Merge pull request #7229 from maomaocun/fix/devin-responses-reasoning-passthrough (merge).
- [cfa25b4a](https://github.com/router-for-me/CLIProxyAPI/commit/cfa25b4a0a80b52cfee1a84f5a87ebd79def6c15) — refactor(translator): build Devin thought step with a single ojson.Doc
  Fork path overlap: none.
- [01b24f1f](https://github.com/router-for-me/CLIProxyAPI/commit/01b24f1f95dc9b309765212c52b25b25e005ff01) — fix(codex): preserve and forward max_output_tokens across request translators
  Fork path overlap: `internal/runtime/executor/codex_executor_auth_test.go`.
- [6d7e6a3b](https://github.com/router-for-me/CLIProxyAPI/commit/6d7e6a3bb9da6f53bac316cea17e8061002cee7d) — feat(usage): extract cached image/audio and interactions modality tokens
  Fork path overlap: `sdk/cliproxy/usage/manager.go`.
- [fb30a9e8](https://github.com/router-for-me/CLIProxyAPI/commit/fb30a9e8bb4ac06c2748da667515ad81920aca4f) — fix(thinking): prefer Claude mid-conversation effort directives in usage extraction
  Fork path overlap: none.
- [061ffe6f](https://github.com/router-for-me/CLIProxyAPI/commit/061ffe6fbc222b43e7bfd39fc77536e3b1e93858) — fix(devin): honor Claude mid-conversation effort directives when selecting model variants
  Fork path overlap: `internal/runtime/executor/devin_executor.go`.
- [606e667a](https://github.com/router-for-me/CLIProxyAPI/commit/606e667a8ef17d05b4aa18bb52b262b5252f70e4) — fix(codex): sanitize invalid characters in Codex input item IDs
  Fork path overlap: none.
- [6e103599](https://github.com/router-for-me/CLIProxyAPI/commit/6e10359968392c5fef1483cb091393d5fe10aeac) — fix(pluginhost): abort hot reload on quiesce failure and coordinate usage replay
  Fork path overlap: none.
- [196ca839](https://github.com/router-for-me/CLIProxyAPI/commit/196ca839aa4df0cc04c546a16ae60af3c16d48fb) — Merge pull request #7231 from sususu98/feat/usage-cached-modality-tokens (merge).
- [f72f8bfd](https://github.com/router-for-me/CLIProxyAPI/commit/f72f8bfd6a1c7d69ca371dd4a7bdf490db50c154) — feat(antigravity): bump fallback version to 2.29.0 and add changelist mapping
  Fork path overlap: none.
- [f3762c64](https://github.com/router-for-me/CLIProxyAPI/commit/f3762c64de51a0d111e073a14288cf0981f7afcb) — feat(auth): extract Antigravity model discovery to sdk/auth
  Fork path overlap: `sdk/cliproxy/antigravity_models.go`.
- [f49bd5d9](https://github.com/router-for-me/CLIProxyAPI/commit/f49bd5d91ac99cff4be1b94c3ad12e63ee946744) — refactor(auth): route Antigravity model JSON operations through ojson
  Fork path overlap: none.
- [cbf0188e](https://github.com/router-for-me/CLIProxyAPI/commit/cbf0188effe5f93d448cac38124ed9cf431e1649) — Merge pull request #7238 from sususu98/feat/antigravity-models-discovery-sdk-auth (merge).
- [0e02765c](https://github.com/router-for-me/CLIProxyAPI/commit/0e02765ccfceefef7f502620c71cdbdf1b7a4ce1) — fix(auth): clone parsed model IDs and preserve nil unknown catalogs
  Fork path overlap: none.
- [c3938b72](https://github.com/router-for-me/CLIProxyAPI/commit/c3938b72bdaaa803ab983eeeb0005ce59cef505c) — fix(logging,clienterror): classify mid-stream cyber blocks as 400 and persist error logs
  Fork path overlap: `internal/api/middleware/request_logging_test.go`, `sdk/api/handlers/claude/code_handlers.go`, `sdk/api/handlers/gemini/gemini_handlers.go`, `sdk/api/handlers/openai/openai_handlers.go`, `sdk/api/handlers/openai/openai_responses_websocket.go`.
- [0a94b2ae](https://github.com/router-for-me/CLIProxyAPI/commit/0a94b2aefb520469869ef8ea7b66b74a3da68e1a) — fix(logging,executor): preserve Claude Fast classified error status and cap deferred body buffering
  Fork path overlap: `internal/api/middleware/request_logging_test.go`.
- [69a032de](https://github.com/router-for-me/CLIProxyAPI/commit/69a032dea3688fbab0d67686d1631ac9d2ea95f3) — perf,fix(logging,websocket): avoid 200 OK body buffering and synchronize ws disconnect error logging
  Fork path overlap: `internal/api/middleware/request_logging_test.go`, `sdk/api/handlers/claude/code_handlers.go`, `sdk/api/handlers/openai/openai_responses_websocket.go`.
- [03a8743c](https://github.com/router-for-me/CLIProxyAPI/commit/03a8743c3cf9d1f803a4503d038b745b2dc2b739) — fix(executor): classify non-stream Claude policy refusals as 400 request faults
  Fork path overlap: none.
- [508ac2bc](https://github.com/router-for-me/CLIProxyAPI/commit/508ac2bce0966af94a30e6d44c43283390932b45) — fix(executor,handlers): require stream-level evidence for Gemini filter blocks and pre-log terminal chunk errors
  Fork path overlap: `sdk/api/handlers/openai/openai_handlers.go`.
- [78a9cc9a](https://github.com/router-for-me/CLIProxyAPI/commit/78a9cc9aaa8ae90a66300d9cbddfe7c894307622) — chore(deps): update github.com/router-for-me/moego to v0.1.2
  Fork path overlap: `go.mod`, `go.sum`.
- [ec14dc47](https://github.com/router-for-me/CLIProxyAPI/commit/ec14dc4703f0a5b7185c0c625032af30b94f0a0e) — feat(kvstore): add local bbolt key-value store and integration routing
  Fork path overlap: `config.example.yaml`, `go.mod`, `go.sum`, `internal/config/config.go`, `internal/config/config_types.go`, `internal/config/config_v8.go`, `sdk/cliproxy/service.go`, `sdk/cliproxy/service_config.go`, `sdk/cliproxy/service_lifecycle.go`.
- [d57270f4](https://github.com/router-for-me/CLIProxyAPI/commit/d57270f465cc69ba73b9c34c2c783aa4a9196e2f) — fix(executor): support Codex remote compaction v2 on openai-compatible routes
  Fork path overlap: none.
- [e6e5708e](https://github.com/router-for-me/CLIProxyAPI/commit/e6e5708e677adb2463d45accdd24bc9fd028ddd8) — feat(claude): persist cloaked session date pins in persistent KV store
  Fork path overlap: `config.example.yaml`, `internal/config/config_types.go`.
- [f208c908](https://github.com/router-for-me/CLIProxyAPI/commit/f208c90824f265f3bc61c74544aac29918a2f7a3) — fix(handlers): record TTFT for direct plugin executor routes
  Fork path overlap: none.
- [7a7d37ec](https://github.com/router-for-me/CLIProxyAPI/commit/7a7d37ecb5c61d69413d19d7bce085a82740f5ca) — feat(codex): add support for gpt-image-2.6 models
  Fork path overlap: none.
- [010de194](https://github.com/router-for-me/CLIProxyAPI/commit/010de194c294ad3b562b4c8de71f59740f5bc8c8) — Merge pull request #7262 from Nciae-Zyh/feat/codex-gpt-image-26 (merge).
- [8c9a7851](https://github.com/router-for-me/CLIProxyAPI/commit/8c9a78518e39f0ca527e188f9efea0193f436a31) — fix(openai): bridge client tool_search in Chat Completions Responses translator
  Fork path overlap: none.
- [6044c5d1](https://github.com/router-for-me/CLIProxyAPI/commit/6044c5d14270fa094333b25932ac11ab13282821) — fix(openai): translate thinking content-part arrays in Claude response translator
  Fork path overlap: none.
- [be691624](https://github.com/router-for-me/CLIProxyAPI/commit/be69162478da2a082844e8223962fe035ea92138) — fix(executor,logging): preserve Antigravity usage evidence, sparse Claude refusal identity, and Home stream error logs
  Fork path overlap: `sdk/api/handlers/openai/openai_handlers.go`.
- [6897eebd](https://github.com/router-for-me/CLIProxyAPI/commit/6897eebd50617aa86cf04f1f8d9f38299b1a78cc) — Merge pull request #7242 from sususu98/fix/cyber-block-request-fault-and-error-log (merge).
- [dfe8b182](https://github.com/router-for-me/CLIProxyAPI/commit/dfe8b182c54de4f9c5ccc3d8581ca23b5e1b249b) — fix(codex,auth): bypass SIWC for OpenAI image requests and route directly to Codex images endpoint
  Fork path overlap: `internal/runtime/executor/codex_executor_auth_test.go`, `sdk/cliproxy/auth/conductor_execution.go`, `sdk/cliproxy/auth/conductor_home.go`, `sdk/cliproxy/auth/conductor_selection.go`, `sdk/cliproxy/auth/conductor_stream.go`, `sdk/cliproxy/auth/types.go`.
- [b5e6027c](https://github.com/router-for-me/CLIProxyAPI/commit/b5e6027ce4d72c88a92d5571d23afc22bbd7bd30) — fix(openai): expose terminal 429 error frames on responses websocket
  Fork path overlap: `sdk/api/handlers/openai/openai_responses_websocket.go`, `sdk/cliproxy/auth/conductor_home.go`, `sdk/cliproxy/auth/conductor_selection.go`.
- [62c58a74](https://github.com/router-for-me/CLIProxyAPI/commit/62c58a7480f3544d8cf08e7f3aadb1a1192a8949) — Merge pull request #7265 from sususu98/fix/codex-openai-image-bypass-siwc (merge).
- [585f97de](https://github.com/router-for-me/CLIProxyAPI/commit/585f97deac9aa57c700301221fb1a7085e1827cc) — fix(auth): pass context and headers to availableAuthsForSelector in HasAlternativeTargetForModel
  Fork path overlap: `sdk/cliproxy/auth/conductor_selection.go`.
- [f2e255f7](https://github.com/router-for-me/CLIProxyAPI/commit/f2e255f73eda25d8106097fbf2eeb5aa00f8ae29) — fix(usage): clear consumed queue slot in usage manager dispatcher
  Fork path overlap: `sdk/cliproxy/usage/manager.go`.
- [8fb60326](https://github.com/router-for-me/CLIProxyAPI/commit/8fb60326f405e8492580b98af6ffbb4b672b5810) — fix(codex): handle Responses-Lite hosted image_generation compatibility
  Fork path overlap: `internal/runtime/executor/codex_websockets_execute.go`, `internal/runtime/executor/codex_websockets_stream.go`.
- [a3c84a37](https://github.com/router-for-me/CLIProxyAPI/commit/a3c84a37c3a7875b9d306403145cdbdb96fdaac3) — feat(devin): update devin_models.json with swe-2-flash, nemotron-3-5-super, and gpt-6-1-terra
  Fork path overlap: none.
- [1c537211](https://github.com/router-for-me/CLIProxyAPI/commit/1c5372116735f7f5d88980bba651cbf175973d2b) — fix(auth): keep count_tokens 501 and unsupported capability responses availability-neutral
  Fork path overlap: `sdk/cliproxy/auth/conductor_execution.go`.
- [c2f402b4](https://github.com/router-for-me/CLIProxyAPI/commit/c2f402b45643f0740d70b9010fd7d8d0aab7ef65) — fix(meta): send X-Tbh-Contributor-Mode header only for contributor models
  Fork path overlap: none.
- [c1842be2](https://github.com/router-for-me/CLIProxyAPI/commit/c1842be2111065160ea7768ffb9a3f71b17b890d) — feat(models): add gpt-6-chat-thinking to registries
  Fork path overlap: none.
- [55c05f70](https://github.com/router-for-me/CLIProxyAPI/commit/55c05f703302f9b4789117055464cdd70e221fa1) — fix(claude): correct gemini tool result and signature translation
  Fork path overlap: none.
- [9be48133](https://github.com/router-for-me/CLIProxyAPI/commit/9be48133ed4de7a27bb91958183145acf0cc46f0) — fix(codex): normalize small claude output budgets
  Fork path overlap: `internal/runtime/executor/codex_websockets_execute.go`, `internal/runtime/executor/codex_websockets_stream.go`.
- [e458999b](https://github.com/router-for-me/CLIProxyAPI/commit/e458999bb3dafb0ec81cea5b95b3b98d87da72d7) — fix(claude): show cooldown retry time in error messages
  Fork path overlap: `sdk/api/handlers/claude/code_handlers.go`.
- [1d4e0549](https://github.com/router-for-me/CLIProxyAPI/commit/1d4e054978160a5a702021d422c363c24a95aa51) — fix(auth): preserve terminal responses across execution paths
  Fork path overlap: `sdk/cliproxy/auth/conductor_execution.go`.
- [2c16b1a3](https://github.com/router-for-me/CLIProxyAPI/commit/2c16b1a3dfcc6e0c890e344cca61af11f45f9270) — Merge pull request #7294 from router-for-me/fix/home-terminal-request-boundaries (merge).
- [fb77445f](https://github.com/router-for-me/CLIProxyAPI/commit/fb77445f0454ff6a3f063b144ed972bc198e2d1d) — fix(executor): support Codex remote compaction v2 on native Devin route
  Fork path overlap: `internal/runtime/executor/devin_executor.go`.
- [d1e5cc60](https://github.com/router-for-me/CLIProxyAPI/commit/d1e5cc607acd93c10a585f00b41776bbb26dcc11) — fix(codex): reject unsupported hosted image requests
  Fork path overlap: `config.example.yaml`, `internal/runtime/executor/codex_websockets_execute.go`, `internal/runtime/executor/codex_websockets_stream.go`, `sdk/cliproxy/usage/manager.go`.
- [e5e6ad1e](https://github.com/router-for-me/CLIProxyAPI/commit/e5e6ad1e1b91fc20ab9964e6fb22c0c269b9d882) — Merge pull request #7295 from router-for-me/fix/codex-hosted-image-request-scope (merge).
- [68cd7572](https://github.com/router-for-me/CLIProxyAPI/commit/68cd75724e4de4e0450475cc99c7491166cf3392) — fix(codex): prevent replay after uncertain refresh outcomes
  Fork path overlap: `internal/auth/codex/openai_auth.go`, `internal/runtime/executor/codex_executor_auth.go`, `internal/runtime/executor/codex_executor_auth_test.go`, `sdk/cliproxy/auth/conductor_refresh.go`.
- [0559fa3c](https://github.com/router-for-me/CLIProxyAPI/commit/0559fa3cfd5722cf1755a7dc3a90573ede3835f6) — feat(management): expose oauth_provider in credential list
  Fork path overlap: `internal/api/handlers/management/auth_files.go`.
- [bfac8586](https://github.com/router-for-me/CLIProxyAPI/commit/bfac85864cd41a8ad253364fda4381cf5eeef50a) — fix(codex): normalize small output budgets for all source formats
  Fork path overlap: `internal/runtime/executor/codex_websockets_execute.go`, `internal/runtime/executor/codex_websockets_stream.go`, `sdk/cliproxy/auth/conductor_execution.go`.
- [2443e883](https://github.com/router-for-me/CLIProxyAPI/commit/2443e8830c401e6054370c109388bb3b59daf21c) — fix(codex): normalize nested custom tools across Chat Completions round trips
  Fork path overlap: none.
- [374056f2](https://github.com/router-for-me/CLIProxyAPI/commit/374056f28bfaa6b7582a82cba9f8ca84868f642e) — fix(codex): preserve prompt cache prefix when loading deferred tools via ToolSearch
  Fork path overlap: none.
- [c9f97398](https://github.com/router-for-me/CLIProxyAPI/commit/c9f9739869b2bb0e3d2f7d29d54bddecc6caa12e) — Merge pull request #7308 from WizisCool/feat/management-oauth-provider (merge).
- [482efbf5](https://github.com/router-for-me/CLIProxyAPI/commit/482efbf5a777771523c1237ed7aaee965451c51d) — feat(auth): enhance OAuth provider handling with explicit hints and metadata preservation
  Fork path overlap: `internal/api/handlers/management/auth_files_provider_oauth.go`.
- [ea0b4712](https://github.com/router-for-me/CLIProxyAPI/commit/ea0b471291a992c5d80358917dabc6cb9f0f25f6) — fix(claude): align OAuth authorization state and scopes
  Fork path overlap: `internal/api/handlers/management/auth_files_provider_oauth.go`, `internal/auth/claude/anthropic_auth.go`.
- [eabcd01d](https://github.com/router-for-me/CLIProxyAPI/commit/eabcd01d77e977d5b93a1d647ab8f6ca90b9cbb3) — Merge pull request #7317 from router-for-me/fix/claude-oauth-authorization-parameters (merge).
- [27762819](https://github.com/router-for-me/CLIProxyAPI/commit/277628193f54d99b2eeca57ecd7f9ff9b349a996) — fix(claude): keep mid-session responses system items in place to preserve prompt cache
  Fork path overlap: none.
- [c5251b57](https://github.com/router-for-me/CLIProxyAPI/commit/c5251b57b3fa5dcbc689f514f4aa166daacb4481) — fix(translator): preserve reasoning and text across multi-part gemini and antigravity chunks
  Fork path overlap: none.
- [5f38d557](https://github.com/router-for-me/CLIProxyAPI/commit/5f38d5572c432c67ec260be55f73b0ec72ac0764) — fix(auth): keep native Codex available during SIWC recovery
  Fork path overlap: `sdk/cliproxy/auth/conductor_lifecycle.go`, `sdk/cliproxy/auth/conductor_refresh.go`.
- [bbefed68](https://github.com/router-for-me/CLIProxyAPI/commit/bbefed68244d9d79140ea10b74815a035c735fc7) — Merge pull request #7305 from router-for-me/fix/codex-refresh-uncertain-outcome (merge).
- [f4d3f061](https://github.com/router-for-me/CLIProxyAPI/commit/f4d3f0618d158c3168e113340fd06af15200d67a) — fix(openai): replace responses websocket transcript when pending tool calls are unanswered
  Fork path overlap: none.
- [b5b724d0](https://github.com/router-for-me/CLIProxyAPI/commit/b5b724d06df01748055259512d163605fd2f482d) — fix(pluginhost): reuse content-addressed plugin shadow copies across restarts
  Fork path overlap: none.
- [69637a66](https://github.com/router-for-me/CLIProxyAPI/commit/69637a662a05aebc64805760be960284b2459cb2) — fix(openai): skip whitespace-only reasoning when opening Claude thinking blocks
  Fork path overlap: none.
- [ce38c75c](https://github.com/router-for-me/CLIProxyAPI/commit/ce38c75c624df1ceb0777f3e7c21f892883569ed) — fix(claude): drain streamed response body after message_stop to reuse upstream connections
  Fork path overlap: none.
- [e6931d51](https://github.com/router-for-me/CLIProxyAPI/commit/e6931d51e328343088d5902e5cbd06edbe4d8633) — fix(translator): preserve standalone Claude output_config.effort on OpenAI requests
  Fork path overlap: none.
- [91e0dee8](https://github.com/router-for-me/CLIProxyAPI/commit/91e0dee827abf476f9f86246f68c500a112f7155) — fix(openai): prevent SSE keep-alive injection mid-line during image streaming
  Fork path overlap: `sdk/api/handlers/handlers.go`.
