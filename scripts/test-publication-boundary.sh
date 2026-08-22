#!/usr/bin/env bash
# Regression tests for the publication-boundary guard.
#
# The guard's entire value is that it FAILS CLOSED. A guard that passes when it
# cannot tell is worse than no guard: it manufactures confidence.
#
# So these tests do not check that the guard passes on a clean tree (CI proves
# that on every PR anyway, and ci-local.sh runs the guard against the live tree
# one step before this one). They check that it FAILS when it is blinded:
# manifest corrupt, manifest missing, manifest vacuous, decisions still open,
# and a planted private file.
#
# ── Why every case runs in a throwaway worktree (#707) ───────────────────────
#
# Several cases assert `expect_exit 0`: plant a BENIGN fixture, require the
# whole checker to pass, and so prove the guard does not cry wolf. Those cases
# are only meaningful when the fixture is the ONLY thing the checker can see
# that it did not see a moment ago.
#
# That held for free until the `issue_references` rule landed (#701). Two of the
# guard's rule families read the WORKING TREE rather than a commit:
#
#   * `issue_references` is diff-scoped by design — it reads the lines this tree
#     ADDS over a base commit. That is the correct shape for the rule, and it
#     makes every `expect_exit 0` case a function of whatever is uncommitted at
#     that instant.
#   * the content and hashed-token rules read each tracked path off disk, so an
#     in-flight edit to an already-tracked file is in scope too.
#
# The failure that produces is the expensive kind: an unrelated case ("benign
# token does not trip the denylist") goes red and names the wrong rule, so the
# reader debugs the denylist. It cost real time during #553.
#
# The fix is to give the suite a fixed corpus instead of the developer's desk: a
# detached worktree at HEAD, in a temp dir, with the working copy's manifest
# copied in. Then
#
#   * tracked paths and file contents are HEAD's, not the working tree's;
#   * NG_BOUNDARY_DIFF_BASE=HEAD makes the planted fixture the entire diff;
#   * `git add -f` stages into the SANDBOX index, so an interrupted run can no
#     longer leave planted fixtures staged in the real checkout.
#
# No rule is disabled or stubbed anywhere: all ten issue-reference cases still
# run the real rule against a real diff — the diff is just the fixture and
# nothing else. What the suite gives up is noticing a violation the developer
# has not committed yet, which was never this suite's job: the live-tree
# invocation of the guard (CI, and ci-local.sh step 5) covers that, and it names
# the offending rule correctly when it fires.
#
# The guard is still exercised as the WORKING COPY defines it — the checker is
# invoked from its working-copy path and the working-copy manifest is copied in
# — so an uncommitted change to either is under test, which is the whole reason
# to run this before committing.
#
# Run: bash scripts/test-publication-boundary.sh

set -uo pipefail
REPO="$(git rev-parse --show-toplevel)"
cd "$REPO"

# Absolute, because the checker is invoked from the working copy but WITH THE
# SANDBOX AS ITS CWD: every path it resolves (manifest, tracked paths, git
# history) is relative to the process's directory, not to the script's.
CHECK="$REPO/scripts/publication-boundary-check.py"
MANIFEST_SRC="$REPO/.github/publication-boundary.yaml"
MANIFEST=".github/publication-boundary.yaml" # relative — inside the sandbox

# The unresolvable-issue-reference rule (#673) scans the lines this working tree
# ADDS over a base commit. The sandbox is checked out AT HEAD, so pinning the
# base to HEAD makes every planted violation exactly the diff — whatever branch
# the suite runs on, and whatever is uncommitted next door.
export NG_BOUNDARY_DIFF_BASE="HEAD"

# ── Where sandboxes live, and why it is a fixed root (#722) ──────────────────
#
# `trap` cannot catch SIGKILL, and a harness timeout or an OOM kill is exactly
# how a long suite run dies. When cleanup never runs the leak is PERMANENT: the
# sandbox directory survives, and `git worktree prune` only removes entries
# whose directory is GONE -- so the surviving directory is precisely what makes
# the registration unprunable. Stale entries then accumulate in the real
# repository, which is also where this project's own worktree-reclamation and
# active-worktree scanners look.
#
# Prune structurally cannot fix that, so the only reliable moment is the START
# of the NEXT run. Sandboxes therefore live under one known root with one known
# prefix, and each records the PID that owns it.
SANDBOX_ROOT="${TMPDIR:-/tmp}/nightgauge-pubboundary-sandboxes"
SANDBOX_PREFIX="run."

