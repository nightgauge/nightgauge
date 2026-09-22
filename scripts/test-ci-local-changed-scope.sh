#!/usr/bin/env bash
#
# test-ci-local-changed-scope.sh — the change-scoping contract of
# scripts/ci-local.sh's `--changed` fast path (#1985).
#
# WHY THIS FILE EXISTS. `--changed` can remove the two most expensive steps in
# the gate — `go test ./...` and `go test -race ./...`, ~396s of a 10m31s run
# measured 2026-09-22 on an idle 12-core Apple M-series — from a run. That is a
# reduction in what was asserted, and the entire difference between a fast path
# and a lie is whether the run SAYS SO. #983 is the precedent: steps used to
# vanish from this gate silently and it still exited 0, because a skipped step
# and a passing step were indistinguishable in its output. A scoped gate must
# not reintroduce that by another door, so a skipped step is a THIRD state here
# — the same shape #1983 gave INFRASTRUCTURE ERROR — and never a pass.
#
# The other half is the decision itself, which must be keyed on GENERATOR
# INPUTS rather than on file extensions.
# `packages/nightgauge-vscode/src/services/IpcClient.generated.ts` is rendered
# from Go sources by `cmd/ipc-codegen`; `internal/terminalkind/table.json` is
# embedded into a Go binary; `internal/adaptercompat/manifests/*.json` reaches Go
# through `//go:embed`. For every one of those, "only TS/JSON changed" does NOT
# imply "Go cannot matter", and a scope rule that looked only at `*.go` would
# skip the suites that would have caught it.
#
# Everything is asserted through the REAL code paths in ci-local.sh, in under a
# second, via `--scope-probe` and `--summary-probe` — never by running a second
# full gate. Arms 1-9 run against a throwaway git fixture built to mirror this
# repository's Go shape, because the decision reads the tree (`git ls-files`,
# `git grep //go:embed`, the Makefile) and doctoring the real checkout to change
# that is a crash away from a broken working copy.
#
# Run: bash scripts/test-ci-local-changed-scope.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

CI_LOCAL="$REPO_ROOT/scripts/ci-local.sh"
PASS=0
FAIL=0
TMP=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

check() { # check <description> <0-if-ok>
  if [ "$2" = "0" ]; then
    echo "PASS: $1"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $1"
    FAIL=$((FAIL + 1))
  fi
}

TMP="$(mktemp -d)" || { echo "FAIL: mktemp -d" >&2; exit 1; }

g() { # g <args...> — git in the fixture, with no dependence on user config
  git -C "$FIX" \
    -c user.email=gate@example.invalid -c user.name='Gate Fixture' \
    -c commit.gpgsign=false -c init.defaultBranch=main "$@"
}

# ── The fixture: this repository's Go shape, in miniature ────────────────────
#
# Each file below exists to make one derivation rule observable:
#   internal/ipc/server.go                  a plain Go source
#   go.mod                                  the module file
#   Makefile                                a `go run ./cmd/...` codegen recipe
#   cmd/ipc-codegen/main.go                 the generator, whose flag DEFAULT
#                                           names its TypeScript output
#   packages/vscode/.../IpcClient.generated.ts   that output — TypeScript that
#                                           is produced FROM Go
#   internal/terminalkind/table.json        a non-Go file in a Go package dir
#   internal/adaptercompat/manifests/a.json a `//go:embed manifests/*.json` target
#   packages/app/src/foo.ts                 TypeScript that reaches Go nowhere
FIX="$TMP/fixture"
mkdir -p "$FIX/scripts" "$FIX/internal/ipc" "$FIX/internal/terminalkind" \
  "$FIX/internal/adaptercompat/manifests" "$FIX/cmd/ipc-codegen" \
  "$FIX/packages/vscode/src/services" "$FIX/packages/app/src"

