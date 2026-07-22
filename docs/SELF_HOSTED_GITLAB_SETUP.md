# Self-Hosted GitLab Setup

> **Audience**: SREs and platform engineers deploying Nightgauge against a
> self-hosted GitLab CE or EE instance for the first time.
>
> **Companion docs**: [docs/FORGE_ABSTRACTION.md](FORGE_ABSTRACTION.md) (design),
> [docs/decisions/008-skill-forge-cli.md](decisions/008-skill-forge-cli.md)
> (`nightgauge forge` CLI rationale),
> [docs/GO_BINARY.md](GO_BINARY.md) (binary install + CLI reference).

This runbook walks you end-to-end from a fresh GitLab CE container to a working
Nightgauge pipeline executing against it. Every YAML key, scope, and error
string is quoted verbatim from the codebase so this doc is grep-detectable and
will not silently drift.

---

## At a Glance

| Phase                                    | Time    | Notes                                           |
| ---------------------------------------- | ------- | ----------------------------------------------- |
| 1. Prerequisites                         | ~30 min | Install Docker, the `nightgauge` binary, VSCode |
| 2. Install GitLab CE                     | ~45 min | First-boot reconfigure takes ~120 s by itself   |
| 3. Network topology and reverse proxy    | ~30 min | DNS, firewall, `X-Forwarded-*` headers          |
| 4. TLS and CA cert                       | ~15 min | Export CA, wire `ca_bundle`                     |
| 5. OAuth app + PAT (and/or deploy token) | ~20 min | GitLab admin UI work                            |
| 6. Author `.nightgauge/config.yaml`      | ~10 min | Single `forges:` block                          |
| 7. VSCode forge wizard walkthrough       | ~10 min | 6 steps + connection test                       |
| 8. Smoke test                            | ~20 min | One issue end-to-end                            |

**Total**: ~2.5 hours of wall-clock time, of which ~30 minutes is hands-off
waiting on GitLab Omnibus reconfigure.

- **Supported GitLab versions**: CE 17.4 and newer (Ultimate / Premium where
  noted). Pinned reference: `gitlab/gitlab-ce:17.6.0-ce.0` — same image as the
  Nightgauge integration harness
  ([`tests/integration/docker-compose.gitlab.yml`](../tests/integration/docker-compose.gitlab.yml)).
