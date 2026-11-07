#!/usr/bin/env bash
# Judge where each stage loaded its skill from (#3036).
#
# Usage: bundle-skill-source.sh <vscode_logs_dir> <bundle_skills_path>
# Prints one JSON object:
#   {"skill_paths": [...], "outside_bundle": [...], "pass": bool}
#
# The extension renders each stage's skill into the prompt (#3031), so a
# correct install makes no tool call under the bundle. Its provenance is the
# "[skillRunner] skill_path=<path>" line the extension writes to the
# "Nightgauge Slot N" output channels, persisted under
# <logs>/**/output_logging_*/. pass is true only when at least one skill_path
# was logged and every one starts with <bundle_skills_path> (trailing slash
# kept, so dist/skills-old/ does not count). Missing logs, no skill_path line,
# or any failure to run prints pass:false, never a false pass.
set -uo pipefail

LOGS="${1:?usage: bundle-skill-source.sh <vscode_logs_dir> <bundle_skills_path>}"
BUNDLE="${2:?usage: bundle-skill-source.sh <vscode_logs_dir> <bundle_skills_path>}"
FAIL_JSON='{"skill_paths":[],"outside_bundle":[],"pass":false}'

if [[ ! -d "$LOGS" ]]; then
  echo "$FAIL_JSON"
  exit 0
fi

# awk 1 ends every file with a newline; -exec ... + never runs with no arguments.
find "$LOGS" -type f -path '*output_logging_*' -iname '*slot*' -exec awk 1 {} + 2>/dev/null \
  | jq -R -n -c --arg p "$BUNDLE" '
      [inputs | capture("\\[skillRunner\\] skill_path=(?<p>\\S+)")? | .p] as $paths
      | [$paths[] | select(startswith($p) | not)] as $out
      | {skill_paths: $paths, outside_bundle: $out,
         pass: (($paths | length) > 0 and ($out | length) == 0)}' 2>/dev/null \
  || echo "$FAIL_JSON"
