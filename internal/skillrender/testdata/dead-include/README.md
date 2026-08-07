# Dead-include capture (#337)

`pr-merge.pre-fix.rendered.md` is a **real capture of what the model received**,
not a hand-authored example. It is the composed `pr-merge` skill text as it
shipped from `main` before this fix — 1684 lines, including the literal HTML
comment that stood in for the batch-mode instructions.

## Why a capture and not a written example

#337 is a #166-class silent no-op: include expansion is fail-open, so a
directive whose target does not exist is left in the output verbatim
(`skillrender.ExpandIncludes` — "A missing target is left as-is: the same
document must remain readable under a host that does not expand"). A dead
include and a deliberately-unexpanded one are therefore **byte-identical**.
Writing the fixture by hand would mean writing down what we believed the
composer emits; the whole defect is that nobody had looked. The capture is the
proof that the comment reaches the model, at a known line, in the middle of a
phase whose body it was supposed to be.

## What was captured, and how

```bash
go build -o bin/nightgauge ./cmd/nightgauge
scripts/capture-skill-render.sh pr-merge \
  > internal/skillrender/testdata/dead-include/pr-merge.pre-fix.rendered.md
```

`scripts/capture-skill-render.sh` is committed alongside the fixture so the
capture is repeatable. It shells out to `nightgauge skill render`, which is the
same code path the VSCode extension spawns
(`packages/nightgauge-vscode/src/utils/skillRunner.ts` → `renderSkill()`), so
its stdout **is** the stage prompt.

Captured from `main` at `8c942971`, before any of this branch's skill edits.

## What was redacted

`RewriteSkillRelativePaths` bakes **absolute host paths** into every render
(#196), so the capture script rewrites the repository root to the literal
placeholder `<REPO_ROOT>` and any surviving `$HOME` to `~`. This is a public
repository; no username or home directory may appear here. Nothing else is
altered — the body is verbatim, which is why `TestDeadIncludeFixture_*`
re-asserts the redaction on every run.

## What the capture shows

Line 511, the entire body of a declared phase:

````text
### Phase 0.5: Batch PR Detection

```bash
printf '<!-- phase:start name="batch-detection" index=1 total=14 stage="pr-merge" -->\n'
```

<!-- include: ../_shared/BATCH_MODE.md -->

---

### Phase 1: Validate Environment
````

The phase marker prints, the phase tracker advances, and the model is handed a
comment. `skills/_shared/BATCH_MODE.md` had never existed — `git log --all`
over that path returns nothing — and all six stage skills carried the same
directive. `pr-merge` is the worst of the six because the directive was not
supplementary context at the head of the file: it was the phase body.

The same render after the fix expands to the shared batch contract (`## Batch
Mode` at line 65) and Phase 0.5 carries a real body with a read-directive to
`_includes/batch-detection.md`. No directive survives — asserted live by
`TestRealSkillsRenderClean` and `TestSkillIncludes_WorkingTreeIsClean`, not by
a second golden file.

## Do not regenerate this file

It is a historical artifact: the pre-fix state, pinned. Re-capturing it against
the current tree would erase the evidence and silently turn the before/after
test into a tautology. `TestDeadIncludeFixture_StillExhibitsThePreFixDefect`
fails if the dead directive is edited out.
