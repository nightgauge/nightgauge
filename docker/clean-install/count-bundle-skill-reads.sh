#!/usr/bin/env bash
# Count tool calls that read the installed bundle's dist/skills (#2722, #3032).
#
# Usage: count-bundle-skill-reads.sh <vscode_logs_dir> <bundle_skills_path>
# Prints one integer: the number of tool_use entries in the stage streams that
# open a file under <bundle_skills_path>: a Read whose input.file_path, or a
# Grep/Glob whose input.path, starts with it, or a Bash call whose
# input.command names it. A line that is not a tool call (a grant, a listing,
# a tool result, prose) never counts.
#
# Stages run with --no-session-persistence, so no session transcript exists.
# The stream-json survives in the "Nightgauge Slot N" output channels, which
# VS Code persists under <logs>/**/output_logging_*/. The extension appends one
# log line per stdout chunk, so a JSON line longer than one pipe read is split
# across log lines; fragments are re-joined before parsing. A fragment that
# never completes is dropped, so errors bias toward 0 (the row fails), never
# toward a false pass.
# A missing or empty logs dir prints 0.
set -uo pipefail

LOGS="${1:?usage: count-bundle-skill-reads.sh <vscode_logs_dir> <bundle_skills_path>}"
BUNDLE="${2:?usage: count-bundle-skill-reads.sh <vscode_logs_dir> <bundle_skills_path>}"

if [[ ! -d "$LOGS" ]]; then
  echo 0
  exit 0
fi

# awk 1 ends every file with a newline so one file's last fragment cannot glue
# to the next file's first line; -exec ... + never runs with no arguments.
find "$LOGS" -type f -path '*output_logging_*' -iname '*slot*' -exec awk 1 {} + 2>/dev/null \
  | jq -R -n --arg p "$BUNDLE" '
      def obj: (try fromjson catch null) | if type == "object" then . else null end;
      reduce inputs as $line ({buf: "", objs: []};
        if .buf != "" and ((.buf + $line) | obj) != null then
          .objs += [(.buf + $line) | obj] | .buf = ""
        elif ($line | obj) != null then
          .objs += [$line | obj] | .buf = ""
        elif ($line | startswith("{")) then
          .buf = $line
        elif .buf != "" then
          .buf += $line | if (.buf | length) > 4194304 then .buf = "" else . end
        else . end)
      | [.objs[] | .message.content? | arrays | .[]
          | select(.type? == "tool_use")
          | select(
              ((.name? == "Read") and ((.input.file_path? // "") | startswith($p)))
              or ((.name? == "Grep" or .name? == "Glob")
                  and ((.input.path? // "") | startswith($p)))
              or ((.name? == "Bash") and ((.input.command? // "") | contains($p))))]
      | length' 2>/dev/null || echo 0
