# Interactive hub v8.0.60 cutover plan (t16638)

**Prepared only. Do not run this from a session whose model uses the hub.** Production remains on the existing build. This task never contacted m6. The interactive cutover must wait for **t16644** (journal-safe failover and rollback), and run as one detached, self-checking transaction with no model calls after launch.

## Why the current binary-only command is insufficient

Upstream `68cd7572` writes immutable refresh claims and credential-bearing receipts below `auth-dir/.codex-refresh/<hash>/receipt`. An empty hash directory is itself a pending claim. Current `automancer-ops/scripts/hub-native-control.mjs` only rsyncs top-level `*.json`, hashes only those files/config, and hands back only credential JSON. Each mirror is a fresh generation. It therefore drops the journal. `hub-apply.py` can also restore an old binary that ignores the journal and may replay an already spent refresh grant.

This is a proven source incompatibility, not a report of a live failure. t16644 must fix and drill it before the sequence below is executable. Its receipt must name the tested ops commit, release wrapper, invocation and journal-aware fallback. Do not bypass it with an ordinary binary swap, snapshot restore, deleting `.codex-refresh`, or a hold/disabled flag that has not been proven to stop background refresh. A completed receipt can precede credential-file adoption, so checking only pending claims is insufficient.

## Inputs and preparation, before any live change

1. Use an interactive session with an independent model route. Read t16638, t16644 and the latest ops `docs/hub-apply.md`, `docs/hub-failover.md`, and this fork's `docs/estate-hub.md`. Confirm t16644's fixture drill passed and its release transaction includes all takeover-capable backups. t15588 determines whether auto-bee is active or still mirror-only; do not assume its role. Do not activate shared `hub-authority`, change routing policy, repin accounts, or rewrite live config as part of this release.
2. Resolve the merged CLIProxyAPI revision, preserving v8.0.60 ancestry. The initial integration is `82d600f983f52f153e0ff447793ac67ba9f96171`; the final artifact SHA is in the t16638 result and attached validation receipt. Later commits finish the base64 migration, repair mock CONNECT buffering and fix root-module CI package selection. Rebuild the exact merged revision in a clean checkout rather than trusting a mutable branch or copying a lane's scratch binary:

   ```sh
   git clone https://github.com/Automancer-Ltd/CLIProxyAPI.git hub-release-t16638
   cd hub-release-t16638
   git checkout --detach <full-merged-revision>
   git merge-base --is-ancestor 91e0dee8 HEAD
   go version                         # Go 1.26.10 or the checked go.mod toolchain
   go test ./...
   go vet ./...
   revision=$(git rev-parse HEAD)
   built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
   CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath \
     -ldflags "-X main.Version=v8.0.60-estate -X main.Commit=$revision -X main.BuildDate=$built_at" \
     -o cliproxyapi-darwin-arm64 ./cmd/server
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
     -ldflags "-X main.Version=v8.0.60-estate -X main.Commit=$revision -X main.BuildDate=$built_at" \
     -o cliproxyapi-linux-amd64 ./cmd/server
   sha256sum cliproxyapi-darwin-arm64 cliproxyapi-linux-amd64
   ```

   Confirm backup architectures before using the Linux artifact; build the corresponding GOARCH if different. CGO=0 disables Unix native-plugin loading; verify the current deployment uses the same build mode and does not rely on native plugins. If it does, stop and use the existing native-plugin build procedure. On m6, **codesign the staged Darwin candidate**, verify its signature, then generate its `.sha256` (signing changes bytes). Use `codesign --force --sign - <staged>` and `codesign --verify --strict <staged>` as the existing runbook specifies. Never sign/replace the live executable during preparation.
3. Stage candidates and matching checksum files in `~/.local/state/hub-apply/staged/` on m6 and the existing staging location on each backup. Record their full revision and checksums in the release manifest. Stage the tested journal-aware rollback executable too. Retain existing binaries as immutable backups. Read the current served revision, registry, account IDs/provider identity, holds, pins, consume-first strategy, allowance listing and standby roles through existing authenticated tooling; persist only redacted results. Fetch keys through Executor/existing secure tooling, never shell arguments or logs.
4. Copy the exact tested ops scripts, including `hub-apply.py` and `hub-drill.sh`, to their documented paths. Run the current apply helper's dry run on m6 before launching the coordinated transaction (this is staging/spare proof only):

   ```sh
   A='/usr/bin/python3 ~/.local/share/hub-apply/hub-apply.py'
   b='~/.local/state/hub-apply/staged/cliproxyapi-darwin-arm64'
   r=$(ssh m6 "$A binary $b <full-merged-revision> --dry-run" | sed -n 's/.* result //p')
   ssh m6 "$A result $r --wait 300"
   ```

   Require `dry-run-ok`; refusal/busy means no live transaction. The helper checks candidate checksum/signature/version, baseline, local empty-auth proof on port 8395, required models and drill lock. Include the operator's existing required model IDs as positional arguments, derived from the baseline/config; do not invent a new model list. Backups receive their equivalent spare-port proof before becoming eligible for takeover.

