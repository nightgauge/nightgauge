OpenCode is the execution host, whatever model is dispatched. Tool ids
are lowercase (`bash`, `read`, `edit`, `write`, `grep`, `glob`, `task`,
`skill`) — a capitalized Claude-style name will not match a permission
rule. An unpermitted tool call is rejected silently and the run
continues rather than erroring, so never retry the same call expecting
a different result; if the stage cannot proceed without it, fail the
stage with a clear reason instead of retrying. There is no interactive
user and no `AskUserQuestion` — an undecidable choice fails the stage
rather than asking. Read `_includes` at the absolute paths this render
already resolved them to, not relative to the working directory. Never
edit `opencode.json` or anything under `.opencode/` — that is host
configuration, not stage output.
