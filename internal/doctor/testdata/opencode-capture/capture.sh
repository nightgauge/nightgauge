#!/usr/bin/env bash
# Re-captures the OpenCode evidence the doctor's OpenCode checks are tested
# against, and how `opencode debug config` treats a config (ADR-022 § 20).
#
#   bash internal/doctor/testdata/opencode-capture/capture.sh
#
# Run it from the repository root. It writes, into internal/doctor/testdata/:
#
#   opencode-<version>-debug-config-invalid.txt
#       `opencode debug config` with OPENCODE_CONFIG_CONTENT set to the per-run
#       config `nightgauge opencode config` builds for anthropic/claude-sonnet-5,
#       three times: unchanged, with an unknown top-level key added, and with
#       `share` given a value of the wrong type. Each case records the content,
#       the exit code, stdout and stderr.
#   opencode-<version>-models-configured.txt
#       `opencode models` under the per-run config for lmstudio/qwen/qwen3.8-27b.
#   opencode-<version>-models-other.txt
#       `opencode models` under the per-run config for lmstudio/stub/stub-model,
#       which does not declare qwen/qwen3.8-27b.
#
# Update README.md's provenance table in the same change, and re-run
# `go test ./internal/doctor/`.
#
# Isolation, and why each step exists:
#   - Every opencode and nightgauge process runs under `env -i` with HOME,
#     TMPDIR and the four XDG base directories inside one throwaway directory,
#     so no operator OpenCode config, login, session or cache is read or
#     written, and nothing is inherited but PATH.
#   - Every process runs in its own process group with a wall-clock cap; the
#     whole group is killed at the cap and after the process exits, and the
#     script fails if any process of the group is still alive.
#   - The endpoint in the machine-tier config is the repository's loopback stub
#     provider (cmd/stub-provider), never a real model server. Its PID is
#     captured when it starts, and it is killed and checked dead on every exit.
#     `opencode debug config` and `opencode models` send it no request; it is
#     there so that a request, if one were sent, could reach nothing else.
#   - No credential is set. ANTHROPIC_API_KEY holds a placeholder only for the
#     nightgauge process, whose refusal checks the variable is set; OpenCode
#     never receives it, so the config's {env:ANTHROPIC_API_KEY} reads as empty.
#
# Redaction, and why each step exists:
#   - ANSI colour codes are stripped, so a capture from a terminal and one from
#     a pipe are byte-identical.
#   - The throwaway directory's path is replaced with <capture-root>, and the
#     stub provider's port with 1, so a capture does not depend on the machine
#     or the run.
#   - Trailing whitespace is trimmed, matching .editorconfig.
#   - A capture is refused if it names any IPv4 address other than 127.0.0.1:
#     nothing machine-specific belongs in a committed fixture.
#
# Captures are made and checked in a private staging directory and moved into
# place only after every one passes, so a refused capture never reaches a
# committed path.
set -euo pipefail

repo="$(pwd)"
out_dir="$repo/internal/doctor/testdata"
if [ ! -f "$repo/go.mod" ] || [ ! -d "$out_dir/opencode-capture" ]; then
  echo "capture.sh: run it from the repository root" >&2
  exit 1
fi
for tool in opencode go perl python3; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "capture.sh: $tool is not on PATH" >&2
    exit 1
  fi
done
opencode_dir="$(dirname "$(command -v opencode)")"

root="$(mktemp -d "${TMPDIR:-/tmp}/opencode-capture.XXXXXX")"
# The same directory with every symbolic link resolved, the form a process
# that resolves its paths prints (macOS /var is a link to /private/var).
root_resolved="$(cd "$root" && pwd -P)"
stub_pid=""
cleanup() {
  if [ -n "$stub_pid" ]; then
    kill "$stub_pid" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      kill -0 "$stub_pid" 2>/dev/null || break
      perl -e 'select(undef, undef, undef, 0.2)'
    done
    if kill -0 "$stub_pid" 2>/dev/null; then
      kill -KILL "$stub_pid" 2>/dev/null || true
      echo "capture.sh: the stub provider (pid $stub_pid) outlived SIGTERM and was killed" >&2
    fi
  fi
  rm -rf "$root"
}
trap cleanup EXIT

