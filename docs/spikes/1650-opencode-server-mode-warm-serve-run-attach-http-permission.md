# Spike #1650: OpenCode server mode — warm `serve` + `run --attach`, HTTP permission approver, `json_schema` output, GitHub agent/ACP

**Issue**: #1650
**Status**: Complete
**Date**: 2026-09-21
**Prerequisite ADR**: [ADR-022](../decisions/022-opencode-multi-provider-adapter.md)
**Observed against**: opencode **1.18.31** (ADR-022 was observed against 1.18.30 — see § 7)

## Spike Contract (Path A)

Path A: the recommendations below materialize as follow-up issues after this spike's PR
merges. No dependent implementation issues were filed up front.

**Artifact**: `docs/spikes/1650-opencode-server-mode-warm-serve-run-attach-http-permission.md`

## Executive Summary

**One verdict per question: 1 defer, 2 defer, 3 skip, 4 skip.**

The decisive finding is not a latency number. It is that **`run --attach` ignores the
attached run's configuration and isolation entirely**. With
`OPENCODE_CONFIG_CONTENT`, all four XDG base directories, `HOME`, `TMPDIR` and the
permission map pointed at a second sandbox, the model request still went to the
**server's** endpoint and the transcript landed in the **server's** session database
(249856 bytes) while the attached run's stayed empty (4096 bytes, § 2). The attached
`run` is a thin HTTP client; the server is the agent. Everything ADR-022 § 8, § 9,
§ 15 and § 17 build — per-run XDG roots, the per-run config, the permission map, the
pinned models, the per-stage credential set — is a property of the **spawn**, not of
the run. A server shared across stages therefore collapses every stage into one
identity, one credential set, one permission map and one transcript database. That is
the gate any warm-serve work has to pass, and it is an architectural collision, not a
tuning problem.

Against that, the measured upside is small. Process startup, with the model held
constant by the #1618 stub, is **~1705 ms cold per stage vs ~647 ms attached** in steady
state, against a **1217 ms one-time server boot** — about **1.05 s saved per stage after
the first**, and roughly 1.4 s net across a three-stage sequence (§ 1). The issue's
premise is a ~76 s cold prefill on the local model; ~1 s of process startup is noise
against it. The entire case for warm serve rests on whether the **LM Studio prefix cache
survives across attached runs**, and that is a property of the model server, which
`run --attach` does not address. No LM Studio instance with Qwen3.8 27B was reachable on
this machine (`127.0.0.1:1234/v1/models` returned nothing), so **every latency row that
depends on a local model is marked "operator to measure"** and, per the issue's own
instruction, the warm-serve verdict **defaults to defer**.

The HTTP permission approver **works** — `permission.asked` over a directory-scoped
`/event` stream, answered by `POST /session/{sessionID}/permissions/{permissionID}` with
`{"response":"reject"}`, **3 ms** ask-to-reply on loopback, and the model saw the tool
fail (§ 3). But it is strictly worse than the #1635 in-process gate on the three things
that matter: the reply body admits **no reason the model sees**; a pending ask is
**invisible to every polling endpoint** (both v2 list routes returned `{"data": []}`
while an ask was pending), so a restarted approver is blind to it; and with no approver
the ask **hangs forever** — still `status: "running"` at t+29 s, never auto-rejected,
never timed out. That is the opposite of headless `run`, which ADR-022 § 9 observed
auto-rejecting an `ask`. **It does not fail closed; it fails open into a hang.**

`json_schema` output is **unusable on 1.18.31**, and not because of the model. The only
route that both accepts `format` and returns a body synchronously —
`POST /session/{id}/prompt` — is **not routed** on this build (it serves the web UI's
HTML). `prompt_async` accepts `format`, persists it, and then
`GET /session/{id}/message` returns **400 BadRequest** for the whole session:
`Expected OutputFormatJsonSchema, got {"type":"json_schema","schema":{"type":"object"},"retryCount":0}`
— a value that is byte-for-byte what the server's own published `OutputFormatJsonSchema`
component declares valid. A `json_schema` prompt makes the session's transcript
unreadable. `retryCount: 2` produced exactly **one** upstream model request against a
reply that could never satisfy the schema, so the retry is not enforced either (§ 4).
The v2 route `/api/session/{id}/prompt` has **no `format` field at all**.

GitHub agent and ACP are **interactive-only surfaces** and are recorded as non-goals
(§ 5), the expected answer. Compat coverage is **partial and must not key on the
version** (§ 6): `info.version` is a static `"1.0.0"`, and the two API generations are
**not interoperable on this build** — a v1-issued ask 404s against the v2 reply route.

