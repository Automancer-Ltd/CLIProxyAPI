# Shared hub credential authority

Shared authority remains inactive pending the reviewed coverage change and parent installation acceptance.
Tracker t13262 extends the t12857 foundation to the enabled provider pool and Codex transports. Parent t12792 owns
installation and live acceptance. This source task changes no production credential, binary, service or configuration.

## Ownership and source contract

- Foundation t12857 merged in fork PR47 as `fc067016e00c4265711fea0bdbb4a5548138ad52`. Its reviewed head was
  `28ae113cdd135fe3c8387f2811333593ef276cc5` (t12857 event187054). Coverage lane ln-8bc0c823 owns t13262.
  Read `tracker.items.get {id:"t13262"}` for its current deliverable and exact-commit review receipt.
- Tracker t12884 owns `apps/tracker/HUB-AUTHORITY.md` and `packages/tracker-contract/src/hub.ts` in
  Automancer-Ltd/automancer-dashboard. Its contract merge is `5fc922cf9dd4b6ec35dc012f61313746c1580a23`.
  Read that item for the current served revision and protected identity acceptance; source review is separate.
- t12900 owns the dedicated private B2 prefix, key and encryption binding. Its human credential-transfer dependency
  is t13054. The Tracker Access pair alone cannot read B2 or decrypt credentials.
- t12792 owns failover routing, installation, rollback coordination and the one live outage/return acceptance.
  Read that item's latest t11543 ruling before the drill; this code change authorizes no drill or installation.
- Parent activation waits on the reviewed t13262 merge. This implementation does not wait on the live drill,
  which would create a circular dependency.

## Provider and mode coverage

The inventory below records the non-secret m6 configuration read on 2026-11-02. Refresh it through
`hub-readonly.usage` and the read-only preflight below before installation. The service plist is
`/Users/waseem/Library/LaunchAgents/uk.automancer.hub.plist`; its `ProgramArguments` identify the binary and config.
The inspected config uses v8 `oauth.auth-dir` and `api-keys.openai-compatibility[].keys`. File field presence was
read on m6 without copying or printing token values. Enabled labels alone were not used to infer credential type.

| Enabled route | Observed source/mode | Guarded source path |
| --- | --- | --- |
| antigravity | OAuth access/refresh token and existing project_id | `antigravity_executor_auth.go`: request-time and scheduled refresh; native HTTP, credits and SDK model discovery use guarded transport |
| claude | Single OAuth refresh grant | Foundation Claude native refresh and HTTP/stream/prepare/raw-request guards |
| codex | Outer OAuth grant; no SIWC or upstream websockets flag in inspected files | `CodexAutoExecutor`: HTTP for current configuration; guarded upstream WebSocket/duplex when explicitly enabled |
| xai | OAuth access/refresh token with token_endpoint; no upstream websockets flag | `xai_executor_auth.go` and `internal/auth/xai`: one token POST; native HTTP routes guarded |
| meta | DCA grant with minted access/API key | `meta_executor.go` and `internal/auth/meta`: key mint is a durable refresh, including management usage polls |
| devin | Session token/API key | `devin_executor.go`: serving and status HTTP guarded; status observations commit as metadata without rotating the grant |
| deepseek | Configured static OpenAI-compatible key | Native config synthesizer and OpenAICompatExecutor |
| zai | Configured static OpenAI-compatible key | Native config synthesizer and OpenAICompatExecutor |
| opencode-go | Configured static OpenAI-compatible key | Native config synthesizer and OpenAICompatExecutor |
| local | Configured local OpenAI-compatible route/key | Native config synthesizer and OpenAICompatExecutor |
| gemini-credit | Configured static OpenAI-compatible key | Native config synthesizer and OpenAICompatExecutor; this is not Gemini OAuth |

Static routes retain their config identity, model mappings, headers, proxy and scheduling settings. They require
current process ownership and hydration to serve, but create no OAuth account, generation or refresh intent.
Their configuration is immutable for a running shared process; an ordinary coordinated config release changes it.
Encrypted snapshot identities cannot collide with these routes. Mutable logins retain native timed holds, group,
pin and disabled settings alongside Tracker holds. Devin's known status fields and Antigravity's catalog cache
are metadata observations; they cannot rotate a grant or clear quarantine.