## One detached transaction

The **journal-aware coordinated release entry point delivered and fixture-tested by t16644** is the only live entry point. Its exact installed path/CLI and immutable manifest must be copied into the interactive task before launch; that executable does not exist in today's audited ops source. This document deliberately does not pretend that the current `hub-apply.py binary` command alone coordinates all hosts. A missing prerequisite receipt is an explicit stop before cutover, not permission to improvise a live script.

The transaction must perform this sequence without another model call:

1. Acquire the existing apply/drill/ownership locks and record old served revision, settled model registry and backup ownership. Refuse an active drill, concurrent apply, shared authority, unready baseline or unaccounted takeover-capable backup. Register the deployment using the existing estate-watch `deploy.start({product:"estate-hub",host:"m6",commit:<revision>})` route, keeping its receipt.
2. Make old backup executables ineligible to take over before new journal writes become possible. Back up binary/config and credential/journal state privately. Quiesce writers with the existing native ownership protocol; transfer and verify latest credentials **and journal** without stale overwrite or completed-to-pending regression. Keep all backup guards/workers in the transaction's tested ownership state until revision and journal readback agree. Do not restore a pre-cutover credential snapshot after a refresh.
3. Install and prove same-revision standby candidates using the tested release path. Perform the m6 atomic binary swap via the journal-aware apply helper. Existing m6 service is `gui/$(id -u)/uk.automancer.hub`; existing apply backs up `~/.local/hub/cliproxyapi` and kickstarts once. Do not issue an extra kickstart from the caller. The helper's existing **240-second** restart budget includes native handback and launchd codesign recovery.
4. Verify m6 is serving the full target revision, `/hub/ready` says `serving:true`, the baseline required models have settled, keyed empty `/v1/messages` returns 400, wrong key 401, and management usage succeeds. Compare account identity/holds/pins, consume-first routing and allowance data with the redacted baseline. Use an explicitly chosen existing low-cost inference probe only if authorized by the interactive session; fixture readiness alone does not prove provider inference.
5. Verify every eligible backup reports the same revision and fresh journal-inclusive mirror; re-enable ordinary ownership/failover only after those checks pass. Verify handback and journal freshness with the tests/checks shipped by t16644. Do not force a live refresh merely to generate a journal receipt; inspect a naturally occurring one privately when available, or record that production refresh was not yet observed.
6. Confirm `https://hub-svc.automancer.uk/revision` and the local served revision agree with the manifest; run estate-watch verify/get for the recorded deployment. Write a redacted machine-readable terminal result with old/new revisions, checks, backups, rollback outcome and any unverified provider path. The initiating session reads the result and waits inside its run; losing SSH/model connectivity must not interrupt the transaction.

The existing apply result reader is:

```sh
ssh m6 "$A result <returned-result-path> --wait 900"
```

For the coordinated wrapper use t16644's tested result reader and wait budget. `hub-apply.py` outcomes today are `ok`/`dry-run-ok` (0), `rolled_back` (1), `rollback_failed` (2), `refused` (3), and `busy` (75). The coordinated result must include every child result, not report success merely because a process launched.

## Rollback, including failure after a refresh

On failed revision/readiness/model/auth/account checks, the transaction owns rollback. Use the tested **journal-aware fallback** from t16644, retain latest auth files and the full monotonic journal on all owners, restore the previous config only if the release changed it (this release should not), atomically replace the binary and restart once through the existing helper. Verify fallback served revision, readiness, exact baseline registry and usable unaffected accounts; restore backup eligibility only after same-revision/journal agreement. Preserve all newer receipts and pending claims; do not restore old credential snapshots or erase the journal.

If using a pre-journal binary is unavoidable, t16644 must first prove reconciliation and fail-closed quarantine: finish receipt adoption where provable, remove every affected/uncertain grant from **all** scanned auth paths while writers are stopped, preserve it privately outside auth directories, and prove no background refresh can dispatch it. Mere holds/disabled state are insufficient. Restart on unaffected credentials and explicitly report quarantined accounts. If neither safe route is proved, refuse the release **before** the live swap. Do not claim the old pre-journal binary is a safe universal rollback.

A `rollback_failed` result requires the independent interactive session to follow the recorded fallback/ownership recovery procedure immediately and page Gandalf if availability is lost. Release notes/result must say what served, what was rolled back, how it was checked and what remains unverified. No automatic lane should be dispatched to perform this m6 cutover.