## 1. Warm `serve` + `run --attach` — verdict: **defer**

### Startup cost, model held constant

Three-stage sequence, same stub model endpoint, same worktree
(`/tmp/ng1650-lab/e7-warm.sh`):

| Stage                 | Cold (fresh `run`) | Warm (`run --attach`) |
| --------------------- | -----------------: | --------------------: |
| 1                     |            2049 ms |               1547 ms |
| 2                     |            1693 ms |                642 ms |
| 3                     |            1705 ms |                652 ms |
| **total**             |        **5447 ms** |           **2841 ms** |
| one-time `serve` boot |                  — |           **1217 ms** |
| **total incl. boot**  |        **5447 ms** |           **4058 ms** |

Steady-state saving is **~1.05 s per stage after the first**; net saving across the
three-stage sequence is **~1.4 s**. This is process and server startup only — the stub
returns immediately and does no prefill.

### The rows that need the operator's machine

| Measurement                                                   | Value                   |
| ------------------------------------------------------------- | ----------------------- |
| Cold-stage prefill, local model (Qwen3.8 27B on LM Studio)    | **operator to measure** |
| Per-step wall time with a warm prefix cache                   | **operator to measure** |
| Prefill time on an attached run, 2nd and 3rd stage            | **operator to measure** |
| Does the LM Studio prefix cache survive across attached runs? | **operator to measure** |

No LM Studio was reachable (`curl -s -m 2 http://127.0.0.1:1234/v1/models` returned
nothing). Per the issue's instruction, the mechanics were measured against the #1618
stub (`cmd/stub-provider`) and this verdict defaults to **defer**.

### Do per-run config, the permission map and XDG isolation apply per attached run?

**No. They apply only at server start.** This is the spike's central result.

Method (`/tmp/ng1650-lab/e4-attach.sh`): two distinguishable stub providers, A and B.
`serve` was started with `XDG_CONFIG_HOME` → sandbox A, whose `opencode.json` points
`lmstudio`'s `baseURL` at **stub A** and sets `permission: {bash: deny, edit: deny,
write: deny}`. The attached run was then given `HOME`, `TMPDIR`, all four XDG base
directories **and** `OPENCODE_CONFIG_CONTENT` pointing at sandbox B and **stub B**.

| Signal                                   | Result                       |
| ---------------------------------------- | ---------------------------- |
| Stub A (server-start config) request log | **1 request served**         |
| Stub B (attached-run config) request log | **0 requests**               |
| Sandbox A session DB (`opencode.db`)     | **249856 bytes**             |
| Sandbox B session DB (`opencode.db`)     | **4096 bytes** (empty)       |
| Attached run exit                        | 0, 3 stream events on stdout |

The attached run's `OPENCODE_CONFIG_CONTENT`, permission map and XDG isolation were all
silently discarded. The transcript — the full prompt and reply — was written into the
**server's** data directory, not the run's.

### Why that is disqualifying as stated, and what would not be

ADR-022 § 8 confines a stage by giving its **spawn** a private `HOME`, private XDG
roots, a private config and a private permission map, and § 17 gives it exactly one
credential. All of that is set at `serve` time. So:

- **One server shared across stages** voids per-stage isolation wholesale: one
  permission map, one pinned-model set, one credential set, and one SQLite transcript
  database holding every stage's prompt (§ 22's retention boundary moves with it).
- **One server per stage** restores isolation but saves nothing: the 1217 ms boot
  exceeds the ~1.05 s per-stage saving.

The one coherent shape left is **one server per worktree run, for a group of stages that
already share an identical config, permission map and credential set** — which is the
thing a follow-up would have to establish is ever true, and is not true across the
current six stages. `POST /session` does accept a per-session `permission`
ruleset (a `PermissionRuleset`, an **array** of `{permission, pattern, action}` rules —
note the shape differs from the config file's map form that #1638 builds), which is the
only per-request lever found; it does not move `HOME`, the XDG roots, the pinned models,
the credentials or the transcript database.

### Auth, bind and reaping (the security bar)

Every `serve` in this spike was started as
`opencode serve --hostname 127.0.0.1 --port <random> --log-level ERROR`, with a
`openssl rand -hex 32` (32-byte) `OPENCODE_SERVER_PASSWORD`, and **never** `--mdns` or
`--cors`. Observed:

| Probe                                                                      | Result                                          |
| -------------------------------------------------------------------------- | ----------------------------------------------- |
| `lsof -a -iTCP -sTCP:LISTEN -P -p <serve pid>`                             | `TCP localhost:<port> (LISTEN)` — loopback only |
| Unauthenticated `GET /doc` (password set)                                  | **401**                                         |
| Unauthenticated `GET /session` (password set)                              | **401**                                         |
| Wrong-password `GET /doc`                                                  | **401**                                         |
| Authenticated `GET /doc`                                                   | **200**                                         |
| Unauthenticated `GET /doc` (**password unset**)                            | **200**                                         |
| Unauthenticated `GET /session` (password unset)                            | **200**, body `[]`                              |
| Unauthenticated `GET /api/permission/request` (password unset)             | **200**                                         |
| Unauthenticated `GET /config` (password unset)                             | **200**                                         |
| `GET http://<this host's LAN address>:<port>/doc` (`--hostname 127.0.0.1`) | connection refused                              |
| `run --attach` with a wrong password                                       | exit **1**, `Error: Session not found`          |

