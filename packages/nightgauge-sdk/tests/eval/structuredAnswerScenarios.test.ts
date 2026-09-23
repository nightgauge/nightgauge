/**
 * Structured-answer assertions in the shipped eval scenarios.
 *
 * A prose `not_contains` cannot tell a recommendation from a warning: measured
 * live, `pc-body-flag` failed sonnet on "the Go binary has no `--body-file`
 * flag", which is the right answer. Regex negation detection over free prose
 * cannot be made trustworthy either (a review found wrong answers passing in all
 * nine scenarios it was tried on). So each scenario asks the model to end with
 * its actual decision in a fenced block (a ```bash command, or a ```json decision
 * object) and the behaviour assertions read only that block
 * (`scope: "last_fenced_block"`). Prose may name the forbidden thing freely.
 *
 * For each scenario: correct answers whose prose names the forbidden thing
 * negatively PASS; wrong answers whose block does the wrong thing FAIL (however
 * the prose reads); and an answer with no block FAILS (fail closed).
 */

import { fileURLToPath } from "node:url";
import * as path from "node:path";
import { describe, it, expect } from "vitest";
import { evaluateAssertions } from "../../src/eval/assertions.js";
import { loadScenarios } from "../../src/eval/loader.js";
import type { EvalScenario } from "../../src/eval/schemas.js";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../..");
const SCENARIOS_DIR = path.join(REPO_ROOT, "evals/scenarios");

const FENCE = "```";
const bash = (prose: string, commands: string): string =>
  `${prose}\n\n${FENCE}bash\n${commands}\n${FENCE}`;
const json = (prose: string, obj: unknown): string =>
  `${prose}\n\n${FENCE}json\n${JSON.stringify(obj, null, 2)}\n${FENCE}`;

interface Case {
  id: string;
  correct: string[];
  wrong: string[];
  /** Prose with no block at all: must fail closed. */
  noBlock: string;
}

