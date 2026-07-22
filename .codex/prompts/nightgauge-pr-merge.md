---
description: Run Nightgauge PR merge stage for an issue.
argument-hint: "<issue-number> [extra stage args]"
---

Run the Nightgauge Codex adapter wrapper for PR merge.

1. If no issue number is provided, ask for one.
2. Run `scripts/run-stage.sh codex pr-merge $ARGUMENTS`.
3. Summarize the result and report pipeline completion status.