SANDBOX=""
TREE=""
BACKUP=""
PLANTED=""
PASS=0
FAIL=0

cleanup() {
  cd "$REPO" 2>/dev/null || return
  [ -n "$TREE" ] && git worktree remove --force "$TREE" >/dev/null 2>&1
  [ -n "$SANDBOX" ] && rm -rf "$SANDBOX"
  git worktree prune >/dev/null 2>&1
}
# A signalled run must STOP, not resume.
#
# `trap cleanup INT TERM` alone is a trap in both senses: bash runs the handler
# and then RESUMES the script at the point it was interrupted. cleanup() has by
# then `cd`-ed back to the real repository and deleted the sandbox -- and
# MANIFEST is a RELATIVE path. So every remaining arm writes its fixture into
# the operator's own checkout, and the arm that plants a vacuous manifest
# overwrites the tracked .github/publication-boundary.yaml outright.
#
# Observed exactly that way: a suite run killed by a harness timeout left the
# real manifest replaced by `allow: [ path: "**" ]`, 623 lines deleted. That is
# the #713 failure class -- the suite dirtying the repository it tests --
# surviving the sandbox work in a shape the SIGKILL-only test could not see,
# because SIGKILL runs no handler at all and therefore never resumes anything.
#
# `trap - EXIT` first so cleanup does not run twice, then exit with the
# conventional 128+signal.
trap cleanup EXIT
trap 'trap - EXIT; cleanup; exit 130' INT
trap 'trap - EXIT; cleanup; exit 143' TERM

# Sweep sandboxes abandoned by an earlier run. Scoped to this suite's own root
# and prefix, so an unrelated worktree is never a candidate (and the byte-level
# `git worktree list` assertion in test-publication-boundary-hermeticity.sh
# holds it to that).
#
# A sandbox is abandoned when the process that created it is gone. `kill -0`
# treats another user's live process as alive, which is the conservative
# direction. PID reuse could make a dead run look alive; the age fallback
# bounds that, since no run of this suite lasts an hour.
sweep_abandoned_sandboxes() {
  local swept=0 d pid
  [ -d "$SANDBOX_ROOT" ] || return 0
  for d in "$SANDBOX_ROOT/$SANDBOX_PREFIX"*; do
    [ -d "$d" ] || continue
    # Physical path: on macOS $TMPDIR is a symlink (/var -> /private/var) and
    # `git worktree` records the resolved form.
    d="$(cd "$d" && pwd -P)" || continue
    pid=""
    [ -f "$d/owner.pid" ] && pid="$(cat "$d/owner.pid" 2>/dev/null)"
    # Only a plausible PID gets liveness credit. `kill -0 0` signals the CURRENT
    # PROCESS GROUP and therefore SUCCEEDS, so an absent, empty, malformed or
    # zero owner.pid would otherwise read as "a concurrent run owns this" and
    # the sandbox would never be reclaimed. Anything unparseable means the run
    # died before it could claim ownership: reclaim it.
    case "$pid" in
    "" | *[!0-9]* | 0) pid="" ;;
    esac
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null &&
      [ -z "$(find "$d" -maxdepth 0 -mmin +60 2>/dev/null)" ]; then
      continue # a concurrent run owns it
    fi
    # Unlock BEFORE removing. A SIGKILL landing inside `git worktree add`
    # leaves the entry marked `locked initializing`, and a locked worktree is
    # skipped by `prune` and refused by a single `--force`. Observed in CI:
    # the directory went, the registration stayed.
    #
    # This is where a single `--force` is genuinely not enough, and it does not
    # contradict the note on #722 -- that note is about `cleanup()`, whose
    # sandbox is unclean but never locked because the run got far enough to
    # finish creating it. The sweep exists precisely for the runs that did not.
    git worktree unlock "$d/tree" >/dev/null 2>&1
    git worktree remove --force --force "$d/tree" >/dev/null 2>&1
    rm -rf "$d"
    swept=$((swept + 1))
  done
  if [ "$swept" -gt 0 ]; then
    git worktree prune >/dev/null 2>&1
    printf 'swept %s abandoned sandbox(es) from a previously killed run\n' "$swept"
  fi
}
sweep_abandoned_sandboxes

