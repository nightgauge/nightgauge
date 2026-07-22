# Backslash-Path Fixture — `anti-patterns.sh` / `skill-anti-patterns`

> **Do not edit.** This file is the sentinel for the backslash-path negative
> test in `.github/workflows/lint.yml`. The CI job copies it into a temporary
> skills tree, runs the linter, and asserts it exits 1.

Skills must use forward-slash paths so they work cross-platform. The line
below intentionally uses Windows backslash separators so the linter catches it:

Edit `skills\example\SKILL.md` and then open `config\settings.json`.

Note: regex escapes like `\d+`, `\bgh `, `\n`, and `\.` must NOT be flagged —
only path-like `word\word` tokens are findings.

End of fixture.