cp "$CI_LOCAL" "$FIX/scripts/ci-local.sh"
printf 'module example.invalid/fixture\n\ngo 1.22\n' > "$FIX/go.mod"
printf 'package ipc\n\nfunc Serve() {}\n' > "$FIX/internal/ipc/server.go"
printf 'package ipc\n\nconst Version = 1\n' > "$FIX/internal/ipc/protocol.go"
cat > "$FIX/internal/terminalkind/table.go" <<'EOF'
package terminalkind

import _ "embed"

//go:embed table.json
var tableJSON []byte
EOF
printf '{"rules":[]}\n' > "$FIX/internal/terminalkind/table.json"
cat > "$FIX/internal/adaptercompat/manifest.go" <<'EOF'
package adaptercompat

import "embed"

//go:embed manifests/*.json
var manifests embed.FS
EOF
printf '{"cli":"x"}\n' > "$FIX/internal/adaptercompat/manifests/a.json"
cat > "$FIX/cmd/ipc-codegen/main.go" <<'EOF'
package main

import "flag"

var (
	serverPath = flag.String("server", "internal/ipc/server.go", "Path to server.go")
	outPath    = flag.String("out", "packages/vscode/src/services/IpcClient.generated.ts", "Output")
)

func main() {}
EOF
printf '// GENERATED FROM GO. DO NOT EDIT.\nexport const x = 1;\n' \
  > "$FIX/packages/vscode/src/services/IpcClient.generated.ts"
printf 'export const foo = 1;\n' > "$FIX/packages/app/src/foo.ts"
cat > "$FIX/Makefile" <<'EOF'
generate-ipc-client:
	go run ./cmd/ipc-codegen \
		--server internal/ipc/server.go \
		--protocol internal/ipc/protocol.go \
		--out packages/vscode/src/services/IpcClient.generated.ts
EOF

g init -q . >/dev/null 2>&1 || { echo "FAIL: git init in fixture" >&2; exit 1; }
g add -A >/dev/null 2>&1
g commit -q -m 'fixture base' >/dev/null 2>&1
g branch -M main >/dev/null 2>&1
# The fixture's own `origin/main`, without a remote: the scope derivation only
# ever reads the ref.
g update-ref refs/remotes/origin/main HEAD >/dev/null 2>&1
g checkout -q -b feature >/dev/null 2>&1

# reset_fixture — back to a clean `feature` identical to origin/main.
reset_fixture() {
  g checkout -q feature >/dev/null 2>&1
  g reset -q --hard refs/remotes/origin/main >/dev/null 2>&1
  g clean -qfd >/dev/null 2>&1
}

# probe <flags...> — the real ci-local.sh's scope decision in the fixture.
probe() {
  ( cd "$FIX" && bash scripts/ci-local.sh "$@" 2>&1 )
}

# touch_and_commit <path> <line>
touch_and_commit() {
  printf '%s\n' "$2" >> "$FIX/$1"
  g add -A >/dev/null 2>&1
  g commit -q -m "change $1" >/dev/null 2>&1
}

decision() { # decision <probe output> -> RUN | SKIP | ?
  case "$1" in
    *"go suites: SKIP"*) printf 'SKIP\n' ;;
    *"go suites: RUN"*) printf 'RUN\n' ;;
    *) printf '?\n' ;;
  esac
}

# Sanity: the fixture is a tree the probe can read at all. Without this, every
# arm below could "pass" for the wrong reason.
out="$(probe --changed --scope-probe)"
case "$(decision "$out")" in
  RUN|SKIP) check "the fixture yields a readable scope decision (baseline)" 0 ;;
  *) check "the fixture yields a readable scope decision (baseline) — got: $out" 1 ;;
esac

