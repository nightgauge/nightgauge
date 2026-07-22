### Retrospective Feedback Capture

## Contents

- [Step RF.0: Detect Execution Mode](#step-rf0-detect-execution-mode)
- [Step RF.1: Prepare Question Payload](#step-rf1-prepare-question-payload)
- [Step RF.2: Prompt User](#step-rf2-prompt-user)
- [Step RF.3: Parse Response](#step-rf3-parse-response)
- [Step RF.4: Validate & Truncate](#step-rf4-validate--truncate)
- [Step RF.5: Update Context File](#step-rf5-update-context-file)
- [Step RF.6: Write Assessment Record](#step-rf6-write-assessment-record-optional)

**PURPOSE**: Capture post-merge workflow feedback from the user after a
successful PR merge. This phase is **non-blocking** — errors at any step are
logged but never cause the pipeline to exit with a failure. In headless mode the
prompt is skipped entirely; only interactive sessions collect responses.

#### Step RF.0: Detect Execution Mode

```bash
if [ -t 0 ]; then
  EXECUTION_MODE="interactive"
else
  EXECUTION_MODE="headless"
fi
```

#### Step RF.1: Prepare Question Payload

```bash
RETROSPECTIVE_QUESTIONS_JSON=$(cat <<'EOF'
{
  "questions": [
    {
      "id": "q0",
      "header": "What Went Well",
      "text": "What went well in the workflow for this issue?",
      "multiSelect": true,
      "options": [
        "Smooth execution — no blockers",
        "Good documentation — easy to follow",
        "Fast turnaround",
        "Other"
      ]
    },
    {
      "id": "q1",
      "header": "Areas to Improve",
      "text": "What could be improved in the workflow?",
      "multiSelect": true,
      "options": [
        "Clearer requirements — more detail needed",
        "Better documentation in codebase",
        "Faster CI/build process",
        "Better skill feedback/guidance",
        "Other"
      ]
    }
  ]
}
EOF
)
```

#### Step RF.2: Prompt User

**Interactive mode**: Use the `AskUserQuestion` tool, passing
`$RETROSPECTIVE_QUESTIONS_JSON` as the question payload. Store the tool's JSON
response in `RETROSPECTIVE_RESPONSE`. Both questions are optional — the user may
skip either or both.

**Headless mode**: Log that retrospective feedback is only collected in
interactive sessions and set `RETROSPECTIVE_RESPONSE` to empty JSON:

```bash
if [ "$EXECUTION_MODE" = "headless" ]; then
  echo "NOTE: Retrospective feedback skipped — interactive session required"
  RETROSPECTIVE_RESPONSE="{}"
fi
```

#### Step RF.3: Parse Response

```bash
WHAT_WENT_WELL=$(echo "$RETROSPECTIVE_RESPONSE" | jq -r '.answers.q0 // ""' 2>/dev/null || echo "")
WHAT_TO_IMPROVE=$(echo "$RETROSPECTIVE_RESPONSE" | jq -r '.answers.q1 // ""' 2>/dev/null || echo "")
```

If either value is a JSON array (multi-select), convert it to a
comma-joined string:

```bash
_join_if_array() {
  local val="$1"
  if echo "$val" | jq -e 'type == "array"' >/dev/null 2>&1; then
    echo "$val" | jq -r 'join(", ")'
  else
    echo "$val"
  fi
}
WHAT_WENT_WELL=$(_join_if_array "$WHAT_WENT_WELL")
WHAT_TO_IMPROVE=$(_join_if_array "$WHAT_TO_IMPROVE")
```

Any parse error is treated as an empty string — this step is non-blocking.

#### Step RF.4: Validate & Truncate

Truncate each field to 500 characters maximum:

```bash
WHAT_WENT_WELL="${WHAT_WENT_WELL:0:500}"
WHAT_TO_IMPROVE="${WHAT_TO_IMPROVE:0:500}"

if [ -z "$WHAT_WENT_WELL" ] && [ -z "$WHAT_TO_IMPROVE" ]; then
  RETROSPECTIVE_CAPTURED=false
else
  RETROSPECTIVE_CAPTURED=true
fi
```

#### Step RF.5: Update Context File

Merge the retrospective data into the existing `pr-${ISSUE_NUMBER}.json` context
file using a safe temp-file swap:

```bash
CONTEXT_FILE=".nightgauge/pipeline/pr-${ISSUE_NUMBER}.json"
CAPTURED_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

if [ -f "$CONTEXT_FILE" ]; then
  TMP_FILE=$(mktemp)
  if jq \
    --arg went_well "$WHAT_WENT_WELL" \
    --arg improve "$WHAT_TO_IMPROVE" \
    --arg captured_at "$CAPTURED_AT" \
    --arg exec_mode "$EXECUTION_MODE" \
    '.retrospective_feedback = {
      what_went_well: (if $went_well == "" then [] else [$went_well] end),
      what_could_improve: (if $improve == "" then [] else [$improve] end),
      captured_at: $captured_at,
      execution_mode: $exec_mode
    }' "$CONTEXT_FILE" > "$TMP_FILE" 2>/dev/null; then
    mv "$TMP_FILE" "$CONTEXT_FILE"
    echo "Retrospective feedback written to $CONTEXT_FILE"
  else
    rm -f "$TMP_FILE"
    echo "WARNING: Failed to update context file with retrospective feedback" >&2
  fi
else
  echo "WARNING: Context file $CONTEXT_FILE not found — skipping retrospective update" >&2
fi
```

#### Step RF.6: Write Assessment Record (Optional)

Only when `WHAT_TO_IMPROVE` is non-empty, write a minimal improvement record:

```bash
if [ -n "$WHAT_TO_IMPROVE" ]; then
  ASSESSMENT_DIR=".nightgauge/pipeline/assessments"
  mkdir -p "$ASSESSMENT_DIR"
  ASSESSMENT_FILE="$ASSESSMENT_DIR/pr-merge-retrospective-${ISSUE_NUMBER}.json"

  IMPROVEMENTS_JSON=$(echo "$WHAT_TO_IMPROVE" | jq -Rc 'split(", ")')

  cat > "$ASSESSMENT_FILE" <<RECORD
{
  "stage": "pr-merge",
  "phase": "retrospective",
  "issue_number": ${ISSUE_NUMBER},
  "execution_mode": "${EXECUTION_MODE}",
  "captured_at": "${CAPTURED_AT}",
  "improvements_suggested": ${IMPROVEMENTS_JSON}
}
RECORD

  echo "Improvement record written to $ASSESSMENT_FILE"
fi
```

#### Error Handling

All steps in this fragment are **non-blocking**. No step should call `exit 1`.
Failures are surfaced as `WARNING:` or `NOTE:` log lines to stderr so they are
visible in pipeline output without halting execution. If `jq` or `mktemp` are
unavailable, log the absence and skip the affected step gracefully.
