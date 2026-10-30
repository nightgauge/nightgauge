# PR Review Passes

Pull requests the pipeline opens are reviewed inside the pipeline. Pull requests
opened by a person or by another tool are not. The
`.github/workflows/pr-review.yml` workflow closes that gap: it runs the
severity-ranked review passes defined here on every pull request the pipeline
did not author, and posts the result as one advisory comment.

This file is the committed definition. The review model reads it at run time,
and `scripts/pr-review.mjs` enforces its limits before anything is posted.

## Passes

Each pass reads the diff and the files it touches, and reports concrete,
actionable findings only. A pass does not restate what the code does and does
not praise it.

| Pass              | Asks                                                                                                                           |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `correctness`     | Does the change do what it claims? Logic errors, unhandled cases, broken contracts                                             |
| `security`        | Does it apply [standards/security.md](../standards/security.md)? Secrets, injection, unvalidated input, over-broad permissions |
| `plan-compliance` | Does it deliver the issue it closes, and only that? Missing criteria, scope creep                                              |

`plan-compliance` uses the bodies of the issues the pull request closes. When
it closes none, the pass reports nothing.

## Severity

The scale is the one in
[nightgauge-adversarial-review](../skills/nightgauge-adversarial-review/SKILL.md).
Both use this definition.

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
  `ghu_`, `github_pat_`). The model runs beside the API key, so such a string
  is treated as a leak and the whole file is rejected.

## Caps

| Limit             | Value | Effect                                                    |
| ----------------- | ----- | --------------------------------------------------------- |
| Findings per file | 30    | More is rejected; nothing is posted                       |
| Nitpicks shown    | 5     | The rest are counted in an "N more nitpicks omitted" line |
| `claim` length    | 300   | Longer is rejected                                        |
| `fix` length      | 1000  | Longer is rejected                                        |
| Turns per review  | 80    | Reaching it stops the review; the job fails, no comment   |

## Turn limit

A review that reaches the turn limit did not finish. Its red check is neither
a pass nor a finding, and nothing is posted. The job summary and the error
annotation say so and name the limit.

The review scales with the diff, so the remedy is to split the pull request
into smaller ones. One re-run is reasonable because turn use varies; repeating
it is not the remedy. The value lives once, as `REVIEW_MAX_TURNS` in
`.github/workflows/pr-review.yml`.

## Output

The comment starts with the marker `<!-- nightgauge:pr-review -->`. Findings
are ranked `blocker`, then `major`, then `nitpick`. When the review finds
nothing, the comment says so.

Each run edits the same comment in place, so a pull request carries one review
comment however many times it is pushed. A newer push cancels a review still in
progress for the same pull request.

## Identity

The pipeline's own pull requests are skipped by author login, not by branch
name or label, because anyone can add a label or name a branch. Set the
repository variable `NIGHTGAUGE_PIPELINE_AUTHORS` to a comma-separated list of
the logins the pipeline opens pull requests as, for example the GitHub App's
bot login `<app-slug>[bot]`. When the variable is empty, every pull request is
reviewed. A misconfiguration therefore produces extra reviews, never a skipped
one.

The comment is posted with the workflow's `GITHUB_TOKEN`, as
`github-actions[bot]`. `pr-review.mjs render` refuses to produce a comment when
the reviewer login equals the author login, so the reviewer is never the
author.

Also skipped: `dependabot[bot]`, draft pull requests, runs without an
`ANTHROPIC_API_KEY` secret, and runs whose base commit has no
`scripts/pr-review.mjs` (the pull request that introduces it). A skip is
explained in the job summary.

## Not a merge gate

The review is advisory:

- No job fails because of a finding. A job fails only when the harness itself
  breaks, for example when the findings file is rejected or the review reaches
  its turn limit.
- The comment is a plain comment. It never approves or requests changes.
- The workflow is not a required status check, and must not be added to a
  ruleset.

## Fork pull requests

Under `pull_request`, a fork's run gets no secrets and a read-only token, so it
cannot call the model or post. Fork pull requests are skipped with an
explanation. A maintainer reviews one by running the workflow manually with
`workflow_dispatch` and the pull request number. Automatic fork review needs a
different trigger and key isolation and is not implemented.

## Security model

The review reads untrusted pull request content with a model. The workflow
follows the split used by `continuous-improvement.yml`:

1. `gate` holds no model. It reads the pull request and collects the review
   inputs: the diff, title, body, and closing-issue bodies.
2. `review` runs the model with `contents: read`, no forge token, no persisted
   checkout credentials, and no Bash. It can only read files and write the
   findings file.
3. `post` holds no model. It validates the findings file against the closed
   schema above, renders it, and upserts the comment with
   `pull-requests: write`.

See [WORKFLOW_FANOUT_SECURITY.md](security/WORKFLOW_FANOUT_SECURITY.md) for
the general threat model.