# ── (1) a TypeScript-only diff skips both Go suites ─────────────────────────
reset_fixture
touch_and_commit packages/app/src/foo.ts 'export const bar = 2;'
out="$(probe --changed --scope-probe)"
[ "$(decision "$out")" = "SKIP" ]
check "--changed on a TS-only diff decides SKIP for the Go suites" $?
case "$out" in
  *"none of the"*"changed path(s) is a Go source"*)
    check "the SKIP reason says what was checked and found absent" 0 ;;
  *) check "the SKIP reason says what was checked and found absent — got: $out" 1 ;;
esac

# ── (2) THE DEFAULT SCOPES NOTHING ──────────────────────────────────────────
# Same TS-only diff, no `--changed`. This is the arm that holds the non-goal:
# the repository rule is "run the complete local gate once before every push",
# and a scoped DEFAULT would weaken it everywhere while looking identical.
out="$(probe --scope-probe)"
[ "$(decision "$out")" = "RUN" ]
check "WITHOUT --changed the same TS-only diff still decides RUN" $?
case "$out" in
  *"--changed was not given"*) check "the default's reason names the absent flag" 0 ;;
  *) check "the default's reason names the absent flag — got: $out" 1 ;;
esac

# ── (3) one .go file runs both Go suites ────────────────────────────────────
reset_fixture
touch_and_commit internal/ipc/server.go 'func Extra() {}'
out="$(probe --changed --scope-probe)"
[ "$(decision "$out")" = "RUN" ]
check "--changed on a diff touching one .go file decides RUN" $?
case "$out" in
  *"internal/ipc/server.go"*) check "the RUN reason names the Go file that forced it" 0 ;;
  *) check "the RUN reason names the Go file that forced it — got: $out" 1 ;;
esac

# ── (4) go.mod alone runs both Go suites ────────────────────────────────────
reset_fixture
touch_and_commit go.mod '// dependency bump'
[ "$(decision "$(probe --changed --scope-probe)")" = "RUN" ]
check "--changed on a go.mod-only diff decides RUN" $?

# ── (5) IpcClient.generated.ts alone runs both Go suites ────────────────────
# THE CASE THE ISSUE NAMES. Extension-keyed scoping gets this wrong: the file is
# `.ts`, and it is rendered from Go by cmd/ipc-codegen. Deleting the
# codegen-path rule from `decide_go_scope` turns this arm red.
reset_fixture
touch_and_commit packages/vscode/src/services/IpcClient.generated.ts 'export const y = 2;'
out="$(probe --changed --scope-probe)"
[ "$(decision "$out")" = "RUN" ]
check "--changed on a diff touching ONLY the generated TS IPC client decides RUN" $?
case "$out" in
  *"codegen"*) check "the RUN reason says the file is a Go codegen input/output" 0 ;;
  *) check "the RUN reason says the file is a Go codegen input/output — got: $out" 1 ;;
esac

# ── (6) a //go:embed target alone runs both Go suites ───────────────────────
# `internal/adaptercompat/manifests/a.json` is compiled INTO the binary and its
# directory holds no .go file, so neither the extension rule nor the
# package-directory rule sees it.
reset_fixture
touch_and_commit internal/adaptercompat/manifests/a.json '{"cli":"y"}'
out="$(probe --changed --scope-probe)"
[ "$(decision "$out")" = "RUN" ]
check "--changed on a //go:embed target in a dir with no .go file decides RUN" $?
case "$out" in
  *"go:embed"*) check "the RUN reason names //go:embed as the reason" 0 ;;
  *) check "the RUN reason names //go:embed as the reason — got: $out" 1 ;;
esac

# ── (7) a non-Go file inside a Go package directory runs both suites ────────
reset_fixture
touch_and_commit internal/terminalkind/table.json '{"rules":["x"]}'
out="$(probe --changed --scope-probe)"
[ "$(decision "$out")" = "RUN" ]
check "--changed on a non-Go file inside a Go package directory decides RUN" $?

