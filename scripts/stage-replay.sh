#!/usr/bin/env bash
# stage-replay.sh — run ONE pipeline stage against a model, on a private copy
# of an issue's working state, and record how it went.
#
# The feedback loop for tuning a stage: instead of a full pipeline run
# (hours on a local model), replay just the stage you changed, as often as
# you like, from the same starting state, and compare runs by their summary
# lines. The prompt is the pipeline's own (`nightgauge skill render --issue`,
# execution.BuildPrompt), and each adapter is spawned the way the Go adapter
# spawns it: opencode from `nightgauge opencode config`, claude with the
# claude-headless adapter's flags and environment.
#
# Usage:
#   scripts/stage-replay.sh --worktree DIR --issue N --stage STAGE
#       [--adapter opencode|claude] [--model MODEL] [--context-type TYPE]
#       [--profile compact|full] [--max-turns N] [--label NAME] [--out DIR]
#       [--nightgauge BIN] [--skills-root DIR]
#   scripts/stage-replay.sh --summarize RUN_DIR   (re-summarize a finished run)
#
#   --worktree      the issue's pipeline worktree (or any checkout); only read
#   --adapter       opencode (default; model defaults to opencode.model) or
#                   claude (model defaults to sonnet)
#   --profile       compact (default for opencode, as a local endpoint gets)
#                   or full (default for claude, as a hosted model gets)
#   --context-type  the stage's input context; default by stage (issue,
#                   planning, dev, validate)
#   --max-turns     cap the stage's model turns, e.g. 2 for a smoke test
#   --skills-root   the directory holding nightgauge-*/SKILL.md
#
# Safety: the copy is a standalone `git clone --local` (its own .git, so no
# commit or checkout touches the source worktree or its branch) whose pushes
# to origin are disabled. `nightgauge` and `gh` are wrapped: forge and board
# WRITES (issue/PR edits, board moves, labels, API mutations) are skipped and
# logged to blocked.log instead, so a replay shows what the stage would have
# written without writing it; reads pass through. The stage can still write
# the main checkout's knowledge base (knowledge_path is absolute).
#
# Output: $OUT/<label>-<timestamp>/ holds the copy (wt/), prompt.md,
# events.jsonl, stderr.log, blocked.log and summary.json; the summary line is
# also appended to $OUT/results.jsonl.
set -euo pipefail

# summarize RUN_DIR — write summary.json for a finished replay, append it to
# results.jsonl beside the run directory, and print it.
summarize() {
  python3 - "$1" <<'PY'
import json, re, subprocess, sys
run_dir = sys.argv[1]
meta = json.load(open(f"{run_dir}/meta.json"))
turns = out_tok = compactions = 0
cost = None
last_phase = ""
marker = re.compile(r'phase:start name=\\?"([^"\\]+)\\?" index=(\d+) total=(\d+)')
for line in open(f"{run_dir}/events.jsonl", errors="replace"):
    try:
        ev = json.loads(line)
    except ValueError:
        continue
    part = ev.get("part") or {}
    if part.get("type") == "step-finish":  # opencode: one per model turn
        turns += 1
        out_tok += (part.get("tokens") or {}).get("output", 0) or 0
    if ev.get("type") == "result":  # claude stream-json: the run's totals
        turns = ev.get("num_turns", turns)
        out_tok = (ev.get("usage") or {}).get("output_tokens", out_tok)
        cost = ev.get("total_cost_usd")
    if part.get("type") == "compaction" or "compact_boundary" in line:
        compactions += 1
    for m in marker.finditer(line):
        last_phase = f"{m.group(2)}/{m.group(3)} {m.group(1)}"
changed = subprocess.run(["git", "-C", f"{run_dir}/wt", "status", "--short"],
                         capture_output=True, text=True).stdout.splitlines()
try:
    blocked = sum(1 for _ in open(f"{run_dir}/blocked.log"))
except OSError:
    blocked = 0
summary = dict(meta, turns=turns, output_tokens=out_tok, compactions=compactions,
               last_phase=last_phase, changed_files=len([c for c in changed if c.strip()]),
               blocked_writes=blocked, run_dir=run_dir)
if cost is not None:
    summary["cost_usd"] = cost
json.dump(summary, open(f"{run_dir}/summary.json", "w"), indent=2)
with open(f"{run_dir}/../results.jsonl", "a") as f:
    f.write(json.dumps(summary) + "\n")
print(json.dumps(summary, indent=2))
PY
}

worktree="" issue="" stage="" model="" ctx_type="" profile="" label="" out="/tmp/stage-replay"
adapter="opencode" ng="$(command -v nightgauge || true)" skills_root="" max_turns=0
while [ $# -gt 0 ]; do
  case "$1" in
    --worktree) worktree="$2"; shift 2 ;;
    --issue) issue="$2"; shift 2 ;;
    --stage) stage="$2"; shift 2 ;;
    --adapter) adapter="$2"; shift 2 ;;
    --model) model="$2"; shift 2 ;;
    --context-type) ctx_type="$2"; shift 2 ;;
    --profile) profile="$2"; shift 2 ;;
    --label) label="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --nightgauge) ng="$2"; shift 2 ;;
    --skills-root) skills_root="$2"; shift 2 ;;
    --max-turns) max_turns="$2"; shift 2 ;;
    --summarize) summarize "$2"; exit 0 ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$worktree" ] && [ -n "$issue" ] && [ -n "$stage" ] || { echo "--worktree, --issue and --stage are required" >&2; exit 2; }
