---
description: Run Nightgauge planning stage for an issue.
argument-hint: "<issue-number> [extra stage args]"
---

Run the Nightgauge Codex adapter wrapper for feature planning.

1. If no issue number is provided, ask for one.
2. Run `scripts/run-stage.sh codex feature-planning $ARGUMENTS`.
3. Summarize the result and suggest the next stage command.
