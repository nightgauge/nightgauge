# Mattermost Inbound Webhook Receiver

The Go binary hosts a process-resident HTTP receiver that accepts
Mattermost outgoing-webhook callbacks, verifies them against per-channel
signing tokens, and emits each verified slash command as a
`mattermost.command` IPC event for downstream handlers to consume
(slash-command parsing, action routing, and authorization live in
companion issues — see [References](#references)).

This document is the operator runbook: configuration schema, defaults,
TLS exposure pattern, and troubleshooting.

## Defaults

| Setting       | Default       | Notes                                                           |
| ------------- | ------------- | --------------------------------------------------------------- |
| Enabled       | `false`       | Receiver only starts when `notifications.inbound.enabled: true` |
| Host          | `127.0.0.1`   | Loopback. TLS is expected to terminate at a reverse proxy.      |
| Port          | `8765`        | Plaintext HTTP — never expose directly to the public internet.  |
| Path          | `/mattermost` | POST endpoint; healthz is appended as `<path>/healthz`.         |
| Replay window | 5 minutes     | Requests older than this are rejected `408`.                    |
| Body cap      | 64 KiB        | Mattermost slash-command fields are tiny; this is a hard cap.   |

## Configuration Schema

`.nightgauge/config.yaml`:

```yaml
notifications:
  inbound:
    enabled: true
    host: 127.0.0.1 # default — change only if a reverse proxy fronts the binary
    port: 8765
    path: /mattermost

notifiers:
  mattermost:
    channels:
      dev:
        token_env: MATTERMOST_TOKEN_DEV
      ops:
        token_env: MATTERMOST_TOKEN_OPS
```

- `token_env` is the env-var name from which the signing token is read.
  Plaintext tokens are not accepted — the env-var indirection follows
  the existing `forges:` and `github_auth:` conventions used elsewhere
  in this file.
- The map key is the Mattermost `channel_name` field that arrives on
  every webhook POST. Keys are case-sensitive and must match exactly.

Set the referenced env vars before starting the binary (or VSCode):

```bash
export MATTERMOST_TOKEN_DEV='paste-the-token-from-mattermost-here'
export MATTERMOST_TOKEN_OPS='paste-the-other-token'
```

## Wiring a Mattermost Outgoing Webhook

In Mattermost (`Integrations → Outgoing Webhooks → Add`):

1. **Content Type**: `application/x-www-form-urlencoded` (the default).
2. **Channel**: pick the channel — its name becomes the `channel_name`
   form field on the request and must match the YAML map key.
3. **Trigger words**: e.g. `/inc`, `/nightgauge`.
4. **Callback URL**: the externally-reachable URL of the receiver
   including the `path`. With the defaults above and the receiver
   exposed at `https://webhooks.example.com/mattermost`, set this to
   `https://webhooks.example.com/mattermost`.
5. **Token**: copy the value Mattermost generates — paste it into the
   env var named by `token_env` for that channel.

## Exposing the Receiver Securely

The binary binds plaintext to loopback by default. Operators expose it
to the public internet by fronting it with a TLS-terminating reverse
proxy. Three patterns are supported:

### Cloudflare Tunnel

```bash
cloudflared tunnel --url http://127.0.0.1:8765
```

Cloudflare assigns a `https://*.trycloudflare.com` URL automatically.
For production, register a named tunnel with your own hostname.

### Tailscale Funnel

```bash
tailscale serve --bg http://127.0.0.1:8765
tailscale funnel 443 on
```

The funnel exposes the receiver on `https://<machine>.<tailnet>.ts.net`.

### nginx (or caddy)

Minimal nginx server block:

```nginx
server {
    listen 443 ssl;
    server_name webhooks.example.com;

    ssl_certificate     /etc/letsencrypt/live/webhooks.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/webhooks.example.com/privkey.pem;

    location /mattermost {
        proxy_pass http://127.0.0.1:8765;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
    }
}
```

If clock skew between Mattermost and your proxy exceeds the 5-minute
replay window, set `X-Request-Timestamp` (unix milliseconds) at the
proxy — the receiver prefers this header over the `trigger_id` suffix
when both are present.

## Reloading Tokens Without Restarting

The receiver exposes the IPC method `notifications.reloadTokens`. The
TS extension calls this whenever the user edits the `notifiers:` block
in VSCode, atomically swapping the in-memory token map. Operators can
also trigger a reload manually by issuing the IPC call directly via
the extension's developer commands.

The reload re-reads `.nightgauge/config.yaml` and re-resolves
every `token_env` against the current process environment. To pick up
new env values, restart VSCode (the env is captured at extension-host
launch).

## Troubleshooting

| Status | Likely cause                                                           | Fix                                                                                                      |
| ------ | ---------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `401`  | Token mismatch, OR channel name not in `notifiers.mattermost.channels` | Confirm the env var is set; confirm the YAML map key matches `channel_name` exactly.                     |
| `408`  | `trigger_id` timestamp is older than 5 minutes (clock skew or replay)  | Sync the proxy clock; or set `X-Request-Timestamp` at the proxy to a fresh unix-ms value.                |
| `415`  | `Content-Type` not `application/x-www-form-urlencoded`                 | Mattermost defaults to form-encoded; check the outgoing webhook setting in Mattermost.                   |
| `404`  | Wrong path on the request URL                                          | Path must match `notifications.inbound.path` exactly (default `/mattermost`).                            |
| `405`  | Method other than POST on the webhook path                             | Mattermost should always POST; check for misconfigured curl tests.                                       |
| `413`  | Body exceeds 64 KiB                                                    | Slash-command payloads should never be this large. Check for a misconfigured trigger flooding the field. |

To verify the receiver is up:

```bash
curl -sS http://127.0.0.1:8765/mattermost/healthz
# → ok
```

The health endpoint returns `404` when invoked from a non-loopback
caller, even with the receiver bound to a public interface — this is a
defense-in-depth check, not a config knob.

## Security Notes

- Tokens are compared with `crypto/subtle.ConstantTimeCompare` plus an
  explicit length check. Different-length tokens short-circuit before
  the compare to avoid leaking length via timing.
- The receiver returns a single generic 401 for both "wrong token" and
  "unknown channel" so callers cannot enumerate configured channels.
- Plaintext binding is loopback-only by default. Binding to a
  non-loopback address logs a `WARN` line — TLS termination at a
  reverse proxy is mandatory for any non-loopback deployment.
- Tokens are never logged. The store reload logs the channel count
  only; individual tokens stay in process memory.

## References

- Mattermost outgoing-webhook spec: <https://developers.mattermost.com/integrate/webhooks/outgoing/>
- Issue #3375 — this receiver
- Issue #3376 — slash-command parsing / dispatcher (consumes
  `mattermost.command` events)
- Issue #3377 — per-user authorization (Mattermost user → GitHub
  identity mapping)
- Issue #3378 — VSCode UX for setting channel tokens
- Epic #3371 — outbound + inbound Mattermost integration
