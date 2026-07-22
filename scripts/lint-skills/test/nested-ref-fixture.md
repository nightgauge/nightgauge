# Nested-Reference Fixture — `anti-patterns.sh` / `skill-anti-patterns`

> **Do not edit.** This file is the sentinel for the nested-reference negative
> test in `.github/workflows/lint.yml`. The CI job copies it into a temporary
> `skills/<name>/_includes/` tree, runs the linter, and asserts it exits 1. If
> the linter ever stops firing on the line below, the negative test fails.

This is a SUPPORTING file (it lives under `_includes/`). It intentionally
directs the agent to read ANOTHER supporting file, which is the nested-
reference anti-pattern (references must be one level deep):

> **Read `skills/example/_includes/nested-ref-child.md` now** and follow its
> instructions before continuing.

End of fixture.
