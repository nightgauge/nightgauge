# Mattermost Integration — Operator Guide

This is the operator-facing runbook for standing up the Nightgauge
Mattermost integration end-to-end: server prerequisites, bot account, incoming
and outgoing webhooks, ingress, user mapping, routing rules, troubleshooting,
coexistence with Discord, and security considerations.

For deep receiver internals (port, path, TLS termination, signing-token replay
window, HTTP-status table) this document cross-links to
[docs/MATTERMOST_INBOUND.md](MATTERMOST_INBOUND.md) rather than duplicating
them. Each section below ends with a pointer when the receiver doc owns the
underlying detail.

**Audience**: site reliability / platform operators wiring up a real Mattermost
workspace to a self-hosted Nightgauge deployment.

> 📸 **Screenshots pending** — the screenshot placeholders below reference
> images that have not yet been captured against a live dev-installed
> extension. See
> [docs/images/mattermost/README.md](images/mattermost/README.md) for the
> exact VSCode command and panel state required for each capture. The capture
> work is tracked as a follow-up; the structural references in this document
> are final.

---

## 1. Server Requirements

| Requirement       | Minimum                                                                          |
| ----------------- | -------------------------------------------------------------------------------- |
| Mattermost server | **v9.0** or later (modern post-id-returning incoming webhooks — see §4)          |
| Admin access      | **System Admin** role — required to create bot users, incoming/outgoing webhooks |
| Network           | Inbound HTTPS to reach the Nightgauge receiver (see §6)                          |
| Outbound from MM  | Allow Mattermost egress to your receiver hostname                                |
| TLS               | TLS-terminating reverse proxy or tunnel in front of the receiver (see §6)        |
| Time sync         | NTP-synced clocks within the 5-minute replay window (see §6)                     |

**Why v9+?** The outbound notifier's live-edit feature
([packages/nightgauge-vscode/src/services/notifications/MattermostService.ts](../packages/nightgauge-vscode/src/services/notifications/MattermostService.ts))
relies on the webhook response carrying a post id so the service can edit the
in-flight post via `PUT /api/v4/posts/{id}` as stages progress. Older servers
return an empty body; the notifier auto-detects this and downgrades to
**post-only mode** (single terminal-state post at run end). Edits are
unavailable but the integration still works.

---

## 2. Bot Account Creation

In the Mattermost **System Console → Integrations → Bot Accounts**:

1. Click **Add Bot Account**.
2. Username: `nightgauge-bot` (or your team naming convention).
3. Display name + description as desired.
4. Click **Create Bot Account** — Mattermost shows the **bot access token**
   exactly once. **Copy it now.**

![Bot account creation — System Console](images/mattermost/bot-create.png)

Add the bot to every channel where pipeline messages should appear:

- **Channel header → Members → Add Members** → search for the bot username.

The bot token is later stored in VSCode SecretStorage via the
`Configure Mattermost Workspace` wizard (§4). It is **never** committed to
config files.

**Permissions to verify** on the bot account:

- `create_post` on each notification channel — required for incoming-webhook
  posts and any future direct-post fallback.
- `edit_post` on each notification channel — required for the live-edit path
  (`PUT /api/v4/posts/{id}`). Without this permission the notifier falls back
  to post-only mode automatically.

---

## 3. OAuth Application Registration

**Deferred — not yet implemented.** Nightgauge does not currently use an
OAuth app or SSO flow against Mattermost. Authentication for outbound posts
uses the per-channel **incoming-webhook URL** (no auth header); inbound
slash-command requests authenticate via the per-channel **outgoing-webhook
signing token** (HMAC-style compare against the in-memory token store).

When OAuth/SSO is needed (e.g. for ephemeral responses via
`/api/v4/posts/ephemeral` instead of channel posts), this section will be
expanded in a follow-up epic. The
[`setEphemeral()`](../packages/nightgauge-vscode/src/services/notifications/MattermostService.ts)
notifier surface is wired as a forward-compatible no-op today, exactly so
upgrading to bot-token auth later does not require API changes.

---

## 4. Incoming Webhook Setup

Incoming webhooks are the **outbound** channel — Nightgauge posts pipeline
status updates **to** Mattermost. Set one up per channel that should receive
pipeline notifications.

### 4.1 Create the webhook in Mattermost

In **Integrations → Incoming Webhooks → Add Incoming Webhook**:

1. Title: `Nightgauge Pipeline`
2. Channel: pick the target channel.
3. Lock to channel: optional — recommended on shared servers.
4. **Add** — Mattermost shows the webhook URL of the form
   `https://mattermost.example.com/hooks/<token>`. Copy it.