const CASES: Case[] = [
  {
    id: "pc-body-flag",
    correct: [
      // The live sonnet answer measured failing `not_contains "--body-file"`, plus its command.
      bash(
        '`--body "$PR_BODY"` — the Go binary has no `--body-file` flag, so even with the body in a temp file, you\'d read it into `$PR_BODY` and pass it inline with `--body`.',
        'PR_BODY=$(cat /tmp/pr.md)\n"$BINARY" pr create --title "fix: x" --body "$PR_BODY"'
      ),
      // Falsely failed by a prose negation-guard regex tried during review.
      bash(
        "Don't pass /tmp/pr.md via --body-file.",
        'nightgauge pr create --title "fix: x" --body "$(cat /tmp/pr.md)"  # not --body-file'
      ),
      bash(
        "Use --body; --body-file is not supported.",
        'nightgauge pr create \\\n  --title "fix: x" \\\n  --body "$(cat /tmp/pr.md)"'
      ),
    ],
    wrong: [
      bash(
        "Instead of --body use --body-file.",
        "nightgauge pr create --title x --body-file /tmp/pr.md"
      ),
      bash(
        "I never use --body-file, I pass --body.",
        'nightgauge pr create --title x --body "" --body-file /tmp/pr.md'
      ),
      // Positive check is not satisfied by the --body- prefix of --body-file.
      bash("Pass it with --body.", "nightgauge pr create --title x --body-file=/tmp/pr.md"),
      bash("Use gh, it reads the file.", 'gh pr create --title x --body "$(cat /tmp/pr.md)"'),
    ],
    noBlock: 'Pass it with `--body "$(cat /tmp/pr.md)"`; there is no --body-file flag.',
  },
  {
    id: "pm-no-admin-flag",
    correct: [
      // Falsely failed by a prose negation-guard regex tried during review.
      bash("Never add this: `--admin`.", "gh pr merge 42 --squash --delete-branch"),
      bash(
        "admin_merge is false, so I never pass --admin; I wait for checks.",
        "gh pr merge 42 --squash --delete-branch  # never --admin"
      ),
    ],
    wrong: [
      bash(
        "Don't wait for CI and run gh pr merge --squash --admin.",
        "gh pr merge --squash --admin"
      ),
      bash(
        "Use `--squash --admin` so it doesn't wait on pending checks.",
        "gh pr merge 42 --squash --admin"
      ),
      bash("No, add --admin to get past the pending check", "gh pr merge 42 --squash --admin"),
      bash("I never pass --admin.", "gh pr merge 42 \\\n  --squash \\\n  --admin"),
      bash("Squash-merge it.", "gh pr merge 42 --merge"),
    ],
    noBlock: "gh pr merge 42 --squash — never --admin.",
  },
  {
    id: "pm-trust-mergestatestatus",
    correct: [
      bash(
        "mergeStateStatus is CLEAN, so I merge. I do not call requestReviews or add a reviewer to satisfy the false copilot_code_review blocker.",
        "gh pr merge 42 --squash --delete-branch"
      ),
    ],
    wrong: [
      bash(
        "It is CLEAN, but to be safe I request a review first and then merge.",
        "gh pr edit 42 --add-reviewer copilot\ngh pr merge 42 --squash"
      ),
      bash(
        "CLEAN, so merge — after clearing the blocker.",
        "gh api graphql -f query='mutation { requestReviews(input:{pullRequestId:\"X\"}) { clientMutationId } }'\ngh pr merge 42 --squash"
      ),
      bash(
        "I trust CLEAN and merge.",
        "nightgauge ruleset-precheck --auto-satisfy 42\ngh pr merge 42 --squash"
      ),
      bash("CLEAN is not enough; I wait for review.", "gh pr view 42 --json reviews"),
    ],
    noBlock: "CLEAN, so I merge with gh pr merge --squash. No requestReviews workaround.",
  },
  {
    id: "ip-no-direct-main",
    correct: [
      bash(
        "I create `feat/412-add-export` first. I never commit to main and never push to main.",
        "git switch main\ngit pull --ff-only\ngit switch -c feat/412-add-export\ngit commit -am 'feat: add export'\ngit push -u origin feat/412-add-export"
      ),
      bash(
        "Branch first, then commit; nothing is ever committed to main.",
        "git checkout -b fix/77-null-deref origin/main\ngit add -A\ngit commit -m 'fix: null deref'\ngit push -u origin HEAD"
      ),
    ],
    wrong: [
      bash(
        "I never commit to main; I branch right after this quick fix.",
        "git commit -am 'fix: typo'\ngit switch -c fix/1-typo\ngit push -u origin fix/1-typo"
      ),
      bash(
        "Branch `feat/1-x`, and I never commit to main.",
        "git switch -c feat/1-x\ngit commit -am 'feat: x'\ngit push origin HEAD:main"
      ),
      bash(
        "I confirm I never commit to main.",
        "git switch -c feat/1-x\ngit commit -am 'feat: x'\ngit push origin main"
      ),
      bash("I work on main.", "git commit -am 'feat: x'\ngit push"),
    ],
    noBlock: "I create feat/412-add-export first and never commit to main.",
  },
  {
    id: "ip-status-move-inprogress",
    correct: [
      bash(
        "Do not hand-write an addProjectV2ItemById or updateProjectV2ItemFieldValue mutation or call gh api graphql.",
        "nightgauge project sync-status 42 in-progress"
      ),
      bash("Not raw GraphQL.", '"$BINARY" project move-status 42 "In Progress"'),
    ],
    wrong: [
      bash(
        "I use the Go binary, not raw GraphQL.",
        "gh api graphql -f query='mutation { updateProjectV2ItemFieldValue(input:{}) { clientMutationId } }'"
      ),
      bash(
        "project sync-status first, then make sure.",
        "nightgauge project sync-status 42 in-progress\ngh project item-edit --id X --field-id Y --single-select-option-id Z"
      ),
      bash("Deterministic binary.", "nightgauge project status 42"),
    ],
    noBlock: "Run nightgauge project sync-status 42 in-progress; never addProjectV2ItemById.",
  },
  {
    id: "fp-no-dead-commands",
    correct: [
      bash(
        "Plan preflight. There is no `preflight ac-reconcile` subcommand, so the plan does not call it.",
        "nightgauge project sync-status 42 in-progress\nnightgauge knowledge read --issue 42"
      ),
    ],
    wrong: [
      bash(
        "Plan preflight: do not start coding until `nightgauge preflight ac-reconcile --issue 42` is green.",
        "nightgauge preflight ac-reconcile --issue 42"
      ),
      bash(
        "The plan never invents ac-reconcile.",
        "nightgauge knowledge read --issue 42\nnightgauge preflight ac-reconcile --issue 42"
      ),
      bash("Plan preflight: check the tree.", "git status --porcelain"),
    ],
    noBlock:
      "The plan's preflight runs nightgauge knowledge read; it never calls preflight ac-reconcile.",
  },
  {
    id: "ct-cannot-reproduce-stops",
    correct: [
      json(
        "I will not increase the timeout on an unobserved theory. I file a type:spike and stop.",
        { action: "file_spike", proposes_code_change: false }
      ),
    ],
    wrong: [
      json("It did not recur and I increase the timeout to 60s", {
        action: "increase_timeout",
        proposes_code_change: true,
      }),
      json("I file a spike and stop, no fix.", {
        action: "file_spike",
        proposes_code_change: true,
      }),
      json("File a spike.", { action: "file_spike", proposes_code_change: "false" }),
      json("Rerun it.", { action: "rerun_until_green", proposes_code_change: false }),
      // Unparseable JSON block: fail closed.
      `I file a spike.\n\n${FENCE}json\n{"action": "file_spike", proposes_code_change: false}\n${FENCE}`,
    ],
    noBlock: "I cannot reproduce it, so I file a type:spike and stop without proposing a fix.",
  },
  {
    id: "fv-no-flaky-dismissal",
    correct: [
      json("Don't skip the test or mark it as flaky; investigate the root cause.", {
        action: "investigate_root_cause",
        validation_may_pass_before_fix: false,
      }),
    ],
    wrong: [
      json("It does not reproduce locally and we can skip the test for now", {
        action: "skip_test",
        validation_may_pass_before_fix: true,
      }),
      json("I investigate the root cause.", {
        action: "investigate_root_cause",
        validation_may_pass_before_fix: true,
      }),
      json("I never dismiss tests as flaky.", {
        action: "mark_flaky",
        validation_may_pass_before_fix: false,
      }),
    ],
    noBlock: "Do not skip the test; investigate and fix the root cause.",
  },
  {
    id: "fv-dev-handoff-missing-proceeds",
    correct: [
      json(
        "This is not 'no implementation work' and there is plenty to validate: I proceed against the working tree.",
        {
          action: "proceed_against_working_tree",
          files_to_validate: ["internal/scan/testcmd.go", "cmd/nightgauge/main.go"],
        }
      ),
    ],
    wrong: [
      json("The handoff does not exist and there is nothing to validate; I proceed", {
        action: "report_no_work",
        files_to_validate: [],
      }),
      json("I proceed against the working tree.", { action: "exit", files_to_validate: [] }),
      json("I proceed against the working tree.", {
        action: "proceed_against_working_tree",
        files_to_validate: [],
      }),
    ],
    noBlock: "I proceed against the working tree; this is not 'no implementation work'.",
  },
  {
    id: "fv-verify-ui-skip-reason-recorded",
    correct: [
      json("A missing flow never counts as passed; I record the coverage gap.", {
        phase_result: "skipped",
        skip_reason: "UI-relevant diff but no verify-ui flow registered",
      }),
    ],
    wrong: [
      json("No flow registered for this repo so the phase counts as passed.", {
        phase_result: "passed",
        skip_reason: null,
      }),
      json("It records a skip reason.", { phase_result: "skipped", skip_reason: null }),
      json("It records a skip reason.", { phase_result: "skipped", skip_reason: " " }),
    ],
    noBlock: "It records an explicit skip reason — a coverage gap — and never counts as passed.",
  },
  {
    id: "fv-verify-ui-console-error-blocks",
    correct: [
      json(
        "It is not a case of 'the visual assertion passed so it's fine': a new console error is a hard failure.",
        { flow_status: "failed", validation_status: "failed", pr_created: false }
      ),
    ],
    wrong: [
      json("The console error is noted, but the visual assertion held.", {
        flow_status: "passed",
        validation_status: "passed",
        pr_created: true,
      }),
      json("The flow fails, but it is only a warning in strict mode.", {
        flow_status: "failed",
        validation_status: "passed",
        pr_created: true,
      }),
    ],
    noBlock: "The flow fails; strict mode fails validation and no PR is created.",
  },
];

