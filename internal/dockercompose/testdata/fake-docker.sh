#!/bin/sh
# Fake docker binary used by internal/dockercompose tests.
#
# Records every invocation (one args-line per call) to $FAKE_DOCKER_LOG and
# returns canned output / exit codes driven by env vars set per test:
#
#   FAKE_DOCKER_LOG          path to log file (each invocation appends a line)
#   FAKE_DOCKER_VERSION_EXIT exit code for `docker version` (default 0)
#   FAKE_DOCKER_LS_OUTPUT    stdout for `docker compose ls --format json`
#   FAKE_DOCKER_DOWN_EXIT    exit code for `docker compose -p X down ...`
#   FAKE_DOCKER_DOWN_STDERR  optional stderr for the down call
#   FAKE_DOCKER_IMAGES_OUT   stdout for `docker images --format ...`
#   FAKE_DOCKER_RMI_EXIT     exit code for `docker rmi -f ...` (default 0)
#
# Tests should keep this script hermetic: do not rely on any real docker
# CLI. Real exec paths are exercised through the harness.

LOG="${FAKE_DOCKER_LOG:-/dev/null}"
echo "$@" >> "$LOG"

case "$1" in
  version)
    exit "${FAKE_DOCKER_VERSION_EXIT:-0}"
    ;;
  compose)
    case "$2" in
      ls)
        printf '%s' "${FAKE_DOCKER_LS_OUTPUT:-[]}"
        exit 0
        ;;
      -p)
        # `docker compose -p NAME down ...`
        if [ "$4" = "down" ]; then
          if [ -n "$FAKE_DOCKER_DOWN_STDERR" ]; then
            printf '%s' "$FAKE_DOCKER_DOWN_STDERR" >&2
          fi
          exit "${FAKE_DOCKER_DOWN_EXIT:-0}"
        fi
        exit 0
        ;;
    esac
    ;;
  images)
    printf '%s' "${FAKE_DOCKER_IMAGES_OUT:-}"
    exit 0
    ;;
  rmi)
    exit "${FAKE_DOCKER_RMI_EXIT:-0}"
    ;;
  ps)
    exit 0
    ;;
  volume|network)
    exit 0
    ;;
esac

exit 0