mkdir -p "$root/home" "$root/tmp" "$root/config" "$root/data" "$root/cache" \
  "$root/state" "$root/machine" "$root/worktree" "$root/bin" "$root/staging"

# bin/bounded <seconds> <cmd...>: runs cmd in its own process group, stdin
# from /dev/null, kills the group at the deadline and after it exits, and
# fails if any process of the group survives. Exits with the command's code,
# 124 when it ran past the deadline, 125 when a process of it survived.
cat >"$root/bin/bounded" <<'PERL'
#!/usr/bin/perl
use strict; use warnings; use POSIX ":sys_wait_h"; use Time::HiRes qw(time);
my $secs = shift @ARGV;
my $pid = fork(); die "fork: $!" unless defined $pid;
if ($pid == 0) { setpgrp(0, 0); open(STDIN, "<", "/dev/null"); exec @ARGV or exit 127; }
my $deadline = time + $secs; my $status = 0; my $timed = 0;
while (1) {
  my $r = waitpid($pid, WNOHANG);
  if ($r == $pid) { $status = $?; last; }
  if (time >= $deadline) { kill "KILL", -$pid; $timed = 1; waitpid($pid, 0); $status = $?; last; }
  select(undef, undef, undef, 0.05);
}
kill "KILL", -$pid;
my @left = grep { /\d/ } split /\n/, `pgrep -g $pid 2>/dev/null`;
if (@left) { print STDERR "capture.sh: process group $pid still has @left\n"; exit 125; }
if ($timed) { print STDERR "capture.sh: @ARGV ran past ${secs}s and was killed\n"; exit 124; }
exit($status >> 8);
PERL
chmod 0755 "$root/bin/bounded"

# isolated <cmd...>: the environment every captured process gets.
isolated() {
  env -i PATH="$root/bin:$opencode_dir:/usr/bin:/bin" HOME="$root/home" TMPDIR="$root/tmp" \
    XDG_CONFIG_HOME="$root/config" XDG_DATA_HOME="$root/data" \
    XDG_CACHE_HOME="$root/cache" XDG_STATE_HOME="$root/state" \
    OPENCODE_DISABLE_MODELS_FETCH=1 OPENCODE_DISABLE_AUTOUPDATE=1 OPENCODE_DISABLE_LSP_DOWNLOAD=1 \
    OPENCODE_DISABLE_DEFAULT_PLUGINS=1 OPENCODE_DISABLE_SHARE=1 OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1 \
    OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1 OPENCODE_DISABLE_EXTERNAL_SKILLS=1 \
    "$@"
}

go build -o "$root/bin/nightgauge" ./cmd/nightgauge
go build -o "$root/bin/stub-provider" ./cmd/stub-provider

version="$(isolated bounded 30 opencode --version | tr -d '[:space:]')"
case "$version" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *)
    echo "capture.sh: opencode --version printed no version" >&2
    exit 1
    ;;
esac

# The stub provider: loopback only, one JSON line with its base URL on stdout.
"$root/bin/stub-provider" --script tool-edit-stop --idle-timeout 120s >"$root/stub.out" 2>"$root/stub.err" &
stub_pid=$!
base_url=""
for _ in $(seq 1 50); do
  base_url="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["base_url"])' "$root/stub.out" 2>/dev/null || true)"
  [ -n "$base_url" ] && break
  perl -e 'select(undef, undef, undef, 0.1)'
done
case "$base_url" in
  http://127.0.0.1:*/v1) ;;
  *)
    echo "capture.sh: the stub provider did not report a loopback base URL" >&2
    exit 1
    ;;
esac
stub_port="${base_url#http://127.0.0.1:}"
stub_port="${stub_port%/v1}"

