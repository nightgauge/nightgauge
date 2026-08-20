# Data-arrival fixture store — provenance and re-recording

These payloads are the **transport-level** responses the data-arrival tier
(#746) feeds to the extension. They are not view models and not hand-shaped
convenience objects: each one is the bytes that cross a real boundary — a Go
IPC reply, an HTTPS body, or a file another process wrote.

Why that matters: every other dashboard test builds its own fixture _after_
the boundary and asserts the renderer draws it. A fixture written from the
renderer's point of view can disagree with the API forever and keep the suite
green. Epic #741 is what that looks like in production — four tabs blank for
months behind ~1,600 passing tests.

## The two things that keep a fixture honest

1. **`manifest.json` binds each fixture to the struct that serialises it.**
   `tests/arrival/fixtureContract.test.ts` parses the named Go source, reads
   the `json:"…"` tags, and fails when the fixture has a key the boundary does
   not emit, or omits a key the boundary always emits (no `omitempty`). A
   fixture that drifts from `internal/platform/*.go` or `pkg/types/types.go`
   turns the tier red rather than quietly lying.
2. **Assertions read rendered output, never the fixture object.** An arrival
   test asserts a value from the fixture appears in the HTML the view actually
   produced, so deleting the transport still fails even if the fixture is
   perfect.

Fixtures with `"contract": null` have no in-repo artifact to check against
(the platform's own OpenAPI document is not vendored here — see the header of
`api/platform-operations.yaml`). For those, the shape is transcribed from the
consuming parser and the provenance below is the only guarantee. Treat them as
the weakest links in the store.

## Re-recording

All IPC fixtures come from the same place: a signed-in Go daemon answering the
method named in `manifest.json`.

> **Status of the committed payloads.** The recording procedure below is the
> normative one and is what a refresh must use. The payloads currently in the
> tree were produced without a live platform account: each is a redacted,
> structurally faithful instance of the boundary struct named in
> `manifest.json`, with values chosen to exercise the render paths (a `null`
> `projectId`, a failed run with no `stages`, a `processing` report with no
> `downloadUrl`, a `next_cursor`). That is weaker provenance than a capture,
> and the contract test is what stands in for it — it is a mechanical check
> against the same Go source the daemon serialises from, so the fixture cannot
> disagree with the boundary in shape, only in content. Re-record against a
> real account when one is available and delete this note.

### IPC fixtures (`platform/*.json` except `audit-log.json`, `github/*.json`)

There is no `nightgauge ipc` CLI verb. The daemon speaks newline-delimited
JSON-RPC over stdio (`nightgauge serve`, hidden), so a recording is one request
written to its stdin and one response read back off stdout. The daemon also
emits unsolicited events with no `id`, hence the `select(.id == …)`.

The platform's user-scoped analytics routes require a **session JWT**, not a
license key — `internal/platform/contract_conformance_test.go` refuses the
license-key path before it reaches the network. The daemon does not persist a
session; the extension hands it one at runtime via `platform.setSessionToken`
(#742/#756), so a recording session must do the same in the same stdin stream.
Get the token by signing in through the extension (`Nightgauge: Sign In`) and
reading `accessToken` out of VSCode SecretStorage.

```bash
export NG_SESSION_TOKEN='eyJ…'   # session JWT from the extension, never a license key
cd /path/to/a/real/workspace

record() {   # record <fixture-path> <ipc-method> [params-json]
  local out="$1" method="$2" params="${3:-}"
  {
    printf '{"id":1,"method":"platform.setSessionToken","params":{"token":"%s"}}\n' "$NG_SESSION_TOKEN"
    if [ -n "$params" ]; then
      printf '{"id":2,"method":"%s","params":%s}\n' "$method" "$params"
    else
      printf '{"id":2,"method":"%s"}\n' "$method"
    fi
  } | nightgauge serve --workspace "$PWD" 2>/dev/null \
    | jq -c 'select(.id == 2)' \
    | jq '.result' > "$out"
}

F=packages/nightgauge-vscode/tests/fixtures/arrival
record "$F/platform/analytics-health.json"   platform.getAnalyticsHealth
record "$F/platform/usage-summary.json"      platform.getUsageSummary
record "$F/platform/analytics-runs.json"     platform.getAnalyticsRuns    '{"limit":20}'
record "$F/platform/analytics-trends.json"   platform.getAnalyticsTrends  '{"period":"7d"}'
record "$F/platform/cost-analytics.json"     platform.getCostAnalytics    '{"startDate":"2026-08-04","endDate":"2026-08-11"}'
record "$F/platform/compliance-reports.json" platform.auditListReports    '{"limit":20}'
record "$F/github/pr-list.json"              pr.list                      '{"owner":"nightgauge","repo":"nightgauge","state":"OPEN"}'
```

`jq` printing `null` means the response carried an `error` rather than a
`result` — rerun without the final `jq '.result'` to read it. A `null` fixture
must never be committed; the contract test rejects it, but notice it here.

### HTTPS fixture (`platform/audit-log.json`)

`AuditLogService` bypasses IPC and calls the platform directly with the session
JWT, so record it with the same request the service builds
(`buildCanonicalUrl` in `src/services/AuditLogService.ts`):

```bash
curl -sS -H "Authorization: Bearer $NG_SESSION_TOKEN" \
  "$PLATFORM_URL/v1/audit-log?from=2026-08-04T00:00:00Z&to=2026-08-11T00:00:00Z&limit=50" \
  > packages/nightgauge-vscode/tests/fixtures/arrival/platform/audit-log.json
```

### Filesystem fixtures (`discovery/*.json`)

These are written by scheduled GitHub Actions, so recording them is a copy:

```bash
cp .nightgauge/release-watch/creation-log.json      …/arrival/discovery/creation-log.json
cp .nightgauge/improvement-runs/latest.json         …/arrival/discovery/improvement-runs-latest.json
cp .nightgauge/release-watch/backlog.json           …/arrival/discovery/backlog.json
```

Then replace absolute timestamps with the `__RECENT_MINUS_1D__` /
`__RECENT_MINUS_2D__` placeholders that `tests/arrival/fixtures.ts` substitutes
at load time. Without that, the "issues created this week" rollup silently
falls to zero once the recording ages past seven days — a fixture that rots
into a false negative.

### Local telemetry (history / overview / analytics tabs)

The local-telemetry tabs read real pipeline JSONL, so they reuse the recorded
run records already in `tests/fixtures/telemetry/`. To refresh, copy a day file
out of a real workspace:

```bash
cp .nightgauge/pipeline/history/2026-08-11.jsonl \
   packages/nightgauge-vscode/tests/fixtures/telemetry/health-history-multi-run.jsonl
```

**Redact before committing.** Recorded payloads carry account ids, emails,
repository names, and download URLs. Every fixture in this directory has had
identifiers replaced with structurally identical synthetic values; keep that
property. The contract test checks shape, not content, so redaction never
weakens it.

## Adding a fixture

1. Record it as above; do not hand-write it.
2. Add a `manifest.json` entry, with a `contract` block whenever a Go struct
   serialises the payload. `"contract": null` requires a `note` saying why.
3. Redact identifiers.
4. Reference it from an arrival test through `tests/arrival/fixtures.ts` —
   `fixtureContract.test.ts` fails on any fixture the manifest does not list,
   and on any manifest entry with no file.
