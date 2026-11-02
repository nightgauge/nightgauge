## Passes

Each pass reads the diff and the files it touches, and reports concrete,
actionable findings only. A pass does not restate what the code does and does
not praise it.

| Pass              | Asks                                                                                                 |
| ----------------- | ---------------------------------------------------------------------------------------------------- |
| `correctness`     | Does the change do what it claims? Logic errors, unhandled cases, broken contracts                   |
| `security`        | Does it apply `standards/security.md`? Secrets, injection, unvalidated input, over-broad permissions |
| `plan-compliance` | Does it deliver the issue it closes, and only that? Missing criteria, scope creep                    |

`plan-compliance` uses the bodies of the issues the pull request closes. When
it closes none, the pass reports nothing.

## Severity

The scale is the one in `skills/nightgauge-adversarial-review/SKILL.md`. Both use
this definition.

| Severity  | Meaning                                                                 |
| --------- | ----------------------------------------------------------------------- |
| `blocker` | The change is wrong or unsafe as written and should not merge like this |
| `major`   | A real defect or gap the author should fix before merging               |
| `nitpick` | Style, naming or polish; the author decides                             |

## Finding schema

The review writes one JSON file with exactly this shape. Any other key is
rejected.

```json
{
  "schema": 1,
  "findings": [
    {
      "pass": "correctness",
      "file": "internal/forge/pr.go",
      "line": 42,
      "claim": "One line stating the defect.",
      "severity": "major",
      "fix": "What to change."
    }
  ]
}
```

- `file` is a repository-relative path. It is not absolute and has no `..`
  segment.
- `line` is a positive integer, or `null` for a finding about the whole file.
- `claim` is one line of at most 300 characters.
- `fix` is at most 1000 characters.
- No string may contain a credential shape (`sk-ant-`, `ghp_`, `ghs_`, `gho_`,
  `ghu_`, `github_pat_`). The model runs beside the model token, so such a string
  is treated as a leak and the whole file is rejected.

## Caps

| Limit             | Value | Effect                                                    |
| ----------------- | ----- | --------------------------------------------------------- |
| Findings per file | 30    | More is rejected; nothing is posted                       |
| Nitpicks shown    | 5     | The rest are counted in an "N more nitpicks omitted" line |
| `claim` length    | 300   | Longer is rejected                                        |
| `fix` length      | 1000  | Longer is rejected                                        |
| Turns per review  | 80    | Reaching it stops the review; the job fails, no comment   |
