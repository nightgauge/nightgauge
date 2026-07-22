---
description: Run Nightgauge validation stage for an issue.
argument-hint: "<issue-number> [extra stage args]"
---

Run the Nightgauge Codex adapter wrapper for feature validation.

1. If no issue number is provided, ask for one.
2. Run `scripts/run-stage.sh codex feature-validate $ARGUMENTS`.
3. Summarize the result and suggest the next stage command.
