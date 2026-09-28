#!/usr/bin/env bash
# stage-replay.sh — run ONE pipeline stage against a model, on a private copy
# of an issue's working state, and record how it went.
#
# The feedback loop for tuning a stage on a slow (local) model: instead of a
# full pipeline run (hours), replay just the stage you changed, as often as
# you like, from the same starting state, and compare runs by their summary
# lines. The prompt and OpenCode config are the pipeline's own:
#   nightgauge skill render --issue ...   (execution.BuildPrompt)
#   nightgauge opencode config ...        (the Go adapter's spawn config)
#
# Usage:
#   scripts/stage-replay.sh --worktree DIR --issue N --stage STAGE
#       [--model provider/model] [--context-type TYPE] [--profile compact|full]
#       [--label NAME] [--out DIR] [--nightgauge BIN] [--skills-root DIR]
#       [--max-turns N]   (cap the stage's model turns, e.g. 2 for a smoke test)
#   scripts/stage-replay.sh --summarize RUN_DIR   (re-summarize a finished run)
#
#   --skills-root  the directory holding nightgauge-*/SKILL.md
#   --worktree  the issue's pipeline worktree (or any checkout) to start from;
#               it is only read, never modified
#   --context-type  the stage's input context (planning for feature-dev, dev
#               for feature-validate, validate for pr-create); default by stage
#
# Safety: the copy is a standalone `git clone --local` (its own .git, so no
# commit or checkout touches the source worktree or its branch), with pushes
# to origin disabled. The stage may still write the main checkout's knowledge
# base (knowledge_path is absolute) and call the forge (board/issue reads).
#
# Output: $OUT/<label>-<timestamp>/ holds the copy (wt/), prompt.md, the
# JSON event stream (events.jsonl), stderr.log and summary.json; the summary
# line is also appended to $OUT/results.jsonl.
set -euo pipefail

# summarize RUN_DIR — print summary.json for a finished replay and append it
# to results.jsonl beside the run directory.
summarize() {
  python3 - "$1" <<'PY'
import json, re, subprocess, sys
run_dir = sys.argv[1]
meta = json.load(open(f"{run_dir}/meta.json"))
turns = out_tok = compactions = 0
last_phase = ""
marker = re.compile(r'phase:start name=\\?"([^"\\]+)\\?" index=(\d+) total=(\d+)')
for line in open(f"{run_dir}/events.jsonl", errors="replace"):
    try:
        ev = json.loads(line)
    except ValueError:
        continue
    part = ev.get("part") or {}
    if part.get("type") == "step-finish":
        turns += 1
        out_tok += (part.get("tokens") or {}).get("output", 0) or 0
    if part.get("type") == "compaction" or ev.get("type") == "compaction":
        compactions += 1
    for m in marker.finditer(line):
        last_phase = f"{m.group(2)}/{m.group(3)} {m.group(1)}"
changed = subprocess.run(["git", "-C", f"{run_dir}/wt", "status", "--short"],
                         capture_output=True, text=True).stdout.splitlines()
summary = dict(meta, turns=turns, output_tokens=out_tok, compactions=compactions,
               last_phase=last_phase, changed_files=len([c for c in changed if c.strip()]),
               run_dir=run_dir)
json.dump(summary, open(f"{run_dir}/summary.json", "w"), indent=2)
with open(f"{run_dir}/../results.jsonl", "a") as f:
    f.write(json.dumps(summary) + "\n")
print(json.dumps(summary, indent=2))
PY
}

