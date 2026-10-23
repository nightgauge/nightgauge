# Orchestrator-crash reader fixtures

`phantom-record.jsonl` pins the backup-write population that the history
filters intentionally suppress: one pending stage, no completed or running
work, zero tokens, zero cost, and no terminal-failure marker. It is paired in
the reader tests with the real captured
`../undetermined-branch/crash-record.jsonl`, whose
`terminal_failure_kind: "orchestrator_crash"` positively identifies a real
terminal failure despite the otherwise similar one-stage, zero-cost shape.

The phantom fixture is deliberately not a synthesized crash record. Adding a
terminal marker to it changes the population under test and must make it
visible.
