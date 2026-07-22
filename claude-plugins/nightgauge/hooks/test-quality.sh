#!/bin/bash
# PostToolUse hook — warns on zero-value test patterns in *.test.ts / *.spec.ts
# Non-blocking: always exits 0 (warnings only).
# Skip with: NIGHTGAUGE_SKIP_TEST_QUALITY=1

set -euo pipefail

# --- Skip gate -----------------------------------------------------------
if [ "${NIGHTGAUGE_SKIP_TEST_QUALITY:-0}" = "1" ]; then
  exit 0
fi

# --- Parse stdin JSON to extract file_path --------------------------------
# PostToolUse hooks receive JSON on stdin:
#   {"tool_name":"Write","tool_input":{"file_path":"...","content":"..."}}
INPUT="$(cat)"

# Extract file_path from tool_input (lightweight jq-free parsing)
FILE_PATH="$(echo "$INPUT" | grep -oE '"file_path"\s*:\s*"[^"]*"' | head -1 | sed 's/.*"file_path"\s*:\s*"//;s/"$//')"

if [ -z "$FILE_PATH" ]; then
  exit 0
fi

# --- File-pattern filter: only *.test.ts and *.spec.ts --------------------
case "$FILE_PATH" in
  *.test.ts|*.spec.ts) ;;
  *) exit 0 ;;
esac

# --- Extract content ------------------------------------------------------
# For Write, content is in tool_input.content
# For Edit, the new content is in tool_input.new_string
# We check both to cover all cases.
CONTENT="$(echo "$INPUT" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    ti = data.get('tool_input', {})
    print(ti.get('content', '') + '\n' + ti.get('new_string', ''))
except:
    pass
" 2>/dev/null || true)"

if [ -z "$CONTENT" ]; then
  exit 0
fi

# --- Zero-value pattern checks --------------------------------------------
WARNINGS=()

# 1. Tautological assertions: expect(true).toBe(true), expect(false).toBe(false)
if echo "$CONTENT" | grep -qE 'expect\(true\)\.toBe\(true\)|expect\(false\)\.toBe\(false\)'; then
  WARNINGS+=("Tautological assertion detected (expect(true).toBe(true) or expect(false).toBe(false)). Replace with meaningful assertions.")
fi

# 2. Empty test bodies: it("...", () => {})  or  it("...", () => { })
if echo "$CONTENT" | grep -qE 'it\(.*\(\)\s*=>\s*\{\s*\}\)'; then
  WARNINGS+=("Empty test body detected. Tests must contain assertions.")
fi

# 3. console.log as only "assertion" — test body contains console.log but no expect/assert
# Check for it() blocks that have console.log but lack expect(
if echo "$CONTENT" | grep -qE 'console\.log'; then
  if ! echo "$CONTENT" | grep -qE 'expect\(|assert[.(]'; then
    WARNINGS+=("Test file contains console.log but no expect()/assert() calls. Add real assertions.")
  fi
fi

# --- Output warnings (non-blocking) --------------------------------------
if [ ${#WARNINGS[@]} -gt 0 ]; then
  echo "" >&2
  echo "⚠  Test Quality Warning — $FILE_PATH" >&2
  for w in "${WARNINGS[@]}"; do
    echo "   • $w" >&2
  done
  echo "" >&2
fi

exit 0
