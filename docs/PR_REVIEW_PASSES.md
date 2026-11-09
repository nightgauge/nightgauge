# PR Review Passes

Pull requests the pipeline opens are reviewed inside the pipeline. The
`.github/workflows/pr-review.yml` workflow is the manual second opinion: a
maintainer runs it on one pull request with `workflow_dispatch`, and it runs the
severity-ranked review passes defined here and posts the result as one advisory
comment. It never runs on its own, so it adds no model spend and no check to a
pull request. To run it:

```bash
gh workflow run pr-review.yml -f pr_number=<N>
```

It authenticates with the `CLAUDE_CODE_OAUTH_TOKEN` secret, created with
`claude setup-token`, so it draws on the Claude subscription. The workflow passes
no `ANTHROPIC_API_KEY`: the CLI would prefer it, and it bills per use.

This file is the committed definition. The review model reads it at run time,
and `scripts/pr-review.mjs` enforces its limits before anything is posted.

`feature-validate` applies the same passes, severities and finding schema locally
(Step 2.7.3), through `nightgauge review`. It reviews each tree once, in a fresh
context holding only the diff, the issue and the tree. Blockers and `correctness`
majors go to a fix loop of at most three catches per run, other majors become
discovered work, and nitpicks are counted. Each finished round replaces the
review's discovered work with its own, so only the last finished round's findings
are filed, and two passes at one location stay two findings. A review that does
not finish records a `catch`, never a pass, and changes no discovered work. The
sections below on comments and identity describe the manual workflow only.

The binary carries its own copy of the Passes through Caps sections, as
`internal/review/passes.md`, and writes it into each review's bundle, so a review
works in any repository and a diff cannot edit its own reviewer's rules. Those
sections name files in plain text rather than linking them because that copy is
read where the links would not resolve. A test pins the copy to this file: edit
both in the same change.

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

- `file` is a repository-relative path of at most 300 characters. It is not
  absolute and has no `..` segment.
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
| `claim` length    | 300   | Longer is truncated with a marker; the comment says so    |
| `fix` length      | 1000  | Longer is truncated with a marker; the comment says so    |
| `file` length     | 300   | Longer drops that finding only; the comment says so       |
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

Also skipped: `dependabot[bot]`, draft pull requests, runs without a
`CLAUDE_CODE_OAUTH_TOKEN` secret, and runs whose base commit has no
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

The pipeline's own merge is the one consumer. Before it enqueues, `pr-merge` reads
the comment and may hold the merge or file its findings as issues. See
[PR_MERGE_STAGE.md](PR_MERGE_STAGE.md#enqueue-review-gate-2730), which depends on
this comment's format. Change `scripts/pr-review.mjs` and
`internal/orchestrator/stages/prmerge_review.go` together.

## Fork pull requests

A manual run reviews a fork's pull request like any other, because a maintainer
chose to start it. Automatic fork review needs a `pull_request` trigger, which
gives a fork no secrets, so it is not implemented. The workflow never uses
`pull_request_target`, and automatic fork review stays a recorded follow-up.

## Security model

The review reads untrusted pull request content with a model. The workflow
follows the split used by `continuous-improvement.yml`:

1. `gate` holds no model. It reads the pull request and collects the review
   inputs: the diff, title, body, and closing-issue bodies.
2. `review` runs the model with `contents: read`, no forge token, no persisted
   checkout credentials, and no Bash. It can only read files and write the
   findings file.

   The model token is in the CLI's environment, so an unconfined `Read` of
   `/proc/self/environ` would recover it. The run therefore calls
   `scripts/pr-review-claude.sh`, the one definition of the confinement, which passes
   `--restricted` (file tools confined to the checkout; project settings,
   hooks and code-running tools ignored), `--tools "Read,Glob,Grep,Write"`,
   `--strict-mcp-config`, `--permission-mode dontAsk`, and the committed
   `.github/pr-review/claude-settings.json`. That file allows reads of
   `.pr-review/`, `docs/`, `standards/` and
   `skills/nightgauge-adversarial-review/` only, writes of `.pr-review/out/findings.json`
   only, and denies `/proc`, `/sys`, `/dev`, `~`, `.git`, Bash and web tools.
   Symlinks are deleted from the pull request's tree before the model runs.
   After the model exits, a model-free step fails the run if the findings
   file contains the token's exact value.

3. `post` holds no model. It validates the findings file against the closed
   schema above, renders it, and upserts the comment with
   `pull-requests: write`.

### Confinement probe

The settings above show that the rules are configured, not that the CLI
enforces them. `pr-review-confinement.yml` checks enforcement. It runs on manual
dispatch, weekly, and on each push to `main` that touches the confinement
(`.github/pr-review/`, either workflow, `scripts/pr-review-claude.sh` or
`scripts/pr-review.mjs`). It runs trusted code only, never pull request content,
and it is not a required check.

The probe exports a random per-run canary, masked in the log, into the CLI's
environment. It asks the model, through `scripts/pr-review-claude.sh`, to
`Read` `/proc/self/environ` and its parent shell's environ and to `Grep`
`/proc/self`. The CLI writes its `stream-json` transcript, so the verdict sees
every tool call and the result it got, not only the recorded denials.
A `/proc` call counts as denied when `permission_denials` records it, as it
does for the `--restricted` Grep, or when its tool result is the CLI's own
permission refusal. CLI 2.1.258 enforces the `Read(//proc/**)` deny inside the
Read tool, so a denied Read ends in the tool error "File is in a directory that
is denied by your permission settings." and is never added to
`permission_denials`. Any other error, such as a missing path or an OS
`EACCES`, is not a denial. `node scripts/pr-review.mjs probe-verdict` decides
without a model:

| Exit | Verdict                                                                          | Probe                              |
| ---- | -------------------------------------------------------------------------------- | ---------------------------------- |
| 0    | every requested `/proc` call was denied or refused, even with `is_error`         | green                              |
| 1    | the canary appears anywhere, or a `/proc` call got a result that is not an error | red, confinement broken            |
| 4    | a requested `/proc` call has no denial                                           | retried, then red as unproven      |
| 5    | the transcript has no JSON result event: the CLI itself failed                   | red at once; CLI output shown      |
| 6    | not proven, and the result event has `is_error: true`                            | retried, then red as a CLI failure |

A `/proc` call that returned content is red even when the model does not quote
it, because the canary check alone would miss it. On exits 4 and 6 the log
lists each `/proc` call the model made and its outcome (denied, refused by the
permission settings, another tool error with its first 200 characters, or no
result), the turn count, and the final
reply. It never prints the canary or the content of a successful tool result.

The probe tries three times before it reports an unproven result. It never
reports green by default. A green probe run on `main` is the evidence that the CLI enforces the
confinement.

See [WORKFLOW_FANOUT_SECURITY.md](security/WORKFLOW_FANOUT_SECURITY.md) for
the general threat model.
