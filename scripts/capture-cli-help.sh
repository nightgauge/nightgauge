#!/usr/bin/env bash
# capture-cli-help.sh — capture each CLI adapter's real `--help` at the version
# its compat manifest pins as max_tested (#1617).
#
#   bash scripts/capture-cli-help.sh [adapter ...]
#
# With no arguments it captures every adapter that has a manifest in
# internal/adaptercompat/manifests/. Each capture is written to
# internal/execution/adapters/testdata/cli-help/<adapter>[-<sub>]-<version>.txt:
# one header line naming the adapter, the version and the command, then the
# help text, redacted as below. TestFlagContract
# (internal/execution/adapters/flag_contract_test.go) reads those files. Update
# that directory's README.md, the provenance record, in the same change.
#
# The script installs third-party packages and runs them. Each step below
# answers part of that hazard:
#   - Every CLI is installed fresh, at the pinned version, into one `mktemp -d`
#     prefix, never globally, and the prefix is removed on every exit (trap).
#     npm packages use `npm install --prefix`; the grok CLI uses its vendor
#     installer, with HOME and GROK_BIN_DIR inside the prefix. A vendor
#     installer's sha256 is checked against `expected_installer_sha256`
#     before it runs, refusing an installer that does not match the recorded
#     provenance rather than running it blind (#1721).
#   - Installs and CLIs run under `env -i` with a minimal environment. PATH
#     holds only node, npm, curl and the system directories. HOME, TMPDIR and
#     the XDG directories point inside the prefix, and the working directory is
#     an empty directory there. No provider credential, token or operator
#     config (~/.npmrc, ~/.grok, ~/.config) is visible to them.
#   - The script runs each CLI with `--help` only, after the subcommand the
#     adapter dispatches where it uses one, with stdin from /dev/null. Vendor
#     install steps run the binary themselves: opencode's npm postinstall runs
#     `--version`, and the grok installer runs `--version` and generates shell
#     completions. Nothing else runs it.
#   - Every download, install and CLI call is bounded by a timeout. macOS has no
#     timeout(1), so a perl wrapper runs the command in a process group of its
#     own and kills the whole group when the time runs out. It also kills the
#     group after a normal exit, and every descendant (and process group a
#     descendant leads) that its 0.2s poll saw, even one that called setsid()
#     to leave the group. It never kills a pid it cannot still identify as
#     the process it saw (same pid and start time), so a recycled pid is safe.
#     Limit: a daemon that double-forks faster than one poll interval
#     (fork, setsid, fork, intermediate exits) is never seen and can outlive
#     the call; see bounded().
#   - The version comes from the pin, the manifest's max_tested. For npm
#     packages it is confirmed against the installed package.json. The script
#     never runs `--version`.
#
# Redaction: ANSI escape sequences are stripped. The prefix's HOME and the
# capturing user's HOME become `~`, and any other prefix path becomes
# `<capture-prefix>`. The host name (full and short) becomes `<host>` wherever
# it stands as a whole name. The login name becomes `<user>` only where it
# names the account: as the home-directory component at the start of a path
# (`/Users/<u>`, `/home/<u>`, or `/root` and `/var/root` when running as
# root), after `~`, or before `@`. It is never matched after an arbitrary `/`
# or as a bare word, so help prose such as "path/to/root" or "the working
# root" is left alone when the capture runs as root (#1721). A host named
# exactly `localhost` is never substituted at all: it never identifies a
# machine, so a CLI's own generic example text naming it is untouched.
# Trailing whitespace is trimmed and a final newline ensured. A capture that
# names any IPv4 address other than 127.0.0.1 is refused. Captures are staged
# and moved into place only after every requested adapter passed, so a refused
# run changes nothing.
#
# An adapter whose manifest pins no max_tested is skipped with the manifest's
# reason: there is no pinned version to install.
#
# Environment, read before the child environment is cleared:
#   CAPTURE_CLI_HELP_OUT              output directory (default: the testdata directory above)
#   CAPTURE_CLI_HELP_TIMEOUT          seconds for each --help call (default 60)
#   CAPTURE_CLI_HELP_INSTALL_TIMEOUT  seconds for each download or install (default 900)
#
# Regression suite: scripts/test-capture-cli-help.sh, run by scripts/ci-local.sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFESTS="$REPO_ROOT/internal/adaptercompat/manifests"
OUT="${CAPTURE_CLI_HELP_OUT:-$REPO_ROOT/internal/execution/adapters/testdata/cli-help}"
HELP_TIMEOUT="${CAPTURE_CLI_HELP_TIMEOUT:-60}"
INSTALL_TIMEOUT="${CAPTURE_CLI_HELP_INSTALL_TIMEOUT:-900}"

