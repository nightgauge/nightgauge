#!/usr/bin/env bash
# test-capture-cli-help.sh — regression suite for scripts/capture-cli-help.sh
# (#1617).
#
# The capture script installs third-party CLIs and runs them. This suite runs
# it with npm, curl and the CLIs replaced by stubs on PATH, and checks the
# mitigations it promises:
#   1. No credential in the caller's environment reaches an install or a CLI.
#   2. Every CLI is run with `--help` only, after its subcommand if it has one.
#   3. The mktemp prefix is removed on exit, success and failure alike.
#   4. A CLI or an install that outlives its timeout is killed, with everything
#      it started, and the script exits non-zero having written nothing.
# It also checks the redaction and the IPv4 refusal, and that an adapter whose
# manifest pins no max_tested is skipped.
#
# Drives the working-tree copy of the script, reading the real manifests, so
# the pinned versions it expects are the ones the manifests hold.
#
# Run: bash scripts/test-capture-cli-help.sh
# Also run by scripts/ci-local.sh.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/capture-cli-help.sh"
MANIFESTS="$REPO_ROOT/internal/adaptercompat/manifests"

PASS=0
FAIL=0
TMP=""
cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

ok() {
  echo "  ✓ $1"
  PASS=$((PASS + 1))
}
bad() {
  echo "  ✗ $1"
  FAIL=$((FAIL + 1))
}
check() { # check <description> <command...>
  local desc="$1"
  shift
  if "$@"; then ok "$desc"; else bad "$desc"; fi
}

TMP="$(mktemp -d)"
TMP="$(cd "$TMP" && pwd -P)"
STUBS="$TMP/stubs"
LOG="$TMP/log"
mkdir -p "$STUBS" "$LOG"

max_tested() {
  python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["max_tested"])' "$MANIFESTS/$1.json"
}
CLAUDE_V="$(max_tested claude-headless)"
CODEX_V="$(max_tested codex)"
GROK_V="$(max_tested grok)"
OPENCODE_V="$(max_tested opencode)"

# The fake CLI every stub install produces. The script clears the environment,
# so everything the stub needs is baked in when it is written: the log
# directory and a mode file the test switches between runs.
#   ok       print help naming the child's HOME, the caller's HOME, the login
#            name, the host name and 127.0.0.1, all of which the script must
#            redact or keep
#   sleep    start a child that sleeps, record both pids, and wait on it
#   address  print help naming 192.0.2.10
write_fake_cli() { # write_fake_cli <path> <name>
  cat >"$1" <<EOF
#!/bin/bash
log="$LOG/cli-$2.\$\$"
printf '%s\n' "\$*" >"\$log.argv"
env >"\$log.env"
case "\$(cat "$TMP/mode")" in
  sleep)
    sleep 30 &
    printf '%s %s\n' "\$\$" "\$!" >"$LOG/sleepers"
    wait
    ;;
  address)
    echo "usage: $2 [options]"
    echo "  --attach <url>  e.g. http://192.0.2.10:4096"
    ;;
  wordcheck)
    echo "usage: $2 [options]"
    echo "the working root of the project"
    echo "listening on http://localhost:4096 by default"
    echo "config lives at /root/.config/$2.toml"
    echo "owner default root@localhost"
    ;;
  escape)
    echo "usage: $2 [options]"
    marker="$LOG/escape-marker"
    rm -f "\$marker"
    # macOS has no setsid(1); a backgrounded perl stands in (#1721). It calls
    # POSIX::setsid itself, leaving this process's process group (and
    # bounded()'s -\$pid kill) while staying this process's direct child, so
    # its ppid chain to bounded()'s tracked pid holds until this script
    # exits; it records its own PID and outlives this script, which returns
    # normally right after.
    perl -e '
      use POSIX ();
      POSIX::setsid();
      open(my \$fh, ">", \$ARGV[0]) or exit 1;
      print \$fh \$\$;
      close \$fh;
      sleep 30;
    ' "\$marker" &
    # Wait for the marker so the escaped descendant reliably exists, and is
    # therefore visible to at least one bounded() scan, before this process
    # (bounded()'s direct child) exits normally.
    for _ in \$(seq 1 50); do
      [ -s "\$marker" ] && break
      sleep 0.1
    done
    # Stay alive a little longer: this process (bounded()'s tracked pid) is
    # the escaped descendant's real parent until it exits, so ending the
    # instant the marker appears would reparent the descendant to init in
    # the same instant, leaving bounded()'s poll no window to see the intact
    # chain — not the leak this test is pinning.
    sleep 1.5
    ;;
  *)
    echo "usage: $2 [options]"
    echo "  --config <path>  default \$HOME/.config/$2.toml"
    echo "  --home <path>    or $HOME/.$2"
    echo "  --owner <name>   default $(id -un)@$(hostname)"
    echo "  --attach <url>   e.g. http://127.0.0.1:4096"
    printf '  --plain        \033[1mbold\033[0m text   \n'
    ;;
