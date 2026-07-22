# Stop-Hook Output Audit

> Audit of every source of Stop-hook-related output that can reach a subagent
> transcript during a pipeline run, with the resolved status of each.
>
> Companion to PR fixing issue #3262. PR #3234 (issue #3204) fixed the
> hard-fail mechanism in `claude-plugins/nightgauge/hooks/lib/guard.sh`;
> this audit catalogs the residual sources that still leaked into subagent
> transcripts on the autonomous run of issue #3224.

## Summary of Findings

| Source                                                    | Status         | Fix                                                                          |
| --------------------------------------------------------- | -------------- | ---------------------------------------------------------------------------- |
| Plugin Stop hook (`stop-verification.sh`)                 | Fixed          | PR #3234 (binary resolution) + this PR (silent stderr)                       |
| `guard.sh` graceful-skip stderr                           | Fixed          | This PR — `NIGHTGAUGE_HOOK_SILENT=true` (default) routes to side-channel log |
| Skill Phase 0 preflight (PREFLIGHT.md + 16 inline copies) | Fixed          | This PR — 5-step cascade replicated; matches `guard.sh`                      |
| Bare `nightgauge` calls inside skill bodies               | Fixed          | This PR — preflight prepends `dirname($BINARY)` to `PATH` after resolution   |
| User `~/.claude/settings.json` Stop hooks                 | Not registered | No change required (user-owned scope)                                        |
| Project `.claude/settings.json` Stop hooks                | Not registered | No change required                                                           |
| Other plugin hooks (`pre-push-validate.sh`, etc.)         | Not Stop-event | Not in scope (don't fire at the Stop event)                                  |

## 1. Plugin Stop Hook (`stop-verification.sh` → `lib/guard.sh`)

**File**: `claude-plugins/nightgauge/hooks/stop-verification.sh`
**Registration**: `claude-plugins/nightgauge/hooks/hooks.json`, `Stop` matcher.

The plugin registers a single Stop hook. The wrapper sources `lib/guard.sh` to
locate the `nightgauge` Go binary, then `exec`s
`nightgauge hook stop-verify`.

**Pre-#3234**: When invoked from a worktree (concurrent-mode pipeline), the
wrapper hard-failed with `nightgauge: command not found` because the
worktree's own `bin/` directory does not contain the build artifact. PR #3234
landed a 5-step resolution cascade in `guard.sh` that includes
`git rev-parse --git-common-dir` to find the canonical repo's `bin/`. After
that landed, the hard-fail was eliminated.

**Pre-#3262 residual**: On the graceful-skip path (binary truly missing),
`guard.sh` still wrote `[hook-skipped] nightgauge binary not found …`
to stderr. The Claude CLI surfaces hook stderr to the parent agent as a
`stop-hook-error`-style notification regardless of exit code — so the
"graceful" skip was still noisy. The autonomous run of #3224 emitted these
notifications across five stages, occasionally causing the LLM to interpret
"stop now" and exit early.

**This PR**: Adds an `NIGHTGAUGE_HOOK_SILENT` env var (default `true`)
that routes the skip notice to a side-channel log file
(`~/.nightgauge/hook-warnings.log`, overridable via
`NIGHTGAUGE_HOOK_LOG`) instead of stderr. The blocking-error path
(`NIGHTGAUGE_HOOK_BLOCKING=true`) keeps stderr output because that's
the legitimate user-facing diagnostic AC3 requires preserving.

## 2. Skill Phase 0 Preflight (`skills/_shared/PREFLIGHT.md` + Inline Copies)

**Files**: `skills/_shared/PREFLIGHT.md` plus inline copies in 16 individual
`SKILL.md` files (and several `_shared/*.md` helper docs that gate on the
binary). Each skill inlines its own preflight bash because Markdown does not
support `include`.

**Pre-#3262 lookup**: Two-step:

1. `command -v nightgauge` (PATH lookup)
2. `$REPO_ROOT/bin/nightgauge` (where `REPO_ROOT = git rev-parse
--show-toplevel`)

This was already known broken when invoked from a worktree — the same
mechanism that broke the Stop hook pre-#3234. From a worktree, step 2
resolves `REPO_ROOT` to the worktree path (not the canonical repo), and the
preflight `exit 1`ed. The Bash tool surfaced the failure as
`nightgauge not found` in the subagent's tool-result stream.

**This PR**: Replicates `guard.sh`'s 5-step cascade in `PREFLIGHT.md` and in
every inlined copy. Resolution order:

1. PATH lookup (`command -v nightgauge`).
2. `$REPO_ROOT/bin/nightgauge` (same-repo build).
3. `$(git rev-parse --git-common-dir)/../bin/nightgauge`
   (canonical-repo lookup from inside a worktree).
4. `$HOME/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge`
   (VSCode extension–bundled binary).
5. `$HOME/go/bin/nightgauge` (`go install` default).

The skill preflight still `exit 1`s with a visible message when the binary
remains unresolvable — that's the legitimate diagnostic AC3 requires
preserving for genuine missing-binary cases.

**Drift risk**: The cascade now exists in two places (`guard.sh` and
`PREFLIGHT.md`). Both files include a header comment pointing at the other,
and this audit lists them as authoritative copies. Future changes to one
require a matching change to the other.

## 3. Bare `nightgauge` Calls Inside Skill Bodies

**Files**: ~30+ skill bodies contain bare `nightgauge ...` invocations
(e.g. `nightgauge format run`, `nightgauge ci parity-check`). When
the binary is resolved via a fallback location (canonical-repo bin, VSCode
extension bundle, `~/go/bin`) but is **not** on the user's `PATH`, these bare
calls fail with "command not found" — surfacing as another
`nightgauge not found` notification in the subagent transcript.

**This PR**: After the preflight resolves `$BINARY`, the new cascade exports
`PATH="$(dirname "$BINARY"):$PATH"`. This propagates the resolved location to
the bash subprocess, so subsequent bare `nightgauge ...` calls in the
same skill body resolve via PATH. The change is scoped to the bash invocation
and does not leak to other tools or the parent agent.

A blanket sweep of 30+ skill files to replace bare calls with `"$BINARY" ...`
was rejected as too risky for a noise-cleanup PR — the PATH injection closes
the gap with a 1-line addition per preflight.

## 4. User-Level `~/.claude/settings.json`

**Status**: No `Stop` event hook registered. The user's settings file
registers only a `Notification` hook (`osascript` for macOS notifications).
Audited during this PR's work. User retains ownership of this file; this
PR does not modify it.

## 5. Project-Level `.claude/settings.json`

**Status**: Registers `PreToolUse` (file protection), `PostToolUse` (auto-
prettier), and `SessionStart:compact` hooks. **No `Stop` hook registered.**
This file is not a source of stop-hook-error notifications during pipeline
execution.

## 6. Other Plugin Hooks (Non-Stop)

The plugin registers nine hooks across `Notification`, `PostToolUse`,
`PreToolUse`, `Stop`, and `SessionStart`. All non-Stop hooks source the same
`lib/guard.sh` for binary resolution but do **not** fire at the Stop event,
so their stderr output cannot be surfaced as a `stop-hook-error` notification.
For completeness:

| Hook                   | Event        | Fires at Stop? |
| ---------------------- | ------------ | -------------- |
| `notify.sh`            | Notification | No             |
| `format-on-save.sh`    | PostToolUse  | No             |
| `version-check.sh`     | PostToolUse  | No             |
| `test-quality.sh`      | PostToolUse  | No             |
| `workflow-gate.sh`     | PreToolUse   | No             |
| `prompt-sanitize.sh`   | PreToolUse   | No             |
| `stop-verification.sh` | Stop         | **Yes**        |
| `inject-context.sh`    | SessionStart | No             |

Other plugin scripts that source `guard.sh` but are not registered as Claude
Code hooks (e.g. `pre-push-validate.sh`) still inherit the new silent-by-
default behavior; they cannot leak into subagent transcripts because they're
git hooks, not Claude hooks.

## 7. Verification Protocol (AC2)

To reproduce the controlled 10-stage no-op run cited in AC2:

1. Open a fresh terminal in a worktree directory, ensuring `nightgauge`
   is **not** on PATH and `bin/nightgauge` is absent (the canonical
   reproduction of the worktree-mode failure).
2. Dispatch a `chore: noop` issue through the autonomous orchestrator:

   ```bash
   nightgauge autonomous run --issue <noop-issue-number>
   ```

3. Inspect the resulting session log for the two failure strings:

   ```bash
   grep -c 'stop-hook-error' .nightgauge/autonomous/<run-id>/session.log
   grep -c 'nightgauge not found' .nightgauge/autonomous/<run-id>/session.log
   ```

4. Both counts must be **zero**. The side-channel log
   (`~/.nightgauge/hook-warnings.log`) may contain `[hook-skipped]`
   entries — that's expected and proves the silent path is working.

## Side-Channel Log Contract

`~/.nightgauge/hook-warnings.log` is a new producer of diagnostic data
introduced by this PR. The path is overridable via the
`NIGHTGAUGE_HOOK_LOG` environment variable. No consumer exists today —
the file is for the user to inspect when debugging hooks. Future work could
add a VSCode extension panel that surfaces it; that's not in scope here.

## See Also

- Issue #3262 — fix: residual stop-hook-error sources after PR #3234
- Issue #3204 — original Stop-hook hard-fail in worktree mode
- PR #3234 — `guard.sh` 5-step cascade
- `claude-plugins/nightgauge/hooks/lib/guard.sh` — canonical cascade
- `skills/_shared/PREFLIGHT.md` — replicated cascade for skill preflight