case "$adapter" in opencode|claude) ;; *) echo "--adapter must be opencode or claude" >&2; exit 2 ;; esac
[ -x "$ng" ] || { echo "nightgauge binary not found (use --nightgauge)" >&2; exit 2; }
ng="$(cd "$(dirname "$ng")" && pwd)/$(basename "$ng")"
worktree="$(cd "$worktree" && pwd)"
if [ -z "$profile" ]; then
  if [ "$adapter" = opencode ]; then profile=compact; else profile=full; fi
fi
[ "$adapter" = claude ] && [ -z "$model" ] && model=sonnet
if [ -z "$ctx_type" ]; then
  case "$stage" in
    feature-planning) ctx_type=issue ;;
    feature-dev) ctx_type=planning ;;
    feature-validate) ctx_type=dev ;;
    pr-create) ctx_type=validate ;;
  esac
fi
label="${label:-$stage-$adapter}"
run_dir="$out/$label-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$run_dir"
copy="$run_dir/wt"
printf '{"label": "%s", "stage": "%s", "issue": %s, "adapter": "%s", "profile": "%s", "started": "%s"}\n' \
  "$label" "$stage" "$issue" "$adapter" "$profile" "$(date -u +%FT%TZ)" > "$run_dir/meta.json"

# 1. A standalone copy of the working state: clone the source's repository at
#    its HEAD, then lay the working tree (tracked edits and untracked pipeline
#    files alike) over it. node_modules is left out, as a fresh pipeline
#    worktree has none.
head_sha="$(git -C "$worktree" rev-parse HEAD)"
src_git="$(git -C "$worktree" rev-parse --path-format=absolute --git-common-dir)"
git clone --quiet --local --no-checkout "$src_git" "$copy"
git -C "$copy" checkout --quiet "$head_sha"
branch="$(git -C "$worktree" rev-parse --abbrev-ref HEAD)"
[ "$branch" != "HEAD" ] && git -C "$copy" checkout --quiet -B "$branch"
git -C "$copy" remote set-url --push origin "replay-push-disabled://$label"
rsync -a --exclude .git --exclude node_modules "$worktree"/ "$copy"/

# 2. Write guards: wrappers for nightgauge and gh that skip forge and board
#    writes (logging them to blocked.log) and pass everything else through.
shims="$run_dir/shims"
mkdir -p "$shims"
real_gh="$(command -v gh || true)"
cat > "$shims/nightgauge" <<EOF
#!/usr/bin/env bash
case " \$* " in
  *" project move-status "*|*" project sync-status "*|*" project set-field "*|*" project add "*|\
*" project set-hours "*|*" project set-estimate "*|*" project sync-iteration "*|*" project reconcile "*|\
*" board move"*|*" board set"*|*" board update"*|*" board add"*|\
*" issue close"*|*" issue edit"*|*" issue comment"*|*" issue create"*|\
*" pr create"*|*" pr merge"*|*" pr comment"*|*" pr edit"*|*" pr close"*|*" pr-stage "*|\
*" label create"*|*" label rename"*|*" label delete"*|*" label ensure"*)
    printf '%s\tnightgauge %s\n' "\$(date -u +%FT%TZ)" "\$*" >> "$run_dir/blocked.log"
    echo "[replay] skipped a write: nightgauge \$*" >&2
    exit 0 ;;
esac
exec "$ng" "\$@"
EOF
cat > "$shims/gh" <<EOF
#!/usr/bin/env bash
write=0
case " \$* " in
  *" issue close"*|*" issue edit"*|*" issue comment"*|*" issue create"*|*" issue reopen"*|*" issue delete"*|\
*" pr create"*|*" pr merge"*|*" pr comment"*|*" pr edit"*|*" pr close"*|*" pr review"*|*" pr ready"*|\
*" project item-"*|*" project field-create"*|*" label create"*|*" label edit"*|*" label delete"*|\
*" release "*|*" workflow run"*|*" run rerun"*|*" run cancel"*|*" repo edit"*) write=1 ;;
  *" api "*)
    case " \$* " in
      *" -X GET "*|*" --method GET "*) ;;
      *" -X "*|*" --method "*|*" -f "*|*" -F "*|*" --field "*|*" --raw-field "*|*" --input "*) write=1 ;;
    esac ;;
esac
if [ "\$write" = 1 ]; then
  printf '%s\tgh %s\n' "\$(date -u +%FT%TZ)" "\$*" >> "$run_dir/blocked.log"
  echo "[replay] skipped a write: gh \$*" >&2
  exit 0