esac
EOF
  chmod +x "$1"
}

# npm stub: `npm install --prefix DIR ... <pkg>@<version>` lays out a package
# with that version and a fake bin.
cat >"$STUBS/npm" <<EOF
#!/bin/bash
log="$LOG/npm.\$\$"
printf '%s\n' "\$*" >"\$log.argv"
env >"\$log.env"
if [ "\$(cat "$TMP/mode")" = npm-sleep ]; then
  sleep 30 &
  printf '%s %s\n' "\$\$" "\$!" >"$LOG/sleepers"
  wait
  exit 0
fi
prefix=""; spec=""
while [ "\$#" -gt 0 ]; do
  case "\$1" in
    --prefix) prefix="\$2"; shift ;;
    -*) ;;
    *@*) spec="\$1" ;;
  esac
  shift
done
printf '%s\n' "\$prefix" >>"$LOG/prefixes"
pkg="\${spec%@*}"; version="\${spec##*@}"
case "\$pkg" in
  @anthropic-ai/claude-code) bin=claude ;;
  @openai/codex) bin=codex ;;
  opencode-ai) bin=opencode ;;
  *) echo "stub npm: unexpected package \$pkg" >&2; exit 1 ;;
esac
mkdir -p "\$prefix/node_modules/\$pkg" "\$prefix/node_modules/.bin"
printf '{"name":"%s","version":"%s"}\n' "\$pkg" "\$version" >"\$prefix/node_modules/\$pkg/package.json"
cp "$STUBS/fake-cli-\$bin" "\$prefix/node_modules/.bin/\$bin"
EOF
chmod +x "$STUBS/npm"

# curl stub: writes the fake grok installer to the -o path.
cat >"$STUBS/curl" <<EOF
#!/bin/bash
log="$LOG/curl.\$\$"
printf '%s\n' "\$*" >"\$log.argv"
env >"\$log.env"
out=""
while [ "\$#" -gt 0 ]; do
  case "\$1" in -o) out="\$2"; shift ;; esac
  shift
done
cp "$STUBS/fake-grok-installer" "\$out"
EOF
chmod +x "$STUBS/curl"

cat >"$STUBS/fake-grok-installer" <<EOF
#!/bin/bash
log="$LOG/installer.\$\$"
printf '%s\n' "\$*" >"\$log.argv"
env >"\$log.env"
mkdir -p "\$GROK_BIN_DIR"
cp "$STUBS/fake-cli-grok" "\$GROK_BIN_DIR/grok"
EOF

for name in claude codex opencode grok; do
  write_fake_cli "$STUBS/fake-cli-$name" "$name"
done

# The real sha256 of the stub above, so run_capture's default env satisfies
# capture-cli-help.sh's installer-hash pin (#1721) for every ordinary run;
# the pin test below overrides it with a wrong value instead.
if command -v shasum >/dev/null 2>&1; then
  FAKE_GROK_INSTALLER_SHA256="$(shasum -a 256 "$STUBS/fake-grok-installer" | awk '{print $1}')"
