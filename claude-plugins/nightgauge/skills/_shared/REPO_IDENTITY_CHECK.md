### Repo Identity Assertion (HARD GATE — non-recoverable)

If the `NIGHTGAUGE_TARGET_REPO` environment variable is set, the orchestrator
has pinned the exact repo this stage must run in. Verify the current repository
matches **before doing any work**:

```bash
if [ -n "$NIGHTGAUGE_TARGET_REPO" ]; then
  # Derive this checkout's identity from its own git remote. The question
  # is "which repository is this working directory?", and the remote answers
  # it exactly — locally, with no API call. This used to shell out to the
  # GitHub CLI for the same string, spending a request per stage on every
  # repo in the workspace, invisible to `nightgauge api-usage`.
  ACTUAL_REPO=$(git remote get-url origin 2>/dev/null \
    | sed -E 's#^git@[^:]+:#-#; s#^https?://[^/]+/#-#; s#^-##; s#\.git$##')
  if [ -n "$ACTUAL_REPO" ] && [ "$ACTUAL_REPO" != "$NIGHTGAUGE_TARGET_REPO" ]; then
    echo "[repo-mismatch] FATAL: repository identity assertion failed"
    echo "  Expected: $NIGHTGAUGE_TARGET_REPO"
    echo "  Actual:   $ACTUAL_REPO"
    echo "  CWD:      $(pwd)"
    echo ""
    echo "The orchestrator expected this stage to run in $NIGHTGAUGE_TARGET_REPO"
    echo "but the working directory belongs to $ACTUAL_REPO."
    exit 1
  fi
fi
```

**This is a terminal, non-recoverable stop condition. If the check prints
`[repo-mismatch]` and exits 1, you MUST immediately end the stage with a failure.**

Specifically, on a mismatch you must NOT:

- `cd` into a different directory, switch worktrees, or `gh repo set-default` to
  "make it match" — the orchestrator owns CWD; second-guessing it corrupts state.
- Continue to later phases, create or push a branch, or open a PR — every
  downstream artifact would land in the wrong repo.
- Ask the user a question (`AskUserQuestion`) — autonomous runs have no human at
  the prompt; the question is auto-dismissed and you would proceed on a guess.
- Write a context/assessment file that claims success.

The correct and ONLY action is to stop now and let the stage exit non-zero. This
is a configuration fault in repo routing (the orchestrator set the wrong
`NIGHTGAUGE_TARGET_REPO` for this worktree); it is fixed upstream in the
orchestrator, never worked around inside the stage. The `[repo-mismatch]` marker
is how the pipeline classifies and surfaces it.

This check is a no-op — and the stage proceeds normally — when
`NIGHTGAUGE_TARGET_REPO` is unset (e.g. manual single-stage CLI
invocations) or already matches the current repo.