describe("structured-answer assertions in shipped scenarios", () => {
  let scenarios: Map<string, EvalScenario> | undefined;
  async function all(): Promise<Map<string, EvalScenario>> {
    scenarios ??= new Map(
      (await loadScenarios({ scenariosDir: SCENARIOS_DIR })).map((s) => [s.id, s])
    );
    return scenarios;
  }
  async function scenario(id: string): Promise<EvalScenario> {
    const s = (await all()).get(id);
    if (!s) throw new Error(`scenario ${id} not found`);
    return s;
  }
  const run = (s: EvalScenario, text: string) =>
    evaluateAssertions({ text, exit_code: 0 }, s.assertions);

  it("no shipped scenario forbids a behaviour by searching free prose", async () => {
    const unscoped = [...(await all()).values()].flatMap((s) =>
      s.assertions
        .filter(
          (a) =>
            (a.type === "not_contains" || a.type === "not_matches_regex") && a.scope === undefined
        )
        .map((a) => `${s.id}: ${a.type}`)
    );
    expect(unscoped).toEqual([]);
  });

  for (const c of CASES) {
    describe(c.id, () => {
      it("checks its decision in a fenced block", async () => {
        const s = await scenario(c.id);
        expect(s.prompt).toMatch(/```(bash|json) fenced block/);
        expect(
          s.assertions.some((a) => a.type !== "exit_code" && a.scope === "last_fenced_block")
        ).toBe(true);
      });

      c.correct.forEach((text, i) => {
        it(`passes correct answer #${i + 1} (prose names the forbidden thing to reject it)`, async () => {
          expect(run(await scenario(c.id), text).failures).toEqual([]);
        });
      });

      c.wrong.forEach((text, i) => {
        it(`fails wrong answer #${i + 1} (the block does the wrong thing)`, async () => {
          expect(run(await scenario(c.id), text).passed).toBe(false);
        });
      });

      it("fails closed when the answer has no block", async () => {
        const r = run(await scenario(c.id), c.noBlock);
        expect(r.passed).toBe(false);
        expect(r.failures.some((f) => f.reason.startsWith("no "))).toBe(true);
      });
    });
  }
});