else
  FAKE_GROK_INSTALLER_SHA256="$(sha256sum "$STUBS/fake-grok-installer" | awk '{print $1}')"
fi

# WORDSTUBS holds fake `id`/`hostname` commands so a dedicated run (below,
# #1721) can put the script's own bare-word redaction under a login name and
# a host name it does not control on this machine: "root" and "localhost".
WORDSTUBS="$TMP/wordstubs"
mkdir -p "$WORDSTUBS"
cat >"$WORDSTUBS/id" <<'EOF'
#!/bin/bash
echo root
EOF
cat >"$WORDSTUBS/hostname" <<'EOF'
#!/bin/bash
echo localhost
EOF
chmod +x "$WORDSTUBS/id" "$WORDSTUBS/hostname"

SENTINEL="sentinel-credential-6f1d"
SCRATCH="$TMP/scratch"
OUT="$TMP/out"

# run_capture <mode> [VAR=value ...] -- <adapter...>: run the script with the
# stubs first on PATH, TMPDIR in a scratch directory, every credential set to
# the sentinel, and output to OUT. Sets RC and SECONDS_TAKEN.
run_capture() {
  local mode="$1"
  shift
  local extra=()
  while [ "$1" != "--" ]; do
    extra+=("$1")
    shift
  done
  shift
  printf '%s\n' "$mode" >"$TMP/mode"
  rm -rf "$SCRATCH" "$LOG"/*
  mkdir -p "$SCRATCH"
  local started=$SECONDS
  env PATH="$STUBS:$PATH" TMPDIR="$SCRATCH" CAPTURE_CLI_HELP_OUT="$OUT" \
    ANTHROPIC_API_KEY="$SENTINEL" OPENAI_API_KEY="$SENTINEL" XAI_API_KEY="$SENTINEL" \
    GEMINI_API_KEY="$SENTINEL" GITHUB_TOKEN="$SENTINEL" GH_TOKEN="$SENTINEL" \
    COPILOT_GITHUB_TOKEN="$SENTINEL" NPM_TOKEN="$SENTINEL" GROK_DEPLOYMENT_KEY="$SENTINEL" \
    OPENCODE_AUTH_CONTENT="$SENTINEL" AWS_SECRET_ACCESS_KEY="$SENTINEL" \
    CAPTURE_CLI_HELP_GROK_INSTALLER_SHA256="$FAKE_GROK_INSTALLER_SHA256" \
    ${extra[@]+"${extra[@]}"} \
    bash "$SCRIPT" "$@" >"$TMP/run.out" 2>&1
  RC=$?
  SECONDS_TAKEN=$((SECONDS - started))
}

no_credentials_reached() {
  ! grep -rqE '_API_KEY=|_TOKEN=|_SECRET_|GROK_DEPLOYMENT_KEY=|OPENCODE_AUTH_CONTENT=' "$LOG" &&
    ! grep -rqF "$SENTINEL" "$LOG"
}
scratch_is_empty() {
  [ -z "$(ls -A "$SCRATCH")" ]
}
sleepers_are_dead() {
  local pid
  [ -s "$LOG/sleepers" ] || return 1
  for pid in $(cat "$LOG/sleepers"); do
    if kill -0 "$pid" 2>/dev/null; then
      kill -9 "$pid" 2>/dev/null
      return 1
    fi
  done
}

echo "▶ capture-cli-help.sh"

# --- 1. A clean run over every adapter ---------------------------------------
rm -rf "$OUT"
run_capture ok -- claude-headless codex copilot gemini grok opencode
check "a clean run exits 0" [ "$RC" -eq 0 ]
[ "$RC" -eq 0 ] || sed 's/^/    /' "$TMP/run.out"

check "no credential reached npm, curl, the installer or a CLI" no_credentials_reached

argv_ok=0
for f in "$LOG"/cli-*.argv; do
  name="$(basename "$f")"
  name="${name#cli-}"
  name="${name%%.*}"
  case "$name" in
    codex) want="exec --help" ;;
    opencode) want="run --help" ;;
    *) want="--help" ;;
  esac
  [ "$(cat "$f")" = "$want" ] || argv_ok=1
done
check "every CLI ran with --help only, after its subcommand" [ "$argv_ok" -eq 0 ]
check "four CLIs ran" [ "$(ls "$LOG"/cli-*.argv 2>/dev/null | wc -l | tr -d ' ')" -eq 4 ]

home_ok=0
for f in "$LOG"/cli-*.env "$LOG"/npm.*.env "$LOG"/curl.*.env "$LOG"/installer.*.env; do
  h="$(sed -n 's/^HOME=//p' "$f")"
  case "$h" in "$SCRATCH"/capture-cli-help.*/home) ;; *) home_ok=1 ;; esac