# ── (8) the WORKING TREE counts, not just the committed diff ───────────────
# This gate is run BECAUSE there is uncommitted work. Scoping on
# `origin/main...HEAD` alone would skip the Go suites for an unstaged .go edit —
# a false green with no CI round trip to catch it, because the change is not
# pushed yet.
reset_fixture
touch_and_commit packages/app/src/foo.ts 'export const baz = 3;'
[ "$(decision "$(probe --changed --scope-probe)")" = "SKIP" ]
check "baseline for the working-tree arm: committed TS-only is SKIP" $?
printf 'func Unstaged() {}\n' >> "$FIX/internal/ipc/server.go"
[ "$(decision "$(probe --changed --scope-probe)")" = "RUN" ]
check "an UNCOMMITTED .go edit flips the same branch to RUN" $?
g checkout -q -- internal/ipc/server.go >/dev/null 2>&1
printf 'package brandnew\n' > "$FIX/internal/brandnew.go"
[ "$(decision "$(probe --changed --scope-probe)")" = "RUN" ]
check "an UNTRACKED new .go file flips the same branch to RUN" $?
rm -f "$FIX/internal/brandnew.go"

# ── (9) fail closed: no origin/main means run everything ────────────────────
# Every branch that cannot answer "did a Go input change?" must answer "run
# them". Inverting this is the single worst defect this feature could ship.
reset_fixture
touch_and_commit packages/app/src/foo.ts 'export const qux = 4;'
g update-ref -d refs/remotes/origin/main >/dev/null 2>&1
out="$(probe --changed --scope-probe)"
[ "$(decision "$out")" = "RUN" ]
check "with no origin/main the decision FAILS CLOSED to RUN" $?
case "$out" in
  *"origin/main is not available"*) check "the fail-closed reason names the missing ref" 0 ;;
  *) check "the fail-closed reason names the missing ref — got: $out" 1 ;;
esac
g update-ref refs/remotes/origin/main "$(g rev-parse HEAD~1)" >/dev/null 2>&1

# ── (10) a skipped step is reported as a THIRD state, never as a pass ──────
# `--summary-probe` drives the real run_step / skip_step / print_skip_summary /
# print_final_verdict path with one passing step and one skipped one. Reverting
# the verdict to the unconditional "✓ All CI-parity checks passed." turns the
# last two of these red.
SUMMARY_LOGS="$TMP/summary-logs"
summary_out="$TMP/summary.txt"
CI_LOCAL_LOG_DIR="$SUMMARY_LOGS" bash "$CI_LOCAL" --summary-probe > "$summary_out" 2>&1
summary_rc=$?
[ "$summary_rc" -eq 0 ]
check "a run whose only shortfall is a SKIP still exits 0 (it is a fast path) — got $summary_rc" $?
grep -q 'SKIPPED' "$summary_out"
check "the summary uses the word SKIPPED" $?
grep -qi 'NOT PASSED\|not passes' "$summary_out"
check "the summary says explicitly that a skipped step is NOT a pass" $?
grep -q 'summary probe skipped step' "$summary_out"
check "the summary names the skipped step" $?
grep -qi 'because:' "$summary_out"
check "the summary gives the reason the step was skipped" $?
grep -qi 'PARTIAL gate' "$summary_out"
check "the summary calls the run a PARTIAL gate" $?
! grep -qF '✓ All CI-parity checks passed.' "$summary_out"
check "the verdict is NOT the unqualified 'All CI-parity checks passed.'" $?
grep -qF 'that RAN passed' "$summary_out"
check "the verdict is qualified to the steps that actually ran" $?

