### Cross-cutting gotchas (every Nightgauge skill)

> These apply to **all** skills. Skill-specific gotchas live above this line in
> each skill's `## Gotchas` section. Keep both terse: symptom → why → do-instead.
> When a new failure mode is root-caused (retro / failure-taxonomy), add it here
> or to the owning skill — Gotchas are the highest-signal part of a skill.

- **Bare `nightgauge` is not always on `PATH`.** Run the Phase 0 PREFLIGHT
  binary-discovery cascade first and prepend `dirname($BINARY)` to `PATH`.
  Skipping it surfaces as `command not found` only on some machines.
- **Never call the forge CLI directly.** Use `nightgauge forge …`
  (`issue`/`pr`/`project`/`label`/`repo`/`auth`/`graphql`), never a bare
  `gh`/`glab`. The `no-direct-gh` lint fails CI on regressions; the abstraction
  is what lets `IB_FORGE=gitlab` work unchanged.
- **Headless skills must not block on input.** If `AskUserQuestion` is not in
  `allowed-tools`, the skill runs headless — make autonomous decisions or fail
  with a clear error. A blocked prompt hangs the whole pipeline run.
- **Write your handoff/context file before exiting — even on failure.** Pipeline
  stages that exit without their `.nightgauge/pipeline/{stage}-{N}.json`
  force the orchestrator onto a repo-blind deterministic fallback that may
  misreport state (#3114). Do not rely on the fallback.
- **Never report success when a step failed.** Swallowing a failed build/test
  lets a broken change flow downstream where it is caught later at higher cost
  (#2779). Surface the failure with its output.
- **Never push to `main`.** Use `feat/`/`fix/`/`docs/` branches; merges are
  manual squash only (no `--auto`, no `--admin`).
- **Production safety.** Never run `docker compose down -v` against a production
  stack — `-v` destroys `postgres_data`. Fix credential mismatches with
  `ALTER USER`, not a volume wipe.
