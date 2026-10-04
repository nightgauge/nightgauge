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
#
# `sed -n 1,Np`, not `head`, and a process substitution, not
# `strip_ansi | grep -q` below: a reader that stops early SIGPIPEs the command
# writing into it, and under a caller's pipefail a log with many markers then
# fails this function, and a HARNESS ERROR near the top of a long log reads as
# an assertion (#2360).
failure_markers() { # failure_markers <file> [limit]
  strip_ansi "$1" |
    grep -aE '^[[:space:]]*(×|✗|!|FAIL |--- FAIL|AssertionError|Error:|HARNESS ERROR|panic:|ran [0-9]+ assertions)' |
    sed -n "1,${2:-15}p"
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
#
# grep reads the stripped log from a process substitution: it streams, so a
# long log is never held in memory or rewritten as a here-string, and a NUL
# byte in a log does not make bash warn. Its writer's status is never read,
# so grep -q stopping at the first match cannot fail anything.
#
# Only a DECLARATION counts: a line that starts, after optional indent and an
# optional `✗`, with `HARNESS ERROR` or `INFRASTRUCTURE failure|error`, which is
# how every suite here prints one. The match used to be unanchored, so any
# mention of the phrase counted: test-ci-local-concurrency.sh's own PASS lines
# name it ("PASS: a HARNESS ERROR log classifies as infra"), and every real
# assertion failure in that suite was summarised as "the check could not run"
# (#2374), the direction the paragraph above says must never happen.
classify_failure() { # classify_failure <file> <exit-code>
  if grep -qaE '^[[:space:]]*(✗[[:space:]]*)?(HARNESS ERROR|INFRASTRUCTURE (failure|error))' \
    < <(strip_ansi "$1"); then
    printf 'infra\n'
  else
    printf 'assert\n'
  fi
}
