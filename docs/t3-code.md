# T3 Code on auto-vps

T3 Code's server runs on auto-vps. Every other machine is a client. Each provider that can go
through the estate hub does: one Claude instance and one Codex instance, both on the hub, never
several accounts inside T3 (Waseem, 28 Oct 2026, following Theo's advice in
[theo-gist-2026-10.md](theo-gist-2026-10.md)). Every thread runs in **Full access**.

Last verified: 2026-10-28 on auto-vps (T3 0.1.2; moved to the nightly train 22:05, see Updates). Each instance below answered a one-word thread
started with `t3 thread start . "Reply with the single word pong" --provider <id> --model <model> --wait`.

## Where things are

| What | Where |
|---|---|
| Server | user unit `t3code.service`, `http://127.0.0.1:3773`; tailnet `https://automancer-vps.taild6c90e.ts.net:3773` (Tailscale Serve) |
| T3 home | `~/.t3` (settings `~/.t3/userdata/settings.json`; secrets in T3's own secret store, never in that file) |
| Provider config dirs | `~/.config/t3-hub/{claude,codex,pi,opencode}` (mode 700; no keys in them) |
| Settings as code | [estate-hub-devices/t3-hub-instances.mjs](estate-hub-devices/t3-hub-instances.mjs): writes every instance, the defaults and the hub usage source; its header has the run command |
| Hub keys | BWS `AUT_HUB_ACCESS_KEY` (instances, as sensitive env vars), `AUT_HUB_MANAGEMENT_KEY` (T3's usage panel) |

The script is the source of truth. A `providerInstances` write replaces the whole map, so edit the
script and rerun it rather than changing one instance through the API.

The Claude instance starts with `USE_BUILTIN_RIPGREP=0` and a full PATH with
`/opt/automancer/bin/agent-shims` first. The generator takes the remaining PATH
from its launch environment, so run it with the host's normal login PATH and a
real `rg` behind the shims. T3 passes values literally; `$PATH` in an environment
entry will not expand. Claude's native Grep chooses its binary before user
settings and hooks load. Installation and synthetic verification are in
`Automancer-Ltd/automancer-ops`, `runbooks/claude-search-shims.md` (t12269).

Changing a provider instance can close its previous scope. Before rerunning the
generator or changing `claude-hub.environment`, check the installed T3 version's
lifecycle and ensure no Claude sessions can be interrupted. Preserve the live
settings and avoid overwriting newer instance changes with this generator's
whole map. A settings write is not proof that a new Claude launch uses the shim.

## Instances

| Instance id | Name in T3 | Reaches models through | Models |
|---|---|---|---|
| `claude-hub` | Claude (hub) | hub: `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CONFIG_DIR=~/.config/t3-hub/claude` | T3's Claude list (Fable, Opus, Sonnet, Haiku) |
| `codex-hub` | Codex (hub) | hub: `CODEX_HOME=~/.config/t3-hub/codex`, provider `cliproxy`, websockets on | Sol, Astra, Luna, and Terra as a custom model |
| `pi-hub` | Pi (hub) | hub: `PI_CODING_AGENT_DIR=~/.config/t3-hub/pi`, provider `hub` only | only hub models with no native provider in T3 (Waseem, 28 Oct): DeepSeek Flash (the default, DeepSeek pay-as-you-go), GLM 5.3 Flash, the two CommandCode copies, Devin SWE-2 and Lightning, the four `local/*` models. List and default in `~/.config/t3-hub/pi/{models,settings}.json` (not the standalone pi's `~/.pi/agent`) |
| `opencode` | OpenCode | OpenCode's own logins (Zen free models, OpenCode Go). OpenCode 2.2 ignores `OPENCODE_CONFIG`, so it also loads the hub provider from the global `~/.config/opencode/opencode.jsonc` | eight shown (Waseem, 28 Oct): Beam Free, DeepSeek V4.1 Flash (Go) and Free, GLM 5.3 Flash (Go) and Free, Nemotron 3.5 Lightning Free and Super VL Free, Qwen3.8 Flash (Go); the rest hidden in T3 |
| `kimi-hub` | Kimi (hub) | Kimi CLI as a local ACP command (`kimi acp`), `KIMI_CODE_HOME=~/.config/t3-hub/kimi` with the hub as its only provider ([t3-kimi-config.toml](estate-hub-devices/t3-kimi-config.toml)) | K3, K3.1 Pro |
| `grok` | Grok | its own login (`grok login` on auto-vps); Grok also covers the Cursor models, so there is no Cursor instance | `t3` lists them |
| `muse` | Muse Code | its own login (`muse login` on auto-vps) | Wonder, Wonder Full, Spark |
| `antigravity` | Antigravity | T3's managed Antigravity runtime and its own Google sign-in (not the `agy` CLI's login) | listed after sign-in |

The built-in `claudeAgent`, `codex` and `cursor` instances are switched off. The Claude and Codex
config dirs link back to Waseem's instructions, skills, hooks and plugins in `~/.claude` and
`~/.codex`, but hold no login, so his own seat is untouched. Live lists: Settings → Providers, or
`orchestrator_capabilities` from any T3 agent.

Defaults: new threads and all eight projects start on Claude (hub) Opus in Full access; delegated
children inherit Full access. Thread titles and commit text use Codex (hub) Luna. Settings →
Providers → Usage shows the hub's pooled limits (usage source `hub`, kind `cliproxy`).

## OpenCode's keys

OpenCode 2.2 keeps provider keys in its database (`~/.local/share/opencode/opencode.db`, table
`credential`), not in `auth.json`; editing `auth.json` changes nothing. On 28 Oct 2026 the database
held an expired OpenCode Go key while BWS `AUT_OPENCODE_GO_API_KEY` worked, so every Go model failed
with "This API key has expired" (t9518). Replace a key with `opencode auth login opencode-go`
(interactive), or write the BWS value into that row from a script that reads it from the environment.

## Which models each provider shows

T3 keeps one hide list and order per instance on the server (`providerModelPreferences` in
`~/.t3/userdata/settings.json`), so a change applies on every client running the nightly app. Change
it in Settings → Providers → Models. A hide list is a deny list: a model a provider adds later
appears until it is hidden. Pi and Kimi are different: their lists are the config files above.

A model leaves Pi when it gets a native provider in T3 that reaches the hub. Devin's CLI only talks
to Cognition's servers, so Devin's models stay in Pi. The ACP Registry's GLM agent is a third-party
build, and DeepSeek Harness (`dsh`) is not installed, so those stay in Pi too.

## Pins

The hub's `X-CPA-Account` header picks an account or group ([estate-hub.md](estate-hub.md#account-pins)).
T3 has no per-thread environment, so a pin would be a whole extra instance. There is none: Waseem
wants one Claude and one Codex instance (28 Oct 2026), and the hub chooses the account.

## Delegation

Any T3 agent can start children on other instances with the `delegate_task` tool. Checked 28 Oct
2026: a Claude (hub) Sonnet thread delegated one child to Codex (hub) Terra and one to Pi (hub)
`hub/kimi-k3`, waited, and reported both replies. Both children show under the parent as child
threads. The hub log on m6 showed the parent on Claude Gmail Max 20, the Codex child on Codex Pro
Gmail and the Pi child on the Kimi login (`ssh m6 grep "new binding" ~/Library/Logs/hub.log`).

## The `t3 thread` command line

Checked 29 Oct 2026 on nightly 0.1.4 (`t3 thread --help` for the current flags):

- `start` takes `--provider` and `--model` but no effort option, and does not use the project's default model
  selection: a thread started without them came up on Claude (hub) Opus. To set effort, the thread's first turn calls
  the T3 tool `t3_thread_configure` on itself (no `threadId`); its options apply from the next turn.
- `show --json` nests the state under `.thread.status` (`running`, `completed`, ...). An archived thread answers "no
  longer available".
- There is no stop or archive command. Threads started this way still get T3's own tools (`delegate_task`,
  `t3_thread_*`), so a thread can archive itself with `t3_thread_organize`.
- Gandalf's chairs use exactly this: chief-of-staff `scripts/agents/chairs.mjs`, reached over ssh through the cos-helper
  gate, which is his route to T3 until the wrapper below exists.

## Did the automations run?

Scheduled tasks live in `~/.t3/userdata/statev2.sqlite` (open it read-only: `sqlite3 "file:...?mode=ro"`). The
`scheduled_tasks.last_run_status` column says `succeeded` once the turn is queued, even when the agent run then fails.
The real result is `orchestration_v2_projection_runs.status` (`completed`, `failed`, `running`, `queued`) for runs on
the task's thread. On 31 Oct, while m6 was off the tailnet, every run said `succeeded` in `scheduled_tasks` and
`failed` after about 28 minutes of retries in the runs table (t11539).

## Gandalf's route (designed, not wired)

Gandalf runs in a Cloudflare Durable Object, not on auto-vps. He reaches auto-vps services through
the `estate-gateway` tunnel behind a Cloudflare Access service token, like `llm-svc` and
`autodash-svc`.

T3's own MCP endpoint (`/mcp`) cannot serve him without approvals:

- Its sign-in is OAuth with an approval page; the grant lasts 30 days and has no refresh, so Waseem
  would re-approve every month.
- A session issued from the CLI (`t3 auth session issue --subject mcp-client`) needs no approval but
  is capped at **Supervised**: it was refused `runtime_mode_escalation_denied` both when starting a
  Full-access thread and when archiving one.

So the route is a small wrapper on auto-vps:

1. A user unit on `127.0.0.1:<port>` that checks `Authorization: Bearer <AUT_T3_GANDALF_TOKEN>` and
   runs `t3 thread start | send | show | list` (the [host scripts](https://github.com/pingdotgg/t3code/blob/main/docs/user/outside-agents.md#scripts-on-the-host)
   route: no sign-in, project defaults, so Full access). Arguments go as argv to `t3`, never a shell;
   messages go on stdin (`-`).
2. A hostname such as `t3-svc.automancer.uk` on the `estate-gateway` tunnel, added to the
   "estate-gateway (Executor)" Access app, as the hub cut-over's `hub-svc` step does.
3. An Executor app (or a Gandalf tool) holding the Access service token and the bearer.

BWS `AUT_T3_GANDALF_TOKEN` (project personal) holds the wrapper's bearer. It is a random value, not a
T3 session, so it never expires and T3 never asks for approval. The chair wires it after Waseem's trial.

## Sign-ins

Antigravity is signed in with Waseem's Google account (checked 28 Oct 2026 22:35: status
authenticated, a Gemini 3.8 Flash thread answered). If it signs out: Settings → Providers →
Antigravity → **Sign in**, choose Google, finish in the browser.

## Clients

Every client is the T3 desktop app paired to auto-vps (`t3 pair --label <device> --ttl 30m`, then the
pairing URL with `localhost` swapped for `automancer-vps.taild6c90e.ts.net` pasted into Settings →
Connections → Add environment). Live list: `t3 auth session list`. Paired: m6, mbp, wasnewpc,
omarchy (AUR `t3code-nightly-bin`; launched over SSH it needs `--ozone-platform=wayland` and
`WAYLAND_DISPLAY=wayland-1`).

## Updates and versions

auto-vps follows the **nightly** channel since 28 Oct 2026 22:05 (`t3 update --channel nightly --yes`),
for two features stable lacks: model hide/order/favourites stored on the server, so one choice applies
on every client, and linked environments (Settings → Labs). Clients should run the nightly desktop
app too; a stable client keeps its own model list. Before the switch, `~/.t3/userdata` was saved to
`/opt/automancer/var/scratch/t3-backup-20261028-2202/`. To go back: `t3 update --channel stable
--allow-downgrade --yes`.

The earlier 0.1.3 rollbacks (27 and 28 Oct) came from 0.1.3 binding `0.0.0.0` as a managed service
while Tailscale Serve holds the tailnet address on :3773 (`EADDRINUSE`). The drop-in
`~/.config/systemd/user/t3code.service.d/host.conf` sets `T3CODE_HOST=127.0.0.1`; with it the
nightly update started first time.
