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
3. repo, then worktree canonical-repo, `bin/nightgauge`: only with
   `NIGHTGAUGE_DEV_REPO_BIN=1` in a verified Nightgauge core checkout
4. `$HOME/go/bin`

Then `nightgauge doctor --json` confirms the environment is healthy.

## Command groups most-used by skills

```bash
# Project board (deterministic board sync)
"${NIGHTGAUGE_BIN:-nightgauge}" project add <issue> --status Backlog        # add + set Status
"${NIGHTGAUGE_BIN:-nightgauge}" project move-status <issue> in-progress
"${NIGHTGAUGE_BIN:-nightgauge}" project ensure-fields                       # idempotent field setup

# Epics
"${NIGHTGAUGE_BIN:-nightgauge}" epic check-completion <epic>
"${NIGHTGAUGE_BIN:-nightgauge}" epic assess <epic>
"${NIGHTGAUGE_BIN:-nightgauge}" epic plan-waves <epic>

# Issues (native sub-issues, blocking, routing)
"${NIGHTGAUGE_BIN:-nightgauge}" issue create-sub <parent> --title "…" --labels …   # create + link + board
"${NIGHTGAUGE_BIN:-nightgauge}" issue link-sub <parent> <child>
"${NIGHTGAUGE_BIN:-nightgauge}" issue add-blocked-by <blocked> <blocker>     # blocker: N, owner/repo#N, repo#N, URL
"${NIGHTGAUGE_BIN:-nightgauge}" issue route <issue>                          # change_type/complexity/route

# Gates / CI
"${NIGHTGAUGE_BIN:-nightgauge}" gate <name> …                                # stage post-condition gates
"${NIGHTGAUGE_BIN:-nightgauge}" ci wait …                                    # wait for CI (don't --watch in a loop)

# Telemetry / history (read-only aggregations)
"${NIGHTGAUGE_BIN:-nightgauge}" pipeline aggregate
"${NIGHTGAUGE_BIN:-nightgauge}" health trends --limit 10 --json
"${NIGHTGAUGE_BIN:-nightgauge}" health gate-metrics --json
"${NIGHTGAUGE_BIN:-nightgauge}" skills usage --json                          # skill-usage telemetry (#3957)
"${NIGHTGAUGE_BIN:-nightgauge}" exit-records tail
"${NIGHTGAUGE_BIN:-nightgauge}" exit-records gate-summary --issue 42         # full gates per run and stage

# Per-clone data layout (ADR-024 § 7): pipeline, plans, retros, logs
"${NIGHTGAUGE_BIN:-nightgauge}" layout                                       # whole layout as JSON
"${NIGHTGAUGE_BIN:-nightgauge}" layout path pipeline issue-42.json           # absolute path to read
jq -n '{…}' | "${NIGHTGAUGE_BIN:-nightgauge}" layout write pipeline issue-42.json   # atomic write
"${NIGHTGAUGE_BIN:-nightgauge}" layout write plans 42-slug.md --from /tmp/plan.md
echo '{…}' | "${NIGHTGAUGE_BIN:-nightgauge}" layout append pipeline history/events.jsonl

# Hooks (invoked by the plugin's hooks.json wrappers, not by hand)
"${NIGHTGAUGE_BIN:-nightgauge}" hook <subcommand>
```

Most read-only commands accept `--json`. Default owner is `nightgauge`, default
repo `nightgauge` — pass `--owner` / `--repo` for other targets.

## Gotchas

- Per-clone data (`pipeline`, `plans`, `retros`, `logs`) lives in
  `<git-common-dir>/nightgauge/<class>/`, not in the working tree, and a linked
  worktree shares the main clone's. Never hard-code it and never write under
  the git directory by path: read at `$(nightgauge layout path <class> <name>)`,
  write with `nightgauge layout write <class> <name>` (stdin or `--from FILE`;
  atomic, confined to the class directory, prints the path) and append with
  `nightgauge layout append`. `nightgauge layout` prints the whole layout as JSON
  (`schema_version`, `root`, `git_common_dir`, `clone`, `pipeline`, `plans`,
  `retros`, `logs`, `state`, `cache`, `runtime`); outside a git repository every
  `layout` command fails with `not a git repository`.
- Prefer the binary over hand-rolled GraphQL/bash for board, epic, and sub-issue
  operations — it encodes the routing/field rules that caused incidents like
  #3232 when done by hand.
- No binary command takes a GitHub token on argv (it would be visible through
  `ps`). For one-shot auth set `GITHUB_TOKEN` / `GH_TOKEN` in the command's
  environment; otherwise they use the config/gh token chain.
- In multi-account workspaces, scope a token per-command —
  `GH_TOKEN=$(gh auth token --user <acct>) nightgauge …` — rather than
  `gh auth switch`, which changes the **global** active account and silently
  breaks every other open workspace.
- Build/test the binary with `make build-cli` / `go test ./... -count=1`. Adding a
  CLI command does **not** require IPC regen; only `internal/ipc/server.go`
  changes do (`make generate-ipc-client`).