done
check "every child's HOME is inside the prefix" [ "$home_ok" -eq 0 ]

pins_ok=0
for spec in "@anthropic-ai/claude-code@$CLAUDE_V" "@openai/codex@$CODEX_V" "opencode-ai@$OPENCODE_V"; do
  grep -qxF -- "$spec" <(cat "$LOG"/npm.*.argv | tr ' ' '\n') || pins_ok=1
done
check "npm installed each package at its manifest's max_tested" [ "$pins_ok" -eq 0 ]
check "the grok installer was asked for max_tested $GROK_V" grep -qx "$GROK_V" "$LOG"/installer.*.argv
check "the grok installer's GROK_BIN_DIR is inside the prefix" \
  grep -qE "^GROK_BIN_DIR=$SCRATCH/capture-cli-help\." "$LOG"/installer.*.env

prefixes_gone=0
while IFS= read -r p; do
  [ -e "$p" ] && prefixes_gone=1
done <"$LOG/prefixes"
check "the npm prefixes are gone after exit" [ "$prefixes_gone" -eq 0 ]
check "nothing is left in TMPDIR after exit" scratch_is_empty

want_files="claude-headless-$CLAUDE_V.txt codex-exec-$CODEX_V.txt grok-$GROK_V.txt opencode-run-$OPENCODE_V.txt"
check "exactly the four captures were written" [ "$(cd "$OUT" && ls | tr '\n' ' ' | sed 's/ $//')" = "$want_files" ]
check "each capture starts with its header" \
  [ "$(head -n 1 "$OUT/codex-exec-$CODEX_V.txt")" = "# adapter=codex version=$CODEX_V command=codex exec --help" ]
check "gemini and copilot were skipped" grep -q "skip gemini" "$TMP/run.out"

capture="$OUT/opencode-run-$OPENCODE_V.txt"
check "the child's HOME became ~" grep -qF -- '--config <path>  default ~/.config/opencode.toml' "$capture"
check "the caller's HOME became ~" grep -qF -- '--home <path>    or ~/.opencode' "$capture"
check "the login and host names were scrubbed" grep -qF -- '--owner <name>   default <user>@<host>' "$capture"
check "127.0.0.1 is kept" grep -qF 'http://127.0.0.1:4096' "$capture"
check "ANSI codes and trailing whitespace were stripped" grep -qxF '  --plain        bold text' "$capture"
check "no prefix path survived" sh -c "! grep -rqF '$SCRATCH' '$OUT'"

# --- 2. A capture naming another address is refused --------------------------
printf 'previous capture\n' >"$OUT/opencode-run-$OPENCODE_V.txt"
run_capture address -- opencode
check "a capture naming 192.0.2.10 exits non-zero" [ "$RC" -ne 0 ]
check "the refused capture left the committed file as it was" \
  [ "$(cat "$OUT/opencode-run-$OPENCODE_V.txt")" = "previous capture" ]
check "nothing is left in TMPDIR after a refusal" scratch_is_empty

