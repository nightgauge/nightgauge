---
description: Start Nightgauge issue-to-PR flow by picking up an issue.
argument-hint: "<issue-number> [extra stage args]"
---

Run the Nightgauge Codex adapter wrapper for issue pickup.

1. If no issue number is provided, ask for one.
2. Run `scripts/run-stage.sh codex issue-pickup $ARGUMENTS`.
3. Summarize the result and suggest the next stage command.
