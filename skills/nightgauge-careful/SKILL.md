---
name: nightgauge-careful
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

This complements the always-on workflow gate (which already blocks pushes to
`main`, force-pushes, `git reset --hard`, `git clean -f`, and secret read/write)
by adding the **prod-data-destruction** set that you only want guarded
occasionally.

> Implementation note: skill-frontmatter session hooks are not wired into this
> codebase, so careful mode is a sentinel lock (`.nightgauge/careful.lock`)
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

<!-- include: ../_shared/PREFLIGHT.md -->

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
- **It complements, not replaces, the always-on gate.** Force-push / main-push /
  secret protection are always enforced; careful adds the prod-data set.

<!-- include: ../_shared/GOTCHAS.md -->

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) Issue-to-PR pipeline.
