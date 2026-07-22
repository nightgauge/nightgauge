# Regression Fixture — `no-direct-gh.sh`

> **Do not edit.** This file is the sentinel for the negative test in
> `.github/workflows/lint.yml`: the CI job copies it to a temporary
> location under `skills/__lint_negative__/SKILL.md`, runs
> `nightgauge preflight skill-no-direct-gh --root <tmp>`, and asserts
> the linter exits 1. If the linter ever stops firing on the line below,
> the negative test fails — guaranteeing the deprecation gate is real.

The line below intentionally contains a direct `gh` call so the linter
catches it:

```bash
gh issue view 1 --json number,title
```

End of fixture.