mkdir -p "$SANDBOX_ROOT" || exit 2
SANDBOX="$(mktemp -d "$SANDBOX_ROOT/${SANDBOX_PREFIX}XXXXXXXX")" || exit 2
# Written before the worktree is registered, so a kill between the two still
# leaves the next run something to reclaim by.
printf '%s\n' "$$" > "$SANDBOX/owner.pid"
TREE="$SANDBOX/tree"
if ! git worktree add --detach --quiet "$TREE" HEAD >/dev/null 2>&1; then
  printf '\033[31msetup: cannot create the sandbox worktree at HEAD.\033[0m\n' >&2
  printf 'Refusing to fall back to the live checkout — the results would depend on\n' >&2
  printf 'uncommitted work and would name the wrong rule when they failed.\n' >&2
  exit 2
fi
cd "$TREE" || exit 2
cp "$MANIFEST_SRC" "$MANIFEST"
BACKUP="$SANDBOX/manifest.bak"
cp "$MANIFEST" "$BACKUP"

expect_exit() {
  local want="$1" desc="$2"
  python3 "$CHECK" >/dev/null 2>&1
  local got=$?
  if [ "$got" = "$want" ]; then
    printf '  \033[32m✓\033[0m %s (exit %s)\n' "$desc" "$got"
    PASS=$((PASS + 1))
  else
    printf '  \033[31m✗\033[0m %s — wanted exit %s, got %s\n' "$desc" "$want" "$got"
    FAIL=$((FAIL + 1))
  fi
}

expect_exit_with_base() {
  local base="$1" want="$2" desc="$3"
  NG_BOUNDARY_DIFF_BASE="$base" python3 "$CHECK" >/dev/null 2>&1
  local got=$?
  if [ "$got" = "$want" ]; then
    printf '  \033[32m✓\033[0m %s (exit %s)\n' "$desc" "$got"
    PASS=$((PASS + 1))
  else
    printf '  \033[31m✗\033[0m %s — wanted exit %s, got %s\n' "$desc" "$want" "$got"
    FAIL=$((FAIL + 1))
  fi
}

echo "publication-boundary guard — fail-closed tests"
printf 'sandbox: detached worktree at %s + the working-copy manifest\n' "$(git rev-parse --short HEAD)"
echo ""

# ── Precondition: the empty sandbox must be clean ────────────────────────────
# Not a test case — the control that keeps every `expect_exit 0` case honest. If
# the guard already rejects HEAD's tree under this manifest, all of those cases
# go red and each blames its own fixture. Say the real cause once, print what the
# checker actually said, and stop rather than emit a page of wrong diagnoses.
if ! BASELINE="$(python3 "$CHECK" 2>&1)"; then
  printf '\033[31msetup: the guard does not pass on a pristine HEAD worktree with this manifest.\033[0m\n' >&2
  printf 'Every "no false positives" case below would fail and blame its own fixture, so\n' >&2
  printf 'the suite stops here. This is a finding about the guard or the manifest — not\n' >&2
  printf 'about any fixture, and not about uncommitted work (the sandbox has none):\n\n' >&2
  printf '%s\n' "$BASELINE" >&2
  exit 2
fi

# ── The guard must FAIL when it cannot see ──────────────────────────────────
printf 'allow: [\n  - path: "unclosed\n' > "$MANIFEST"
expect_exit 2 "malformed manifest fails closed (does not skip)"

rm -f "$MANIFEST"
expect_exit 2 "missing manifest fails closed (does not skip)"

printf 'version: 1\nallow: []\n' > "$MANIFEST"
expect_exit 2 "vacuous manifest (no allow rules) fails closed"

# ── The guard must FAIL on open decisions ───────────────────────────────────
cp "$BACKUP" "$MANIFEST"
cat >> "$MANIFEST" <<'YAML'
needs_decision:
  - path: "docs/undecided.md"
    rationale: "test fixture — must fail the build"
YAML
expect_exit 1 "non-empty needs_decision fails the build (work-list, not parking lot)"

