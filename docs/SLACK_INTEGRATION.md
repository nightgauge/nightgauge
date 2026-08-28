# Slack Integration — Operator Guide

This is the operator-facing runbook for sending Nightgauge pipeline status to a
Slack channel: creating the Slack app, granting scopes, storing the token,
configuring the channel, routing which events go where, and troubleshooting a
channel that stays silent.

**Audience**: anyone wiring a Slack workspace to a local Nightgauge install.

**Time**: about five minutes, most of it in Slack's app UI.

**Scope**: outbound only. Nightgauge posts pipeline status _to_ Slack. Inbound
Slack slash commands are **not supported** — if you have read
[docs/MATTERMOST_INTEGRATION.md](MATTERMOST_INTEGRATION.md), note that its
outgoing-webhook and user-mapping sections have no Slack equivalent.

---

## 1. What you get

One message per pipeline run, **edited in place** as stages progress — the same
live-updating message Discord and Mattermost show, built from the same renderer,
so all three say the same thing:

- Issue title, repository and branch
- Every stage with its status, duration and cost
- Model escalations, retries, RALPH iterations, gate results
- Budget and stall warnings
- A final outcome with total cost and elapsed time

Plus three alerts posted by the Go binary rather than the extension:

| Alert         | Fires when                                                 |
| ------------- | ---------------------------------------------------------- |
| Ready to ship | An epic closes with every sub-issue merged                 |
| Stalled epic  | An epic is open but has no eligible work and no active run |
| Release alert | Release-watch finds high-impact upstream changes           |

All four use **one bot token and one channel**.

---

## 2. Create the Slack app

Nightgauge uses a **bot token**, not an incoming webhook. Only
`chat.postMessage` returns a message timestamp, and only that timestamp lets
`chat.update` edit the message in place; a webhook can only append, which would
put one message per stage into your channel.

