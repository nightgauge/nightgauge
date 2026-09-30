#!/bin/bash
INPUT=$(cat)
FILE_PATH=$(echo "$INPUT" | jq -r '.tool_input.file_path // empty')

if [ -z "$FILE_PATH" ]; then
  exit 0
fi

# ".git/" also covers per-clone state under <git-common-dir>/nightgauge/
# (ADR-024 § 7): agents write it through `nightgauge layout write`, never by path.
PROTECTED_PATTERNS=(".env" "package-lock.json" ".git/" "secrets" "credentials")

for pattern in "${PROTECTED_PATTERNS[@]}"; do
  if [[ "$FILE_PATH" == *"$pattern"* ]]; then
    echo "Blocked: $FILE_PATH matches protected pattern '$pattern'. This file should not be modified by Claude." >&2
    exit 2
  fi
done

exit 0