The inspected service has Home and plugins disabled. No SIWC was found. Native CLI login/import commands, Home,
dynamic plugins, SIWC, native config-key providers outside OpenAI compatibility, and xAI upstream WebSockets are
not enrolled. The builder/preflight, shared management handlers and direct xAI WebSocket executor reject those
paths before provider effects. Codex downstream WebSocket capability is preserved: downstream transport alone
does not force upstream WebSockets. This work does not alter any client transport setting.

### Read-only activation preflight

Build `go build -o /PATH/hub-preflight ./cmd/hub-preflight` from the reviewed commit. On each intended host run:

```sh
/PATH/hub-preflight -config /PATH/TO/SERVICE/config.yaml
# Optional override for the source inventory being reconciled:
/PATH/hub-preflight -config /PATH/TO/SERVICE/config.yaml -auth-dir /PATH/TO/SOURCE/AUTHS
```

Use the paths returned by that host's service definition. The command reads config and source credential files,
reports only provider/mode/count rows, and returns nonzero on an unsupported or unreadable source. It starts no
service, performs no provider request, writes no file, and does not acquire ownership. It deliberately avoids
`LoadConfig`, which can hash and rewrite a plaintext management key. Reject symlinked credential entries.
Reconcile the report with `hub-readonly.usage`, disabled accounts and each host's expected static routes. A passing
preflight establishes format coverage, not credential validity, migration completeness or live outage acceptance.

Startup never ingests copied OAuth files. It loads only the encrypted authority plus synthesized static config.
An unsupported encrypted record prevents manager load and `/hub/ready` success. Empty bootstrap readiness still
requires the parent's explicit account/mode reconciliation before routing.

## Protocol and encrypted data

Each boot generates a random process nonce. Tracker grants a separate hub-process epoch; the Dispatcher lease only
chooses its watcher. Conservative expiry uses monotonic request-start time and a safety margin. Acquire, renew,
hydrate and the current account controls govern serving. There is no local-file fallback.

A refresh finishes request preparation before creating its durable intent inside the final native HTTP dispatch
check. That check serializes with local Stop, synchronizes the latest head, and compares the prepared target grant
and controls. Unrelated account changes do not invalidate that baseline. Only the first
`provider_call_allowed:true` admits one provider call. There is no post-begin global-head read to re-authorize it.
Later controls still govern serving, and result commits preserve current controls and reject superseded generations.
The local guard consumes permission once and checks the conservative deadline after the reply. It also closes when
the callback returns, so retained callback contexts cannot create later admissions.

Parent decision t12884 event183361 keeps the existing protocol: if an accepted begin reply is lost or arrives after
the conservative deadline, send nothing and retain the unresolved intent. There is no unsent cancellation/rearm or
fabricated successful refresh/login. This can require a new genuine login for that account even when the process
knows it sent nothing; it is an accepted availability limitation of the inactive foundation. A local attempted begin
also survives an older account-list reply until a definite authority refusal or a newer grant generation resolves it.
Native refresh has no retries or redirect replay. Claude returns rotation data before any optional profile lookup.

Encrypted objects use `HSN1` + random 12-byte nonce + AES-256-GCM ciphertext/tag. The full object key is associated
data. Tracker stores only UUIDs, encrypted digests, counters and pointers. Object names are write-once in the client;
B2 does not enforce immutability. Both upload and readback must succeed before head CAS. Losing CAS never deletes.
Ciphertext is fsynced in the private spool before upload; retries preserve its exact bytes.

- `hub-auth-snapshots/heads/<operation UUID>` contains `{accounts:{<account UUID>:{generation,data}}}`.
- `data` contains the native credential's ID, provider, complete metadata and attributes, label, prefix, proxy URL and
  native disabled control. Provider identity and payload exist only inside encryption.
- `hub-auth-snapshots/results/<intent UUID>` contains `{account_id,generation,intent_id,complete,data}`.
  `complete:false` retains a partial outcome without allowing recovery to resolve its intent. Such an account remains
  quarantined pending explicit reconciliation/new login. The enrolled Codex path refuses dual grants before effects.
- Account UUIDs and grant generations are separate from native scheduler counters and credential filenames.

Metadata writes use the closed `kind:metadata` commit, preserving generation, status and every unresolved intent.
The store compares the whole grant after excluding only explicitly recognized user controls and native status/catalog observations. Unknown/nested fields
are grant data. It rebases only changed controls onto the latest record. Late refresh results merge into the latest
controls. No metadata write, timed expiry, refresh or login can lift Tracker held. Explicit administrative unhold
remains an existing authenticated Tracker action. Duplicate/renamed refresh grants and a replayed login cannot clear
quarantine by advancing a generation.