# --- 3. A CLI that outlives the help timeout is killed ----------------------
run_capture sleep CAPTURE_CLI_HELP_TIMEOUT=2 -- opencode
check "a sleeping CLI makes the script exit non-zero" [ "$RC" -ne 0 ]
check "it stopped at the timeout (${SECONDS_TAKEN}s, the stub sleeps 30s)" [ "$SECONDS_TAKEN" -lt 20 ]
check "the CLI and the child it started are dead" sleepers_are_dead
check "the refused capture left the committed file as it was" \
  [ "$(cat "$OUT/opencode-run-$OPENCODE_V.txt")" = "previous capture" ]
check "nothing is left in TMPDIR after a timeout" scratch_is_empty

# --- 4. An install that outlives the install timeout is killed -------------
run_capture npm-sleep CAPTURE_CLI_HELP_INSTALL_TIMEOUT=2 -- codex
check "a sleeping npm makes the script exit non-zero" [ "$RC" -ne 0 ]
check "it stopped at the timeout (${SECONDS_TAKEN}s, the stub sleeps 30s)" [ "$SECONDS_TAKEN" -lt 20 ]
check "npm and the child it started are dead" sleepers_are_dead
check "nothing is left in TMPDIR after an install timeout" scratch_is_empty

# --- 5. Redaction only fires in a path- or account-like context (#1721) -----
rm -rf "$OUT"
run_capture wordcheck "PATH=$WORDSTUBS:$STUBS:$PATH" -- opencode
check "the run under a fake root/localhost identity succeeded" [ "$RC" -eq 0 ]
capture="$OUT/opencode-run-$OPENCODE_V.txt"
check "\"root\" as an ordinary word in help prose is untouched" \
  grep -qxF 'the working root of the project' "$capture"
check "a host literally named localhost in example text is untouched" \
  grep -qF 'http://localhost:4096' "$capture"
check "a real home-directory leak (/root/...) is still redacted" \
  grep -qF 'config lives at /<user>/.config/opencode.toml' "$capture"
check "a user@host leak still redacts the user, but never a localhost host" \
  grep -qxF 'owner default <user>@localhost' "$capture"
check "the bare words root/localhost never survive unredacted next to a path or @" \
  sh -c "! grep -qE '/root[/.]|root@localhost' '$capture'"

# --- 6. A setsid-escaped descendant is reaped too (#1721) -------------------
rm -rf "$OUT"
marker="$LOG/escape-marker"
rm -f "$marker"
run_capture escape -- grok
check "the run that spawned an escaped descendant still succeeded" [ "$RC" -eq 0 ]
check "the escaped descendant's marker was written" [ -s "$marker" ]
escaped_pid="$(cat "$marker" 2>/dev/null || true)"
check "the escaped descendant is not a live pid we forgot to check" [ -n "$escaped_pid" ]
check "the setsid-escaped grandchild does not survive a normal exit" \
  sh -c "! kill -0 '$escaped_pid' 2>/dev/null"

# --- 7. A grok installer that does not match the pinned sha256 is refused --
# (#1721). Every other check above already proves the matching-hash case:
# they run with CAPTURE_CLI_HELP_GROK_INSTALLER_SHA256 set to the stub
# installer's real hash and grok installs and captures normally.
printf 'previous capture\n' >"$OUT/grok-$GROK_V.txt"
run_capture ok "CAPTURE_CLI_HELP_GROK_INSTALLER_SHA256=0000000000000000000000000000000000000000000000000000000000000000" -- grok
check "a grok installer sha256 mismatch exits non-zero" [ "$RC" -ne 0 ]
check "the mismatch names both hashes" sh -c "
  grep -qF '$FAKE_GROK_INSTALLER_SHA256' '$TMP/run.out' &&
  grep -qF '0000000000000000000000000000000000000000000000000000000000000000' '$TMP/run.out'
"
check "the installer never ran on a sha256 mismatch" sh -c "! grep -q . '$LOG'/installer.*.argv 2>/dev/null"
check "the refused capture left the committed file as it was" \
  [ "$(cat "$OUT/grok-$GROK_V.txt")" = "previous capture" ]

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
