#!/usr/bin/env bash
# Captures a REAL `ps -axo pid=,etime=,command=` snapshot and redacts it into a
# committable public fixture (#341). The parser under test reads exactly this
# column shape, and a shape recalled from memory is not evidence that it does.
#
#   bash internal/doctor/testdata/orphaned-processes/capture-ps-snapshot.sh \
#     > internal/doctor/testdata/orphaned-processes/ps-snapshot.txt
#
# Given a path argument the snapshot is read from that file instead of `ps`, so
# a redaction change can be re-applied to an existing raw capture.
#
# What survives, and why:
#   * every nightgauge process, argv INTACT — argv is the evidence the
#     classifier reads (subcommand, --dry-run), so truncating it would remove
#     the thing under test;
#   * up to two non-nightgauge processes per etime format (mm:ss, hh:mm:ss,
#     dd-hh:mm:ss), reduced to their executable path — the fixture must carry
#     all three formats, and a third-party process's ARGUMENTS are where
#     secrets and private paths live;
#   * every line's original column spacing, byte for byte outside the
#     substitutions below.
#
# What is scrubbed: the capturing machine's home directory path (replaced with
# /Users/operator) and its login name (replaced with `operator`), anywhere they
# appear.
set -euo pipefail

if [ $# -ge 1 ]; then
	raw=$(cat "$1")
else
	raw=$(ps -axo pid=,etime=,command=)
fi

printf '%s\n' "$raw" | awk -v home="${HOME:-/nonexistent}" -v user="$(id -un)" '
	# Literal (non-regex) replace: a home path or login name may contain
	# characters gsub would read as metacharacters.
	function replace(s, from, to,   out, i) {
		if (from == "") return s
		out = ""
		while ((i = index(s, from)) > 0) {
			out = out substr(s, 1, i - 1) to
			s = substr(s, i + length(from))
		}
		return out s
	}
	function scrub(s) {
		s = replace(s, home, "/Users/operator")
		return replace(s, user, "operator")
	}
	{
		if (!match($0, /^[ \t]*[0-9]+[ \t]+[^ \t]+[ \t]+/)) next
		prefix = substr($0, 1, RLENGTH)
		cmd = substr($0, RLENGTH + 1)
		etime = $2

		split(cmd, argv, " ")
		split(argv[1], seg, "/")
		if (seg[length(seg)] == "nightgauge") {
			print prefix scrub(cmd)
			next
		}

		format = "mmss"
		if (etime ~ /^[0-9]+-/) format = "ddhhmmss"
		else if (etime ~ /^[0-9]+:[0-9][0-9]:[0-9][0-9]$/) format = "hhmmss"
		if (kept[format] >= 2) next
		kept[format]++
		print prefix scrub(argv[1])
	}
'
