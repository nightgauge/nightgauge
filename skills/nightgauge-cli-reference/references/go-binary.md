# `nightgauge` Go binary reference

The deterministic layer. Skills should **reuse** these commands rather than
reimplementing logic in prose/bash. Full architecture: `docs/GO_BINARY.md`.

## PATH / preflight contract

The binary is not guaranteed to be on `PATH`. Resolve it first via the Phase-0
PREFLIGHT cascade (`skills/_shared/PREFLIGHT.md`), which is provider-neutral
(#4029) — it references no VSCode-extension path so it works identically under
Claude, Codex, Gemini, etc.:

1. `$NIGHTGAUGE_BIN` (exported by the host that spawns the skill)
2. `command -v nightgauge` (PATH)
3. `$REPO_ROOT/bin/nightgauge`
4. canonical repo `bin/` in worktrees, then `$HOME/go/bin`

Then `nightgauge doctor --json` confirms the environment is healthy.

## Command groups most-used by skills

```bash
# Project board (deterministic board sync)
nightgauge project add <issue> --status Backlog        # add + set Status
nightgauge project move-status <issue> --status "In progress"
nightgauge project ensure-fields                       # idempotent field setup

# Epics
nightgauge epic check-completion <epic>
nightgauge epic assess <epic>
nightgauge epic plan-waves <epic>

# Issues (native sub-issues, blocking, routing)
nightgauge issue create-sub <parent> --title "…" --labels …   # create + link + board
nightgauge issue link-sub <parent> <child>
nightgauge issue add-blocked-by <blocked> <blocker>
nightgauge issue route <issue>                          # change_type/complexity/route

# Gates / CI
nightgauge gate <name> …                                # stage post-condition gates
nightgauge ci wait …                                    # wait for CI (don't --watch in a loop)

# Telemetry / history (read-only aggregations)
nightgauge pipeline aggregate
nightgauge health trends --limit 10 --json
nightgauge health gate-metrics --json
nightgauge skills usage --json                          # skill-usage telemetry (#3957)
nightgauge exit-records tail

# Hooks (invoked by the plugin's hooks.json wrappers, not by hand)
nightgauge hook <subcommand>
```

Most read-only commands accept `--json`. Default owner is `nightgauge`, default
repo `nightgauge` — pass `--owner` / `--repo` for other targets.

## Gotchas

- Prefer the binary over hand-rolled GraphQL/bash for board, epic, and sub-issue
  operations — it encodes the routing/field rules that caused incidents like
  #3232 when done by hand.
- For one-shot auth, binary commands accept `--token "$TOKEN"`; otherwise they use
  the config/gh token chain.
- In multi-account workspaces, scope a token per-command —
  `GH_TOKEN=$(gh auth token --user <acct>) nightgauge …` — rather than
  `gh auth switch`, which changes the **global** active account and silently
  breaks every other open workspace.
- Build/test the binary with `make build-cli` / `go test ./... -count=1`. Adding a
  CLI command does **not** require IPC regen; only `internal/ipc/server.go`
  changes do (`make generate-ipc-client`).