# per_run_config <model>: OPENCODE_CONFIG_CONTENT as `nightgauge opencode
# config` builds it, and the files it refers to, for model.
per_run_config() {
  isolated NIGHTGAUGE_CONFIG_HOME="$root/machine" NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1 \
    ANTHROPIC_API_KEY=placeholder-not-a-key \
    bounded 60 nightgauge opencode config --stage feature-dev --worktree "$root/worktree" \
    --model "$1" --json >"$root/verb.json" 2>"$root/verb.err"
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["config_content"])' "$root/verb.json"
}

cat >"$root/machine/config.yaml" <<EOF
opencode:
  provider: lm-studio
  base_url: $base_url
  limit:
    context: 131072
    output: 8192
EOF

redact() {
  sed -e $'s/\x1b\\[[0-9;]*[A-Za-z]//g' -e "s#${root_resolved}#<capture-root>#g" -e "s#${root}#<capture-root>#g" \
    -e "s#127\\.0\\.0\\.1:${stub_port}#127.0.0.1:1#g" -e 's/[[:space:]]*$//'
}

# debug config: unchanged, an unknown top-level key, and a wrong-typed value.
valid="$(per_run_config anthropic/claude-sonnet-5)"
unknown="{\"nightgauge_unknown_key\":true,${valid#\{}"
wrong_type="${valid/\"share\":\"disabled\"/\"share\":42}"
if [ "$wrong_type" = "$valid" ]; then
  echo "capture.sh: the per-run config sets no share of \"disabled\"; update the wrong-type case" >&2
  exit 1
fi
dc="$root/staging/opencode-$version-debug-config-invalid.txt"
{
  echo "# opencode $version \`opencode debug config\`, captured by opencode-capture/capture.sh."
  echo "# OPENCODE_CONFIG_CONTENT is the per-run config \`nightgauge opencode config\` builds"
  echo "# for anthropic/claude-sonnet-5, changed as each case says. See opencode-capture/README.md."
} >"$dc"
for case_name in valid unknown-key wrong-type; do
  case "$case_name" in
    valid) content="$valid" change="none" ;;
    unknown-key) content="$unknown" change="top-level key nightgauge_unknown_key added" ;;
    wrong-type) content="$wrong_type" change="share set to the number 42" ;;
  esac
  set +e
  isolated OPENCODE_CONFIG_CONTENT="$content" bounded 30 opencode debug config \
    >"$root/dc.out" 2>"$root/dc.err"
  code=$?
  set -e
  if [ "$code" -ge 124 ]; then
    echo "capture.sh: opencode debug config ($case_name) did not finish cleanly" >&2
    exit 1
  fi
  {
    echo "==> case $case_name"
    echo "==> change $change"
    echo "==> exit $code"
    echo "==> content"
    printf '%s\n' "$content"
    echo "==> stdout"
    cat "$root/dc.out"
    echo "==> stderr"
    cat "$root/dc.err"
    echo "==> end"
  } >>"$dc"
done

# models: the per-run config for the configured model, and for another one.
for pair in "configured:lmstudio/qwen/qwen3.8-27b" "other:lmstudio/stub/stub-model"; do
  name="${pair%%:*}"
  model="${pair#*:}"
  content="$(per_run_config "$model")"
  isolated OPENCODE_CONFIG_CONTENT="$content" bounded 30 opencode models \
    >"$root/staging/opencode-$version-models-$name.txt" 2>"$root/models.err"
done

for f in "$root"/staging/*.txt; do
  redact <"$f" >"$f.redacted"
  mv "$f.redacted" "$f"
  if [ -n "$(tail -c 1 "$f")" ]; then
    printf '\n' >>"$f"
  fi
  # Every dotted quad on its own line, so 127.0.0.1 on the same line cannot
  # hide another address. No `grep -q`: its early exit would SIGPIPE the first
  # grep, and pipefail would turn a found address into a pass.
  others="$(grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' "$f" | grep -vxF '127.0.0.1' || true)"
  if [ -n "$others" ]; then
    echo "capture.sh: $(basename "$f") names an IPv4 address other than 127.0.0.1; no fixture was written" >&2
    exit 1
  fi
done

mv "$root"/staging/*.txt "$out_dir/"
printf 'captured opencode %s into %s\n' "$version" "$out_dir"
