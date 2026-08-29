# Marketing imagery — generated, not drawn

Every file in this directory is produced by one command and is safe to delete
and regenerate:

```bash
npm run -w nightgauge-vscode marketing:screenshots            # everything
npm run -w nightgauge-vscode marketing:screenshots -- cards   # Discord + Slack only
npm run -w nightgauge-vscode marketing:screenshots -- dashboard
```

The generator lives in `packages/nightgauge-vscode/scripts/marketing/`.
Commit the regenerated PNGs alongside the change that altered them, the same
way the Marketplace README screenshots are committed — the marketing site
copies from here and never renders at build time.

## What is real and what is a frame

| File                              | Content                                                                                                        | Chrome                                                |
| --------------------------------- | -------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| `extension-dashboard-*.png`       | The real `getDashboardHtml()` output — the same function the extension renders — fed with the run below        | A mock VS Code Dark Modern window (`vscode-frame.ts`) |
| `notification-discord.{png,json}` | The exact embed `DiscordService.buildEmbed()` posts to the webhook for the run below; the JSON is that payload | A mock of Discord's message layout                    |
| `notification-slack.{png,json}`   | The exact attachment `SlackService` sends via `chat.postMessage`; the JSON is that payload                     | A mock of Slack's message layout                      |

The run every image is built from is `bowlsheet-flutter#338`, which the
pipeline closed on 2026-08-29 with PR #351 — stage durations, per-stage cost,
token totals and cache-hit ratio are copied from the run record the pipeline
wrote. The other runs shown are that day's real merges in the workspace with
representative costs. `run-data.ts` is the single source; change it there and
every image moves together.

Why a frame at all: VS Code, Discord and Slack cannot be rendered headless
from a script. The window and the chat chrome are therefore mocks; the
product output inside them is not. Compare `notification-*.png` against a real
screenshot of the same run whenever the builders change — the reference pair
lives with the release runbooks.