worktree="" issue="" stage="" model="" ctx_type="" profile="compact" label="" out="/tmp/stage-replay"
ng="$(command -v nightgauge || true)" skills_root="" max_turns=0
while [ $# -gt 0 ]; do
  case "$1" in
    --worktree) worktree="$2"; shift 2 ;;
    --issue) issue="$2"; shift 2 ;;
    --stage) stage="$2"; shift 2 ;;
    --model) model="$2"; shift 2 ;;
    --context-type) ctx_type="$2"; shift 2 ;;
    --profile) profile="$2"; shift 2 ;;
    --label) label="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --nightgauge) ng="$2"; shift 2 ;;
    --skills-root) skills_root="$2"; shift 2 ;;
    --max-turns) max_turns="$2"; shift 2 ;;
    --summarize) summarize "$2"; exit 0 ;;
    -h|--help) sed -n '2,34p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$worktree" ] && [ -n "$issue" ] && [ -n "$stage" ] || { echo "--worktree, --issue and --stage are required" >&2; exit 2; }
[ -x "$ng" ] || { echo "nightgauge binary not found (use --nightgauge)" >&2; exit 2; }
worktree="$(cd "$worktree" && pwd)"
if [ -z "$ctx_type" ]; then
  case "$stage" in
    feature-planning) ctx_type=issue ;;
    feature-dev) ctx_type=planning ;;
    feature-validate) ctx_type=dev ;;
    pr-create) ctx_type=validate ;;
  esac
fi
label="${label:-$stage}"
run_dir="$out/$label-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$run_dir"
copy="$run_dir/wt"
printf '{"label": "%s", "stage": "%s", "issue": %s, "started": "%s"}\n' "$label" "$stage" "$issue" "$(date -u +%FT%TZ)" > "$run_dir/meta.json"

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

# 2. The exact stage prompt the scheduler would send.
roots=()
[ -n "$skills_root" ] && roots=(--skills-root "$skills_root")
ctx_file=""
[ -n "$ctx_type" ] && ctx_file="$copy/.nightgauge/pipeline/$ctx_type-$issue.json"
render_args=(skill render --stage "$stage" --profile "$profile" --issue "$issue" ${roots[@]+"${roots[@]}"})
[ "$profile" = compact ] && render_args+=(--supply-includes)
[ -n "$ctx_type" ] && render_args+=(--context-type "$ctx_type" --context-file "$ctx_file")
[ -n "$model" ] && render_args+=(--model "$model")
"$ng" "${render_args[@]}" > "$run_dir/prompt.md"

# 3. The Go adapter's spawn config for this stage and copy.
cfg_args=(opencode config --stage "$stage" --worktree "$copy" --json)
[ -n "$model" ] && cfg_args+=(--model "$model")
# opencode config's --skills-root names the directory ABOVE skills/
# (skillrender.DefaultRoots' convention); skill render's names skills/ itself.
[ -n "$skills_root" ] && cfg_args+=(--skills-root "$(dirname "$skills_root")")
[ "$max_turns" -gt 0 ] && cfg_args+=(--max-turns "$max_turns")
"$ng" "${cfg_args[@]}" > "$run_dir/opencode-config.json"

# 4. Spawn opencode as the adapter does: withhold, then set, the environment;
#    prompt on stdin.
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
print(f"OC_RUN_ID={shlex.quote(c.get('run_id', ''))}")
PY
)"
model_used="${model:-$(python3 -c "import json,sys;print(json.loads(json.load(open(sys.argv[1]))['config_content']).get('model',''))" "$run_dir/opencode-config.json")}"
start=$(date +%s)
set +e
(
  eval "$spawn_env"
  "$OC_BIN" run --format json --print-logs --log-level ERROR -m "$model_used" --dir "$copy" \
    < "$run_dir/prompt.md" > "$run_dir/events.jsonl" 2> "$run_dir/stderr.log"
)
code=$?
set -e
end=$(date +%s)

# 5. Summary: wall time, turns, tokens, compactions, last phase marker, files
#    changed. `--summarize RUN_DIR` recomputes it for a finished run.
python3 - "$run_dir" "$model_used" "$code" "$((end - start))" <<'PY'
import json, sys
run_dir, model, code, secs = sys.argv[1:5]
meta = json.load(open(f"{run_dir}/meta.json"))
meta.update(model=model, exit=int(code), wall_s=int(secs))
json.dump(meta, open(f"{run_dir}/meta.json", "w"), indent=2)
PY
summarize "$run_dir"
