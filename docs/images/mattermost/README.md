# Mattermost Integration — Screenshot Inventory

This directory holds the screenshots referenced from
[docs/MATTERMOST_INTEGRATION.md](../../MATTERMOST_INTEGRATION.md). The Markdown
references are already in place; the bitmap files are captured separately
against a live Mattermost server + dev-installed extension and committed in a
follow-up PR.

## Capture procedure

1. **Mattermost prerequisites**: a Mattermost v9+ server with system-admin
   access and one notification channel (e.g. `town-square`).
2. **Extension prerequisites**: install the local build via
   `./packages/nightgauge-vscode/scripts/dev-install.sh` and reload the
   VSCode window. The extension reads SecretStorage at startup, so an
   un-configured fresh state is required for the wizard screenshots.
3. **Tool**: native macOS screenshot (`Cmd+Shift+4` → space → click window)
   or any tool that produces PNGs ≤ 2× scale.
4. **Dimensions**: target **1600 × 1000** for VSCode panels and
   **1400 × 900** for Mattermost System Console pages. Crop to the relevant
   pane; do not include the whole screen.
5. **PII**: blur or replace real workspace names, user names, and any token
   suffixes. Use `mattermost.example.com` and `nightgauge-bot` in
   visible URL bars / fields. Tokens shown in the wizard step 4/4 must be
   masked.
6. Save each PNG with the exact filename listed below — the doc links are
   case-sensitive.

## Required captures

| Filename                      | Where                         | Panel state to capture                                                                                                                   |
| ----------------------------- | ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `bot-create.png`              | Mattermost System Console     | **Integrations → Bot Accounts → Add Bot Account** with username `nightgauge-bot` filled in.                                              |
| `incoming-webhook-create.png` | Mattermost System Console     | **Integrations → Incoming Webhooks → Add Incoming Webhook** with title and channel selected, before **Save**.                            |
| `outgoing-webhook-create.png` | Mattermost System Console     | **Integrations → Outgoing Webhooks → Add** with `application/x-www-form-urlencoded`, trigger `/nightgauge` and a callback URL filled in. |
| `configure-step-1.png`        | VSCode Command Palette wizard | **Configure Mattermost Workspace (1/4) — Server URL** input box, placeholder text visible.                                               |
| `configure-step-3.png`        | VSCode Command Palette wizard | **Configure Mattermost Workspace (3/4) — Incoming Webhook URL** input box with example URL pattern in placeholder.                       |
| `configure-step-4.png`        | VSCode Command Palette wizard | **Configure Mattermost Workspace (4/4) — Signing Tokens** loop: title shows "(1 added)" with the channel-id prompt visible.              |
| `settings-multi-notifier.png` | VSCode Nightgauge settings    | **Settings → Notifier Instances** with at least one Discord and one Mattermost entry visible side-by-side.                               |

## After capturing

1. Copy the PNGs into this directory.
2. Run `npm run format` to ensure Markdown stays clean (does not touch
   images).
3. Verify the doc renders by previewing
   `docs/MATTERMOST_INTEGRATION.md` in VSCode (or your Markdown viewer).
4. Open a follow-up PR titled
   `docs(#3382): capture Mattermost integration screenshots`.