Slack has also [deprecated standalone custom-integration webhooks](https://api.slack.com/legacy/custom-integrations/messaging/webhooks) —
an incoming webhook is itself an app feature now, so you create an app either
way. The bot token is strictly more capable at the same setup cost.

1. Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** →
   **From scratch**.
2. Name it (e.g. `Nightgauge`) and pick your workspace.
3. **OAuth & Permissions** → **Scopes** → **Bot Token Scopes** → **Add an OAuth
   Scope**:
   - **`chat:write`** — required.
   - **`chat:write.public`** — optional; lets the bot post to a public channel
     without being invited. Without it, invite the bot (step 5).
4. Scroll to the top → **Install to Workspace** → **Allow**.
5. Copy the **Bot User OAuth Token**. It starts with `xoxb-`.

> **Scopes must be added before installing.** If you installed first, add the
> scope and click **Reinstall to Workspace** — the token stays valid and gains
> the scope.

---

## 3. Get the channel ID

Open the channel in Slack → **View channel details** → the ID (`C…`) is at the
bottom of the dialog.

**Use the ID, not `#name`.** A channel rename silently breaks a name lookup; the
ID is stable for the channel's lifetime. A `#name` is accepted, but the ID is
what this guide recommends.

If you did **not** grant `chat:write.public`, invite the bot now:

```
/invite @Nightgauge
```

---

## 4. Store the token

Command palette → **`Nightgauge: Configure Slack Notifications`**.

The command prompts for the token (masked, and never echoed back), validates it,
then prompts for the channel. The token is stored in the OS keychain via VSCode
SecretStorage — **never in a file**.

Validation names the mistake rather than saying "invalid", because pasting the
wrong Slack credential is the most common setup error:

| Pasted                      | Message                                                   |
| --------------------------- | --------------------------------------------------------- |
| `xoxp-…`                    | That is a user token — use the Bot User OAuth Token       |
| `xapp-…`                    | That is an app-level token — use the Bot User OAuth Token |
| `https://hooks.slack.com/…` | That is a webhook URL — use the Bot User OAuth Token      |

**CI / headless**: there is no keychain, so export the token instead. The
variable name comes from `bot_token_env` below and defaults to
`SLACK_BOT_TOKEN`.

---

## 5. Configure the channel

The command hands you this block to paste — it deliberately does not rewrite
your YAML. Add it to `.nightgauge/config.yaml` (project) or
`~/.nightgauge/config.yaml` (global):

```yaml
notifications:
  slack:
    enabled: true
    channel: "C0123456789"
    # bot_token_env: SLACK_BOT_TOKEN   # optional; this is the default
```

**This one block drives both halves** — the extension's pipeline status and the
Go binary's three alerts. There is no second credential and no second channel
setting.

| Field           | Required | Meaning                                              |
| --------------- | -------- | ---------------------------------------------------- |
| `enabled`       | yes      | Absent or `false` disables Slack entirely            |
| `channel`       | yes      | Channel ID (preferred) or `#name`                    |
| `bot_token_env` | no       | Env var holding the token. Default `SLACK_BOT_TOKEN` |

`enabled` defaults to **off**, so upgrading Nightgauge never starts posting to a
workspace that did not ask for it.

Reload the VSCode window to pick up the change. **Stop the autonomous scheduler
and let in-flight runs drain first** — a reload over a live run kills it and it
recycles as an unlabeled failure.

---

## 6. Verify

Command palette → **`Nightgauge: Notifier Settings`** → the **Test** button on
the Slack row posts a test message and reports the result inline.

To check from a shell instead:

```bash
curl -s -X POST https://slack.com/api/chat.postMessage \
  -H "Authorization: Bearer $SLACK_BOT_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"channel":"C0123456789","text":"Nightgauge test"}' | jq '.ok, .error'
```

`true` means token, scope and channel are all correct.

---

## 7. Routing which events go where

By default the Slack notifier receives every event. To split traffic across
channels, add a `notifiers:` block — see
[docs/CONFIGURATION.md → Notification Routing Rules](CONFIGURATION.md#notification-routing-rules-notifiers)
for the full schema and
[Event Key Taxonomy](CONFIGURATION.md#event-key-taxonomy) for every key.

```yaml
notifiers:
  - id: slack
    type: slack
    channel: "#pipeline"
    events:
      - pipeline.start
      - pipeline.complete
      - pipeline.failure
      - stall.warning
```

The `id` must be `slack` — that is the id the notifier is registered under.

**A note on `pipeline.update`**: Slack messages are edited in place, so allowing
update events refreshes the existing message rather than posting new ones. There
is no flood risk in leaving it on; suppress it only if you want the message to
stop refreshing mid-run.

---

## 8. Troubleshooting

Failures are logged to the extension's output channel and are deliberately
specific — each names the fix rather than the symptom. A notifier failure never
fails a pipeline run.

| Symptom                                          | Cause                                                                                                                  | Fix                                                                                     |
| ------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| Nothing posts, no errors                         | `notifications.slack.enabled` missing or `false`                                                                       | Add the block from §5 and reload                                                        |
| Nothing posts, no errors                         | Token stored but channel not configured                                                                                | Add `channel:` to the config block                                                      |
| `missing_scope`                                  | Token lacks `chat:write`                                                                                               | Add the scope, **Reinstall to Workspace**                                               |
| `not_in_channel`                                 | Bot is not a member                                                                                                    | `/invite @Nightgauge`, or grant `chat:write.public`                                     |
| `channel_not_found`                              | Wrong ID, or bot cannot see the channel                                                                                | Re-copy the ID from **View channel details**                                            |
| `invalid_auth` / `token_revoked`                 | Token is wrong or revoked                                                                                              | Re-run the configure command                                                            |
| `is_archived`                                    | Target channel is archived                                                                                             | Pick a live channel                                                                     |
| "configured credential is not a Slack bot token" | A user token, app token or webhook URL was stored                                                                      | Re-run the configure command with the `xoxb-` token                                     |
| Message posts but never updates                  | `chat.postMessage` returned no timestamp                                                                               | The run degrades to a single terminal message — check the log for the downgrade warning |
| Pipeline status works, Go alerts silent          | Alerts come from the Go binary, which reads the same block — confirm `enabled: true` is in the config the binary loads | Check the project vs global tier                                                        |

Permanent errors (bad token, missing scope, unknown channel) are **not**
retried: retrying burns rate limit and delays the honest log line. Transient
failures (429, 5xx, transport) retry with bounded backoff.

---

## 9. Security

- The bot token is the credential. It is stored in the OS keychain, never in
  config, and never written to a log line, an error message or telemetry.
- `chat:write` is the minimum scope. `chat:write.public` is a convenience that
  trades an invite step for broader posting rights — grant it only if you want
  that.
- Pipeline status includes issue titles, branch names and costs. Post to a
  channel whose membership you are comfortable with; Nightgauge applies no
  additional redaction to titles beyond stripping detected secrets.
- Revoking access is done in Slack (**OAuth & Permissions → Revoke**), which
  invalidates the token immediately regardless of what is in the keychain.

---

## 10. Coexistence with Discord and Mattermost

All three can run at once. Each is registered independently and receives every
event unless a `notifiers:` block says otherwise, so adding Slack changes
nothing about existing Discord or Mattermost delivery.

The rendered content is identical across providers — one shared renderer builds
the message, and each provider translates it into its own wire format. Slack's
dialect differs (`*bold*` rather than `**bold**`, `<url|label>` rather than
`[label](url)`), and that translation happens automatically.

---

## Configuration Reference

```yaml
# .nightgauge/config.yaml

notifications:
  slack:
    enabled: true
    channel: "C0123456789"
    bot_token_env: SLACK_BOT_TOKEN

# Optional: route specific events to this notifier.
notifiers:
  - id: slack
    type: slack
    channel: "#pipeline"
    events: [pipeline.start, pipeline.complete, pipeline.failure]
```

Environment:

```bash
# Only needed where there is no OS keychain (CI, headless).
export SLACK_BOT_TOKEN=xoxb-...
```

## See also

- [docs/CONFIGURATION.md](CONFIGURATION.md) — full config schema
- [docs/MATTERMOST_INTEGRATION.md](MATTERMOST_INTEGRATION.md) — the Mattermost
  equivalent, including the inbound half Slack does not have
- [docs/ARCHITECTURE.md](ARCHITECTURE.md) — where notifiers sit in the pipeline