![Incoming webhook creation](images/mattermost/incoming-webhook-create.png)

### 4.2 Wire the webhook into VSCode

In VSCode, run **Command Palette → Nightgauge: Configure Mattermost
Workspace**. This launches a 4-step wizard
([`configureMattermostWorkspace.ts`](../packages/nightgauge-vscode/src/commands/configureMattermostWorkspace.ts)):

| Step | Prompt                                         | Stored in                                         |
| ---- | ---------------------------------------------- | ------------------------------------------------- |
| 1/4  | Server URL (`https://mattermost.example.com`)  | `notifications.mattermost.enabled: true` in YAML  |
| 2/4  | Bot token (masked input)                       | SecretStorage key `mattermost.botToken`           |
| 3/4  | Incoming webhook URL (`/hooks/<token>` format) | SecretStorage key `mattermost.webhookUrl`         |
| 4/4  | Per-channel signing tokens (looped)            | SecretStorage keys `mattermost.signing.<channel>` |

![Configure Mattermost Workspace — Step 1/4: Server URL](images/mattermost/configure-step-1.png)
![Configure Mattermost Workspace — Step 3/4: Incoming Webhook URL](images/mattermost/configure-step-3.png)
![Configure Mattermost Workspace — Step 4/4: Signing Tokens](images/mattermost/configure-step-4.png)

When the wizard completes, it performs a **live connection test**: a POST to
the webhook URL with a `🔗 test connection` message plus a HEAD probe of the
local inbound receiver on `127.0.0.1:8765`. Webhook failures abort the save;
inbound failures degrade to a warning ("receiver ⚠ not running") and still
save credentials.

The wizard writes the `notifications.mattermost.enabled: true` block via the
typed YAML service and the `notifiers.mattermost.channels.*` block via the raw
YAML Document API (see ADR-001 in
[`configureMattermostWorkspace.ts`](../packages/nightgauge-vscode/src/commands/configureMattermostWorkspace.ts)
for why two paths). All credentials live in the OS keychain via
SecretStorage — **never** in `config.yaml`.

### 4.3 Reload without restarting