# ── (11) a SKIPPED step stays in the step inventory (#983) ─────────────────
# The #983 guard compares `--list-steps` against scripts/ci-local-steps.txt. A
# skipped step must still appear there — otherwise `--changed` would make steps
# disappear from the inventory, which is precisely the defect #983 exists to
# prevent, arriving by a new route.
scoped_list="$TMP/scoped-list.txt"
bash "$CI_LOCAL" --list-steps --changed > "$scoped_list" 2>"$TMP/scoped-list.err"
list_rc=$?
[ "$list_rc" -eq 0 ]
check "\`--list-steps --changed\` exits 0 — got $list_rc" $?
diff -q scripts/ci-local-steps.txt "$scoped_list" >/dev/null 2>&1
check "the scoped inventory is IDENTICAL to the unscoped checked-in inventory" $?
grep -qF 'go test -race -count=1 ./...' "$scoped_list"
check "the race suite is still listed even when it would be skipped" $?
grep -qF 'go test ./... -count=1 (with skip accounting)' "$scoped_list"
check "the plain Go suite is still listed even when it would be skipped" $?

# ── (12) the #983 guard still goes red if a step DISAPPEARS ────────────────
# The distinction this whole file rests on: explicitly skipped is fine, gone is
# not. A doctored copy of ci-local.sh with the race step's `run_group` call
# deleted must produce an inventory that no longer matches the checked-in list.
DOCTORED="$TMP/doctored-ci-local.sh"
python3 - "$CI_LOCAL" "$DOCTORED" <<'PY'
import re, sys
src = open(sys.argv[1]).read()
# Delete the race step's dispatch entirely -- the #983 defect, staged.
pat = re.compile(
    r'\nif \[ "\$GO_SCOPE_RUN" -eq 0 \]; then\n'
    r'  skip_step "go test -race[^\n]*\n'
    r'[^\n]*\n'
    r'else\n'
    r'run_group "go test -race[^\n]*\n'
    r'[^\n]*\n'
    r'fi\n')
out, n = pat.subn('\n', src)
if n != 1:
    sys.stderr.write("doctoring failed: matched %d race-step dispatches\n" % n)
    sys.exit(3)
open(sys.argv[2], 'w').write(out)
PY
doctor_rc=$?
[ "$doctor_rc" -eq 0 ]
check "the doctored copy could be produced (arm 12 is not vacuous)" $?
if [ "$doctor_rc" -eq 0 ]; then
  doctored_list="$TMP/doctored-list.txt"
  bash "$DOCTORED" --list-steps > "$doctored_list" 2>/dev/null
  ! grep -qF 'go test -race -count=1 ./...' "$doctored_list"
  check "a DELETED step really is absent from the doctored inventory" $?
  ! diff -q scripts/ci-local-steps.txt "$doctored_list" >/dev/null 2>&1
  check "the #983 guard's comparison goes RED when a step disappears entirely" $?
fi

# ── (13) the derivation is not vacuous against the REAL tree ──────────────
# Arms 1-9 run in a fixture. If the real repository's Go shape stopped being
# derivable — `git ls-files` returning nothing, the Makefile parse breaking —
# every fixture arm would still pass while the live decision skipped Go for a
# `.go` diff. So assert the live rules here too, against this checkout.
real_probe="$TMP/real-scope.txt"
bash "$CI_LOCAL" --changed --scope-probe > "$real_probe" 2>&1
grep -q 'go suites: \(RUN\|SKIP\)' "$real_probe"
check "the live tree yields a scope decision" $?
real_go_dirs="$(git ls-files -- '*.go' | wc -l | tr -d ' ')"
[ "${real_go_dirs:-0}" -gt 100 ]
check "the live tree has tracked .go files to derive from (${real_go_dirs})" $?
real_embeds="$(git grep -I -c -- '//go:embed' -- '*.go' 2>/dev/null | wc -l | tr -d ' ')"
[ "${real_embeds:-0}" -gt 0 ]
check "the live tree has //go:embed declarations to derive from (${real_embeds} files)" $?
grep -qF 'IpcClient.generated.ts' Makefile
check "the live Makefile still names the generated IPC client (the codegen parse's input)" $?

echo ""
if [ "$FAIL" -gt 0 ]; then
  echo "✗ $PASS passed, $FAIL FAILED"
  exit 1
fi
echo "✓ all $PASS ci-local change-scoping tests passed"
