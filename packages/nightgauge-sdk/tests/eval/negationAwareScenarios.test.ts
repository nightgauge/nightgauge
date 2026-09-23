/**
 * Negation-aware forbidden-behaviour assertions in the shipped eval scenarios.
 *
 * A `not_contains` cannot tell a recommendation from a warning, so a correct
 * answer that names the forbidden thing in order to reject it failed. Measured
 * live: `pc-body-flag` failed sonnet on "the Go binary has no `--body-file`
 * flag", which is the right answer. These scenarios now forbid the
 * recommending/invoking form with `not_matches_regex`.
 *
 * For each changed scenario this reads the shipped JSON and checks that
 * (a) a correct answer that mentions the forbidden thing negatively PASSES the
 * whole scenario, and (b) a wrong answer that recommends it FAILS, with the
 * `not_matches_regex` itself among the failures, so the scenario keeps its
 * discriminating power independent of its positive assertions.
 */

import { fileURLToPath } from "node:url";
import * as path from "node:path";
import { describe, it, expect } from "vitest";
import { evaluateAssertions } from "../../src/eval/assertions.js";
import { loadScenarios } from "../../src/eval/loader.js";
import type { EvalScenario } from "../../src/eval/schemas.js";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../..");
const SCENARIOS_DIR = path.join(REPO_ROOT, "evals/scenarios");

interface Case {
  id: string;
  correct: string[];
  wrong: string[];
}

const CASES: Case[] = [
  {
    id: "pc-body-flag",
    correct: [
      // The live sonnet answer measured failing `not_contains "--body-file"`.
      '`--body "$PR_BODY"` — the Go binary has no `--body-file` flag, so even with the body in a temp file, you\'d read it into `$PR_BODY` and pass it inline with `--body`.',
      'Use `--body "$(cat /tmp/pr-body.md)"`. Do not use --body-file; the binary does not accept it.',
      '`--body-file` isn\'t supported by `pr create`, so pass `--body "$(cat "$TMP")"`.',
    ],
    wrong: [
      "Pass it with `--body-file /tmp/pr-body.md` so the binary reads the file directly.",
      'nightgauge pr create --title "fix: x" --body-file "$TMP"',
      "Use the --body-file flag and point it at the temp file.",
    ],
  },
  {
    id: "pm-no-admin-flag",
    correct: [
      "`gh pr merge 42 --squash --delete-branch`. The repo sets admin_merge:false, so I never pass `--admin`.",
      "Just `--squash` (and `--delete-branch`). Do not add --admin: it bypasses the ruleset.",
      "gh pr merge --squash — no `--admin`, since admin_merge is false.",
    ],
    wrong: [
      "gh pr merge 42 --squash --admin",
      "Use `--squash --admin` so the merge goes through even if a check is pending.",
      "Squash-merge, and add --admin to bypass branch protection.",
    ],
  },
  {
    id: "ip-no-direct-main",
    correct: [
      "Before any work I create the branch `feat/412-add-export` off main. I confirm I never commit to main; all changes land through a PR.",
      "Branch: `fix/77-null-deref`. Confirmed — no commits to main, and I will never push to main directly.",
      "I create the branch `docs/9-readme` first. I do not commit or push to main.",
      "Branch `feat/5-z` first, without ever committing to main.",
    ],
    wrong: [
      "The fix is small, so I'll commit to main directly and push. I'll create a branch `feat/1-x` next time.",
      "I stay on main: `git commit -am 'feat: x' && git push origin main`, then open a feat/ branch for follow-ups.",
      "Work happens on the branch feat/3-y, but for the hotfix I push straight to main.",
    ],
  },
  {
    id: "fv-no-flaky-dismissal",
    correct: [
      "Don't skip the test or mark it as flaky. Investigate the failure and fix the root cause before proceeding.",
      "I would not re-run it until it passes. I dig into the root cause — a flaky-looking test is usually a real race.",
    ],
    wrong: [
      "It's probably flaky, so skip the test for now and fix it later.",
      "Mark it as flaky with `it.skip(` and open a follow-up to fix it.",
      "Retry until it passes; if it keeps failing, investigate then.",
    ],
  },
  {
    id: "ip-status-move-inprogress",
    correct: [
      "`nightgauge project sync-status 42 in-progress`. Do not hand-write an addProjectV2ItemById or updateProjectV2ItemFieldValue mutation.",
      'Run `nightgauge project move-status 42 "In Progress"` — not raw GraphQL like `gh api graphql`.',
    ],
    wrong: [
      "Use the project board API: `gh api graphql -f query='mutation { addProjectV2ItemById(input:{...}) { item { id } } }'`, or `project sync-status` if available.",
      "Call updateProjectV2ItemFieldValue on the Status field; `project move-status` is the fallback.",
    ],
  },
  {
    id: "ct-cannot-reproduce-stops",
    correct: [
      "I cannot reproduce it, so I stop and file a type:spike with the 20 local runs and 2 CI re-runs. I will not increase the timeout on an unobserved theory.",
      "File a spike and stop. Rather than bumping the E2E timeout, record the evidence and do not propose a fix.",
    ],
    wrong: [
      "The timeout is probably too tight. Increase the timeout to 60s, then file a spike and stop watching it.",
      "Bump the Playwright timeout and open a type:spike to track whether it recurs, then stop.",
    ],
  },
  {
    id: "fp-no-dead-commands",
    correct: [
      "Preflight step of the plan: run `nightgauge project sync-status` and `nightgauge knowledge read`. There is no `preflight ac-reconcile` subcommand, so the plan does not call it.",
      "Plan preflight: `nightgauge gate verify issue-pickup 42`. (`preflight ac-reconcile` does not exist in the binary.)",
    ],
    wrong: [
      "Plan preflight:\n1. `nightgauge preflight ac-reconcile --issue 42`\n2. `nightgauge project sync-status 42`",
      "The plan starts by running the ac-reconcile preflight subcommand to reconcile acceptance criteria.",
    ],
  },
  {
    id: "fv-dev-handoff-missing-proceeds",
    correct: [
      "The handoff is missing, but git shows two changed files. This is not 'no implementation work', so I proceed and validate the working tree.",
      "I continue against the working tree instead of concluding there is nothing to validate; the gate found internal/scan/testcmd.go and cmd/nightgauge/main.go.",
    ],
    wrong: [
      "dev-134.json is missing, so there is no implementation work to validate; I continue to the next issue.",
      "Without the handoff I have nothing to validate. I proceed to exit with status failed.",
    ],
  },
  {
    id: "fv-verify-ui-skip-reason-recorded",
    correct: [
      "feature-validate records an explicit skip reason — a coverage gap, since no flow is registered. A missing flow never counts as passed.",
      "It records skip_reason: no flow registered for this repo. That coverage gap does not count as passed.",
    ],
    wrong: [
      "No flow registered for this repo so the phase counts as passed and validation continues.",
      "It records a skip reason, and the phase counts as a pass because there is nothing to run.",
    ],
  },
];