## Runtime callers

`internal/hubauth` owns protocol, encrypted objects, coordinator and native Store adapter. `sdk/cliproxy/auth/hub.go`
gates Manager execution, counting, streams, retries, preparation, raw HTTP and refresh. The native Claude, Codex, Antigravity, xAI, Meta and Devin refresh
entry points also route direct calls through the authority. Devin uses its status-only path. HTTP transports recheck the hydrated generation and
controls at actual dispatch. Each authorization reads live owner/head state without holding the mutation mutex and
compares it with immutable verified snapshot data, current local quarantine and the conservative deadline. A changed
head requires serialized hydration before serving; there is no stale-control grace period. Delayed reads cannot
replace newer local state. An unchanged runtime Store.Save performs no credential I/O; it grants no serving rights. Management usage/token helpers use the same guard. Authoritative save failures are fatal
before publishing memory. Polling the head preserves unchanged local runtime state and repairs changed snapshots.

Service startup installs the shared store before refresh and HTTP serving, and never ingests the copied auth directory.
A cold standby may expose its listener while credential operations fail closed. `/hub/ready` returns 200 only with
current process ownership and verified hydration, otherwise 503. This is process readiness, not proof that all expected
accounts were migrated or a served binary revision; retain the release's separate revision check.

Actual shared-credential callers are covered at these boundaries:

| Caller | Final effect boundary |
| --- | --- |
| Manager execute, stream, count, prepare, raw HTTP and retries | `sdk/cliproxy/auth/hub.go` and `conductor_execution.go`; native transport rechecks after preparation |
| Claude/Codex OAuth refresh | Foundation native refresh dispatch guard; SIWC partitions remain refused |
| Antigravity request-time/scheduled refresh | `refreshToken` enters the same authority and returns rotation data before optional work |
| xAI refresh and Meta mint | `RefreshClient` consumes one permission, disables redirects and bypasses local refresh singleflight for enrolled calls |
| Devin status refresh | Serving guard, unchanged-grant comparison and metadata commit; no refresh intent |
| Native request/stream/count/media/credits HTTP | `helps/proxy_helpers.go`, `helps/utls_client.go`, and `newAntigravityHTTPClient`; all selected auths retain their authority binding |
| Antigravity background catalog | `sdk/cliproxy/antigravity_models.go` guards its separately constructed HTTP client |
| Management API calls and usage reads | `api_tools.go` guards token resolution and final transport; required refresh uses Manager |
| Meta management usage | `usage_refresh.go` calls Manager refresh and parses the encrypted committed mint response; never discards a replacement key |
| v8 upload/login, metadata/status and removal | Authoritative Store writes; unsupported login/import providers refuse before dispatch; old file download refuses |
| Codex upstream WebSocket/duplex/reuse/control frames | Live admission at dial and each user request message; local lease/fence/account checks on socket I/O and decoded/queued delivery, including TLS buffering |
| File watcher, live config mutation, Home, plugins, native CLI login | Not started/refused in shared mode; no alternate credential authority |

A WebSocket session captures the process host, nonce and epoch plus the selected account baseline. A later process
acquisition cannot authorize an old socket. Authority failure closes and permanently invalidates that connection.
Live Tracker admission runs at connection opening and each user create, append, steer or interrupt message.
TLS records, ping/pong, response deltas and buffered delivery normally check the immutable locally hydrated view:
the original process fence, current lease deadline, account status/quarantine/generation and exact record baseline.
The coordinator loop in `sdk/cliproxy/service_hub.go` updates that view; observed ownership/head loss, failed
hydration, shutdown or local mutations invalidate it. An empty or concurrently replaced view is not itself a
session refusal: frame checks join any ongoing mutation and, if still needed, perform one serialized rehydration.
They then compare the session's own account and baseline against the current view. Concurrent readers share that
publication; unrelated account edits do not permanently close their sockets. Known lease/fence loss or shutdown
still refuses immediately, and a failed rehydration never authorizes from stale data. External control propagation during a turn is bounded by
that loop's cadence and the conservative lease deadline; a new turn always performs a live check. A failed socket
check permanently closes the connection. Messages already dispatched cannot be recalled. Parent acceptance still
owns live latency and failover measurement. Stable response-frame volume adds no Tracker traffic; publication
gaps can pause delivery for the mutation and one shared rehydration.
For HTTP, authorization is at dispatch; this does not claim cancellation of an already accepted provider request.

