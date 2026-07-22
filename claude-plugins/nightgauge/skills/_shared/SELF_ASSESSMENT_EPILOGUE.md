### Self-Assessment Epilogue

**PURPOSE**: Evaluate whether this skill's instructions matched reality during
this execution. This phase is **non-blocking** — skip entirely if any main phase
failed. A perfectly working skill produces **no output** from this phase.

> See [docs/SKILL_SELF_ASSESSMENT.md](../../docs/SKILL_SELF_ASSESSMENT.md) for
> the full strategy, synthesis algorithm, and integration architecture.

#### When to Skip

- Any prior phase exited with an error before completing
- The skill was cancelled or timed out
- Running in `--dry-run` mode

#### Step 1: Evaluate Execution Friction

Review the execution that just completed. Answer these questions honestly:

1. **Command failures**: Did any command, script, or binary call in the skill
   instructions fail? (e.g., script not found, wrong flags, missing binary)
2. **Workarounds**: Did you have to deviate from the skill instructions to
   accomplish the goal? (e.g., used `gh` directly because a referenced script
   didn't exist, skipped a step that referenced a nonexistent file)
3. **Stale references**: Did any file path, function name, API endpoint, or tool
   referenced in the instructions not exist in the current codebase?
4. **Unclear instructions**: Were any instructions ambiguous enough that you had
   to guess at the intended behavior?
5. **Missing instructions**: Was there a significant step you had to figure out
   on your own that should have been documented in this skill?

**If ALL answers are "no" — write nothing and complete the skill normally.** The
goal is silence when everything works.

#### Step 2: Write Assessment Record

Only if friction was detected in Step 1. Write a single JSON file:

```bash
ASSESSMENT_DIR=".nightgauge/pipeline/assessments"
mkdir -p "$ASSESSMENT_DIR"
```

**File**: `$ASSESSMENT_DIR/{STAGE_NAME}-${ISSUE_NUMBER}.json`

The assessment record MUST follow this schema:

```json
{
  "schema_version": "1",
  "skill": "{STAGE_NAME}",
  "skill_file": "skills/nightgauge-{STAGE_NAME}/SKILL.md",
  "issue_number": 42,
  "timestamp": "2026-03-10T14:30:00Z",
  "friction": [
    {
      "type": "command_failure",
      "severity": "high",
      "description": "hooks/lib/add-to-project.sh not found — script was deleted",
      "skill_line_hint": "claude-plugins/nightgauge/hooks/lib/add-to-project.sh <issue-number>",
      "actual_resolution": "Used gh api graphql to add issue to project board directly",
      "suggested_fix": "Replace with: nightgauge project add <issue-number>"
    }
  ]
}
```

**Friction types**: `command_failure`, `workaround`, `stale_reference`,
`unclear_instruction`, `missing_instruction`

**Severity levels**:

- `high` — instruction is **broken**. Required manual workaround to complete.
- `medium` — instruction is **misleading**. Agent adapted without user help.
- `low` — instruction is **suboptimal**. No functional impact.

**Rules**:

- **One record per execution** — multiple friction items go in the `friction`
  array, not separate files.
- **Be specific** — quote the exact instruction text that was wrong. Not "some
  commands didn't work" but "Step 5.2 calls `hooks/lib/add-to-project.sh` but
  this script was deleted in commit 65915701."
- **Suggest the fix** — every finding MUST include `suggested_fix` with the
  concrete SKILL.md change needed. Not "update the docs" but "replace
  `hooks/lib/add-to-project.sh <N>` with `nightgauge project add <N>`."
- **Don't invent friction** — only report issues you actually encountered during
  this execution. Do not speculate about potential problems.

#### Step 3: Validate and Complete

```bash
# Validate JSON if written
if [ -f "$ASSESSMENT_FILE" ]; then
  python3 -m json.tool "$ASSESSMENT_FILE" > /dev/null 2>&1 || \
    echo "WARNING: Assessment record is not valid JSON" >&2
fi
```