# ── The guard must FAIL on a planted private file ───────────────────────────
# This is the test that matters: a guard which has never rejected anything has
# not been tested. Plant a file in a DENIED path and assert it is caught.
cp "$BACKUP" "$MANIFEST"
mkdir -p docs/strategy
PLANTED="docs/strategy/PLANTED_SECRET_TEST.md"
echo "internal positioning content that must never ship" > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "planted file in a DENIED path (docs/strategy/) is rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
rmdir docs/strategy 2>/dev/null
PLANTED=""

# ── The guard must FAIL on an unclassified path ─────────────────────────────
# The fail-closed core: a brand-new top-level area nobody thought to name.
PLANTED="unclassified-new-area.txt"
echo "nobody classified this" > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "unclassified path is rejected by default (allowlist, not denylist)"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
PLANTED=""

# ── Untracked files are in scope (#716) ─────────────────────────────────────
# The guard read `git ls-files` alone, so a file the author had written but not
# yet staged was invisible to it. That is the newest content in any change, and
# the pre-push gate reported an unqualified "clean" about material it never
# opened -- then CI failed on exactly that material.
#
# Every arm below plants its fixture WITHOUT `git add`. Each one exits 0 against
# the pre-#716 guard, which is what makes them a regression test rather than a
# restatement.
PLANTED="untracked-unclassified-probe.txt"
echo "nobody classified this, and nobody staged it either" > "$PLANTED"
expect_exit 1 "an UNTRACKED file in an unclassified path is rejected (#716)"
rm -f "$PLANTED"
PLANTED=""

mkdir -p docs/spikes
PLANTED="docs/spikes/_untracked_probe.md"
echo "unreviewed company research" > "$PLANTED"
expect_exit 1 "an UNTRACKED file in a DENIED path is rejected (#716)"
rm -f "$PLANTED"
PLANTED=""

PLANTED="docs/_untracked_cogs_probe.md"
echo "Voice minutes have real COGS to meter." > "$PLANTED"
expect_exit 1 "forbidden content in an UNTRACKED file is rejected (#716)"
rm -f "$PLANTED"
PLANTED=""

# The issue-reference rule is doubly blind to untracked content: the file is
# neither tracked nor part of any diff. Its lines must still count as added.
PLANTED="docs/_untracked_issue_ref_probe.md"
printf 'Superseded by the work in #4072.\n' > "$PLANTED"
expect_exit 1 "an unresolvable reference in an UNTRACKED file is rejected (#716)"
rm -f "$PLANTED"
PLANTED=""

# ...and the bound that keeps the widened scope usable: .gitignore still wins.
# docs/strategy/ is both gitignored AND a deny rule, so if the exclusion ever
# broke, this arm goes red on the deny rule and says so.
mkdir -p docs/strategy
PLANTED="docs/strategy/_ignored_probe.md"
echo "internal positioning content that must never ship" > "$PLANTED"
expect_exit 0 "a GITIGNORED untracked file stays out of scope (build output is not source)"
rm -f "$PLANTED"
rmdir docs/strategy 2>/dev/null
PLANTED=""

# Generated company artifacts are private unless explicitly reviewed.
mkdir -p docs/spikes
PLANTED="docs/spikes/9999-private-research.md"
echo "unreviewed company research" > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "unreviewed docs/spikes artifact is rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
PLANTED=""

mkdir -p skills/example/.claude/agent-memory
PLANTED="skills/example/.claude/agent-memory/MEMORY.md"
echo "ephemeral agent memory" > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "tracked agent memory is rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
rmdir skills/example/.claude/agent-memory skills/example/.claude skills/example 2>/dev/null
PLANTED=""

# ── The hashed token denylist must actually fire ────────────────────────────
# The portfolio identifiers are stored as salted hashes, not plaintext, because
# this manifest is published and a denylist that names what it forbids leaks it.
# Enforcement must be identical to a plaintext rule — so prove it. The probe
# values are read from an env var so THIS FILE does not name them either.
#
# ── The positive case is SYNTHETIC, so it never needs a real secret (#723) ──
#
# This case used to run only when NG_BOUNDARY_PROBE_TOKENS was set. Nothing in
# the repository ever set it -- not this suite, not ci-local.sh, not
# publication-boundary.yml -- so the only rule whose plaintext CANNOT be
# committed here was the one rule whose firing was verified nowhere. It printed
# a yellow dash and the suite reported success, which reads as "ran and passed".
# Deleting the rule outright would have left the suite green.
#
# The fix removes the dependency rather than documenting it: hash a token this
# test invents, inject that hash into the SANDBOX manifest (already copied and
# mutated per run, so the tracked manifest is untouched), and require the
# checker to reject a file containing it.
cp "$BACKUP" "$MANIFEST"

