---
name: careful
description: Turn on an opt-in guardrail that blocks production-destructive shell
  commands (docker compose down -v, docker volume rm, kubectl delete, SQL
  DROP/TRUNCATE) for the rest of the working session. Use before touching a
  production droplet, database, or cluster — when you want a safety net while
  doing risky ops — and turn it off when done.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Bash Read
disable-model-invocation: true
---

# Careful Mode

> Opt-in destructive-operation guardrail. On only when you ask for it — an
> always-on version would block routine work.

<!-- phase-registry: standalone-skill -->

## Description

When you are about to touch production, flip this on. While careful mode is
active, the `careful-gate` PreToolUse(Bash) hook **blocks** the documented
production-destructive commands with a clear reason and the safe alternative.
When off (the default), it is a complete no-op.

This complements the always-on workflow gate (which already blocks
`git reset --hard`, `git clean -f`, secret read/write, and pushes to any branch
listed in the opt-in `hooks.push_gate.protected_branches`)
by adding the **prod-data-destruction** set that you only want guarded
occasionally.

> Implementation note: skill-frontmatter session hooks are not wired into this
> codebase, so careful mode is a sentinel lock (`careful.lock` in this
> checkout's directory, `nightgauge layout path checkout careful.lock`)
> that the always-registered gate consults — opt-in via `careful on`, cleared via
> `careful off`, with a TTL backstop so a forgotten lock can't block forever.

## Invocation

| Tool        | Command                                 |
| ----------- | --------------------------------------- |
| Claude Code | `/nightgauge-careful [on\|off\|status]` |
| Codex       | `$nightgauge-careful on`                |

## What it blocks (while ON)

- `docker compose down -v` / `--volumes` — destroys the `postgres_data` volume.
  Fix credential issues with `ALTER USER`, never a volume wipe.
- `docker volume rm` / `docker volume prune` — can delete persistent data.
- `kubectl delete …` — can remove live cluster resources (prefer `kubectl apply`).
- SQL `DROP TABLE/DATABASE/SCHEMA` and `TRUNCATE` — irreversible data loss.

## Workflow

### Phase 0: Environment Preflight

### Phase 0: Environment Preflight

**PURPOSE**: Verify the nightgauge binary is available and the environment
is healthy before executing any pipeline stage. Skills halt immediately with
actionable instructions when this check fails.

Three-layer check:

1. **Binary discovery** — shell-level, runs before invoking the binary
2. **Doctor check** — binary-level, full environment health via `doctor --json`
3. **Project environment** — the checks the project declares under
   `preflight.environment` (#101); opt-in, silent when none are declared

The binary discovery cascade below is **provider-neutral** (issue 4029): it works
identically under every adapter (Claude, Codex, Gemini, …) and references no
VSCode-extension-specific path. The host that spawns the skill (the VSCode
extension's `skillRunner`, or the Go `nightgauge` binary in auto/CLI mode)
resolves the binary authoritatively and exports `NIGHTGAUGE_BIN`, which
this cascade honors first. The remaining steps (PATH, the development-only repo
`bin/` and worktree canonical-repo `bin/`, `~/go/bin`) are fallbacks for direct
or terminal invocation.

This intentionally **diverges** from
`claude-plugins/nightgauge/hooks/lib/guard.sh` (issue 3262 → issue 4029): guard.sh is
Claude-Code-only and retains a trailing `~/.vscode/extensions/...` glob to serve
the standalone-terminal-Claude case (where no host exports `NIGHTGAUGE_BIN`).
Skills must stay portable Markdown and cannot reference that VSCode-only path —
so the shared resolution order (`NIGHTGAUGE_BIN` → PATH → gated repo → gated
canonical → `~/go/bin`) stays in sync, and only guard.sh carries the extra Claude-only
fallback. The `nightgauge preflight skill-portability` gate enforces that
no skill reintroduces a `.vscode/extensions` path, and that every cascade keeps
the `NIGHTGAUGE_DEV_REPO_BIN` gate (#2758): a foreign repository's
`bin/nightgauge` must never run.

After the cascade resolves `$BINARY`, the preflight prepends
`dirname($BINARY)` to `PATH` so subsequent bare `nightgauge ...`
invocations later in the same skill body resolve through PATH rather than
failing with "command not found" (issue 3262).

```bash
# Layer 1: provider-neutral binary discovery (NIGHTGAUGE_BIN → PATH → gated repo
# bin → gated canonical-repo bin → ~/go/bin). No VSCode-extension path — see issue 4029.
# Repo bins need NIGHTGAUGE_DEV_REPO_BIN=1 and a verified core checkout (#2758).
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
  echo "" >&2
  echo "Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest" >&2
  echo "Or download from: https://github.com/nightgauge/nightgauge/releases" >&2
  echo "Run \`nightgauge doctor\` after installing to verify your environment." >&2
  exit 1
fi
# Make the resolved binary callable via bare `nightgauge ...` later in this skill body (issue 3262).
export PATH="$(dirname "$BINARY"):$PATH"

# Per-repo GitHub token: export GH_TOKEN so every `gh` call in this skill
# authenticates as the *current repo's* configured user instead of the machine-
# global `gh auth` active account. `forge auth token` resolves per-repo (config
# github_user/token → GITHUB_TOKEN → `gh auth token --user <github_user>`), so
# concurrent sessions/workspaces owned by different GitHub users each use their
# own token. GH_TOKEN outranks GITHUB_TOKEN and the gh keyring. Only resolve when
# unset (an upstream env value — e.g. the VSCode extension's terminal env —
# wins); never fail preflight over it. Mirrors hooks/lib/guard.sh (sync, issue 3262).
if [ -z "${GH_TOKEN:-}" ]; then
  IB_REPO_TOKEN="$("$BINARY" forge auth token 2>/dev/null || true)"
  if [ -n "$IB_REPO_TOKEN" ]; then
    export GH_TOKEN="$IB_REPO_TOKEN"
    export GITHUB_TOKEN="${GITHUB_TOKEN:-$IB_REPO_TOKEN}"
  fi
  unset IB_REPO_TOKEN
fi

# Layer 2: full environment health check via doctor --json
DOCTOR_RESULT=$("$BINARY" doctor --json 2>/dev/null)
DOCTOR_EXIT=$?
if [ "$DOCTOR_EXIT" -eq 2 ]; then
  echo "ERROR: Environment check failed — nightgauge doctor reports broken environment." >&2
  FAILED_CHECKS=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.failed_checks[]? // empty' 2>/dev/null)
  if [ -n "$FAILED_CHECKS" ]; then
    echo "Failing check(s): $(echo "$FAILED_CHECKS" | tr '\n' ',' | sed 's/,$//')" >&2
  fi
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.errors[]' >&2 2>/dev/null || true
  INSTALL_MSG=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.install_instructions // empty' 2>/dev/null)
  [ -n "$INSTALL_MSG" ] && echo "$INSTALL_MSG" >&2
  exit 1
fi
if [ "$DOCTOR_EXIT" -eq 1 ]; then
  echo "WARNING: Environment has non-critical issues:" >&2
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.warnings[]' >&2 2>/dev/null || true
  # Continue — warnings do not block skill execution
fi

# Layer 3: project-declared environment checks (preflight.environment, #101).
# Opt-in: with none declared this prints nothing and continues. The verb
# writes one `[environment-halt]` line per failed check, with its remedy.
if ! "$BINARY" preflight environment >/dev/null; then
  echo "[environment-halt] Project environment check failed — fix the environment, not the code." >&2
  exit 1
fi
```

**Exit codes from `nightgauge doctor`**:

| Code | Meaning                         | Skill behavior                          |
| ---- | ------------------------------- | --------------------------------------- |
| 0    | Healthy                         | Continue                                |
| 1    | Degraded (warnings only)        | Continue with warning printed to stderr |
| 2    | Broken (required checks failed) | Halt immediately with error details     |

**Layer 3** halts the stage when any check declared under
`preflight.environment` in the workspace's `.nightgauge/config.yaml` fails
(exit 1) or the declaration is invalid (exit 2). The failure is in the
operator's environment, not the code: do not edit code to work around it, do
not run the remedy yourself, and end the stage. The pipeline records it as
`environment_halt` and holds the issue for an operator.

Do not theorize about the cause beyond what is printed here — the failing
check name (`failed_checks`) and its `.errors[]` message are the complete
diagnosis (#277).


### Phase 1: Apply the requested mode

Parse the argument (`on` | `off` | `status`; default `status`) and run the
deterministic command:

```bash
case "${1:-status}" in
  on)     nightgauge careful on --note "manual /careful" ;;
  off)    nightgauge careful off ;;
  status) nightgauge careful status ;;
  *)      echo "usage: /nightgauge-careful [on|off|status]" >&2; exit 2 ;;
esac
```

Report the resulting state to the user. If turning **on**, remind them to run
`/nightgauge-careful off` (or `nightgauge careful off`) when the risky
work is done — otherwise it auto-expires after the TTL (default 12h).

## Gotchas

- **It is opt-in.** Careful mode does nothing until `careful on`. Don't assume a
  prod session is guarded — check `careful status` first.
- **Turn it off when done.** It blocks `docker compose down -v` etc. for everyone
  in the repo until `off` or TTL expiry — a forgotten lock is friction.
- **It complements, not replaces, the always-on gate.** Main-push (including force) /
  secret protection are always enforced; careful adds the prod-data set.

### Cross-cutting gotchas (every Nightgauge skill)

> These apply to **all** skills. Skill-specific gotchas live above this line in
> each skill's `## Gotchas` section. Keep both terse: symptom → why → do-instead.
> When a new failure mode is root-caused (retro / failure-taxonomy), add it here
> or to the owning skill — Gotchas are the highest-signal part of a skill.

- **Bare `nightgauge` is not always on `PATH`.** Run the Phase 0 PREFLIGHT
  binary-discovery cascade first and prepend `dirname($BINARY)` to `PATH`.
  Skipping it surfaces as `command not found` only on some machines.
- **Never emit a foreground `sleep` wait loop.** The harness denies foreground
  `sleep` — an unbounded shell idiom that polls a completion marker in a
  `while`/`until` loop with no iteration cap — the tool call is rejected
  outright. To wait on a long-running background job, start it with the
  backgrounded-command form the harness provides (e.g. a Bash call with
  `run_in_background: true`) and then block on its completion notification /
  the Monitor tool instead of polling on a timer. Never invent a foreground
  poll loop to bridge the wait (#289).
- **Never call the forge CLI directly.** Use `nightgauge forge …`
  (`issue`/`pr`/`project`/`label`/`repo`/`auth`/`graphql`), never a bare
  `gh`/`glab`. The `no-direct-gh` lint fails CI on regressions; the abstraction
  is what lets `IB_FORGE=gitlab` work unchanged.
- **Headless skills must not block on input.** If `AskUserQuestion` is not in
  `allowed-tools`, the skill runs headless — make autonomous decisions or fail
  with a clear error. A blocked prompt hangs the whole pipeline run.
- **Write your handoff/context file before exiting — even on failure.** Pipeline
  stages that exit without their `{stage}-{N}.json` in the clone's pipeline
  state directory (`nightgauge layout path pipeline`) force the orchestrator
  onto a repo-blind deterministic fallback that may misreport state (issue 3114). Do not rely on the fallback.
- **An assertion that cannot go red is decoration.** A test that passes both
  before and after a fix constrains nothing — a regression test written for an
  SSH-push bug asserted "a push succeeds with no auth configured" and passed
  identically on the broken code, because a `file://` remote needs no
  credentials either way. Prove it: `cp <file> /tmp/<file>.bak`, restore the
  pre-fix behavior **from the copy**, confirm the new test FAILS, restore, and
  confirm it passes. Never `git checkout -- <file>` to back a fix out — on an
  uncommitted branch that destroys the fix. See
  [Vacuous Assertion](https://github.com/nightgauge/nightgauge/blob/main/docs/FAILURE_TAXONOMY.md#vacuous-assertion-the-test-that-cannot-go-red).
- **A green suite is a statement about the cases that RAN.** After adding
  assertions, check the reported PASS count rose by exactly the number you
  added; a suite can skip a case silently and still print "all tests passed".
- **Verify the issue's premise before implementing it.** An issue's stated cause
  can be flatly wrong — a request to delete a sweep that was never built, a
  claim that a failure category already classified an error when it fell through
  to `CatUnknown`, a "build it or delete it" fork where building was
  architecturally impossible. Check the claim against the tree first; if the
  premise is false, do the smaller correct thing and say so in the handoff.
- **Never report success when a step failed.** Swallowing a failed build/test
  lets a broken change flow downstream where it is caught later at higher cost
  (#2779). Surface the failure with its output.
- **Never push to `main`.** Use `feat/`/`fix/`/`docs/` branches; merges are
  manual squash only (no `--auto`, no `--admin`).
- **Production safety.** Never run `docker compose down -v` against a production
  stack — `-v` destroys `postgres_data`. Fix credential mismatches with
  `ALTER USER`, not a volume wipe.


## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) Issue-to-PR pipeline.
