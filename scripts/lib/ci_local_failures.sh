#!/usr/bin/env bash
# ci_local_failures.sh — how scripts/ci-local.sh reads a failed step's log.
#
# Sourced, not executed, so scripts/test-ci-local-concurrency.sh can assert
# these three functions directly. They were inline in ci-local.sh and therefore
# only reachable by running the entire 15-minute gate, which is why the defect
# below survived so long (#1983).
#
# THE DEFECT: the summary pulled failing assertions up with
#   grep -aE '^[[:space:]]*(×|✗|FAIL |--- FAIL|AssertionError|Error:)'
# and every suite in this repository that colours its own output prints
#   '  \033[31m✗\033[0m <description>'
# so after the indent comes an escape sequence, not `✗`. The grep matched
# nothing, the summary printed "(no recognised failure marker — see the log
# above for detail)", and an operator was pointed at a log to read by eye. Under
# concurrent gates that arrived on a log whose visible content was 16 passes,
# which is indistinguishable from a real drift failure.

# Drop ANSI SGR (and any other CSI) sequences so a marker sits where the
# pattern expects it.
strip_ansi() { # strip_ansi <file>
  sed $'s/\033\[[0-9;]*[A-Za-z]//g' "$1" 2>/dev/null
}

# The lines from a failed step's log worth lifting into the summary. A vitest
# failure can sit thousands of lines above the exit line, so "scroll up" is not
# a usable instruction — and is exactly how a failure escapes identification.
failure_markers() { # failure_markers <file> [limit]
  strip_ansi "$1" |
    grep -aE '^[[:space:]]*(×|✗|!|FAIL |--- FAIL|AssertionError|Error:|HARNESS ERROR|panic:|ran [0-9]+ assertions)' |
    head -"${2:-15}"
}

# "an arm asserted false" versus "the harness could not run the arm" — a git
# lock, no temp space, a child killed under load (#1983). They are different
# diagnoses: an infrastructure error says NOTHING about the diff, and reporting
# it as a check failure is how a red gate gets written off as flaky, which
# AGENTS.md forbids.
#
# A suite declares the second by printing a `HARNESS ERROR` line. The exit code
# is deliberately NOT the signal: 2 already means an ordinary failure in several
# suites here (the publication-boundary set), so overloading it would
# misclassify real failures as infrastructure — the one direction that must
# never happen.
classify_failure() { # classify_failure <file> <exit-code>
  if strip_ansi "$1" | grep -qaE 'HARNESS ERROR|INFRASTRUCTURE (failure|error)'; then
    printf 'infra\n'
  else
    printf 'assert\n'
  fi
}