die() {
  echo "capture-cli-help.sh: $*" >&2
  exit 1
}

for n in "$HELP_TIMEOUT" "$INSTALL_TIMEOUT"; do
  case "$n" in
    '' | *[!0-9]* | 0) die "timeouts must be positive whole seconds, got '$n'" ;;
  esac
done
for tool in python3 perl; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not on PATH"
done

# The subcommand whose --help holds the flags the adapter's BuildCommand
# emits. Keep in step with helpSubcommand in flag_contract_test.go.
help_subcommand() {
  case "$1" in
    codex) echo exec ;;
    opencode) echo run ;;
    *) echo "" ;;
  esac
}

# expected_installer_sha256 <adapter>: the sha256 that adapter's vendor
# installer script must match before this script runs it, so a compromised
# or silently changed installer refuses rather than being executed blind and
# its (possibly tampered) help text recorded as if it were the real thing
# (#1721). Pinned by hand after downloading and inspecting the installer;
# update it, and the provenance row in testdata/cli-help/README.md, together,
# only after confirming the new installer by hand. Echoes empty for an
# adapter with no pin yet, which this script does not refuse: a first pin
# still has to come from somewhere, and the sha256 this run computed is
# already recorded in the summary line for that purpose.
#
# CAPTURE_CLI_HELP_GROK_INSTALLER_SHA256 overrides grok's pin so
# scripts/test-capture-cli-help.sh can point it at its own stubbed
# installer's real hash instead of the production one; unset in every real
# run.
expected_installer_sha256() {
  case "$1" in
    grok) echo "${CAPTURE_CLI_HELP_GROK_INSTALLER_SHA256:-7fd6fdc75d9418b2e58356726fcbf1ae849416f773925da07d0ccc7a60d3e791}" ;;
    *) echo "" ;;
  esac
}

