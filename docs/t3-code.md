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

## Instances

| Instance id | Name in T3 | Reaches models through | Models |
|---|---|---|---|
| `claude-hub` | Claude (hub) | hub: `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CONFIG_DIR=~/.config/t3-hub/claude` | T3's Claude list (Fable, Opus, Sonnet, Haiku) |
| `codex-hub` | Codex (hub) | hub: `CODEX_HOME=~/.config/t3-hub/codex`, provider `cliproxy`, websockets on | Sol, Astra, Luna, and Terra as a custom model |
| `pi-hub` | Pi (hub) | hub: `PI_CODING_AGENT_DIR=~/.config/t3-hub/pi`, provider `hub` only | only hub models with no native provider in T3 (Waseem, 28 Oct): Kimi K3 and K3.1 Pro, DeepSeek Flash, GLM 5.3 Flash, the two CommandCode copies, Devin SWE-2 and Lightning, the four `local/*` models. The list lives in `~/.config/t3-hub/pi/models.json` (not the standalone pi's `~/.pi/agent`) |
| `opencode` | OpenCode | hub provider from `OPENCODE_CONFIG=~/.config/t3-hub/opencode/opencode.json`, plus OpenCode's own logins | `hub/*`, `opencode/*` free models, `opencode-go/*` |
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

## Still needs Waseem

- **Antigravity sign-in**: open T3 (desktop app, or `https://automancer-vps.taild6c90e.ts.net:3773`
  on the tailnet), Settings → Providers → Antigravity → **Sign in**, choose Google, finish in the
  browser. Until then its threads fail with "Sign in to Antigravity in Settings".

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