# No hyphens or dots: `_stem_prefixes` splits on those, and a single-candidate
# token keeps the assertion about the whole-token hash and nothing else.
SYNTHETIC_TOKEN="ngsyntheticdenylistprobe$$"
if ! python3 - "$MANIFEST" "$SYNTHETIC_TOKEN" <<'PYEOF'; then
import hashlib, sys, yaml

manifest, token = sys.argv[1], sys.argv[2]
text = open(manifest).read()
salt = ((yaml.safe_load(text) or {}).get("forbidden_tokens") or {}).get("salt")
if not isinstance(salt, str) or not salt:
    sys.exit(1)
digest = hashlib.sha256((salt + token.lower()).encode()).hexdigest()
# Appended under the existing `hashes:` key so the rule under test is the real
# one, configured the real way.
lines = text.splitlines(keepends=True)
for i, line in enumerate(lines):
    if line.rstrip() == "  hashes:":
        lines.insert(i + 1, f'    - "{digest}"\n')
        break
else:
    sys.exit(1)
open(manifest, "w").write("".join(lines))
PYEOF
  printf '  \033[31m✗\033[0m could not inject a synthetic hash into the sandbox manifest\n'
  FAIL=$((FAIL + 1))
else
  PLANTED="docs/_synthetic_token_probe.md"
  printf 'contact: %s\n' "$SYNTHETIC_TOKEN" > "$PLANTED"
  git add -f "$PLANTED" 2>/dev/null
  expect_exit 1 "hashed denylist rejects a synthetic token added to the manifest (#723)"
  git rm --cached -q "$PLANTED" 2>/dev/null
  rm -f "$PLANTED"
  PLANTED=""

  # The same manifest must NOT reject a near-miss: proves the arm above fired on
  # the hash and not merely on the presence of an extra manifest entry.
  PLANTED="docs/_synthetic_token_nearmiss_probe.md"
  printf 'contact: %sx\n' "$SYNTHETIC_TOKEN" > "$PLANTED"
  git add -f "$PLANTED" 2>/dev/null
  expect_exit 0 "a one-character-off synthetic token does NOT trip the denylist (#723)"
  git rm --cached -q "$PLANTED" 2>/dev/null
  rm -f "$PLANTED"
  PLANTED=""
fi

# NG_BOUNDARY_PROBE_TOKENS remains available for probing REAL tokens, but it is
# now purely additive: its absence no longer skips any assertion.
cp "$BACKUP" "$MANIFEST"
if [ -n "${NG_BOUNDARY_PROBE_TOKENS:-}" ]; then
  for tok in ${NG_BOUNDARY_PROBE_TOKENS}; do
    PLANTED="docs/_token_probe.md"
    printf 'contact: %s\n' "$tok" > "$PLANTED"
    git add -f "$PLANTED" 2>/dev/null
    expect_exit 1 "hashed denylist rejects a planted private identifier"
    git rm --cached -q "$PLANTED" 2>/dev/null
    rm -f "$PLANTED"
    PLANTED=""
  done
fi

# A benign token must NOT trip it — a guard that cries wolf gets disabled.
PLANTED="docs/_token_probe.md"
echo "contact: nightgauge" > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 0 "benign token does not trip the denylist (no false positives)"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
PLANTED=""

# ── Content rules must fire on commercial economics ─────────────────────────
# These assert that company unit-economics content is rejected even in an
# otherwise public documentation path, without rejecting ordinary cost prose.
cp "$BACKUP" "$MANIFEST"
PLANTED="docs/_cogs_probe.md"
echo "Voice minutes have real COGS to meter." > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "COGS in a docs/ file is rejected (content rule, not path)"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"

PLANTED="docs/_pricing_probe.md"
printf 'All-in cost is $0.02-0.07/min, 2-5x cheaper than native.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "per-minute pricing in a docs/ file is rejected (content rule)"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"

PLANTED="docs/_private_issue_probe.md"
printf 'See nightgauge/nightgauge-platform#1180 for the internal dependency.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "private-repository issue references are rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"

