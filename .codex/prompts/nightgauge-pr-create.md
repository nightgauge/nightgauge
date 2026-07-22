---
description: Run Nightgauge PR creation stage for an issue.
argument-hint: "<issue-number> [extra stage args]"
---

Run the Nightgauge Codex adapter wrapper for PR creation.

1. If no issue number is provided, ask for one.
2. Run `scripts/run-stage.sh codex pr-create $ARGUMENTS`.
3. Summarize the result and suggest the next stage command.
