# Shared hub credential authority

This is an inactive foundation for Tracker t12857. Do not enable it on the estate yet. Parent t12792 owns
installation and live acceptance. Follow-up t13262 owns the remaining provider and transport coverage needed to
preserve the currently enabled routes. No production credential, binary, service or configuration was changed here.

## Ownership and source contract

- This lane, ln-eb836543, owns CLIProxyAPI's client, encrypted snapshots, store adapter, guarded runtime and fixtures.
- Tracker t12884 owns `apps/tracker/HUB-AUTHORITY.md` and `packages/tracker-contract/src/hub.ts` in
  Automancer-Ltd/automancer-dashboard. Baseline PR1056 merged as `35b4aa6f20099596b05cf933b09dd220faf6805e` after
  exact-commit review of `c57959855e6420c170a48936c70e7f6cef1af5e1`. The metadata extension is PR1065 source
  `7b8fc29d51cf7e5c2397a8b19422cf8926670ea1`. Reconcile its accepted review, merge and served contract before activation.
  A previous review of PR1056 does not review the metadata extension.
- t12900 owns the dedicated private B2 prefix, key and encryption binding. Its human credential-transfer dependency
  is t13054. The Tracker Access pair alone cannot read B2 or decrypt credentials.
- t12792 owns failover routing, installation, rollback coordination and the one live outage/return acceptance.
  Read that item's latest t11543 ruling before the drill; this code change authorizes no drill or installation.
- t13262 waits on this foundation; parent activation waits on t13262. This implementation does not wait on the live
  drill, which would create a circular dependency.

## Supported modes and activation boundary

| Mode | This foundation |
| --- | --- |
| Claude single OAuth refresh grant, HTTP requests/streams | Guarded |
| Codex single outer OAuth refresh grant, HTTP requests/streams | Guarded |
| Codex SIWC or combined outer/SIWC | Refused before issuing a refresh intent/provider call |
| Codex upstream WebSocket and duplex/replay | Refused; HTTP fixtures do not establish WebSocket acceptance |
| Other shared providers, static keys and local routes | Not enrolled; no shared credential authorization |
| Dynamic plugins or Home with shared mode | Startup refused |
| Native CLI login/import/SIWC commands with shared config | Refused; use the active hub's v8 management flow |
| v8 management explicit login/upload, native metadata/status, removal | Authoritative encrypted writes |
| Native timed hold expiry/clear | Selection restriction only; never emits Tracker unhold |
| Legacy auth-file watcher, config-key ingestion and live config mutations | Disabled/refused in shared mode |
| Legacy file download | Refused; it cannot return an obsolete copied file |

Parent inventory and transport evidence are t12857 events181815 and182562. They establish enabled wider providers
and configured WebSocket capability, not proof of every grant type or actual upstream WebSocket traffic. Read live
inventory through `hub-readonly.usage` and the configured mode flags when classifying t13262. Do not infer that a
`gemini-credit` route proves Gemini OAuth or that an absent fixture proves SIWC/plugins are unused.

The default configuration retains existing behavior. Adding `hub-authority` switches the entire service to the shared
store; it is not a way to silently remove current routes. No activation is safe until t13262 preserves the required
live provider/transport behavior and parent acceptance is ready.

## Protocol and encrypted data

Each boot generates a random process nonce. Tracker grants a separate hub-process epoch; the Dispatcher lease only
chooses its watcher. Conservative expiry uses monotonic request-start time and a safety margin. Acquire, renew,
hydrate and the current account controls govern serving. There is no local-file fallback.

A refresh records a durable intent before the provider call. Only the first `provider_call_allowed:true` permits a
callback. The actual native refresh HTTP dispatch consumes a second local one-use check of the same epoch/head;
delayed work cannot extend the lease. Native refresh has no retries or redirect replay. Claude returns rotation data
before any optional profile lookup. Ambiguous calls quarantine only the affected account generation.

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
The store compares the whole grant after excluding only explicitly recognized user controls. Unknown/nested fields
are grant data. It rebases only changed controls onto the latest record. Late refresh results merge into the latest
controls. No metadata write, timed expiry, refresh or login can lift Tracker held. Explicit administrative unhold
remains an existing authenticated Tracker action. Duplicate/renamed refresh grants and a replayed login cannot clear
quarantine by advancing a generation.

## Runtime callers

`internal/hubauth` owns protocol, encrypted objects, coordinator and native Store adapter. `sdk/cliproxy/auth/hub.go`
gates Manager execution, counting, streams, retries, preparation, raw HTTP and refresh. The native Claude/Codex refresh
entry points also route direct calls through the authority. HTTP transports recheck the hydrated generation and
controls at actual dispatch. Management usage/token helpers use the same guard. Authoritative save failures are fatal
before publishing memory. Polling the head preserves unchanged local runtime state and repairs changed snapshots.

Service startup installs the shared store before refresh and HTTP serving, and never ingests the copied auth directory.
A cold standby may expose its listener while credential operations fail closed. `/hub/ready` returns 200 only with
current process ownership and verified hydration, otherwise 503. This is process readiness, not proof that all expected
accounts were migrated or a served binary revision; retain the release's separate revision check.

Native `shared-upstream` config is not machine ownership. Home would add another control plane and is not used.

## Prepared installation sequence for the parent

Do these only after reviewed source, t12884 release/identity receipts, t12900 scope/decrypt receipts and t13262 coverage.
The lane has not run these steps with real credentials.

1. Reconcile current release ownership and binary revision on both hosts. Use the parent's existing release workflow.
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

Next: complete the final checks, commit/push one fork PR, record its exact head/CI and request Q7 authentication review
on t12857. End the turn after setting in_review. Reconcile t12884's final reviewed metadata merge before sign-off.
After this foundation merges, t13262 supplies the activation coverage; parent t12792 retains live acceptance.

Last verified: 2026-11-01, synthetic credentials on auto-vault/m6 and source/Tracker reads. No production activation.
