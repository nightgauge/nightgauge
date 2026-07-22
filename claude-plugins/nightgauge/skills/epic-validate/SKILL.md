---
name: epic-validate
description: DEPRECATED — thin wrapper that delegates to nightgauge-issue-audit
  --epic <N>. Use only when an existing slash-command caller or documented workflow
  still invokes epic-validate; new code should call /nightgauge:issue-audit directly.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "2.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
disable-model-invocation: true
---

# Epic Validate (Deprecated — Wrapper)

> **DEPRECATED — use `/nightgauge:issue-audit --epic <N>` directly.**
>
> This skill now delegates to `/nightgauge:issue-audit`, which subsumes
> the previous epic-only validation with a universal post-creation audit
> that covers every issue-creation flow (single, sub-issue, epic, batch,
> cross-repo decomposition). The slash-command surface is preserved so
> existing scripts and documented workflows continue to work, but new code
> SHOULD call `issue-audit` directly. See
> [docs/ISSUE_AUDIT.md](../../docs/ISSUE_AUDIT.md) for the full audit
> taxonomy and severity rules.

## When to Use

- **Don't** — call `/nightgauge:issue-audit --epic <N>` instead.
- This wrapper exists only to preserve backwards compatibility with the
  existing slash-command surface.

## Outcomes (delegated)

`issue-audit --epic <N>` produces the same outcomes the previous
implementation produced, plus more — see
[docs/ISSUE_AUDIT.md](../../docs/ISSUE_AUDIT.md):

- Sub-issue linking, project board membership, `blockedBy` alignment, body
  section completeness, cross-repo consistency, and knowledge scaffold
  validation
- Severity-tiered Markdown report at
  `.nightgauge/pipeline/issue-audit-<timestamp>.md`
- JSON findings for CI consumption
- `--fix` / `--fix-interactive` repair via existing Go binary primitives
- Exit 0 when verdict is READY; exit 1 when CRITICAL findings remain

## Input

```
/nightgauge:epic-validate <epic-number> [--repo <owner/repo>]
```

All flags pass through to `issue-audit --epic <N>` unchanged.

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Delegate to issue-audit

```bash
EPIC_NUMBER="$1"
shift
if [ -z "$EPIC_NUMBER" ]; then
  echo "ERROR: epic number is required" >&2
  echo "Usage: /nightgauge:epic-validate <epic-number> [--repo <owner/repo>] [--fix]" >&2
  exit 2
fi

echo "NOTE: /nightgauge:epic-validate is DEPRECATED."
echo "      This invocation delegates to /nightgauge:issue-audit --epic $EPIC_NUMBER"
echo "      New code should call /nightgauge:issue-audit directly."
echo ""

# Forward every remaining argument verbatim (--repo, --fix, --json, etc.)
exec /nightgauge:issue-audit --epic "$EPIC_NUMBER" "$@"
```

The `exec` form replaces this skill's process with the audit invocation, so
the audit's exit code propagates naturally and the wrapper adds zero
overhead beyond the deprecation banner.

---

## Decision Rules

- This skill performs no validation logic itself. All decisions are made by
  `issue-audit`.
- The deprecation banner is emitted to stderr-equivalent output (above the
  delegated call) so users learn the new invocation. It does not affect the
  exit code.

## Failure Conditions

The wrapper itself fails when:

- No epic number is supplied (exit 2)
- The `issue-audit` skill is not installed (exit 2)

All other failure conditions are owned by `issue-audit` and propagate
unchanged.

## Removal Plan

This wrapper will be retired after a deprecation window. Track the removal
in [docs/DEPRECATIONS.md](../../docs/DEPRECATIONS.md) when scheduling the
cleanup PR. Until then the wrapper stays in place — the slash-command
surface MUST remain working for existing callers.

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->
