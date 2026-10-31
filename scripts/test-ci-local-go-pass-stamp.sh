#!/usr/bin/env bash
#
# test-ci-local-go-pass-stamp.sh — when scripts/ci-local-go-test.sh records a
# passing tree (#2700). Runs against a throwaway repository with a stub `go`.
# TestTreeFingerprintMatchesCILocal pins the Go side to the same tree.
#
# Run: bash scripts/test-ci-local-go-pass-stamp.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/ci-local-go-test.sh"
PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/bin" "$TMP/repo"
cat > "$TMP/bin/go" <<'STUB'
#!/usr/bin/env bash
echo '{"Action":"pass","Package":"example"}'
[ -n "${STUB_EDIT:-}" ] && echo edited >> "$STUB_EDIT"
exit "${STUB_EXIT:-0}"
STUB
chmod +x "$TMP/bin/go"
export PATH="$TMP/bin:$PATH"

cd "$TMP/repo" || { echo "HARNESS ERROR: no fixture repository"; exit 1; }
if ! { git init -q && git config user.email t@example.com && git config user.name t &&
  printf 'go-test.json\n.logs/\n' > .gitignore && echo a > tracked.txt &&
  git add -A && git commit -qm init; }; then
  echo "HARNESS ERROR: fixture repository could not be created"
  exit 1
fi

check() { # $1 label, $2 expected "yes"/"no"/tree, $3 record present
  if [ "$2" = "$3" ]; then
    echo "  ✓ $1"
    PASS=$((PASS + 1))
  else
    echo "  ✗ $1 (got: $3, want: $2)"
    FAIL=$((FAIL + 1))
  fi
}
recorded() { [ -f .logs/pass.tree ] && echo yes || echo no; }

mkdir .logs

echo dirty >> tracked.txt
echo new > untracked.txt
bash "$SCRIPT" 20m .logs/pass.tree >/dev/null 2>&1
check "a passing run records the dirty tree, untracked files included" \
  "$(bash "$SCRIPT" --fingerprint)" "$(cat .logs/pass.tree 2>/dev/null)"

STUB_EXIT=1 bash "$SCRIPT" 20m .logs/pass.tree >/dev/null 2>&1
code=$?
check "a failing run exits non-zero" "1" "$code"
check "a failing run clears the record" "no" "$(recorded)"

STUB_EDIT=tracked.txt bash "$SCRIPT" 20m .logs/pass.tree >/dev/null 2>&1
check "an edit during the run records nothing" "no" "$(recorded)"

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