Native `shared-upstream` config is not machine ownership. Home would add another control plane and is not used.

## Prepared installation sequence for the parent

Do these only after reviewed source, t12884 release/identity receipts, t12900 scope/decrypt receipts and t13262 coverage.
The lane has not run these steps with real credentials.

1. Run the read-only preflight on both hosts and reconcile all expected routes, grant modes and controls.
   Reconcile current release ownership and binary revision on both hosts. Use the parent's existing release workflow.
   Retain the previous binaries/configuration and the newest auth material using its verified backup procedure.
2. Stop both old hub services and their automatic restart paths while switching authority. Do not permit either old
   binary to refresh the shared files during migration. Preserve provider-result spools and unresolved intents.
3. Deliver each host's own Tracker Access pair and the dedicated B2/encryption files with the credential workflow.
   Directories must be 0700 and files 0600. Never use the broad backup key. Do not put values in config, argv or logs.
4. Build/release the reviewed CLIProxyAPI commit. Add this path-only configuration, substituting the host and absolute
   paths returned by the installation workflow. `~` is not expanded by the client:

   ```yaml
   hub-authority:
     host: m6 # auto-vps on standby
     url: https://tracker.automancer.uk/v1/call
     access-id-file: /ABSOLUTE/.config/estate-hub-authority/access-client-id
     access-secret-file: /ABSOLUTE/.config/estate-hub-authority/access-client-secret
     b2-endpoint: s3.eu-central-003.backblazeb2.com
     b2-bucket: automancer-offsite
     b2-id-file: /ABSOLUTE/.config/estate-hub-snapshot/b2-key-id
     b2-key-file: /ABSOLUTE/.config/estate-hub-snapshot/b2-key
     encryption-key-file: /ABSOLUTE/.config/estate-hub-snapshot/encryption-key
     spool: /ABSOLUTE/.local/state/estate-hub-snapshot/spool
   ```

5. Start only the chosen initial process on the controlled installation path. Migrate reconciled latest credentials
   through its authenticated v8 upload/login path. Do not rsync old files into a running guarded process or classify
   metadata edits as new logins. Keep routing closed until expected accounts, native controls and administrative
   holds are compared with the verified encrypted head. Empty bootstrap readiness alone is insufficient.
6. Start the standby with its own fresh nonce. Verify it cannot serve or refresh while the current owner is live.
   Route using ownership/hydration readiness, never HTTP liveness alone. Parent t12792 owns the separate approved
   stop/return drill and served-revision/monitor acceptance; there is no competing drill here.

## Rollback

Before activation, restore the prior binary/config with the existing release procedure; no shared auth state was used.
After activation, first stop provider work on both hubs. Keep Tracker generations/intents, every committed head,
unresolved result object and both ciphertext spools. Prefer a forward fix while this authority remains in force.

Restore previous binaries/configuration only while services remain stopped. Before restarting an older file-based
binary, the parent must reconcile/export the latest verified decrypted head into that binary's native layout,
including current controls and disabled status for held/quarantined accounts. Its export must understand the `data`
schema above; a copy of the pre-install auth directory is not a rollback. This foundation does not provide an
unfenced automatic plaintext exporter. If reconciliation cannot be proved, keep services stopped and preserve the
material. That is safe but does not restore availability; report the limitation rather than serving stale grants.

A point-in-time Tracker restore can forget provider calls after its bookmark. Stop both hubs and reconcile those
results/intents before resuming; a restored epoch is not evidence that old tokens are safe.

## Synthetic verification and continuation

The previous lane's unguarded public-Manager probe on base `3ee7b50a` submitted the same Claude and Codex fixture
generation twice. Current tests exercise the public Manager and v8 management handlers against a local HTTP authority
fixture and an encrypted blob fixture. Production Worker transaction/identity proofs remain with t12884; a client
fixture is not proof of its deployment.

Client checks cover partition with both processes alive, primary restart while standby owns the epoch, late result
recovery, newer login/admin hold/native metadata, failed head publication, lost begin/upload/commit acknowledgments,
watcher transfer, delayed permission, duplicate grants and simultaneous renamed logins, unknown nested grant edits, partial-result retention, object
AEAD/immutability, spool symlinks and stale local files. Native provider checks cover one POST, redirect/retry refusal,
Claude result-before-profile and direct executor gating. Config migration must retain shared authority.