fi
exec "$real_gh" "\$@"
EOF
chmod +x "$shims/nightgauge" "$shims/gh"

# 3. The exact stage prompt the scheduler would send.
roots=()
[ -n "$skills_root" ] && roots=(--skills-root "$skills_root")
ctx_file=""
[ -n "$ctx_type" ] && ctx_file="$copy/.nightgauge/pipeline/$ctx_type-$issue.json"
render_args=(skill render --stage "$stage" --profile "$profile" --issue "$issue" ${roots[@]+"${roots[@]}"})
[ "$profile" = compact ] && render_args+=(--supply-includes)
[ -n "$ctx_type" ] && render_args+=(--context-type "$ctx_type" --context-file "$ctx_file")
[ -n "$model" ] && render_args+=(--model "$model")
[ "$adapter" = claude ] && render_args+=(--adapter claude)
"$ng" "${render_args[@]}" > "$run_dir/prompt.md"

# 4. Spawn the stage the way the Go adapter does, prompt on stdin, with the
#    write guards first on PATH and as NIGHTGAUGE_BIN (skills resolve it first).
start=$(date +%s)
set +e
if [ "$adapter" = opencode ]; then
  cfg_args=(opencode config --stage "$stage" --worktree "$copy" --json)
  [ -n "$model" ] && cfg_args+=(--model "$model")
  # opencode config's --skills-root names the directory ABOVE skills/
  # (skillrender.DefaultRoots' convention); skill render's names skills/ itself.
  [ -n "$skills_root" ] && cfg_args+=(--skills-root "$(dirname "$skills_root")")
  [ "$max_turns" -gt 0 ] && cfg_args+=(--max-turns "$max_turns")
  "$ng" "${cfg_args[@]}" > "$run_dir/opencode-config.json" || exit $?
  spawn_env="$(python3 - "$run_dir/opencode-config.json" <<'PY'
import json, os, shlex, sys
c = json.load(open(sys.argv[1]))
wh = c.get("env_withhold") or {}
prefixes, names = wh.get("prefixes", []), set(wh.get("names", []))
unset = [k for k in os.environ if k in names or any(k.startswith(p) for p in prefixes)]
print("unset " + " ".join(shlex.quote(k) for k in unset) if unset else ":")
for k, v in (c.get("env") or {}).items():
    print(f"export {k}={shlex.quote(v)}")
print(f"OC_BIN={shlex.quote(c['binary'])}")
PY
)"
  model_used="${model:-$(python3 -c "import json,sys;print(json.loads(json.load(open(sys.argv[1]))['config_content']).get('model',''))" "$run_dir/opencode-config.json")}"
  (
    eval "$spawn_env"
    export PATH="$shims:$PATH" NIGHTGAUGE_BIN="$shims/nightgauge"
    "$OC_BIN" run --format json --print-logs --log-level ERROR -m "$model_used" --dir "$copy" \
      < "$run_dir/prompt.md" > "$run_dir/events.jsonl" 2> "$run_dir/stderr.log"
  )
  code=$?
else
  model_used="$model"
  tools="$("$ng" skill render --stage "$stage" --profile "$profile" --adapter claude --model "$model" ${roots[@]+"${roots[@]}"} --json \
    | python3 -c 'import json,sys;print(",".join(json.load(sys.stdin).get("allowed_tools") or []))')"
  claude_args=(-p --no-session-persistence --output-format stream-json --verbose --model "$model")
  [ -n "$tools" ] && claude_args+=(--allowedTools "$tools")
  if [ "$max_turns" -gt 0 ]; then claude_args+=(--max-turns "$max_turns"); else claude_args+=(--max-turns 200); fi
  (
    cd "$copy"
    export PATH="$shims:$PATH" NIGHTGAUGE_BIN="$shims/nightgauge"
    export NIGHTGAUGE_ISSUE_NUMBER="$issue" NIGHTGAUGE_STAGE="$stage" NIGHTGAUGE_OUTPUT_FORMAT=stream-json \
      NIGHTGAUGE_ADAPTER=claude NIGHTGAUGE_DISPATCH_MODEL="$model"
    [ -n "$ctx_file" ] && export NIGHTGAUGE_CONTEXT_FILE="$ctx_file"
    claude "${claude_args[@]}" < "$run_dir/prompt.md" > "$run_dir/events.jsonl" 2> "$run_dir/stderr.log"
  )
  code=$?
fi
set -e
end=$(date +%s)

# 5. Summary: wall time, turns, tokens, compactions, last phase marker, files
#    changed, writes the guards skipped.
python3 - "$run_dir" "$model_used" "$code" "$((end - start))" <<'PY'
import json, sys
run_dir, model, code, secs = sys.argv[1:5]
meta = json.load(open(f"{run_dir}/meta.json"))
meta.update(model=model, exit=int(code), wall_s=int(secs))
json.dump(meta, open(f"{run_dir}/meta.json", "w"), indent=2)
PY
summarize "$run_dir"