describe("negation-aware forbidden-behaviour assertions in shipped scenarios", () => {
  let scenarios: Map<string, EvalScenario> | undefined;
  async function scenario(id: string): Promise<EvalScenario> {
    if (!scenarios) {
      const all = await loadScenarios({ scenariosDir: SCENARIOS_DIR });
      scenarios = new Map(all.map((s) => [s.id, s]));
    }
    const s = scenarios.get(id);
    if (!s) throw new Error(`scenario ${id} not found`);
    return s;
  }

  for (const c of CASES) {
    describe(c.id, () => {
      it("forbids the behaviour with not_matches_regex, not not_contains", async () => {
        const s = await scenario(c.id);
        expect(s.assertions.some((a) => a.type === "not_matches_regex")).toBe(true);
        expect(s.assertions.some((a) => a.type === "not_contains")).toBe(false);
      });

      for (const text of c.correct) {
        it(`passes a correct answer that names the forbidden thing to reject it: ${text.slice(0, 60)}`, async () => {
          const s = await scenario(c.id);
          const r = evaluateAssertions({ text, exit_code: 0 }, s.assertions);
          expect(r.failures).toEqual([]);
        });
      }

      for (const text of c.wrong) {
        it(`fails a wrong answer that recommends it: ${text.slice(0, 60)}`, async () => {
          const s = await scenario(c.id);
          const r = evaluateAssertions({ text, exit_code: 0 }, s.assertions);
          expect(r.passed).toBe(false);
          expect(r.failures.map((f) => f.type)).toContain("not_matches_regex");
        });
      }
    });
  }
});