Checks run through `heavy` with bounded Go workers. The focused Manager/management/native tests pass. Server build,
vet on touched packages and the hub suite with the race detector passed on m6, including the final dispatch check and config migration regression. Fork CI builds and runs all changed Go packages plus its hub packages.

Q7 rejected the first head `05b12bae` for stranding pre-send intents after unrelated head changes and serializing
request-path Tracker calls under the mutation mutex. Both were then reproduced in public-Manager synthetic tests.
The corrected hub suite passed with the race detector on auto-vault; server build and touched-package vet also passed.
The corrected tests cover concurrent account refresh completion, metadata/hold/Stop before admission without a
stranded intent, a delayed accepted permit with zero native sends and retained quarantine, independent requests
while one authority read stalls, rejection of that stale reply after a hold, unchanged runtime saves without network
I/O, and a lost begin acknowledgment whose insertion occurs after an older account-list reply.

Coverage t13262 adds native takeover/redirect tests, partial rotation retention, static routes without invented
refreshes, Devin status semantics, management Meta mint persistence, startup/login refusal and Antigravity discovery.
Codex tests exercise Manager -> AutoExecutor -> duplex with a valid initial create and steering frame, then hold,
generation change or takeover before the next frame. Separate regressions cover decoded and queued buffered frames,
permanent socket failure, reacquired epochs, and downstream WebSocket clients whose upstream remains HTTP.
The new provider and buffered-frame tests fail on foundation merge `fc067016`; all endpoints and grants are synthetic.

Independent review of `7dfd804586d1` found that the generic HTTP helper's hub wrapper hid the Antigravity
transport before native pool selection. The corrected path selects the transport with an unbound clone and
adds one guard around the final credential-scoped pool. `TestAntigravityHubHttpRequestPreservesNativeTransport`
reproduces the failure on that head through public `HttpRequest` calls to a synthetic TLS server offering HTTP/2:
both default and context transports advertised ALPN, used HTTP/2, shared a connection across credentials and
authorized twice per dispatch. It requires native TLS 1.3/HTTP/1.1 without ALPN, same-credential connection reuse,
separate connections across credentials, one authorization per dispatch and zero sends after ownership loss.

Independent review of `c765829d890b` found that WebSocket frame checks performed live Tracker reads, coupling
stream throughput and reliability to control-plane round trips. The corrected session check is local; live
authorization moved to the user-message boundary. `TestCodexDuplexUsesLocalFrameChecksAndLiveTurnAdmission`
fails on that head when a synthetic Tracker outage interrupts the first response delta. It now delivers 64 deltas
plus ping/pong with zero authority calls while the local lease remains valid, admits the next create with one live
call, and refuses a further create during the outage. Local-view checks cover lease expiry, observed takeover,
hold, grant/control changes, quarantine, failed hydration and shutdown. Existing tests retain buffered-frame
refusal, permanent socket failure and rejection of a reacquired epoch.

Independent review of `34e1f601c7e9` found that an empty view during any account's commit incorrectly killed
unrelated sessions. `TestHubSessionSurvivesUnrelatedAccountWrites` reproduces this with 20 concurrent priority
edits on a Claude account and hold/unhold/remove controls, while checking a Codex session. It now requires zero
refused checks for those changes and still rejects a hold on the Codex account itself. Frame checks synchronize
only when publication is missing or changes during their read; the ordinary path remains local. A failed
rehydration and known ownership loss retain refusal, without resurrecting a failed socket or a later epoch.

Initial coverage validation passed on m6: the full hub authority suite with `-race`, the buffered Codex transport checks
with `-race`, both server/preflight builds, vet and tests for every changed Go package. The standalone preflight
returned the expected synthetic provider/mode/count and left its config/auth inputs byte-for-byte unchanged.
Read t13262 for the exact final-head CI and independent authentication review. Coverage is
source/synthetic evidence only. Parent t12792 must reconcile the reviewed merge, release ownership, both served
revisions, identity/B2 receipts, migrated account controls and live monitor/outage acceptance before completion.
Rollback must preserve the newest encrypted head, provider results, both spools and every unresolved intent as above.

Last verified: 2026-11-02, source and non-secret m6 inventory reads; synthetic fixtures on auto-vault and m6. No production activation.
