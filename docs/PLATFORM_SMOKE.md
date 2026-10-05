# Platform Smoke

`.github/workflows/platform-smoke.yml` runs `scripts/platform-smoke.sh` on
`workflow_dispatch` against the **real** production platform API, in two tiers:

- **Anonymous, always.** `GET /v1/health` must answer `200` with
  `status: "ok"`, and the user-scoped routes `GET /v1/analytics/health` and
  `GET /v1/audit/reports` must refuse a caller with no credential (`401`). A
  `2xx` there is a route open to the world and fails the run as `FAIL (open)`.
- **Signed-in, when `PLATFORM_SMOKE_SESSION_TOKEN` is set.** It authenticates
  as a dedicated test account and calls every platform-backed surface the Go
  daemon talks to, asserting on HTTP status codes and — for Health — that the 200
  body is a `PipelineHealthScore` (`compositeScore`, `compositeGrade`,
  `computedAt`, `periodDays`, `totalRunsAnalyzed`). A 200 whose body is some other
  contract still blanks the VSCode Health tab.

**When to run it.** The project keeps no hosted pre-production deployment of
the platform. Pre-release verification is the Docker E2E stack plus smoke tests
against production after each deploy, and this canary is one of those: dispatch
it once a platform deploy is live (#2401). Until #2401 it targeted a hosted
staging deployment that no longer exists.

**There is no schedule.** A daily cron was removed in
[#1087](https://github.com/nightgauge/nightgauge/issues/1087): it failed 9 of 9
scheduled runs (08-20..08-28) at the credential guard because no credential was
provisioned, and because a scheduled run attaches its check-run to `main`'s HEAD
commit, it made the post-merge verification `AGENTS.md` mandates report a
failure against unrelated merges.

**The token is optional and not provisioned (#2425).** Production is
smoke-tested with a license key after every platform deploy, in the platform
repository, so nobody keeps a session JWT for a dedicated production account
here. A dispatch without it runs the anonymous tier and skips the signed-in
one, saying so (see [below](#an-unset-token-skips-the-signed-in-tier)).

This exists because every other test tier for the platform integration (unit
tests, the Go/vitest mocks) runs against a stub, and the defect that epic
[nightgauge/nightgauge#741](https://github.com/nightgauge/nightgauge/issues/741)
exists to fix was a disagreement between what we believed the API required and
what it actually required. A stub cannot catch that disagreement; only a real
deployment can. See [nightgauge/nightgauge#754](https://github.com/nightgauge/nightgauge/issues/754)
for the full rationale.

## What it exercises

The signed-in tier, sourced directly from `internal/platform/*.go` (the Go daemon's own platform
client) — not guessed:

| Surface                             | Method + path                  | Source                                               |
| ----------------------------------- | ------------------------------ | ---------------------------------------------------- |
| Agent registration                  | `POST /v1/agents/register`     | `internal/platform/agent_registration.go`            |
| Agent heartbeat                     | `PUT /v1/agents/:id/heartbeat` | `internal/platform/agent_registration.go`            |
| Agent workspace throttles           | `GET /v1/agents/:id/throttles` | `internal/platform/workspace_throttle.go`            |
| Analytics dashboard / usage summary | `GET /v1/analytics/dashboard`  | `internal/platform/analytics.go` (`GetUsageSummary`) |
| Analytics health                    | `GET /v1/analytics/health`     | `internal/platform/analytics.go`                     |
| Analytics runs                      | `GET /v1/analytics/runs`       | `internal/platform/analytics.go`                     |
| Analytics trends                    | `GET /v1/analytics/trends`     | `internal/platform/analytics.go`                     |
| Analytics cost                      | `GET /v1/analytics/cost`       | `internal/platform/analytics.go`                     |
| Audit reports                       | `GET /v1/audit/reports`        | `internal/platform/compliance.go`                    |
| Audit log retention config          | `GET /v1/audit/retention`      | `internal/platform/audit_retention.go`               |
| Audit integrity verification        | `POST /v1/audit/integrity`     | `internal/platform/audit_retention.go`               |
| Attention (Action Center) sync      | `PUT /v1/attention/sync`       | `internal/platform/attention_sync.go`                |

Any endpoint here that starts accepting an extra parameter, moving to a new
path, or a new platform-backed surface being added to `internal/platform/`
should get a matching row added to `scripts/platform-smoke.sh` — this
table should stay a mirror of that script, not drift from it.

The probe deliberately does **not** call `PUT /v1/audit/retention` (changes
real retention configuration) or `POST /v1/audit/reports` (creates a new
report on every run). It calls only read paths and idempotent-upsert paths
(agent registration upserts by `machine_id`; attention sync with an empty
`requests` array pushes nothing).

## The one assertion that matters

Any `401` or `403` from any endpoint fails the run loudly — a
`::error::` annotation naming the endpoint and status, a non-zero job exit,
and a `FAIL (auth)` row in the job summary. This is not a generic "non-2xx
fails" rule dressed up: it is specifically because a signed-in session
credential answering 401/403 on a user-scoped route is exactly the defect
class this epic exists to catch (see the epic's root-cause writeup for the
original incident). Every other non-2xx status is also a failure, just not
called out with the same urgency.

**Caveat — plan-gated endpoints.** `GET /v1/audit/retention` and
`POST /v1/audit/integrity` are intentionally **enterprise-plan-gated**
on the platform (see `internal/platform/audit_retention.go`): a non-enterprise
account gets a 403 by product design, not by bug. This script does not
special-case that 403 away — the whole point of this canary is that a 403 is
never something to route around silently. **The dedicated test account used
by this workflow must be on a plan tier with access to every surface under
test, including those two**, or they will legitimately and correctly fail
every run. If they start failing and nothing about auth or plan tier changed,
that is real signal, not noise.

## Optional secret

| Name                           | Kind                  | Purpose                                                                                                                                                                                                                                                                                                      |
| ------------------------------ | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `PLATFORM_SMOKE_SESSION_TOKEN` | Repository **secret** | A signed-in session JWT for a **dedicated production test account** — never a maintainer's personal credential. This is the same credential shape the Go daemon receives via `platform.setSessionToken` (#742) and prefers over `NIGHTGAUGE_API_KEY`/license key (`internal/platform/client.go` `bearer()`). |

The target is the public API the daemon uses (`DefaultConfig` in
`internal/platform/client.go`), so the workflow needs no base-URL variable. A
local run can point the script elsewhere, at the Docker E2E stack for example,
with `PLATFORM_SMOKE_BASE_URL`.

Set the secret under **Settings → Secrets and variables → Actions**, or via
`gh`:

```bash
gh secret set PLATFORM_SMOKE_SESSION_TOKEN --repo nightgauge/nightgauge
```

### An unset token skips the signed-in tier

`scripts/platform-smoke.sh` checks the token before making any network call.
When it is empty, the script prints a `::notice::` titled "Platform smoke:
signed-in probes skipped", adds a "Signed-in probes skipped." line to the job
summary, runs only the anonymous tier, and exits on that tier's result. The
skip is never silent: a canary that reads as green while proving nothing is
worse than none (see nightgauge/nightgauge#732 and #744), so the notice says
exactly what did not run. A set token runs both tiers, and any `401`/`403` on
the signed-in tier still fails loudly.

### Rotating `PLATFORM_SMOKE_SESSION_TOKEN`

Session JWTs expire. When the token stops working (the workflow starts
failing every endpoint with 401, not just the plan-gated pair above):

1. Sign in to the platform as the dedicated test account (never a
   personal/maintainer account — this credential lives in a shared CI secret).
2. Capture the session JWT the sign-in flow issues (the same value VSCode
   stores in `platform.accessToken` on sign-in).
3. `gh secret set PLATFORM_SMOKE_SESSION_TOKEN --repo nightgauge/nightgauge`,
   pasting the new token when prompted (or via `--body`, sourced from a local
   environment variable — never a literal on the command line, which would
   land in shell history).
4. `gh workflow run platform-smoke.yml --repo nightgauge/nightgauge` and
   confirm the run goes green before considering the rotation complete.

Rotate on a fixed cadence (align with whatever expiry the platform issues
session JWTs with) and immediately if the token is ever suspected exposed —
treat a suspected leak as compromised the moment it's suspected, per
[standards/security.md](../standards/security.md).

## No credential value reaches the logs

`scripts/platform-smoke.sh` never echoes, prints, or writes the token
into any output it produces — the `call` helper only records the label,
method, path, and HTTP status code. It additionally emits GitHub Actions'
`::add-mask::` workflow command (guarded to real Actions runs via
`GITHUB_ACTIONS=true`, so it never leaks the token into a local test run's own
stdout) as defense in depth on top of the Actions runner's automatic masking
of any value sourced from `secrets.*`.

`scripts/test-platform-smoke.sh` asserts this directly: it runs the
probe against a local mock server with a distinctive fake token, captures
every byte of the script's stdout, stderr, and `$GITHUB_STEP_SUMMARY` output,
and fails the suite if that token string appears anywhere in the capture (see
the "the token never appears in stdout/stderr/summary" case). Run it with:

```bash
bash scripts/test-platform-smoke.sh
```

## What is verified without a real dispatch, and what is not

The regression suite (`scripts/test-platform-smoke.sh`, run by
`scripts/ci-local.sh`) proves,
against a local mock server:

- Every endpoint in the table above is called, with the right method.
- An all-green run exits `0` and reports "Every probe that ran passed." in the
  step summary.
- A single `401` fails the whole run: non-zero exit, a loud `::error::`
  annotation, and a `FAIL (auth)` summary row.
- A `200` on `GET /v1/analytics/health` whose body is not
  `PipelineHealthScore` (for example the invented `{overall_score,
dimensions}` the Health tab used to decode) fails the run as
  `FAIL (shape)`. Status-only probes stay green through that class of
  defect.
- A `200` on `GET /v1/analytics/runs` carrying the shape the platform's
  **published OpenAPI document** declares (`{items, has_more, next_cursor}`)
  fails the run. The route returns `{runs, nextCursor}` and the platform's own
  route tests assert that, so the document is what drifted. Asserting the
  documented shape here would make the canary agree with the spec and
  disagree with production — the exact failure it exists to catch (#801).
- A `200` on `GET /v1/analytics/trends` carrying the pre-#801 client-side
  belief (`{current, previous, period}`) fails the run. Both trends calls are
  made the way the Trends tab makes them — one per metric, with the documented
  `metric`/`granularity`/`dateFrom`/`dateTo` parameters. The old canary sent
  `?period=week`, a parameter the endpoint does not declare and its
  `.passthrough()` schema silently discarded.

  **Write shape assertions from the platform's route, not from its published
  spec.** Both are hand-authored artifacts and either can drift; only the
  route decides what a client receives.

- A `200` on `GET /v1/audit/reports` carrying the pre-#803 client-side belief
  (`{reports, nextCursor, hasMore}`) fails the run. The route returns a single
  `items` envelope and offers no pagination at all — it takes no parameters and
  answers with the account's newest 50 rows. The old probe sent `?limit=1` and
  asserted status only, so it stayed green while the Compliance tab rendered
  "no reports" for an account that had them.

- A `200` on `POST /v1/audit/integrity` carrying the pre-#822 client-side
  belief (`windowDays`, `message` and `checkedAt` alongside `valid` and
  `checkedCount`) fails the run. The route returns `valid`, `checkedCount` and
  `brokenLinks`, and has never sent the other three.
- Probing the old `/v1/audit/integrity/verify` path fails the run. Until #822
  that is what this script sent, with a `{windowDays: 30}` body the route's
  schema rejects — neither could ever have returned a useful 2xx. Nothing here
  caught it because this workflow had never been dispatched against a real
  deployment with a real credential (see the caveat at the end of this
  section); against the mock, an unmapped path answers an empty `200`, which a
  status-only probe called `PASS`. The shape assertion is what makes it red.

- A missing `PLATFORM_SMOKE_SESSION_TOKEN` skips the signed-in tier: the run
  prints the notice and the summary line, calls no signed-in surface, sends no
  bearer, and exits `0` when the anonymous tier passes. With no token, a
  user-scoped route that answers an anonymous caller with `2xx` fails the run
  as `FAIL (open)`, and an API health that answers `503` fails it too. An
  unset base URL is not a failure: it means the public API.
- The workflow is dispatch-only, passes the production test account's token,
  and names no other deployment.
- The credential is actually sent as the bearer (so masking isn't silently
  hiding a broken auth header) and never appears in the script's own output.

**Not verified by this suite, and not verifiable without a maintainer
supplying the real secret and dispatching the workflow**: that the production
API is reachable, that every endpoint's response _shape_ matches what
`internal/platform/*.go` expects (the mock returns `{}` except for the shapes
asserted above), that the `PLATFORM_SMOKE_SESSION_TOKEN` a maintainer
provisions actually carries the right scopes/role for every route (in
particular the audit endpoints' owner/admin role requirement), and that the
dedicated test account is on the enterprise plan tier the retention/integrity
endpoints require. Do not treat a merged PR implementing this workflow as proof
the canary works — treat it as proof the canary is correctly _wired_. It is
proven only by a green `workflow_dispatch` run against production.