PLANTED="docs/_pricing_clean_probe.md"
printf 'Voice is metered by tier; funding was a $100M Series C. See @see acme/platform/src/x.ts and acme/platform#1180.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 0 "capability prose + generic cross-repo refs do NOT trip the content rules (no false positives)"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
PLANTED=""

# ── Unresolvable issue references (#673) ────────────────────────────────────
# The rule is "no NEWLY INTRODUCED reference to a number this repository has
# never issued". A rule asserted in prose is not a rule, so every case below
# plants a real line and reads the exit code.
cp "$BACKUP" "$MANIFEST"

PLANTED="docs/_issue_ref_probe.md"
printf 'Superseded by the work in #4072; see also #3605.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "a newly introduced unresolvable reference (#4072) is rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"

# The same number written as this repository's own qualified form still cannot
# resolve, so it is still rejected.
PLANTED="docs/_issue_ref_qualified_probe.md"
printf 'The earlier attempt is nightgauge/nightgauge#4500.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "self-qualified nightgauge/nightgauge#4500 is rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"

# ── ...and the false positives that would make it unusable ──────────────────
# A guard that cries wolf gets disabled, so each exclusion is proven, not
# assumed. These are the classes measured on the real tree in #673.
PLANTED="docs/_issue_ref_inrange_probe.md"
printf 'Fixed in #673, following #140 and #1; the slack window covers #700.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 0 "in-range self-references (#1, #140, #673, #700) do not trip the rule"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"

PLANTED="docs/_issue_ref_hex_probe.md"
cat > "$PLANTED" <<'MD'
.badge { color: #5865f2; background: #252526; border-color: #454545; }
.dark  { color: #000000; accent: #0000ff; shadow: #101830; }
<div class="icon">&#8635;</div><div class="icon">&#128269;</div>
MD
git add -f "$PLANTED" 2>/dev/null
expect_exit 0 "hex colours and HTML numeric entities do not trip the rule"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"

PLANTED="docs/_issue_ref_external_probe.md"
printf 'Blocked upstream by microsoft/vscode#322741 and google-gemini/gemini-cli#17081.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 0 "live external owner/repo#N citations do not trip the rule"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
PLANTED=""

# Synthetic fixtures are arbitrary identifiers, not citations. ~2,977 of them
# live above the mark today and every one of them is legitimate.
mkdir -p packages/nightgauge-vscode/tests/_boundary_probe
PLANTED="packages/nightgauge-vscode/tests/_boundary_probe/issueRef.test.ts"
printf '// #4500 — a synthetic run identifier, not a citation.\nexport const probe = "nightgauge/nightgauge#4501";\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 0 "synthetic identifiers under a tests/ path are exempt"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
rmdir packages/nightgauge-vscode/tests/_boundary_probe 2>/dev/null
PLANTED=""

# ── The rule must fail closed when it cannot trust its own ceiling ──────────
cp "$BACKUP" "$MANIFEST"
sed -i.sedbak 's/^  high_water_mark: [0-9]*$/  high_water_mark: 1/' "$MANIFEST"
rm -f "$MANIFEST.sedbak"
expect_exit 2 "a stale high_water_mark fails closed instead of rejecting real references"

cp "$BACKUP" "$MANIFEST"
printf 'version: 1\nallow:\n  - path: "**"\n    class: PUBLIC\n' > "$MANIFEST"
expect_exit 2 "a manifest with no issue_references block fails closed (checks nothing otherwise)"

cp "$BACKUP" "$MANIFEST"
expect_exit_with_base "no-such-ref-for-tests" 2 "an unresolvable diff base fails closed (never silently re-bases)"

# ── The private-repository rule covers every nightgauge-* companion ─────────
PLANTED="docs/_private_repo_wide_probe.md"
printf 'Tracked internally as nightgauge-internal#42.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "a nightgauge-* companion repository reference is rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
PLANTED=""

# NOTE: there is deliberately no "clean tree passes" case here. CI runs the guard
# against the real tree on every pull request, which proves that continuously and
# for real. Asserting it a second time here would only couple this test to
# whatever the tree happens to look like today. The sandbox precondition above is
# a different claim: it is about HEAD, which CI already validated, and it exists
# so a broken baseline is reported once instead of misattributed 6 times.

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m%s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
printf '\033[32mall %s fail-closed tests passed\033[0m\n' "$PASS"