So the issue's assumption holds: **`serve` is unauthenticated unless
`OPENCODE_SERVER_PASSWORD` is set**, and with it set, basic auth guards the whole
surface including the permission-reply and shell endpoints. Two corrections to the
stated assumptions are in § 7. Note the wrong-password refusal is reported as
`Session not found` rather than as an auth failure — it fails closed, but the message
misdiagnoses.

**These are mandatory requirements for any adopted follow-up**, and every recommendation
in the block below carries the dependency:

1. `--hostname 127.0.0.1` passed explicitly, with a random port.
2. A random per-server `OPENCODE_SERVER_PASSWORD` of at least 32 bytes from a CSPRNG,
   set only in the child's environment — never on argv, never logged.
3. `--mdns` and `--cors` never passed, to `serve` **or** to `acp` (§ 5).
4. The server PID captured at spawn, its process group killed at stage end, and death
   confirmed with a failing `kill -0`.

## 2. HTTP permission approver — verdict: **defer**

### It works, through the v1 pair only

`/tmp/ng1650-lab/e5-approver.py`, with `permission: {bash: "ask"}` in the server's
config and the `bash-then-stop` stub script:

| Step                                                             | Result                                                                                                                                                                                                      |
| ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /event` (unscoped)                                          | only `server.connected`, `server.heartbeat` — **no session events**                                                                                                                                         |
| `GET /event?directory=<worktree>`                                | delivers `permission.asked` ✓                                                                                                                                                                               |
| Event payload                                                    | `{"type":"permission.asked","properties":{"id":"per_…","sessionID":"ses_…","permission":"bash","patterns":["python3 calc.py"],"metadata":{"command":"python3 calc.py"},"always":["python3 *"],"tool":{…}}}` |
| `POST /api/session/{s}/permission/{p}/reply` (v2)                | **404** `PermissionNotFoundError`                                                                                                                                                                           |
| `POST /permission/{p}/reply`                                     | not reached (v1 plural route answered first)                                                                                                                                                                |
| `POST /session/{s}/permissions/{p}` with `{"response":"reject"}` | **200 `true`** ✓                                                                                                                                                                                            |
| Resulting tool state                                             | `{"status":"error","error":"The user rejected permission to use this specific tool call."}`                                                                                                                 |
| Ask → reply latency                                              | **3 ms** (loopback; an earlier run measured 239–251 ms dominated by the approver's own HTTP retries, not the server)                                                                                        |

So a Go-side approver **can** answer a permission ask over HTTP, and the model does see
the tool fail. Latency is negligible next to the #1635 in-process plugin gate.

### The three blockers

1. **No reason reaches the model.** The route that works takes
   `{"response": "once"|"always"|"reject"}` and `additionalProperties: false` — a
   `reason` key is rejected. The text the model receives is OpenCode's fixed
   `"The user rejected permission to use this specific tool call."` The v2 route's body
   _does_ carry a `message` field (`{reply, message}`), but v2 cannot address a
   v1-issued ask on this build (the 404 above). #1635's gate, by contrast, denies under
   a named `[nightgauge-gate:…]` reason.
2. **A pending ask is invisible to polling.** While the `bash` ask was pending, both
   `GET /api/permission/request` and `GET /api/session/{id}/permission` returned
   `{"data": []}`. The SSE event is the **only** delivery and it is not replayed. An
   approver that restarts mid-stage cannot discover the ask it missed.
3. **An absent approver hangs, it does not fail closed.** With no approver connected,
   the tool part stayed `{"status":"running"}` at **t+29 s** — no auto-rejection, no
   timeout, no error. Headless `opencode run` auto-rejects an `ask` and exits 0
   (ADR-022 § 9); routed through the server, the same `ask` stalls the session
   indefinitely. Any adopted approver must therefore own its own deadline and a
   fail-closed path, and that is work the in-process gate does not need.

Combined with § 1 — an HTTP approver presupposes the supervised server — this is a
**defer**, dependent on the warm-server question being resolved first.

## 3. `json_schema` structured output — verdict: **skip**

The published contract is sound: `OutputFormat` is
`OutputFormatText | OutputFormatJsonSchema`, the latter
`{type: "json_schema", schema: JSONSchema, retryCount?: integer ≥ 0}`, and a
`StructuredOutputError` carries `{message, retries}`. The implementation on 1.18.31 is
not (`/tmp/ng1650-lab/e6-jsonschema.py`):

| Probe                                                                                      | Result                                                                                                                             |
| ------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------- |
| `POST /session/{id}/prompt` (the only route declaring `format` **and** a synchronous body) | **not routed** — returns the web UI's `<!doctype html>`                                                                            |
| `POST /api/session/{id}/prompt` (v2)                                                       | body has `{delivery, id, prompt, resume}` — **no `format` field**                                                                  |
| `POST /session/{id}/prompt_async` with `format`                                            | **204**, accepted, 1 upstream model request made                                                                                   |
| `GET /session/{id}/message` afterwards                                                     | **400 BadRequest**                                                                                                                 |
| The 400's message, minimal schema                                                          | `Expected OutputFormatJsonSchema, got {"type":"json_schema","schema":{"type":"object"},"retryCount":0}` at `[0]["info"]["format"]` |
| Same prompt, no `format` (control)                                                         | 204, messages read back cleanly                                                                                                    |
| `retryCount: 2` against a reply that can never satisfy the schema                          | **1** upstream model request — the retry did not happen                                                                            |

The rejected value is exactly what the server's own OpenAPI document declares valid:
`JSONSchema` is `{"type":"object"}` and the minimal `{"type":"object"}` schema was used,
so this is not a malformed input. **A `json_schema` prompt writes a message the server
can no longer decode, and the whole session's transcript becomes unreadable.** Stage
output contracts cannot be built on it.

Cost of retries: **not measurable**, because retries did not occur. Stage-output
contracts stay where they are — the `--format json` stream and Nightgauge's own parsing
(ADR-022 § 2).

## 4. GitHub agent and ACP — verdict: **skip** (non-goal)

As expected, both are interactive or externally-driven surfaces, not pipeline
execution paths.

- **`opencode github`** offers exactly `install` and `run`. It installs and runs a
  GitHub-hosted agent that reacts to repository events. Nightgauge already owns issue
  intake, branch, worktree, stage routing and PR lifecycle; a second agent acting on the
  same repository from inside GitHub would duplicate and race that ownership, and it is
  driven by GitHub's credentials rather than the per-stage credential set of § 17.
- **`opencode acp`** starts an Agent Client Protocol **server** for an editor to drive.
  It is the inverse of the pipeline's direction of control: the editor is the client and
  the human is in the loop. Its flags are also the listener hazard surface —
  `--port`, `--hostname`, `--mdns` (which "defaults hostname to 0.0.0.0") and `--cors` —
  so if it is ever started, § 1's bar applies to it unchanged.

Recorded as non-goals in ADR-022 § 15 with these reasons.

## 5. Compat coverage — verdict: partial; do not key on the version

| Probe                     | Value                                                               |
| ------------------------- | ------------------------------------------------------------------- |
| `GET /doc` `openapi`      | `3.1.0`                                                             |
| `GET /doc` `info.version` | **`1.0.0`** (static; unrelated to the release)                      |
| Path count                | **162**                                                             |
| Operation count           | **188**                                                             |
| Permission-related paths  | 9, across both generations                                          |
| Shell/command/tool paths  | 8, incl. `/session/{id}/shell`, `/pty/shells`, `/experimental/tool` |

The compat manifest (#1613) and the canary (#1639) **can** cover server mode, but not by
reading `info.version`, which never moves. What they can assert cheaply and meaningfully:

1. **The path count and the presence of the specific endpoints relied on** — here,
   `GET /event`, `POST /session/{sessionID}/permissions/{permissionID}`. A drift in
   either is exactly the breakage that matters.
2. **Routed-ness, not merely declaration.** This build _declares_
   `POST /session/{id}/prompt` and `GET /session/{id}/permission` in its own OpenAPI
   document and then serves the **web UI's HTML** for both. A manifest that trusts
   `/doc` alone would record capabilities the server does not have. Any probe must issue
   the request and assert a JSON content type.
3. **Generation interoperability.** The two generations are **not** interoperable on
   1.18.31: the ask arrives as a v1 `permission.asked` event and 404s against the v2
   reply route, while the v2 list routes report the same ask as absent. Pinning one
   generation per capability is a manifest fact worth recording.

This is testable drift, not untestable drift — but only if the probe exercises the
endpoints rather than reading the document.

## 6. Does plain `opencode run` open a listener? — **No**, on 1.18.31

ADR-022 § 18's observation **holds on 1.18.31**. Measured mid-request
(`/tmp/ng1650-lab/e3-run-listener.sh`), with the stub holding the model request open for
12 s and `lsof` sampled at t+6 s:

```text
process tree: 98944            (no children)
pid 98944 LISTEN: (none)
```

This is worth restating because 1.18.31's `opencode run --help` now advertises
`--port  port for the local server (defaults to random port if no value provided)`,
which reads as though `run` always starts one. It does not: without `--port` or
`--attach`, no listening socket is opened. ADR-022 § 15's locked row — `--port`,
`--mdns` and `--cors` never passed to `run` — is what keeps that true, and it should
stay locked.

### Adjacent observation, not characterised: outbound HTTPS from a plain `run`

During the same run, whose only model endpoint was a loopback stub and whose environment
set `OPENCODE_DISABLE_MODELS_FETCH=1`, `OPENCODE_DISABLE_AUTOUPDATE=1`,
`OPENCODE_DISABLE_LSP_DOWNLOAD=1`, `OPENCODE_DISABLE_DEFAULT_PLUGINS=1` and
`OPENCODE_DISABLE_SHARE=1`, with no credentials of any kind, `lsof` showed **28
established outbound TCP connections to 104.16.0.0/16:443** (Cloudflare space; no PTR
records) alongside the single loopback connection to the stub. A second run reproduced
it (28 connections across four 104.16.x.x addresses).

**This is recorded as an observation only.** The peer host was not identified — that
needs TLS SNI inspection, which is outside this spike's scope — so no claim is made
about what is sent. It is flagged because it is adjacent to the security bar this spike
sets, and because ADR-022 § 10 (egress defaults) and `scripts/opencode-egress-check.sh`
already own exactly this question. The recommendation below is to point that existing
check at it, not to mitigate anything unobserved.

## 7. Divergences from the assumptions stated in the issue

The issue required that observed behaviour differing from its stated assumptions be
recorded and ADR-022 amended before continuing. Recorded here; ADR-022 is amended in
the same change.

| Assumption in #1650                    | Observed                                                                                                                                                                                   | Divergence                                                                                                    |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------- |
| opencode **1.18.30**                   | **1.18.31** installed (`opencode --version`)                                                                                                                                               | Yes — one patch above ADR-022's floor and max-tested. Every observation here is on 1.18.31.                   |
| 162 OpenAPI paths                      | **162**                                                                                                                                                                                    | No                                                                                                            |
| OpenAPI 3.1                            | `3.1.0`                                                                                                                                                                                    | No                                                                                                            |
| `info.version` static `"1.0.0"`        | `1.0.0`                                                                                                                                                                                    | No                                                                                                            |
| `serve` unauthenticated by default     | Confirmed: 200 unauthenticated with the password unset; 401 everywhere with it set                                                                                                         | No                                                                                                            |
| `--mdns` binds `0.0.0.0`               | Confirmed from `serve --help`: "enable mDNS service discovery (**defaults hostname to 0.0.0.0**)". Not exercised, deliberately.                                                            | No                                                                                                            |
| —                                      | **`--hostname` already defaults to `127.0.0.1`** on `serve`, `acp` and the top-level command                                                                                               | New. The § 18 guardrail is the default; pass it explicitly anyway, so an inherited default can never move it. |
| —                                      | **`serve` has no `--password` flag.** Auth comes solely from `OPENCODE_SERVER_PASSWORD` in the environment. (`run --attach` does take `-p`/`--password`, defaulting to the same variable.) | New. There is no argv path for the secret, which is the desired property — record it so nobody adds one.      |
| —                                      | **`opencode acp` carries `--port`, `--hostname`, `--mdns` and `--cors` too**                                                                                                               | New. Same listener hazard surface as `serve`; § 1's bar applies to it.                                        |
| ADR-022 § 18: `run` opens no listener  | Holds on 1.18.31 (§ 6)                                                                                                                                                                     | No                                                                                                            |
| ADR-022 § 9: an `ask` is auto-rejected | Holds for headless `run`; **does not hold** through the server, where an unanswered ask hangs indefinitely (§ 2)                                                                           | Yes — the auto-rejection is a property of the `run` client, not of the agent.                                 |

## 8. Reap log

Every server and stub started during this spike, with its PID captured at spawn, killed
explicitly, and death confirmed by a **failing** `kill -0`.

```text
E1  serve  pid 98506   kill 98506; kill -0 98506 -> failed (dead)
E2  serve  pid 98720   kill 98720; kill -0 98720 -> failed (dead)
E3  stub   pid 98923   kill 98923; wait; kill -0 98923 -> failed (dead)
E4  serve  pid 99381   kill 99381; kill -0 99381 -> failed (dead)
E4  stub A pid 99355   kill 99355; kill -0 99355 -> failed (dead)
E4  stub B pid 99356   kill 99356; kill -0 99356 -> failed (dead)
E5  stub   pid 99870   crashed run; reaped via pkill, leftovers verified empty
E5  serve  pid 99872   crashed run; reaped via pkill, leftovers verified empty
E5  stub   pid 191     kill 191;  kill -0 191  -> failed (dead)
E5  serve  pid 192     kill 192;  kill -0 192  -> failed (dead)
E5  stub   pid 562     kill 562;  kill -0 562  -> failed (dead)
E5  serve  pid 563     kill 563;  kill -0 563  -> failed (dead)
E5  stub   pid 1015    kill 1015; kill -0 1015 -> failed (dead)
E5  serve  pid 1019    kill 1019; kill -0 1019 -> failed (dead)
E5  stub   pid 1321    kill 1321; kill -0 1321 -> failed (dead)
E5  serve  pid 1322    kill 1322; kill -0 1322 -> failed (dead)
E5  stub   pid 1552    kill 1552; kill -0 1552 -> failed (dead)
E5  serve  pid 1553    kill 1553; kill -0 1553 -> failed (dead)
E6  stub   pid 1813    kill 1813; kill -0 1813 -> failed (dead)
E6  serve  pid 1814    kill 1814; kill -0 1814 -> failed (dead)
E6  stub   pid 1986    kill 1986; kill -0 1986 -> failed (dead)
E6  serve  pid 1987    kill 1987; kill -0 1987 -> failed (dead)
E6  stub   pid 2146    kill 2146; kill -0 2146 -> failed (dead)
E6  serve  pid 2147    kill 2147; kill -0 2147 -> failed (dead)
E7  stub   pid 2322    kill 2322; kill -0 2322 -> failed (dead)
E7  serve  pid 2508    kill 2508; kill -0 2508 -> failed (dead)
E8  stub   pid 2833    kill 2833; kill -0 2833 -> failed (dead)
```

Two processes (E5 pids 99870/99872) outlived their script when an early revision of the
approver raised before its reap block. They were killed by pattern in the next command
and the leftover check ran clean; every later experiment reaps in a `finally`-equivalent
block. After the final experiment:

```text
$ pgrep -f 'ng1650-lab/stub-provider|opencode serve --hostname'
(empty)
```

The unrelated `OpenCode.app` desktop Electron processes on this machine match a bare
`pgrep -fl opencode` and are **not** spike processes; the pattern above excludes them.

## 9. Method

Every claim was observed on **opencode 1.18.31** on 2026-09-21, not read from
documentation. Throwaway directories for `HOME`, `TMPDIR` and all four XDG base
directories; a scratch git repository as `--dir`; and the repository's own
`cmd/stub-provider` (#1618) as an OpenAI-compatible model server on `127.0.0.1`, so no
hosted provider and no operator configuration took part. Scripts:
`e1-serve.sh` (auth, bind, OpenAPI surface), `e2-noauth.sh` (default auth posture),
`e3-run-listener.sh` (question 6), `e4-attach.sh` (attached-run isolation),
`e5-approver.py` (HTTP approver), `e6-jsonschema.py` (structured output),
`e7-warm.sh` (cold vs warm startup), `e8-egress.sh` (the § 6 observation).

## Recommendations

```yaml recommendations
spike: 1650
recommendations:
  - id: opencode-supervised-warm-serve
    action: defer
    title: "adapters: supervised warm `opencode serve` + `run --attach` for local-model stages"
    type: feature
    priority: medium
    size: M
    labels: ["component:go-binary"]
    body: |
      Deferred pending two answers this spike could not supply.

      **Blocking, architectural.** `run --attach` ignores the attached run's
      configuration and isolation entirely: with `OPENCODE_CONFIG_CONTENT`, all
      four XDG base directories, `HOME`, `TMPDIR` and the permission map pointed
      at a second sandbox, the model request still went to the server's endpoint
      and the transcript landed in the server's session database. ADR-022 § 8,
      § 9, § 15 and § 17 all build per-stage containment at spawn time, so a
      server shared across stages collapses every stage into one identity, one
      credential set, one permission map and one transcript database. Before any
      implementation, this issue must establish that a group of stages sharing
      one identical config, permission map and credential set actually exists —
      one server per stage restores isolation but saves nothing, because the
      1217 ms server boot exceeds the ~1.05 s per-stage startup saving.

      **Blocking, unmeasured.** The case rests entirely on whether the LM Studio
      prefix cache survives across attached runs. Measured on the #1618 stub,
      warm attach saves only ~1.05 s of process startup per stage after the
      first — noise against the ~76 s cold prefill that motivates the work. An
      operator with Qwen3.8 27B loaded must measure per-stage prefill for a
      three-stage sequence, fresh `run` vs `run --attach`, before this is sized.

      **Mandatory security requirements, binding on any implementation** (from
      the spike's § 1): `--hostname 127.0.0.1` passed explicitly with a random
      port; a random per-server `OPENCODE_SERVER_PASSWORD` of at least 32 bytes
      from a CSPRNG, set only in the child's environment and never on argv or in
      a log; `--mdns` and `--cors` never passed, to `serve` or to `acp`; the
      server PID captured at spawn, its process group killed at stage end, and
      death confirmed by a failing `kill -0`. Unauthenticated `serve` was
      confirmed to serve `/session`, `/config` and `/api/permission/request` to
      any local caller, so the password is not optional.

      See docs/spikes/1650-opencode-server-mode-warm-serve-run-attach-http-permission.md § 1.
    depends_on: []
  - id: opencode-http-permission-approver
    action: defer
    title: "hooks: Go-side OpenCode permission approver over the server API"
    type: feature
    priority: medium
    size: M
    labels: ["component:go-binary"]
    body: |
      Proven to work and deferred anyway. A `permission.asked` event on a
      directory-scoped `GET /event?directory=<worktree>`, answered by
      `POST /session/{sessionID}/permissions/{permissionID}` with
      `{"response":"reject"}`, returns 200 and the model sees the tool fail, in
      3 ms on loopback. The v2 reply route 404s a v1-issued ask on 1.18.31, so
      the v1 pair is the only working combination and must be pinned.

      Three blockers make it strictly worse than the #1635 in-process gate
      today, and each must be closed before adoption:

      1. The working reply body admits only
         `{"response": "once"|"always"|"reject"}` with
         `additionalProperties: false`, so no reason reaches the model — it sees
         OpenCode's fixed rejection string, not a `[nightgauge-gate:…]` cause.
         The v2 body does carry a `message` field; it is unreachable for a
         v1-issued ask on this build.
      2. A pending ask is invisible to polling: both `GET /api/permission/request`
         and `GET /api/session/{id}/permission` returned `{"data": []}` while a
         `bash` ask was pending. The SSE event is the only delivery and is not
         replayed, so an approver that restarts mid-stage cannot recover it.
      3. With no approver connected the ask hangs indefinitely — still
         `status: "running"` at t+29 s, never auto-rejected, never timed out.
         Headless `opencode run` auto-rejects an `ask` (ADR-022 § 9); through the
         server it does not. The approver must therefore own its own deadline and
         an explicit fail-closed path, which the in-process gate does not need.

      Requires the supervised server, and inherits every security requirement of
      `opencode-supervised-warm-serve`.

      See docs/spikes/1650-opencode-server-mode-warm-serve-run-attach-http-permission.md § 2.
    depends_on: ["opencode-supervised-warm-serve"]
  - id: opencode-json-schema-output-skip
    action: skip
    title: "adapters: OpenCode `json_schema` structured output for stage-output contracts"
    type: feature
    priority: low
    size: S
    labels: ["component:go-binary"]
    body: |
      Skipped: unusable on 1.18.31 because of a server-side round-trip defect,
      not because of the model. `POST /session/{id}/prompt` — the only route
      that both declares `format` and returns a synchronous body — is not routed
      and serves the web UI's HTML. The v2 `POST /api/session/{id}/prompt` has no
      `format` field. `prompt_async` accepts `format`, persists it, and then
      `GET /session/{id}/message` returns 400 for the entire session:
      `Expected OutputFormatJsonSchema, got {"type":"json_schema","schema":{"type":"object"},"retryCount":0}`
      — a value byte-for-byte identical to what the server's own published
      `OutputFormatJsonSchema` component declares valid. A `json_schema` prompt
      makes the transcript unreadable. `retryCount: 2` produced one upstream
      model request against a non-conforming reply, so the retry is not enforced
      and its cost could not be measured. Stage-output contracts stay on the
      `--format json` stream and Nightgauge's own parsing (ADR-022 § 2).
      Revisit only if a later release fixes the round trip.
    depends_on: []
  - id: opencode-github-agent-acp-non-goal
    action: skip
    title: "docs: record OpenCode GitHub agent and ACP as non-goals in ADR-022"
    type: docs
    priority: low
    size: XS
    labels: ["component:docs"]
    body: |
      Both are interactive or externally-driven surfaces, not pipeline execution
      paths. `opencode github install|run` installs a GitHub-hosted agent that
      reacts to repository events, duplicating and racing the intake, branch,
      worktree, routing and PR lifecycle Nightgauge already owns, under GitHub's
      credentials rather than the per-stage credential set of ADR-022 § 17.
      `opencode acp` starts an Agent Client Protocol server for an editor to
      drive — the inverse of the pipeline's direction of control, with a human in
      the loop. Recorded as non-goals in ADR-022 § 15 with these reasons, in the
      same change that commits this spike, so this recommendation creates no
      issue.

      One carry-over: `acp` exposes `--port`, `--hostname`, `--mdns` and
      `--cors`, the same listener hazard surface as `serve`, so § 1's security
      requirements apply to it unchanged if it is ever started.
    depends_on: []
  - id: opencode-compat-manifest-covers-server-mode
    action: defer
    title: "compat: cover OpenCode server mode by probing endpoints, never by reading `info.version`"
    type: chore
    priority: low
    size: S
    labels: ["component:go-binary"]
    body: |
      Server mode is testable drift, but only if the probe exercises endpoints
      rather than reading the OpenAPI document. `GET /doc` reports
      `openapi: 3.1.0`, a static `info.version: "1.0.0"` unrelated to the
      release, 162 paths and 188 operations. Three things the compat manifest
      (#1613) and the canary (#1639) should assert instead:

      1. The path count, and the presence of the specific endpoints relied on —
         `GET /event` and `POST /session/{sessionID}/permissions/{permissionID}`.
      2. Routed-ness, not declaration. 1.18.31 declares
         `POST /session/{id}/prompt` and `GET /session/{id}/permission` in its own
         document and serves the web UI's HTML for both, so a manifest trusting
         `/doc` alone would record capabilities the server does not have. The
         probe must issue the request and assert a JSON content type.
      3. Which generation owns each capability. The two are not interoperable on
         1.18.31: an ask arrives as a v1 `permission.asked` event, 404s against
         the v2 reply route, and is reported absent by the v2 list routes.

      Deferred behind the server work itself: there is nothing to keep compatible
      until a server-mode capability is adopted.

      See docs/spikes/1650-opencode-server-mode-warm-serve-run-attach-http-permission.md § 5.
    depends_on: ["opencode-supervised-warm-serve"]
  - id: opencode-run-outbound-https-characterise
    action: defer
    title: "security: characterise the outbound HTTPS a plain `opencode run` opens"
    type: chore
    priority: medium
    size: S
    labels: ["component:go-binary"]
    body: |
      An observation made while answering question 6, recorded without a claim
      about what is sent. A plain `opencode run` whose only model endpoint was a
      loopback stub, with `OPENCODE_DISABLE_MODELS_FETCH=1`,
      `OPENCODE_DISABLE_AUTOUPDATE=1`, `OPENCODE_DISABLE_LSP_DOWNLOAD=1`,
      `OPENCODE_DISABLE_DEFAULT_PLUGINS=1` and `OPENCODE_DISABLE_SHARE=1` set and
      no credentials of any kind, held 28 established outbound TCP connections to
      104.16.0.0/16:443 (Cloudflare space, no PTR records) alongside the single
      loopback connection to the stub. Reproduced across two runs.

      The peer host was not identified — that needs TLS SNI inspection, outside
      this spike's scope — so nothing is asserted about the content, and nothing
      should be mitigated until the mechanism is observed. The work is to point
      the existing `scripts/opencode-egress-check.sh` harness and ADR-022 § 10's
      egress defaults at this case and record what it finds.

      See docs/spikes/1650-opencode-server-mode-warm-serve-run-attach-http-permission.md § 6.
    depends_on: []
```