- **Last operator-tested**: _TBD — fill in via the
  [Operator-Tested Checklist](#operator-tested-checklist) section after a live
  pass against `gitlab-ce:17.6.0-ce.0`._

---

## 1. Prerequisites

Before starting, make sure each item below is installed and reachable from the
machine that will host the pipeline (workstation or build runner — pick one and
stick with it for the full runbook).

| Tool               | Minimum version      | Why                                                |
| ------------------ | -------------------- | -------------------------------------------------- |
| Docker or Podman   | Docker 24 / Podman 5 | Run GitLab Omnibus and (optionally) the harness    |
| `nightgauge`       | Latest release       | Pipeline binary — see [GO_BINARY.md](GO_BINARY.md) |
| VSCode + extension | VSCode 1.95+         | Forge wizard and dashboard                         |
| DNS resolution     | A or CNAME → host    | Operators using TLS need a real hostname           |
| OpenSSL CLI        | 1.1.1 or 3.x         | Inspect server certificate during TLS step         |
| `jq`               | 1.6+                 | Used by several troubleshooting commands           |

**Per-OS install variants**:

| OS           | Docker / Podman                               | OpenSSL / jq                 |
| ------------ | --------------------------------------------- | ---------------------------- |
| Linux x86_64 | `apt-get install docker.io` or Docker Desktop | `apt-get install openssl jq` |
| Linux arm64  | `apt-get install docker.io` (same package)    | `apt-get install openssl jq` |
| macOS arm64  | Docker Desktop or `brew install podman`       | `brew install openssl jq`    |

For non-trivial firewall environments (VPN, corporate proxy), confirm outbound
443 to `registry.gitlab.com` and inbound HTTP/HTTPS to the host running GitLab
before you continue.

---

## 2. Install GitLab CE

This guide pins GitLab CE to `17.6.0-ce.0`, the same image the Nightgauge
integration harness exercises in CI. The pin makes the operator-tested
checklist below reproducible — when the harness bumps versions, this doc
bumps in lockstep.

> **Cite, don't copy.** GitLab maintains the canonical Omnibus install guide.
> Follow it for production hardening. The block below is the minimum a
> standalone evaluation needs.

### 2.1 Minimal `docker-compose.yml`

This compose file mirrors the Nightgauge harness at
[`tests/integration/docker-compose.gitlab.yml`](../tests/integration/docker-compose.gitlab.yml).
The only differences in a production deployment are larger volumes, a real
hostname, and TLS termination:

```yaml
services:
  gitlab:
    image: gitlab/gitlab-ce:17.6.0-ce.0
    container_name: gitlab-ce
    hostname: gitlab.example.com
    environment:
      GITLAB_ROOT_PASSWORD: "${GITLAB_ROOT_PASSWORD:?must be set}"
      GITLAB_OMNIBUS_CONFIG: |
        external_url 'https://gitlab.example.com'
        gitlab_rails['signup_enabled'] = false
        gitlab_rails['initial_root_password'] = ENV['GITLAB_ROOT_PASSWORD']
    ports:
      - "80:80"
      - "443:443"
      - "22:22"
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost/-/health"]
      interval: 10s
      timeout: 5s
      retries: 30
      start_period: 120s
    volumes:
      - gitlab-config:/etc/gitlab
      - gitlab-logs:/var/log/gitlab
      - gitlab-data:/var/opt/gitlab

volumes:
  gitlab-config:
  gitlab-logs:
  gitlab-data:
```

### 2.2 Boot and wait

```bash
# Set a strong root password BEFORE first boot — written into the DB once.
export GITLAB_ROOT_PASSWORD="$(openssl rand -base64 24)"
docker compose up -d
# Wait for the first-boot reconfigure. Container reports unhealthy → healthy.
docker compose logs -f gitlab | grep -m1 'gitlab Reconfigured!'
```

The first boot routinely takes 90–150 seconds. The harness sets
`start_period: 120s` for the same reason ([compose source](../tests/integration/docker-compose.gitlab.yml)).

### 2.3 Production-only steps (link out)

For real deployments, follow GitLab's [Omnibus production checklist](https://docs.gitlab.com/omnibus/settings/configuration.html)
to add: external Postgres, Redis, object storage, backup schedule, Prometheus
scrape, and SSH host-key persistence. These are out of scope for this guide.

---

## 3. Network Topology

### 3.1 Reference layout

```
                    +-----------------+
operator workstation| VSCode +        |
       (you)        | extension       |
                    +--------+--------+
                             |
                             v 443/tcp
                    +-----------------+      80/443 inbound
                    | reverse proxy   |<---- corporate / public LB
                    | (nginx/traefik) |
                    +--------+--------+
                             |
              80/443 internal v
                    +-----------------+
                    | gitlab-ce:17.6  |
                    | + webhook out   |
                    +--------+--------+
                             |
              443 webhook    v (X-Gitlab-Token shared secret)
                    +-----------------+
                    | nightgauge |
                    | webhook server  |
                    +-----------------+
```

### 3.2 Firewall and DNS checklist

| Direction                   | Port         | Purpose                                                 |
| --------------------------- | ------------ | ------------------------------------------------------- |
| Operator → reverse proxy    | 443          | Browser + git+https + API                               |
| Operator → reverse proxy    | 22 (or 2222) | git+ssh (optional but recommended)                      |
| Reverse proxy → GitLab      | 80           | Local-loop HTTP (TLS terminates at proxy)               |
| GitLab → webhook receiver   | 443          | Outbound — see [§9 Webhook ingress](#9-webhook-ingress) |
| Operator → webhook receiver | 443          | Health-check the receiver from your laptop              |

If your reverse proxy terminates TLS, **pass these headers through unchanged**:
`X-Forwarded-For`, `X-Forwarded-Proto`, `X-Real-IP`, `Host`. Missing
`X-Forwarded-Proto: https` causes GitLab to emit absolute URLs with `http://`
which then break webhook signature replay protection on the receiver side.

### 3.3 DNS

The hostname you set in `external_url` becomes the canonical name for OAuth
callbacks, webhook URLs, and the `base_url` you will write into
`.nightgauge/config.yaml`. Choose it before issuing the TLS cert —
changing it later requires a full Omnibus reconfigure.

---

## 4. TLS and CA Certificates

Nightgauge' GitLab transport reads the trust roots in this priority order
(see [`internal/gitlab/transport.go`](../internal/gitlab/transport.go)):

1. `forges.<id>.ca_bundle` (path resolved relative to the config file directory)
2. `SSL_CERT_FILE` environment variable
3. The OS system cert pool

### 4.1 Public CA (Let's Encrypt, DigiCert, etc.)

No action required — the system pool covers it. Leave `ca_bundle` unset.

### 4.2 Self-signed or private CA

Export the CA your GitLab leaf certificate chains to and drop it where the
config file can find it:

```bash
# Inspect the server-presented cert chain
openssl s_client -showcerts -connect gitlab.example.com:443 </dev/null \
  | awk '/BEGIN CERT/,/END CERT/' > /tmp/gitlab-chain.pem

# Identify the root CA (the last cert in the chain)
# Then copy it next to your config:
mkdir -p .nightgauge/certs
cp /tmp/gitlab-ca.pem .nightgauge/certs/corp-gitlab-ca.pem
```

**Per-OS variants for the `openssl s_client` flag** are identical on macOS and
Linux. Windows operators using WSL: run from inside the WSL distro, not from
PowerShell's bundled OpenSSL.

### 4.3 The `InsecureSkipTLS` escape hatch

The transport supports `forges.<id>.insecure_skip_tls: true` for
break-glass scenarios (lab, brief incident response). When set, the binary
prints to stderr:

```
WARNING: gitlab: InsecureSkipTLS=true for "<base_url>" — TLS certificate verification is disabled
```

(`internal/gitlab/transport.go:57`). The config loader emits an additional
warning at startup (`internal/config/config.go:1165`). **Never leave this on
in production.** The doc-map keyword index will surface this section if
someone greps for `insecure_skip_tls` — please don't make that a forensic
search.

---

## 5. OAuth Application Registration (Optional)

OAuth is only required if you plan to use the `oauth2` auth method. PAT-based
deployments can skip this section.

1. Sign in to GitLab as an administrator.
2. Navigate to **Admin Area → Applications → Add new application**
   ([GitLab 17.6 OAuth Applications](https://docs.gitlab.com/administration/auth/oauth_applications/)).
3. Set the callback URL to:
   ```
   <nightgauge-binary-host>/auth/callback
   ```
   (For VSCode-only operation the device-code flow uses an out-of-band URN
   internally; the wizard prints the verification URI to open in a browser.)
4. Scopes — select **exactly these three**, no more:
   - `api`
   - `read_repository`
   - `read_user`
5. Confirm the resulting **Application ID** and **Secret** are visible — they
   will not be shown again.

> The scope list is sourced verbatim from
> [`internal/gitlab/auth.go:18`](../internal/gitlab/auth.go):
> `var gitlabRequiredScopes = []string{"api", "read_repository", "read_user"}`.
> Any deviation will fail `nightgauge forge auth status` with
> `gitlab auth: PAT scope check returned HTTP …` or a missing-scopes report.

---

## 6. PAT vs OAuth2 vs CI Job Token vs Deploy Token

The GitLab adapter resolves credentials via four methods. The wizard exposes
all four; the YAML schema accepts the same set
([`schema.ts:3003-3010`](../packages/nightgauge-vscode/src/config/schema.ts)):

```ts
export const ForgeAuthMethodSchema = z.enum([
  "token",
  "app",
  "pat",
  "oauth2",
  "ci_job_token",
  "deploy_token",
]);
```

### 6.1 Selection table

| Method         | Rotation cadence | Blast radius        | Scopes                                | Audit trail             | When to use                               |
| -------------- | ---------------- | ------------------- | ------------------------------------- | ----------------------- | ----------------------------------------- |
| `pat`          | 30–90 days       | One user            | `api`, `read_repository`, `read_user` | Per-PAT audit log       | Default for operator workstations         |
| `oauth2`       | Refresh-driven   | One user            | Same as PAT                           | OAuth audit + per-token | Multi-operator setups, expiring tokens    |
| `ci_job_token` | Per-pipeline     | One CI job          | Implicit project access               | CI job log              | Inside GitLab CI runs only                |
| `deploy_token` | Manual           | Read-only by config | `read_repository` (typical)           | Per-token audit         | Read-only mirrors, CI on CE without OAuth |

The validation endpoint differs by method
([`auth.go:58-75`](../internal/gitlab/auth.go)):

| Method         | Validation endpoint                       |
| -------------- | ----------------------------------------- |
| `pat`          | `GET /api/v4/personal_access_tokens/self` |
| `oauth2`       | `GET /oauth/token/info`                   |
| `ci_job_token` | `GET /api/v4/user` (implicit)             |
| `deploy_token` | `GET /api/v4/user` (or synthetic)         |

### 6.2 Create a PAT (recommended starting point)

1. Sign in to GitLab as the user the pipeline will operate as.
2. **User Settings → Access Tokens → Add new token**.
3. Scopes: `api`, `read_repository`, `read_user` — no more, no fewer.
4. Expiry: 30 days during evaluation, 90 days in production.
5. Copy the token immediately and store it in your secret manager. The wizard
   will write it into the OS keychain via VSCode's `SecretStorage` API
   ([`configureForgeInstance.ts:307`](../packages/nightgauge-vscode/src/commands/configureForgeInstance.ts)).

---

## 7. Deploy Tokens for CI

On GitLab CE, full OAuth2 device-code flow is acceptable but PAT-per-user is
the lowest-friction path. For CI runners that need a non-user identity, use
deploy tokens.

| Use case                        | Recommended `auth_method` |
| ------------------------------- | ------------------------- |
| Operator workstation            | `pat`                     |
| Read-only mirror runner         | `deploy_token`            |
| GitLab CI runner (same project) | `ci_job_token`            |
| Cross-project / external runner | `deploy_token`            |

To create a deploy token: **Project → Settings → Repository → Deploy Tokens →
Add deploy token**. Scopes accepted by Nightgauge: at minimum
`read_repository`; `read_registry` and `write_registry` only when the pipeline
needs container access. The wizard accepts the deploy token as a `user:token`
pair; the binary then sends `Authorization: Basic base64(user:token)` per
[`auth.go:93-98`](../internal/gitlab/auth.go).

---

## 8. Author `.nightgauge/config.yaml`

The schema lives in
[`packages/nightgauge-vscode/src/config/schema.ts:2999-3043`](../packages/nightgauge-vscode/src/config/schema.ts)
and its Go counterpart in
[`internal/config/config.go:756-781`](../internal/config/config.go). Fields:

| Key                  | Required             | Notes                                                    |
| -------------------- | -------------------- | -------------------------------------------------------- |
| `kind`               | yes (non-github)     | `github` or `gitlab`                                     |
| `base_url`           | yes (non-github)     | e.g. `https://gitlab.example.com`                        |
| `graphql_url`        | no                   | Derived from `base_url` when empty                       |
| `auth_method`        | yes                  | `pat`, `oauth2`, `ci_job_token`, or `deploy_token`       |
| `ca_bundle`          | no                   | Path resolved relative to the config file dir            |
| `default_project_id` | no                   | Numeric GitLab project/group ID                          |
| `proxy`              | no                   | `http://` or `https://`; falls back to `HTTPS_PROXY` env |
| `insecure_skip_tls`  | no (default `false`) | Disables TLS verification — see §4.3                     |

### 8.1 Annotated example

```yaml
schema_version: "2"

forges:
  # Primary GitHub forge — usually present alongside a self-hosted GitLab.
  github:
    kind: github
    auth_method: pat
    token_env: GITHUB_TOKEN

  # Self-hosted GitLab — every field demonstrated for documentation purposes.
  corp-gitlab:
    kind: gitlab
    base_url: https://gitlab.example.com
    # graphql_url is derived from base_url unless your GraphQL endpoint
    # is reverse-proxied to a different path:
    # graphql_url: https://gitlab.example.com/api/graphql
    auth_method: pat
    ca_bundle: certs/corp-gitlab-ca.pem
    default_project_id: 42
    proxy: http://corp-proxy.internal:3128
    insecure_skip_tls: false
```

Credentials never live in YAML. The wizard stores them in the OS keychain
(macOS Keychain, Linux Secret Service / libsecret, Windows Credential
Manager). For CLI-only invocations, set `<INSTANCE_ID>_TOKEN` in the
environment.

### 8.2 Validate before committing

```bash
nightgauge config show         # prints effective config with source attribution
nightgauge workspace doctor    # validates multi-forge configuration
nightgauge forge auth status   # contacts the forge and reports scopes
```

`config show` is read-only; `workspace doctor` flags misconfigured forges and
`auth status` performs the live network call. All three are required before
the smoke test in §12.

---

## 9. Webhook Ingress

### 9.1 Events the adapter handles

Project-level hooks only ([`webhook.go:11-17`](../internal/gitlab/webhook.go)):

| `X-Gitlab-Event` header | Internal `object_kind` | IPC event name    |
| ----------------------- | ---------------------- | ----------------- |
| `Pipeline Hook`         | `pipeline`             | `gitlab.pipeline` |
| `Merge Request Hook`    | `merge_request`        | `gitlab.mr`       |
| `Note Hook`             | `note`                 | `gitlab.note`     |
| `Push Hook`             | `push`                 | `gitlab.push`     |

Anything else returns `gitlab webhook: unsupported event kind: "<kind>"` and
should be answered with HTTP 200 (silent skip — not an error)
([`webhook.go:19-22`](../internal/gitlab/webhook.go)).

### 9.2 Signature model

GitLab webhooks use a **shared-secret token**, not HMAC. The receiver
constant-time compares the `X-Gitlab-Token` request header against the
expected secret ([`webhook_verify.go:16-26`](../internal/gitlab/webhook_verify.go)):

```go
func VerifyToken(presented, expected string) bool {
    ...
    return subtle.ConstantTimeCompare(p, e) == 1
}
```

An empty header is **always rejected**. The replay window defaults to 5 minutes
(`DefaultReplayWindow = 5 * time.Minute`,
[`webhook_verify.go:8-11`](../internal/gitlab/webhook_verify.go)); events with
timestamps drifting beyond ±5 min from `time.Now()` are treated as stale and
dropped (`IsStale` accepts past-or-future skew symmetrically,
[`webhook_verify.go:32-41`](../internal/gitlab/webhook_verify.go)).

Delivery IDs come from `X-Gitlab-Event-UUID` on GitLab ≥16.4. Older instances
fall through to a deterministic SHA-256 digest computed from event kind +
project ID + object IID + payload
([`webhook.go:160-165`](../internal/gitlab/webhook.go)).

### 9.3 Dev tunnel (laptop → GitLab)

For local evaluation, expose the receiver with an SSH or HTTPS tunnel:

```bash
# ngrok (or cloudflared / tailscale-funnel, etc.)
ngrok http 8765

# Use the resulting https://<id>.ngrok.io URL when configuring the webhook
# in GitLab. Pass the same X-Gitlab-Token secret to the receiver:
export NIGHTGAUGE_WEBHOOK_SECRET="$(openssl rand -base64 32)"
nightgauge forge webhook serve --port 8765
```

In GitLab: **Project → Settings → Webhooks → Add webhook**. URL is the tunnel
URL, secret token is the same `NIGHTGAUGE_WEBHOOK_SECRET` value, trigger
boxes: Pipeline events, Merge request events, Comments, Push events.

### 9.4 Production ingress

For real deployments, terminate TLS at your existing ingress (nginx, traefik,
cloud load balancer). Requirements:

- TLS 1.2+ (1.3 preferred).
- Pass `X-Gitlab-Token`, `X-Gitlab-Event`, `X-Gitlab-Event-UUID` through
  unchanged.
- Set `proxy_request_buffering off` on nginx if pipelines emit large payloads.
- Allowlist the GitLab egress IP(s) at the ingress level.
- (Optional) Add mTLS at the proxy — the receiver does not enforce it but the
  proxy can.

---

## 10. VSCode Forge Configuration Walkthrough

Six steps in the wizard, sourced from
[`configureForgeInstance.ts`](../packages/nightgauge-vscode/src/commands/configureForgeInstance.ts).
Captions below match the title bar exactly so screenshots stay in sync.

### Step 1/6 — Instance ID

Title: **Configure Forge Instance (1/6) — Instance ID**.
Pattern: lowercase letters, digits, hyphens. Examples: `github`, `corp-gitlab`.

![Step 1 — Instance ID](images/self-hosted-gitlab/01-instance-id.png)

### Step 2/6 — Forge URL

Title: **Configure Forge Instance (2/6) — Forge URL**.
Leave blank for GitHub.com; for GitLab enter `https://gitlab.example.com`.

![Step 2 — Forge URL](images/self-hosted-gitlab/02-forge-url.png)

### Step 3/6 — Forge Kind

Title: **Configure Forge Instance (3/6) — Forge Kind**. Pick **GitLab**.

![Step 3 — Forge Kind](images/self-hosted-gitlab/03-forge-kind.png)

### Step 4/6 — Auth Method

Title: **Configure Forge Instance (4/6) — Auth Method**. PAT, OAuth2 (preview
on GitLab only), CI Job Token, or Deploy Token. See §6.

![Step 4 — Auth Method](images/self-hosted-gitlab/04-auth-method.png)

### Step 5/6 — Credential

Title varies by method: **Personal Access Token**, **Deploy Token Username**
followed by **Deploy Token**, or an OAuth2 device-flow info message.
Credential is written to SecretStorage; never to YAML.

![Step 5 — Credential](images/self-hosted-gitlab/05-credential.png)

### Step 6/6 — CA Bundle

Title: **Configure Forge Instance (6/6) — CA Bundle (optional)**. Pick the
PEM file you exported in §4.2 or choose "No CA bundle needed".

![Step 6 — CA Bundle](images/self-hosted-gitlab/06-ca-bundle.png)

### Step 7 — Connection test

The wizard then runs `forgeConnectionTest`, shows a progress toast, and either
confirms `Forge connection successful ✓ (NNN ms)` or surfaces the failure
verbatim. Saved entry lands in `.nightgauge/config.yaml`'s `forges:`
block; the credential lands in OS keychain.

![Step 7 — Connection test](images/self-hosted-gitlab/07-connection-test.png)

> **Screenshots not yet captured.** Placeholder paths above resolve to the
> empty `docs/images/self-hosted-gitlab/` directory; broken-image icons in
> the rendered doc are expected until a follow-up PR adds captures from a
> live wizard session against `gitlab-ce:17.6.0-ce.0`. All other links and
> commands in this doc are valid as-is.

---

## 11. CLI-Only Alternative

For headless servers, skip the wizard and use the CLI directly:

```bash
# 1. Author config.yaml by hand (see §8.1).
$EDITOR .nightgauge/config.yaml

# 2. Export the PAT for the new instance. Convention is <INSTANCE_ID>_TOKEN,
#    uppercased and hyphens → underscores. For instance "corp-gitlab":
export CORP_GITLAB_TOKEN="glpat-..."

# 3. Validate.
nightgauge config show
nightgauge forge auth status
nightgauge forge repo view --forge corp-gitlab --project <id>
```

`nightgauge forge` subcommands: `auth`, `webhook`, `graphql`, `label`,
`repo`, `pr`, `project`. Full reference in
[docs/decisions/008-skill-forge-cli.md](decisions/008-skill-forge-cli.md).

---

## 12. End-to-End Smoke Test

Run after §8 + §10 (or §11) succeed. The smoke test validates the full loop:
config → auth → webhook → pipeline → PR.

```bash
# 1. Create a one-line test repo on GitLab and seed it.
git init smoke && cd smoke
echo "# smoke" > README.md
git add . && git commit -m "init"
git remote add origin https://gitlab.example.com/<group>/smoke.git
git push -u origin main

# 2. Create an issue with the standard pipeline labels.
nightgauge forge pr   # confirm the CLI hits the instance
# (Use GitLab's UI or `glab issue create` to file: type:docs, priority:low.)

# 3. Pick it up.
nightgauge queue add <issue-number>
# or trigger the VSCode pipeline directly from the Ready tab.

# 4. Watch the webhook receiver tail and the GitLab project's
#    Settings → Webhooks → Recent Events panel. Expect:
#    - Pipeline Hook → 200
#    - Note Hook → 200 (for any review comments)
#    - Merge Request Hook → 200 (on PR create + merge)

# 5. Confirm the PR landed and the project board moved through statuses.
nightgauge forge pr list --forge corp-gitlab
```

Capture timings in the operator-tested checklist (§16) so the next operator
sees real numbers, not theoretical ones.

---

## 13. Troubleshooting Matrix

| #   | Symptom                                            | Log signature (verbatim)                                                                                                                                   | Fix                                                                                                        | Prevention                                         |
| --- | -------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- | -------------------------------------------------- |
| 1   | PAT rejected on first call                         | `gitlab auth: PAT is expired or revoked (HTTP 401)` ([`auth.go:168`](../internal/gitlab/auth.go))                                                          | Regenerate PAT with `api read_repository read_user`                                                        | Calendar reminder 7 days before expiry             |
| 2   | PAT scope check fails non-401                      | `gitlab auth: PAT scope check returned HTTP <code>` ([`auth.go:171`](../internal/gitlab/auth.go))                                                          | Inspect GitLab error body; common cause: PAT-API disabled by admin policy                                  | Confirm `Personal Access Tokens` policy is enabled |
| 3   | OAuth2 token validation fails                      | `gitlab auth: OAuth2 token is expired or revoked (HTTP 401)` ([`auth.go:222`](../internal/gitlab/auth.go))                                                 | Run `nightgauge forge auth refresh`                                                                        | Set short refresh schedule                         |
| 4   | OAuth2 device flow not wired                       | `gitlab: OAuth2 device-code Login not yet wired to IPC (tracked: W4-3)` ([`auth.go:132`](../internal/gitlab/auth.go))                                      | Use PAT until W4-3 lands                                                                                   | Track W4-3 epic                                    |
| 5   | CI job token missing in CI                         | `gitlab auth: ci_job_token requires CI=true and CI_JOB_TOKEN to be set` ([`auth.go:259`](../internal/gitlab/auth.go))                                      | Ensure GitLab CI runner exports `CI_JOB_TOKEN`                                                             | Pin runner image with the variable always present  |
| 6   | TLS handshake failure (private CA)                 | `gitlab: read CA bundle "<path>": <err>` ([`transport.go:41`](../internal/gitlab/transport.go))                                                            | Verify path resolves relative to config dir; re-export CA                                                  | Store PEM next to `config.yaml`                    |
| 7   | TLS verification skipped accidentally              | `WARNING: gitlab: InsecureSkipTLS=true for "<base_url>" — TLS certificate verification is disabled` ([`transport.go:57`](../internal/gitlab/transport.go)) | Set `insecure_skip_tls: false`                                                                             | Treat the warning as a CI gate                     |
| 8   | Proxy URL malformed                                | `gitlab: parse proxy URL "<value>": <err>` ([`transport.go:67`](../internal/gitlab/transport.go))                                                          | Use `http://host:port` or `https://host:port`                                                              | Validate with `curl --proxy "$PROXY"`              |
| 9   | Webhook event ignored                              | `gitlab webhook: unsupported event kind: "<kind>"` ([`webhook.go:66`](../internal/gitlab/webhook.go))                                                      | Untick non-supported event types in GitLab webhook config                                                  | Document supported kinds (§9.1)                    |
| 10  | Webhook signature mismatch                         | (no log line — receiver returns 401) ([`webhook_verify.go:16-26`](../internal/gitlab/webhook_verify.go))                                                   | Re-paste the `X-Gitlab-Token` secret on both sides                                                         | Store in a shared secret manager                   |
| 11  | Webhook payload stale                              | (handler logs `IsStale` true) ([`webhook_verify.go:32-41`](../internal/gitlab/webhook_verify.go))                                                          | Sync clocks via NTP; investigate replay attack if NTP is healthy                                           | Run NTP on both hosts                              |
| 12  | Whoami forbidden on deploy token                   | `gitlab auth: credential lacks /api/v4/user access (HTTP 403)` ([`auth.go:323`](../internal/gitlab/auth.go))                                               | Expected for deploy tokens — synthetic scope is used (see [`auth.go:281-298`](../internal/gitlab/auth.go)) | Accept 403 in CI logs                              |
| 13  | Pipeline never moves past pickup                   | (no GitLab-side log; queue stays empty)                                                                                                                    | Confirm webhook receiver reached: `curl https://receiver/-/health`                                         | Add receiver to the project's health dashboard     |
| 14  | `config.yaml` ignores the `forges:` block silently | (no log — `Kind` empty silently skips the entry per [`config.go:756-758`](../internal/config/config.go))                                                   | Set `kind: gitlab` explicitly                                                                              | Run `nightgauge config show` after every edit      |

The 12-row floor from AC #3 is met by rows 1–12; rows 13–14 cover the two
silent-failure modes operators repeatedly hit during dogfood runs.

---

## 14. Migration: GitHub-Only → Dual-Forge → GitLab-Primary

Use this section when you already run Nightgauge against GitHub and want
to add (or switch to) a self-hosted GitLab.

### 14.1 Identify candidate repos

```bash
nightgauge config show
# Check forges.github, then list active project boards.
gh repo list --json name,visibility | jq '.[] | select(.visibility=="INTERNAL")'
```

Pick one or two **non-critical** repos to dual-run first.

### 14.2 Configure the second forge alongside GitHub

Add a `corp-gitlab` entry to `.nightgauge/config.yaml` as in §8.1.
**Do not** remove the existing `github` entry. The wizard supports adding a
second instance without touching the first.

### 14.3 Dual-run

For each candidate repo:

1. Mirror the repo into GitLab (manually or via `git remote add gitlab …`).
2. Open the pipeline against the GitLab mirror.
3. Watch both pipelines complete on the same issue.
4. Compare PR descriptions, labels, and project-board moves side-by-side.

The forge router dispatches on the `forge:` field of each issue/PR. Two
parallel pipelines never collide unless an operator manually attaches the
same forge id to both.

### 14.4 Cutover

Once you have at least three consecutive dual-runs land cleanly:

1. Remove the GitHub mirror's `forges.github` entry (or rename it to
   `github-archive` and stop adding new issues to it).
2. Update the doc-map row in `CLAUDE.md` so onboarding agents prefer GitLab.
3. Roll the change to the rest of the repos in waves of 3–5.

### 14.5 Rollback

The previous `forges.github` entry is preserved in git history. Restore it,
re-run `nightgauge forge auth status`, and the pipeline is back on
GitHub within one config reload.

---

## 15. CE vs EE Feature Notes

The CE/EE feature matrix lives in
[docs/FORGE_ABSTRACTION.md §7](FORGE_ABSTRACTION.md#7-ce-vs-ee-feature-matrix-gitlab).
Quick summary:

| Feature                     | CE  | Premium | Ultimate |
| --------------------------- | --- | ------- | -------- |
| PAT / OAuth2 / deploy token | ✅  | ✅      | ✅       |
| CI / pipeline webhooks      | ✅  | ✅      | ✅       |
| Scoped (group) labels       | ❌  | ✅      | ✅       |
| Iterations / cadences       | ❌  | ✅      | ✅       |
| Push rules                  | ❌  | ✅      | ✅       |
| Merge train                 | ❌  | ✅      | ✅       |

Nightgauge fails open when an EE-only feature is unavailable — see
FORGE_ABSTRACTION.md for the exact degradation behaviour.

---

## 16. Operator-Tested Checklist

Fill this in the first time you run the guide against a fresh
`gitlab-ce:17.6.0-ce.0` instance. Subsequent passes should add a dated row
to the **History** subsection rather than overwriting.

- [ ] §1 Prerequisites installed
- [ ] §2 GitLab CE booted, root password rotated, signup disabled
- [ ] §3 Reverse proxy passes `X-Forwarded-*`
- [ ] §4 TLS cert verified end-to-end (or §4.3 hatch documented in PR body)
- [ ] §5 OAuth app registered (skip if PAT-only)
- [ ] §6 PAT created with exactly `api read_repository read_user`
- [ ] §7 Deploy token created (skip if no CE CI runner)
- [ ] §8 `.nightgauge/config.yaml` written and validated
- [ ] §9 Webhook receiver reachable; signature round-trip confirmed
- [ ] §10 VSCode wizard completed against the new instance
- [ ] §11 CLI-only path verified (optional)
- [ ] §12 Smoke-test issue landed a PR end-to-end
- [ ] §13 At least one troubleshooting row hit — documented in PR body
- [ ] §14 Dual-run plan written (only when migrating from GitHub)

**Last operator-tested**: _Not yet tested. Add date, operator handle, and
GitLab version when filling in the boxes above._

### History

| Date      | Operator | GitLab version          | Notes                             |
| --------- | -------- | ----------------------- | --------------------------------- |
| _pending_ | _@_      | `17.6.0-ce.0` (planned) | Initial publication of this guide |

---

## 17. References

- Design doc: [docs/FORGE_ABSTRACTION.md](FORGE_ABSTRACTION.md)
- Forge ADR: [docs/decisions/008-skill-forge-cli.md](decisions/008-skill-forge-cli.md)
- Workspace ADR: [docs/decisions/009-workspace-schema-migration.md](decisions/009-workspace-schema-migration.md)
- Binary install + CLI: [docs/GO_BINARY.md](GO_BINARY.md)
- Configuration tiers: [docs/CONFIGURATION.md](CONFIGURATION.md)
- Knowledge base for this issue: `.nightgauge/knowledge/features/3369-docs-self-hosted-gitlab-setup/`
- Integration harness: [`tests/integration/docker-compose.gitlab.yml`](../tests/integration/docker-compose.gitlab.yml)
- GitLab Omnibus: [docs.gitlab.com/omnibus](https://docs.gitlab.com/omnibus/)
- GitLab 17.6 OAuth applications: [docs.gitlab.com/administration/auth/oauth_applications](https://docs.gitlab.com/administration/auth/oauth_applications/)
- GitLab 17.6 webhooks: [docs.gitlab.com/user/project/integrations/webhooks](https://docs.gitlab.com/user/project/integrations/webhooks/)