# bounded <seconds> <command> [args...]: run the command in a process group of
# its own, with stdin from /dev/null. On timeout, or on INT/TERM/HUP, kill the
# group and every descendant and exit 124 (timeout) or 130. After a normal
# exit, kill whatever the command left running, group and descendants alike,
# then exit with the command's status.
bounded() {
  perl -e '
    use strict;
    use POSIX ();
    my $secs = shift @ARGV;
    my $pid = fork();
    die "fork: $!\n" unless defined $pid;
    if ($pid == 0) {
      setpgrp(0, 0);
      exec { $ARGV[0] } @ARGV;
      print STDERR "exec $ARGV[0]: $!\n";
      POSIX::_exit(127);
    }
    setpgrp($pid, $pid);

    # A descendant that calls setsid() leaves $pid'"'"'s process group, so
    # `kill KILL, -$pid` below never reaches it. Each scan therefore walks
    # the process table from $pid and from every pid already tracked, polled
    # throughout the run rather than read once at the end: once whichever
    # ancestor sits between $pid and an escaped descendant exits, that
    # descendant reparents to init and its ppid chain no longer leads back to
    # $pid, so a single scan taken only at cleanup time would already have
    # lost it (#1721). %seen maps each tracked pid to its start time; %groups
    # holds the process groups tracked pids lead, so a child an escaped
    # group leader starts between two scans is caught by its group too.
    #
    # A pid is dropped from %seen as soon as a scan no longer lists it with
    # the same start time, and a group once a scan finds no member of it, so
    # a pid or group id the kernel recycles for an unrelated process after
    # ours exited is never killed: the reap below only signals pids a fresh
    # scan still shows as the process that was tracked.
    #
    # Limit: tracking is only as fine as the poll. A descendant that forks,
    # calls setsid() and forks again, with the intermediate exiting, all
    # within one 0.2s poll interval (the classic daemon double fork), leaves
    # a grandchild whose parent is init, whose session and group are the
    # vanished intermediate'"'"'s, and which no scan ever linked to $pid: it
    # is not reaped. Nothing portable to macOS closes that window.
    my (%seen, %groups);
    my $own_group = 1;
    my $scan = sub {
      open(my $ps, "-|", "ps", "-e", "-o", "pid=,ppid=,pgid=,lstart=") or return 0;
      my (%children, %start, %members);
      while (my $line = <$ps>) {
        next unless $line =~ /^\s*(\d+)\s+(\d+)\s+(\d+)\s+(.*?)\s*$/;
        my ($p, $pp, $pg) = ($1 + 0, $2 + 0, $3 + 0);
        push @{$children{$pp}}, $p;
        push @{$members{$pg}}, $p;
        $start{$p} = $4;
      }
      close $ps;
      $own_group = $members{$pid} ? 1 : 0;
      for my $p (keys %seen) {
        delete $seen{$p} unless defined $start{$p} && $start{$p} eq $seen{$p};
      }
      for my $g (keys %groups) {
        delete $groups{$g} unless $members{$g};
      }
      my @stack = ($pid, keys %seen, map { @{$members{$_}} } keys %groups);
      my %found;
      while (@stack) {
        my $p = pop @stack;
        next if $found{$p}++;
        push @stack, @{$children{$p} || []};
      }
      delete $found{$pid};
      for my $p (keys %found) {
        next unless defined $start{$p};
        $seen{$p} = $start{$p};
        $groups{$p} = 1 if $members{$p};
      }
      return 1;
    };
    my $reap = sub {
      # Without a fresh scan no tracked pid can be re-identified, so none is
      # signalled; the group kill alone still stands.
      my $fresh = $scan->();
      kill "KILL", -$pid if $own_group;
      kill "KILL", keys %seen if $fresh && %seen;
    };
    my $stop = sub {
      my ($code, $why) = @_;
      $reap->();
      waitpid($pid, 0);
      print STDERR "capture-cli-help.sh: $why: @ARGV\n";
      exit $code;
    };
    $SIG{ALRM} = sub { $stop->(124, "timed out after ${secs}s") };
    $SIG{INT} = $SIG{TERM} = $SIG{HUP} = sub { $stop->(130, "interrupted") };
    alarm $secs;
    # Polls rather than blocks on waitpid, so %seen is kept current while
    # $pid'"'"'s tree is still intact instead of read only once, after exit,
    # when an escaped descendant may already be unreachable from it.
    my $status;
    while (1) {
      $scan->();
      my $r = waitpid($pid, POSIX::WNOHANG());
      if ($r == $pid) { $status = $?; last; }
      select(undef, undef, undef, 0.2);
    }
    alarm 0;
    $reap->();
    exit($status & 127 ? 128 + ($status & 127) : $status >> 8);
  ' "$@" </dev/null
}

sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

# read_manifest <file>: binary, max_tested, install.npm, install.installer and
# max_tested_reason, separated by the ASCII unit separator so an empty field
# is kept (read collapses runs of whitespace separators, not of this one).
read_manifest() {
  python3 - "$1" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
i = m.get("install") or {}
fields = [m.get("binary", ""), m.get("max_tested", ""), i.get("npm", ""),
          i.get("installer", ""), " ".join(m.get("max_tested_reason", "").split())]
print("\x1f".join(fields))
PY
}

# The adapters to capture: the arguments, or every manifest.
ADAPTERS=()
if [ "$#" -gt 0 ]; then
  ADAPTERS=("$@")
