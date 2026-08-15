Grok Build is the execution host. Prefer the deterministic `nightgauge`
binary for board, state, and forge operations. Headless Grok does not
read piped stdin as the prompt. Do not depend on Claude Stop hooks or
`AskUserQuestion` — they are not available here. If a decision is
undecidable without the operator, fail the stage with a clear reason.
Subagent fan-out is optional; if a subagent cannot be launched, do the
work in the main context.