Edit `.nightgauge/config.yaml`, then run **Command Palette →
Nightgauge: Reload Notification Tokens** (or call the IPC method
`notifications.reloadTokens`). The receiver atomically swaps its in-memory
token map; new env values require a VSCode restart because the extension host
captures environment at launch (see
[MATTERMOST_INBOUND.md → Reloading Tokens Without Restarting](MATTERMOST_INBOUND.md#reloading-tokens-without-restarting)).

---

## 5. Outgoing Webhook Setup

Outgoing webhooks are the **inbound** channel — Mattermost forwards
slash-command requests **to** the Nightgauge receiver. Set one up per
channel from which operators should issue commands.

### 5.1 Trigger word

Nightgauge registers `/nightgauge` as the canonical trigger word
(matching the fixture seeder at
[`tests/integration/mattermost-fixtures/fixtures.go`](../tests/integration/mattermost-fixtures/fixtures.go)).
Use the same trigger across all channels for consistent operator muscle
memory.

### 5.2 Create the outgoing webhook

The full Mattermost-side flow is in
[MATTERMOST_INBOUND.md → Wiring a Mattermost Outgoing Webhook](MATTERMOST_INBOUND.md#wiring-a-mattermost-outgoing-webhook).
Highlights:

1. **Integrations → Outgoing Webhooks → Add**.
2. Content type: `application/x-www-form-urlencoded` (default).
3. Channel: the channel name becomes the `channel_name` form field on every
   webhook POST and **must** match the YAML map key under
   `notifiers.mattermost.channels.<key>`.
4. Trigger word: `/nightgauge`.
5. Callback URL: the externally-reachable receiver URL — see §6.
6. **Save** → copy the **signing token** Mattermost generates.

![Outgoing webhook creation](images/mattermost/outgoing-webhook-create.png)

### 5.3 Paste the signing token

The wizard in §4.2 collects per-channel signing tokens in step 4/4. They are
stored in SecretStorage under keys of the form
`mattermost.signing.<channel-name>` and surfaced to the Go binary via env
vars named `MATTERMOST_SIGNING_<CHANNEL>` (uppercased, non-alphanumerics
replaced with `_`).

`config.yaml` references those env vars by name, never the raw token:

```yaml
notifiers:
  mattermost:
    channels:
      town-square:
        token_env: MATTERMOST_SIGNING_TOWN_SQUARE
      ops:
        token_env: MATTERMOST_SIGNING_OPS
```

The receiver uses `crypto/subtle.ConstantTimeCompare` on the token plus a
length check to short-circuit before the compare — see
[MATTERMOST_INBOUND.md → Security Notes](MATTERMOST_INBOUND.md#security-notes).

---

## 6. Ingress / Tunnel Setup

The Go binary's receiver binds plaintext to `127.0.0.1:8765` by default —
**never** expose it directly to the public internet. Front it with a
TLS-terminating reverse proxy or hosted tunnel.

Three supported patterns are documented in detail in
[MATTERMOST_INBOUND.md → Exposing the Receiver Securely](MATTERMOST_INBOUND.md#exposing-the-receiver-securely):

| Pattern           | Best for                           | One-liner                                                                   |
| ----------------- | ---------------------------------- | --------------------------------------------------------------------------- |
| Cloudflare Tunnel | Quick start, no DNS work           | `cloudflared tunnel --url http://127.0.0.1:8765`                            |
| Tailscale Funnel  | Tailscale-native deployments       | `tailscale serve --bg http://127.0.0.1:8765` then `tailscale funnel 443 on` |
| nginx / Caddy     | Existing TLS edge, custom hostname | Reverse-proxy `/mattermost` → `127.0.0.1:8765`                              |

**Clock skew**: if the proxy and Mattermost differ by more than the 5-minute
replay window, set the `X-Request-Timestamp` header at the proxy (Unix
milliseconds). The receiver prefers this header over the `trigger_id`
timestamp suffix when both are present.

**Health check**: `curl http://127.0.0.1:8765/mattermost/healthz` returns
`ok` from loopback. From a non-loopback caller (including through your
reverse proxy) it returns `404` by design — defense-in-depth, not a config
knob.

---

## 7. User Mappings

A signed Mattermost webhook proves the request came from Mattermost — it does
**not** prove the sender is authorized to operate the pipeline. The `users:`
config array maps Mattermost user IDs to GitHub/GitLab identities so the
binary can verify per-command access.

### 7.1 Add a user mapping

In `.nightgauge/config.yaml`, add a `users:` section:

```yaml
users:
  - mattermost_user_id: "U04ABC123"
    github_login: "alice"

  - mattermost_user_id: "U04DEF456"
    github_login: "bob"
    gitlab_username: "bob-gl" # optional — GitLab stub is fail-closed for now

  - mattermost_user_id: "U04GHI789"
    gitlab_username: "carol-gl" # GitLab-only; write commands denied until follow-up
```

**Use `mattermost_user_id` (not `user_name`)**. Usernames can be changed by
the user; the user ID is stable. Find it in **System Console → Users**, or
via the API: `GET /api/v4/users/search`.

### 7.2 Command Permission Tiers

| Tier      | Commands                                                      | Requirement                                                       |
| --------- | ------------------------------------------------------------- | ----------------------------------------------------------------- |
| **Read**  | `status`, `health`, `queue list`, `help`                      | Any mapped user                                                   |
| **Write** | `run`, `pause`, `resume`, `stop`, `queue add`, `queue remove` | Mapped user with **write** or **admin** access on the target repo |

The target repo for the permission check is:

1. The `--repo owner/slug` flag from the command (when provided), or
2. `project.default_repo` from `.nightgauge/config.yaml` (fallback).

Permission results are cached for **5 minutes** to avoid excessive GitHub API
calls.

### 7.3 Bot account vs. per-user

Instead of individual user mappings, you can map a single Mattermost bot
account that represents the entire team.

**Pros:** single mapping entry; no per-user GitHub PAT.

**Cons:** all commands appear as the bot identity in the audit log; no
per-user accountability — any channel member can run write commands; the
bot's GitHub account requires write access to every target repo.

**Recommendation:** prefer per-user mapping for security-conscious
environments and teams where audit accountability matters.

---

## 8. Routing Rules

The `notifiers:` block routes pipeline events to specific notifier channels.
See
[docs/CONFIGURATION.md → Notification Routing Rules](CONFIGURATION.md#notification-routing-rules-notifiers)
for the full schema and merge semantics.

A typical Mattermost + Discord coexistence config (§10):

```yaml
notifiers:
  - id: mattermost-failures
    type: mattermost
    channel: "#pipeline-alerts"
    events:
      - pipeline.failure
      - stage.failure
      - stall.warning
      - budget.warning

  - id: mattermost-success
    type: mattermost
    channel: "#pipeline-success"
    events:
      - pipeline.complete
    suppress:
      - pipeline.update
```

**Event keys** are documented in
[docs/CONFIGURATION.md → Event Key Taxonomy](CONFIGURATION.md#event-key-taxonomy).
Common keys: `pipeline.start`, `pipeline.complete`, `pipeline.failure`,
`stage.failure`, `budget.warning`, `stall.warning`.

**Default behavior**: when `notifiers:` is absent, every registered notifier
receives every event.

![Multi-notifier settings panel](images/mattermost/settings-multi-notifier.png)

---

## 9. Troubleshooting

This matrix focuses on **operator-visible symptoms**. For raw HTTP status
codes returned by the receiver (401 / 408 / 415 / 404 / 405 / 413), see the
single-source-of-truth table at
[MATTERMOST_INBOUND.md → Troubleshooting](MATTERMOST_INBOUND.md#troubleshooting).

| Symptom                                                 | Likely cause                                                                                  | Fix                                                                                                                                                                                              |
| ------------------------------------------------------- | --------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Slash command echoed in channel but no Nightgauge reply | Trigger word doesn't match `notifiers.mattermost.channels` key, or signing token mismatch     | Verify channel name matches the YAML map key exactly (case-sensitive); reload tokens; check audit log for the inbound POST                                                                       |
| "Your Mattermost user is not mapped" reply              | Sender's `mattermost_user_id` is not in `users:`                                              | Find the ID in **System Console → Users**, add a `users:` entry, reload tokens                                                                                                                   |
| "You are not authorized to … on …" reply                | User mapped but lacks `write`/`admin` on the target repo                                      | Verify `github_login` matches GitHub; check **Repo → Settings → Collaborators**; wait 5 min for permission cache TTL                                                                             |
| "authorization not configured" reply                    | Binary started with no `users:` config                                                        | Add `users:` section to `config.yaml`; reload tokens or restart                                                                                                                                  |
| GitLab user denied write commands                       | GitLab project-member API not yet implemented                                                 | Add a `github_login` for that user, or wait for the follow-up issue. Read commands still work                                                                                                    |
| Pipeline post never appears in channel                  | Webhook URL invalid, channel-locked webhook pointed at wrong channel, or notifier not enabled | Re-run **Configure Mattermost Workspace**; check `notifications.mattermost.enabled: true`; verify webhook URL with `curl -X POST <url> -d '{"text":"test"}' -H 'Content-Type: application/json'` |
| Pipeline post appears once but never updates            | Server returned no post id → notifier downgraded to post-only mode                            | Check server version (need v9+); check bot has `edit_post` permission on the channel; review extension log for "downgrading to post-only mode" warning                                           |
| Inbound 401 returned to Mattermost                      | Token mismatch or channel name not in `notifiers.mattermost.channels`                         | See [MATTERMOST_INBOUND.md § Troubleshooting 401 row](MATTERMOST_INBOUND.md#troubleshooting)                                                                                                     |
| Inbound 408 returned to Mattermost                      | Clock skew > 5 minutes (replay window)                                                        | Sync NTP on proxy; or set `X-Request-Timestamp` header at the proxy                                                                                                                              |
| Inbound 415 / 404 / 405 / 413                           | Content-type, path, method, or body-size mismatch                                             | See [MATTERMOST_INBOUND.md § Troubleshooting](MATTERMOST_INBOUND.md#troubleshooting) for status-by-status fixes                                                                                  |
| Permission check fails with API error                   | GitHub API rate limit or network outage — binary fails closed                                 | Run `nightgauge github rate-limit`; verify GitHub token has `repo` scope; check `api.github.com` reachability                                                                                    |

### Audit log

Every inbound command authorization decision is appended to
`.nightgauge/notifications/audit.jsonl`:

```json
{
  "timestamp": "2026-05-13T14:30:00Z",
  "mattermost_user_id": "U04ABC123",
  "mapped_identity": "github:alice",
  "channel_id": "C01XYZ789",
  "command": "run",
  "args": "",
  "result": "allowed"
}
```

| Field             | Values                                        |
| ----------------- | --------------------------------------------- |
| `result`          | `allowed` \| `denied` \| `error`              |
| `mapped_identity` | `github:login`, `gitlab:username`, `unmapped` |

The log rotates at **10 MB**, keeping the last **5** rotated copies
(`audit.jsonl.1` … `audit.jsonl.5`). Older copies are discarded on the next
rotation.

---

## 10. Coexistence with Discord

Mattermost and Discord notifiers live side-by-side in the `notifiers:` array
— there is **no migration step** to move from one to the other. Both
notifier types implement the same `Notifier` interface and receive identical
event streams, filtered per-id by `events:` / `suppress:`.

Example: route failures to Mattermost, completions to Discord, both at once:

```yaml
notifiers:
  - id: mattermost-alerts
    type: mattermost
    channel: "#pipeline-alerts"
    events:
      - pipeline.failure
      - stage.failure

  - id: discord-success
    type: discord
    channel: "#pipeline-success"
    events:
      - pipeline.complete
```

**Important**: `notifiers:` is an array — it **replaces** (does not deep-merge)
across config tiers. If team and local configs both define `notifiers:`, the
local array wins entirely. See
[docs/CONFIGURATION.md → Merge Semantics](CONFIGURATION.md#merge-semantics-important).

---

## 11. Security Considerations

Mattermost integration adds three new long-lived secrets to the deployment:
the bot access token, the incoming webhook URL (token-in-URL), and one
signing token per outgoing webhook. Treat each as a credential.

### 11.1 Secret handling

- **All Mattermost credentials live in VSCode SecretStorage** (OS keychain),
  never in `config.yaml`. The receiver reads signing tokens from env vars
  whose names are referenced in YAML (`token_env: MATTERMOST_SIGNING_…`).
- See [standards/security.md → Data Protection](../standards/security.md#data-protection)
  and [Logging & Monitoring](../standards/security.md#logging--monitoring)
  for the general rules; tokens are never logged by either the extension or
  the Go binary (the store reload logs the channel count only).

### 11.2 Signing-token rotation

Recommended cadence: **rotate every 90 days**, and immediately on personnel
changes that affected access to the workspace admin console.

To rotate without downtime:

1. In Mattermost: **Integrations → Outgoing Webhooks → Edit → Regenerate
   Token**.
2. In VSCode: re-run **Configure Mattermost Workspace** and paste the new
   token at step 4/4 for the affected channel.
3. Reload tokens via **Nightgauge: Reload Notification Tokens** — the
   receiver atomically swaps the in-memory token map; no restart required.

### 11.3 Bot token least privilege

The bot account needs only the channel-scoped permissions called out in §2
(`create_post`, `edit_post`). Do **not** grant `manage_team`,
`manage_channels`, or system-admin to the bot. Channel membership should be
limited to channels that legitimately receive pipeline notifications.

### 11.4 Network segmentation

- Bind the receiver to `127.0.0.1` (default). Front it with a TLS
  terminator. Binding to a non-loopback address logs a `WARN` line and is
  unsupported for production.
- The receiver is a single POST endpoint (`/mattermost`) with a 64 KiB body
  cap and a 5-minute replay window — see
  [MATTERMOST_INBOUND.md → Defaults](MATTERMOST_INBOUND.md#defaults).
- The receiver returns a single generic 401 for both "wrong token" and
  "unknown channel" so callers cannot enumerate configured channels.

### 11.5 Audit log retention

`.nightgauge/notifications/audit.jsonl` rotates at 10 MB with 5 rotated
copies (≈ 50 MB total ceiling). For longer retention, ship the file to a
SIEM or central log store via your existing log-shipping agent. The format
is JSON-lines so any standard shipper (Fluent Bit, Vector, etc.) can ingest
it without parsing rules.

### 11.6 Permission cache invalidation

Per-user write permissions are cached for **5 minutes** post-GitHub-lookup.
If you revoke a user's GitHub repo access urgently, also remove their
`users:` entry and reload tokens — the audit log entry will then show
`mapped_identity: unmapped` and the command will be denied immediately.

---

## Configuration Reference

```yaml
# .nightgauge/config.yaml

notifications:
  mattermost:
    enabled: true
    # webhook_env optional — SecretStorage is the preferred source
    webhook_env: MATTERMOST_WEBHOOK_URL
  inbound:
    enabled: true
    host: 127.0.0.1
    port: 8765
    path: /mattermost

notifiers:
  mattermost:
    channels:
      town-square:
        token_env: MATTERMOST_SIGNING_TOWN_SQUARE
      ops:
        token_env: MATTERMOST_SIGNING_OPS

users:
  - mattermost_user_id: "U04ABC123"
    github_login: "alice"
```

For the inbound receiver schema and defaults, see
[docs/MATTERMOST_INBOUND.md](MATTERMOST_INBOUND.md). For the `notifiers:`
routing schema, see
[docs/CONFIGURATION.md → Notification Routing Rules](CONFIGURATION.md#notification-routing-rules-notifiers).
For secret-handling requirements, see
[standards/security.md](../standards/security.md).