else
  for f in "$MANIFESTS"/*.json; do
    a="$(basename "$f" .json)"
    ADAPTERS+=("$a")
  done
fi
for a in "${ADAPTERS[@]}"; do
  [ -f "$MANIFESTS/$a.json" ] || die "no manifest for adapter '$a' in $MANIFESTS"
done

PREFIX="$(mktemp -d "${TMPDIR:-/tmp}/capture-cli-help.XXXXXX")"
cleanup() {
  chmod -R u+w "$PREFIX" 2>/dev/null || true
  rm -rf "$PREFIX"
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP

CHILD_HOME="$PREFIX/home"
TOOLS="$PREFIX/tools"
STAGE="$PREFIX/stage"
WORK="$PREFIX/work"
LOGS="$PREFIX/logs"
mkdir -p "$CHILD_HOME" "$TOOLS" "$STAGE" "$WORK" "$LOGS" "$PREFIX/tmp"

# The only tools the child environment can reach besides the system
# directories, resolved from the caller's PATH before it is cleared.
for tool in node npm curl; do
  if path="$(command -v "$tool" 2>/dev/null)"; then
    ln -s "$path" "$TOOLS/$tool"
  fi
done

CLEAN_ENV=(
  "PATH=$TOOLS:/usr/bin:/bin:/usr/sbin:/sbin"
  "HOME=$CHILD_HOME"
  "TMPDIR=$PREFIX/tmp"
  "XDG_CONFIG_HOME=$CHILD_HOME/.config"
  "XDG_DATA_HOME=$CHILD_HOME/.local/share"
  "XDG_CACHE_HOME=$CHILD_HOME/.cache"
  "XDG_STATE_HOME=$CHILD_HOME/.local/state"
  "NO_COLOR=1"
  "npm_config_cache=$PREFIX/npm-cache"
  "npm_config_update_notifier=false"
  "npm_config_fund=false"
  "npm_config_audit=false"
)

# in_clean_env <seconds> <command> [args...]: bounded, from the empty working
# directory, under `env -i` with CLEAN_ENV only.
in_clean_env() {
  local secs="$1"
  shift
  (cd "$WORK" && bounded "$secs" /usr/bin/env -i "${CLEAN_ENV[@]}" "$@")
}

redact() {
  SCRUB_CHILD_HOME="$CHILD_HOME" \
    SCRUB_CHILD_HOME_REAL="$(cd "$CHILD_HOME" && pwd -P)" \
    SCRUB_PREFIX="$PREFIX" \
    SCRUB_PREFIX_REAL="$(cd "$PREFIX" && pwd -P)" \
    SCRUB_HOME="${HOME:-}" \
    SCRUB_USER="$(id -un 2>/dev/null || true)" \
    SCRUB_HOST="$(hostname 2>/dev/null || true)" \
    SCRUB_HOST_SHORT="$(hostname -s 2>/dev/null || true)" \
    perl -lpe '
      BEGIN {
        our @paths;
        for (["SCRUB_CHILD_HOME_REAL", "~"], ["SCRUB_CHILD_HOME", "~"],
             ["SCRUB_PREFIX_REAL", "<capture-prefix>"], ["SCRUB_PREFIX", "<capture-prefix>"],
             ["SCRUB_HOME", "~"]) {
          my ($k, $to) = @$_;
          my $v = $ENV{$k} // "";
          push @paths, [$v, $to] if length($v) > 1;
        }
        our (@hosts, $user);
        for my $k ("SCRUB_HOST", "SCRUB_HOST_SHORT") {
          my $v = $ENV{$k} // "";
          next if length($v) == 0;
          # "localhost" never identifies a machine, so leaving it alone
          # cannot leak anything, and redacting it corrupts a CLI own
          # generic example text (opencode default "http://localhost:4096")
          # on a host that happens to be named localhost -- the #1721 finding.
          next if lc($v) eq "localhost";
          push @hosts, $v;
        }
        $user = $ENV{SCRUB_USER} // "";
        our %n;
      }
      our (@paths, @hosts, $user, %n);
      $n{ansi} += s/\e\[[0-9;?]*[ -\/]*[@-~]//g;
      $n{ansi} += s/\e\][^\a\e]*(?:\a|\e\\)//g;
      for my $p (@paths) { $n{$p->[1]} += s/\Q$p->[0]\E/$p->[1]/g; }
      # The host name is redacted wherever it stands as a whole name (not
      # inside a longer word or dotted name). The full name goes first, so a
      # short name never splits it.
      for my $h (@hosts) {
        $n{"<host>"} += s/(?<![\w.-])\Q$h\E(?![\w-]|\.\w)/<host>/g;
      }
      # The login name is redacted only where it identifies the account: as
      # the home-directory component at the start of a path (/Users/<u>,
      # /home/<u>, and /root or /var/root for root), after `~`, or before
      # `@`. Never after an arbitrary `/`, so help prose such as
      # "path/to/root" is left alone when the capture runs as root, and never
      # as a bare word (#1721).
      if (length $user) {
        my $home = $user eq "root" ? qr{/(?:var/)?} : qr{/(?:Users|home)/};
        $n{"<user>"} += s{(?<![\w.~/-])($home)\Q$user\E(?![\w.-])}{$1<user>}g;
        $n{"<user>"} += s/(?<=~)\Q$user\E(?![\w-])/<user>/g;
        $n{"<user>"} += s/(?<![\w.-])\Q$user\E(?=\@)/<user>/g;
      }
      s/\s+$//;
      END {
        my @r = map { "$_=" . ($n{$_} + 0) } sort keys %n;
        print STDERR "  redacted: @r" if grep { $n{$_} } keys %n;
      }
    '
}

# refuse_foreign_ipv4 <file> <label>: every dotted quad on its own line, so
# 127.0.0.1 on the same line cannot hide another address. No `grep -q`: its
# early exit would SIGPIPE the first grep, and pipefail would turn a found
# address into a pass.
refuse_foreign_ipv4() {
  local others
  others="$(grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' "$1" | grep -vxF '127.0.0.1' || true)"
  [ -z "$others" ] || die "$2 names an IPv4 address other than 127.0.0.1; nothing was written"
}

SUMMARY=()
CAPTURED=()
for adapter in "${ADAPTERS[@]}"; do
  IFS=$'\x1f' read -r binary version npm_pkg installer reason < <(read_manifest "$MANIFESTS/$adapter.json")
  sub="$(help_subcommand "$adapter")"

  if [ -z "$version" ]; then
    echo "skip $adapter: its manifest pins no max_tested version, so there is nothing to install (${reason:-no reason given})"
    SUMMARY+=("$adapter | skipped | manifest pins no max_tested")
    continue
  fi

  # The manifest loader validates these; they are checked again here because
  # they become command-line arguments.
  [[ "$binary" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "$adapter: binary '$binary' is not a binary name"
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "$adapter: max_tested '$version' is not MAJOR.MINOR.PATCH"

  echo "▶ $adapter $version"
  source_note=""
  if [ -n "$npm_pkg" ]; then
    [[ "$npm_pkg" =~ ^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$ ]] || die "$adapter: '$npm_pkg' is not an npm package name"
    [ -e "$TOOLS/npm" ] || die "$adapter installs with npm, and npm is not on PATH"
    dir="$PREFIX/npm-$adapter"
    mkdir -p "$dir"
    if ! in_clean_env "$INSTALL_TIMEOUT" "$TOOLS/npm" install --prefix "$dir" --no-save \
      --no-package-lock --loglevel=error "$npm_pkg@$version" >"$LOGS/$adapter-install.log" 2>&1; then
      sed 's/^/    /' "$LOGS/$adapter-install.log" >&2
      die "$adapter: npm install $npm_pkg@$version failed"
    fi
    installed="$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1])).get("version", ""))' \
      "$dir/node_modules/$npm_pkg/package.json" 2>/dev/null || true)"
    [ "$installed" = "$version" ] || die "$adapter: installed $npm_pkg is '$installed', not the pinned $version"
    bin="$dir/node_modules/.bin/$binary"
    source_note="npm $npm_pkg@$version"
  elif [ -n "$installer" ]; then
    [[ "$installer" =~ ^https://[A-Za-z0-9.-]+(/[A-Za-z0-9._~/-]*)?$ ]] || die "$adapter: installer '$installer' is not a plain https URL"
    [ -e "$TOOLS/curl" ] || die "$adapter installs with its vendor installer, and curl is not on PATH"
    script="$PREFIX/$adapter-installer.sh"
    in_clean_env "$INSTALL_TIMEOUT" "$TOOLS/curl" -fsSL --proto '=https' -o "$script" "$installer" \
      >"$LOGS/$adapter-download.log" 2>&1 || {
      sed 's/^/    /' "$LOGS/$adapter-download.log" >&2
      die "$adapter: downloading $installer failed"
    }
    got_sha="$(sha256 "$script")"
    want_sha="$(expected_installer_sha256 "$adapter")"
    if [ -n "$want_sha" ] && [ "$got_sha" != "$want_sha" ]; then
      die "$adapter: installer $installer sha256 is $got_sha, not the pinned $want_sha in" \
        "expected_installer_sha256; refusing to run an installer that does not match the" \
        "recorded provenance. If this is a deliberate, verified update, confirm the new" \
        "installer by hand, then update expected_installer_sha256 and testdata/cli-help/README.md together"
    fi
    bindir="$PREFIX/$adapter-bin"
    if ! in_clean_env "$INSTALL_TIMEOUT" "GROK_BIN_DIR=$bindir" /bin/bash "$script" "$version" \
      >"$LOGS/$adapter-install.log" 2>&1; then
      sed 's/^/    /' "$LOGS/$adapter-install.log" >&2
      die "$adapter: the installer failed for $version"
    fi
    bin="$bindir/$binary"
    source_note="installer $installer (sha256 $(sha256 "$script")) $version"
  else
    die "$adapter: the manifest names neither an npm package nor an installer"
  fi
  [ -x "$bin" ] || die "$adapter: the install produced no executable $binary"

  # stdout and stderr together: opencode 1.18.30 prints `run --help` on
  # stderr, and a CLI's warning on either stream belongs in the reviewed diff.
  command_text="$binary${sub:+ $sub} --help"
  raw="$PREFIX/$adapter.out"
  rc=0
  if [ -n "$sub" ]; then
    in_clean_env "$HELP_TIMEOUT" "$bin" "$sub" --help >"$raw" 2>&1 || rc=$?
  else
    in_clean_env "$HELP_TIMEOUT" "$bin" --help >"$raw" 2>&1 || rc=$?
  fi
  if [ "$rc" -ne 0 ]; then
    redact <"$raw" 2>/dev/null | tail -n 20 | sed 's/^/    /' >&2
    die "$adapter: \`$command_text\` exited $rc; nothing was written"
  fi
  [ -s "$raw" ] || die "$adapter: \`$command_text\` printed nothing; nothing was written"

  stem="$adapter${sub:+-$sub}"
  name="$stem-$version.txt"
  body="$PREFIX/$adapter.body"
  redact <"$raw" >"$body"
  refuse_foreign_ipv4 "$body" "the $adapter capture"
  {
    printf '# adapter=%s version=%s command=%s\n' "$adapter" "$version" "$command_text"
    cat "$body"
  } >"$STAGE/$name"
  CAPTURED+=("$stem|$name")
  SUMMARY+=("$adapter | $name | exit $rc | sha256 $(sha256 "$STAGE/$name") | $source_note")
done

# Every requested adapter passed: replace each captured adapter's previous
# file (any version) with the new one.
if [ "${#CAPTURED[@]}" -gt 0 ]; then
  mkdir -p "$OUT"
  for entry in "${CAPTURED[@]}"; do
    stem="${entry%%|*}"
    name="${entry#*|}"
    for old in "$OUT/$stem"-[0-9]*.txt; do
      [ -e "$old" ] && rm -f "$old"
    done
    mv "$STAGE/$name" "$OUT/$name"
  done
fi

echo ""
echo "captured into $OUT:"
for line in "${SUMMARY[@]}"; do
  printf '  %s\n' "$line"
done
