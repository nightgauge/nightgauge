#!/usr/bin/env bash
# Regression suite for the scope of scripts/credential-scan.sh (#2953).
#
# The scan reads what this branch would publish: HEAD's history plus files
# changed but not yet committed. This suite builds a throwaway repository and
# proves that a key on ANOTHER ref does not fail this branch's scan, that a
# key in an untracked, staged or modified file does, and that a gitignored
# file is never read. Fake keys are built at run time from pieces, so no
# committed file holds a string the scanner matches.
#
# Run: bash scripts/test-credential-scan.sh
# Also run by scripts/ci-local.sh and .github/workflows/credential-scan.yml.
set -uo pipefail
if ! TOPLEVEL="$(git rev-parse --show-toplevel)" || ! cd "$TOPLEVEL"; then
  echo "FAIL: not inside a git work tree; nothing was tested" >&2
  exit 1
fi
if ! command -v gitleaks >/dev/null 2>&1; then
  echo "HARNESS ERROR: gitleaks is not installed, so the scope suite could not run."
  exit 2
fi

PASS=0
FAIL=0
TMP=""
# shellcheck disable=SC2329 # invoked by the trap below
cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT
TMP="$(mktemp -d)"

# Assembled from pieces so this file itself never matches a rule.
PART_A="$(printf '%s%s' AK IA)ZZQWERTYZ7ASDF2X"
PART_B="$(printf '%s' wJalrXUtnFEMI K7MDENG bPxRfiCYzzQWERTY12 | tr -d '\n')"
fake_key() {
  printf 'aws_access_key_id = %s\naws_%s_access_key = %s\n' "$PART_A" "secret" "$PART_B" >"$1"
}

# A fresh repository with the real scanner and config, one clean commit.
REPO=""
fresh_repo() {
  REPO="$TMP/repo-$1"
  mkdir -p "$REPO/scripts"
  cp scripts/credential-scan.sh "$REPO/scripts/"
  [ -f .gitleaks.toml ] && cp .gitleaks.toml "$REPO/"
  printf 'ignored.txt\n' >"$REPO/.gitignore"
  printf 'hello\n' >"$REPO/tracked.txt"
  git -C "$REPO" init -q -b main
  git -C "$REPO" add -A
  git -C "$REPO" -c user.name=t -c user.email=t@example.com commit -q -m init
}

# expect <want-exit> <name>
expect() {
  local got
  (cd "$REPO" && bash scripts/credential-scan.sh >"$TMP/out" 2>&1)
  got=$?
  if [ "$got" -eq 2 ]; then
    echo "HARNESS ERROR: the scan could not run for '$2':"
    cat "$TMP/out"
    exit 2
  fi
  if [ "$got" -eq "$1" ]; then
    PASS=$((PASS + 1))
    echo "  ok   $2"
  else
    FAIL=$((FAIL + 1))
    echo "  FAIL $2 (exit $got, want $1)"
    sed 's/^/       /' "$TMP/out"
  fi
}

echo "credential-scan.sh scope:"

fresh_repo clean
expect 0 "a clean branch passes"

fresh_repo other-ref
git -C "$REPO" checkout -q -b other
fake_key "$REPO/other.txt"
git -C "$REPO" add other.txt
git -C "$REPO" -c user.name=t -c user.email=t@example.com commit -q -m other
git -C "$REPO" update-ref refs/remotes/origin/other HEAD
git -C "$REPO" checkout -q main
expect 0 "a key committed only on another ref does not fail this branch"

fresh_repo untracked
fake_key "$REPO/new.txt"
expect 1 "a key in an untracked file fails"
if [ -n "$(git -C "$REPO" diff --cached --name-only)" ]; then
  FAIL=$((FAIL + 1))
  echo "  FAIL the scan left files staged in the real index"
fi

fresh_repo ignored
fake_key "$REPO/ignored.txt"
expect 0 "a key in a gitignored file is not read"

fresh_repo staged
fake_key "$REPO/staged.txt"
git -C "$REPO" add staged.txt
expect 1 "a key in a staged file fails"

fresh_repo modified
fake_key "$REPO/tracked.txt"
expect 1 "a key in a modified tracked file fails"

fresh_repo history
fake_key "$REPO/old.txt"
git -C "$REPO" add old.txt
git -C "$REPO" -c user.name=t -c user.email=t@example.com commit -q -m add
git -C "$REPO" rm -q old.txt
git -C "$REPO" -c user.name=t -c user.email=t@example.com commit -q -m remove
expect 1 "a key deleted from HEAD but in its history fails"

echo ""
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
