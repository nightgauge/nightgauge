# shellcheck shell=bash
# require-bash4.sh — refuse to run a bash-4 script under an older bash (#3127).
#
# macOS ships bash 3.2 as /bin/bash. Under it scripts/ci-local.sh did not fail:
# a grouped child aborted on `$BASHPID` under `set -u`, the parent waited out
# CI_LOCAL_SLOT_WAIT, and the run was killed at 2400 s having named nothing.
# Sourced before anything bash-4-only, so this file uses bash-3.2 syntax only.

# bash4_major_ok <major> — 0 when <major> is 4 or newer.
bash4_major_ok() {
  case "${1:-}" in
    '' | *[!0-9]*) return 1 ;;
  esac
  [ "$1" -ge 4 ]
}

# require_bash4 <script-name> — exit 2 with one named fix when bash < 4.
# _NG_BASH_MAJOR_OVERRIDE is a test seam for scripts/test-ci-local-machine-deps.sh.
require_bash4() {
  if ! bash4_major_ok "${_NG_BASH_MAJOR_OVERRIDE:-${BASH_VERSINFO[0]}}"; then
    echo "${1:-script}: needs bash 4 or newer, running bash ${BASH_VERSION}; on macOS: brew install bash" >&2
    exit 2
  fi
}
