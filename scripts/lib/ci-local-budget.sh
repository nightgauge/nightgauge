# shellcheck shell=bash
# ci-local-budget.sh — the default machine-wide heavy-step budget (#3127).
#
# A fixed CI_LOCAL_JOBS=4 was sized on a 12-core box after #1983's load-58
# incident; on a 30-core box it leaves most of the machine idle. The default is
# now max(4, ncpu/3), capped at 12. An explicit CI_LOCAL_JOBS still wins.

# ci_local_detect_ncpu — print the online CPU count, or 4 when it is unknown.
ci_local_detect_ncpu() {
  local n=""
  if [ "$(uname -s 2>/dev/null)" = "Darwin" ]; then
    n="$(sysctl -n hw.ncpu 2>/dev/null)"
  else
    n="$(nproc 2>/dev/null)"
  fi
  case "$n" in '' | *[!0-9]*) n="$(getconf _NPROCESSORS_ONLN 2>/dev/null)" ;; esac
  case "$n" in '' | *[!0-9]* | 0) n=4 ;; esac
  echo "$n"
}

# ci_local_default_jobs <ncpu> — print max(4, ncpu/3) capped at 12.
ci_local_default_jobs() {
  local ncpu="${1:-4}" jobs
  case "$ncpu" in '' | *[!0-9]*) ncpu=4 ;; esac
  jobs=$((ncpu / 3))
  [ "$jobs" -lt 4 ] && jobs=4
  [ "$jobs" -gt 12 ] && jobs=12
  echo "$jobs"
}
