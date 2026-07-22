/**
 * Unit tests for classifyFailureCategory (Issue #1260)
 *
 * Covers: infrastructure patterns, agent patterns, organic patterns,
 * default handling (undefined/empty error), and weight constants.
 */

import { describe, it, expect } from "vitest";
import {
  classifyFailureCategory,
  classifyTerminalKind,
  FAILURE_CATEGORY_WEIGHTS,
  type FailureCategory,
} from "../../../src/analysis/health/failureClassifier.js";

// ── classifyTerminalKind ───────────────────────────────────────────────────────

describe("classifyTerminalKind — github_quota_low (#3896)", () => {
  it("matches the pipeline-start marker", () => {
    expect(
      classifyTerminalKind(
        "[pipeline-start-failure] github-quota-low: GitHub API quota too low to start pipeline (8/5000 remaining, need ≥200). (transient; resetInSec=58)"
      )
    ).toBe("github_quota_low");
  });

  it("matches the embedded error-text token", () => {
    expect(
      classifyTerminalKind(
        "[github-quota-low] GitHub API quota too low — pipeline deferred before AI stages (transient; resetInSec=58)."
      )
    ).toBe("github_quota_low");
  });

  it("does not mis-bucket the marker as subagent_crash", () => {
    // The descriptive form contains no "exit "/crash token, but assert the
    // quota matcher wins regardless of ordering.
    expect(classifyTerminalKind("pipeline-start-failure: github api quota too low to start")).toBe(
      "github_quota_low"
    );
  });

  it("returns undefined for unrelated text", () => {
    expect(classifyTerminalKind("something unrelated")).toBeUndefined();
  });
});

describe("classifyTerminalKind — transient network kinds (#4002)", () => {
  it("matches the canonical Anthropic transport-drop message", () => {
    expect(classifyTerminalKind("API Error: The socket connection was closed unexpectedly")).toBe(
      "api_connection_lost"
    );
  });

  it("matches socket hang up", () => {
    expect(classifyTerminalKind("API Error: socket hang up")).toBe("api_connection_lost");
  });

  it("matches the pipeline-start network-outage marker", () => {
    expect(
      classifyTerminalKind(
        "[pipeline-start-failure] github-network-outage: GitHub API unreachable — `gh auth status` could not connect to api.github.com. (transient; retryInSec=120)"
      )
    ).toBe("github_network_outage");
  });

  it("matches the embedded error-text token", () => {
    expect(
      classifyTerminalKind(
        "[github-network-outage] GitHub API unreachable — pipeline deferred before AI stages (transient; retryInSec=120)."
      )
    ).toBe("github_network_outage");
  });

  it("does NOT classify a real auth failure as environmental", () => {
    expect(
      classifyTerminalKind(
        "github-auth-failed: GitHub auth check failed: `gh auth status` returned a non-zero exit code. Run `gh auth login` to authenticate."
      )
    ).not.toBe("github_network_outage");
  });
});

// ── model_unavailable (#42) ────────────────────────────────────────────────────

describe("classifyTerminalKind — model_unavailable (#42)", () => {
  it("matches the Anthropic 404 not_found_error naming the model", () => {
    expect(
      classifyTerminalKind(
        'API Error: 404 {"type":"error","error":{"type":"not_found_error","message":"model: claude-fable-5"}}'
      )
    ).toBe("model_unavailable");
  });

  it("matches plan restrictions naming a registry model", () => {
    expect(classifyTerminalKind("claude-fable-5 is not available on your current plan")).toBe(
      "model_unavailable"
    );
  });

  it("matches model-specific usage caps by tier name", () => {
    expect(classifyTerminalKind("Opus weekly limit reached")).toBe("model_unavailable");
  });

  it("does NOT match account-level limits with no model named", () => {
    expect(classifyTerminalKind("You have reached your usage limit. It resets at 3pm.")).not.toBe(
      "model_unavailable"
    );
  });
});

describe("classifyTerminalKind — premature_turn_end (#74)", () => {
  it("matches the scheduler's gate no-op stamp", () => {
    expect(
      classifyTerminalKind(
        "premature turn end: stage exited 0 with no state change (gate no-op): plan_file does not exist"
      )
    ).toBe("premature_turn_end");
  });

  it("matches validateStageOutput's exit-0 missing-context phrase (reclassified from validation_error)", () => {
    expect(
      classifyTerminalKind(
        "stage feature-planning exited 0 but did not write expected output context: /x/planning-74.json"
      )
    ).toBe("premature_turn_end");
  });

  it("does NOT claim the pr-merge no-op shape (keeps pr_merge_unmerged precedence, #3691)", () => {
    expect(
      classifyTerminalKind(
        "premature turn end: stage exited 0 with no state change (gate no-op): pr-merge reported success but PR #55 is not merged (state: OPEN)"
      )
    ).not.toBe("premature_turn_end");
  });
});

describe("classifyTerminalKind — adapter_auth_failed (#312)", () => {
  it("classifies a burst probe TIMEOUT as adapter_auth_failed, NOT subagent_crash", () => {
    // The real burst false-negative message from HeadlessOrchestrator. It
    // mentions "timed out" but must route to the retryable-infra kind.
    expect(
      classifyTerminalKind(
        "[adapter-auth-failed] Auth pre-flight failed — auth probe timed out after retry (adapter CLI unresponsive — transient, not a logged-out session). Pipeline halted before AI stages (zero tokens spent).\n- **claude-headless**: auth probe timed out after 5s and again after 10s on retry"
      )
    ).toBe("adapter_auth_failed");
  });

  it("classifies a definitive logged-out failure as adapter_auth_failed", () => {
    expect(
      classifyTerminalKind(
        "[adapter-auth-failed] Auth pre-flight failed — adapter not authenticated.\n- **claude-headless**: claude CLI is not authenticated. Run `claude auth login`."
      )
    ).toBe("adapter_auth_failed");
  });

  it("matches the underscore kind form (defense-in-depth reclassify)", () => {
    expect(
      classifyTerminalKind("pipeline-start halted: adapter_auth_failed (probe timed out)")
    ).toBe("adapter_auth_failed");
  });
});

describe("classifyFailureCategory — adapter_auth_failed is infrastructure-class (#312)", () => {
  it("classifies the burst probe timeout as infrastructure (0.05 weight), not organic", () => {
    // Environmental probe starvation / credential state — must not depress the
    // reliability score like an organic implementation failure.
    expect(
      classifyFailureCategory(
        "[adapter-auth-failed] Auth pre-flight failed — auth probe timed out after retry.",
        "pipeline-start"
      )
    ).toBe("infrastructure");
  });
});

describe("classifyTerminalKind — no_changes_produced (#317)", () => {
  it("classifies the real HeadlessOrchestrator no-PR message via the stable marker", () => {
    // The exact shape from the #317 incident: a human-only `owner-action`
    // issue was dispatched, correctly produced zero commits, and pr-create's
    // deterministic fallback declined. Mentions "exited" (not "exit ") so the
    // subagent_crash fallback would not have matched anyway, but the marker
    // makes the classification explicit and robust to wording changes.
    expect(
      classifyTerminalKind(
        'pr-create reported success but no open PR exists (pr context file missing). Deterministic fallback could not open one: [no-changes-produced] feature branch "feat/317-x" has no commits ahead of main. The skill may have exited without pushing the branch or opening the PR.'
      )
    ).toBe("no_changes_produced");
  });

  it("matches the underscore kind form (defense-in-depth reclassify)", () => {
    expect(
      classifyTerminalKind("pr-create declined: no_changes_produced (zero commits ahead of base)")
    ).toBe("no_changes_produced");
  });

  it("does NOT reclassify feature-validate's unrelated lost-implementation check", () => {
    // This message also contains the bare phrase "no commits ahead of" but
    // describes a genuine organic defect (claimed files that don't exist on
    // disk) — it must NOT be softened to no_changes_produced just because it
    // shares that substring with the honest no-op case.
    expect(
      classifyTerminalKind(
        "feature-validate reported success but the commit contract (#1608) is unmet: the branch has no commits ahead of origin/main AND the working tree has no source changes, while the dev context lists 2 implemented file(s) (e.g. src/foo.ts). The implementation was lost or never written."
      )
    ).not.toBe("no_changes_produced");
  });
});

describe("classifyFailureCategory — no_changes_produced is agent-class, not infrastructure (#317)", () => {
  it("classifies the marker as agent (0.5 weight) — a planning/scope failure, not tooling", () => {
    expect(
      classifyFailureCategory(
        '[no-changes-produced] feature branch "feat/317-x" has no commits ahead of main',
        "pr-create"
      )
    ).toBe("agent");
  });
});

describe("classifyTerminalKind — validation_failed (#326)", () => {
  it("classifies the real HeadlessOrchestrator validation-failed message via the stable marker", () => {
    // The exact shape from the #326 incident: feature-validate exited 0 but
    // wrote validation_status="failed", and the orchestrator halted before
    // pr-create instead of advancing with no commit to push.
    expect(
      classifyTerminalKind(
        '[validation-failed] feature-validate reported validation_status="failed" (tests-failed). ' +
          "The validated code was intentionally NOT committed or pushed — the skill leaves it on " +
          "disk for retry. Advancing to pr-create would push an empty branch and fail the " +
          "no-commits-ahead gate. Halting at feature-validate so the failure is surfaced for " +
          "retry/triage instead."
      )
    ).toBe("validation_failed");
  });

  it("matches the underscore kind form (defense-in-depth reclassify)", () => {
    expect(
      classifyTerminalKind("pipeline halted: validation_failed (quality gates did not pass)")
    ).toBe("validation_failed");
  });

  it("is NOT bucketed as subagent_crash even when a caller wraps it with an exit-code prefix", () => {
    // Mirrors the real flow: the Go scheduler's generic stage-error wrapper
    // is `exit %d: %v`, whose "exit " substring would otherwise misbucket
    // this as a process death.
    expect(
      classifyTerminalKind(
        'exit 2: [validation-failed] feature-validate reported validation_status="failed"'
      )
    ).toBe("validation_failed");
  });
});

describe("classifyFailureCategory — validation_failed is organic (the default), not infrastructure/agent (#326)", () => {
  it("classifies the marker as organic (1.0 weight) — a true implementation failure", () => {
    expect(
      classifyFailureCategory(
        '[validation-failed] feature-validate reported validation_status="failed" (build-failed)',
        "feature-validate"
      )
    ).toBe("organic");
  });
});

describe("classifyFailureCategory — premature_turn_end is agent-class (#74)", () => {
  it("classifies the stamp as agent even when the gate reason names a context file", () => {
    // Without the #74 rule, the embedded "context file" phrase would bucket
    // this as infrastructure (0.05) and hide an agent behavior failure (0.5).
    expect(
      classifyFailureCategory(
        "premature turn end: stage exited 0 with no state change (gate no-op): planning context file missing",
        "feature-planning"
      )
    ).toBe("agent");
  });

  it("classifies the exit-0 missing-context phrase as agent", () => {
    expect(
      classifyFailureCategory(
        "stage feature-dev exited 0 but did not write expected output context: /x",
        "feature-dev"
      )
    ).toBe("agent");
  });
});

// ── Weight constants ───────────────────────────────────────────────────────────

describe("FAILURE_CATEGORY_WEIGHTS", () => {
  it("infrastructure weight is 0.05", () => {
    expect(FAILURE_CATEGORY_WEIGHTS.infrastructure).toBe(0.05);
  });

  it("agent weight is 0.5", () => {
    expect(FAILURE_CATEGORY_WEIGHTS.agent).toBe(0.5);
  });

  it("organic weight is 1.0", () => {
    expect(FAILURE_CATEGORY_WEIGHTS.organic).toBe(1.0);
  });

  it("has exactly three keys", () => {
    expect(Object.keys(FAILURE_CATEGORY_WEIGHTS)).toHaveLength(3);
  });
});

// ── Default / missing error text ──────────────────────────────────────────────

describe("classifyFailureCategory — defaults", () => {
  it('returns "organic" when errorText is undefined', () => {
    expect(classifyFailureCategory(undefined, "feature-dev")).toBe("organic");
  });

  it('returns "organic" when errorText is an empty string', () => {
    expect(classifyFailureCategory("", "feature-dev")).toBe("organic");
  });

  it('returns "organic" for an unrecognized error message', () => {
    expect(classifyFailureCategory("Something unexpected happened", "feature-dev")).toBe("organic");
  });

  it("_stage parameter is accepted without affecting the default result", () => {
    const stages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ];
    for (const stage of stages) {
      expect(classifyFailureCategory(undefined, stage)).toBe("organic");
    }
  });
});

// ── Infrastructure patterns ───────────────────────────────────────────────────

describe("classifyFailureCategory — infrastructure", () => {
  const cases: Array<[string, string]> = [
    ["Schema validation failed: expected string", "schema validation"],
    ["Pre-condition failed: context file missing", "pre-condition failed"],
    ["Error reading context file at path", "context file"],
    ["ENOENT: no such file or directory", "enoent"],
    ["EACCES: permission denied", "eacces"],
    ["EPERM: operation not permitted", "eperm"],
    ["Invalid JSON in planning-42.json", "invalid json"],
    ["Extension lifecycle error during activation", "extension lifecycle"],
    ["Failed to read context from disk", "failed to read"],
    ["Cannot read properties of undefined", "cannot read"],
    ["Pipeline state file is corrupt", "pipeline state"],
    // Issue #3004 — baseline-CI gate deferrals are infrastructure
    ["[baseline-ci-deferred] ci.yml failed 3/5 recent runs on main", "[baseline-ci-deferred]"],
    ["Stage held: baseline ci deferred until main goes green", "baseline ci deferred"],
    ["baseline-ci red: ci.yml `Integration & E2E Tests` failed 3/5", "baseline-ci red"],
    // Issue #231 — native blockedBy deferrals are infrastructure (controlled hold)
    ["[blocked-dependency] blocked by #123 (PR not merged)", "[blocked-dependency]"],
    ["issue-pickup deferred: blocked by open dependency #123", "blocked by open dependency"],
  ];

  for (const [errorText, pattern] of cases) {
    it(`classifies as "infrastructure" when error contains "${pattern}"`, () => {
      expect(classifyFailureCategory(errorText, "feature-dev")).toBe("infrastructure");
    });
  }

  it("is case-insensitive for infrastructure patterns", () => {
    expect(classifyFailureCategory("SCHEMA VALIDATION FAILED", "feature-dev")).toBe(
      "infrastructure"
    );
    expect(classifyFailureCategory("ENOENT: File Not Found", "pr-create")).toBe("infrastructure");
  });
});

// ── Agent patterns ────────────────────────────────────────────────────────────

describe("classifyFailureCategory — agent", () => {
  const cases: Array<[string, string]> = [
    ["Request timeout exceeded after 30 seconds", "timeout"],
    ["connect ETIMEDOUT 192.0.2.1:443", "etimedout"],
    ["Rate limit exceeded. Retry after 60s", "rate limit"],
    ["HTTP 503 Service Unavailable", "503"],
    ["HTTP 502 Bad Gateway from upstream", "502"],
    ["HTTP 504 Gateway Timeout", "504"],
    ["Context exhausted during stage execution", "context exhausted"],
    ["Token limit reached for this request", "token limit"],
    ["Exceeded maximum context length", "maximum context"],
    ["Anthropic API error: internal server error", "api error"],
    ["claude.ai is currently overloaded", "overloaded"],
    // Issue #2871 — stall-killed subagent is agent-class
    ["feature-dev stall kill threshold reached after 4800s", "stall kill threshold"],
    ["subagent stalled and killed", "stalled and killed"],
    ["heartbeat stall detected", "heartbeat stall"],
    // Issue #3005 — second stall after adaptive retry is still agent-class
    ["failure_category=stall-killed-after-retry", "stall-killed-after-retry"],
  ];

  for (const [errorText, pattern] of cases) {
    it(`classifies as "agent" when error contains "${pattern}"`, () => {
      expect(classifyFailureCategory(errorText, "feature-dev")).toBe("agent");
    });
  }

  it("is case-insensitive for agent patterns", () => {
    expect(classifyFailureCategory("REQUEST TIMEOUT", "feature-validate")).toBe("agent");
    expect(classifyFailureCategory("Rate Limit Exceeded", "pr-create")).toBe("agent");
  });
});

// ── Organic patterns ──────────────────────────────────────────────────────────

describe("classifyFailureCategory — organic", () => {
  const cases: Array<[string, string]> = [
    ["TypeScript error TS2345: type mismatch", "ts type error"],
    ["Test suite failed: 3 tests failed", "test failure"],
    ["Build failed: cannot find module", "build error"],
    ["Acceptance criteria not met: PR diff is empty", "acceptance criteria"],
    ["Lint errors found in 2 files", "lint error"],
    ["vitest: AssertionError: expected true to be false", "test assertion"],
  ];

  for (const [errorText, description] of cases) {
    it(`classifies as "organic" for ${description}`, () => {
      expect(classifyFailureCategory(errorText, "feature-dev")).toBe("organic");
    });
  }
});

// ── Infrastructure takes priority when pattern appears before agent pattern ───

describe("classifyFailureCategory — pattern priority", () => {
  it('classifies as "infrastructure" when error contains both infra and agent patterns (infra checked first)', () => {
    // Unlikely in practice but verifies the check order
    const errorText = "Schema validation failed with timeout reading config file";
    expect(classifyFailureCategory(errorText, "feature-dev")).toBe("infrastructure");
  });
});

// ── Return type ───────────────────────────────────────────────────────────────

describe("classifyFailureCategory — return type", () => {
  it("always returns one of the three valid FailureCategory values", () => {
    const valid: FailureCategory[] = ["infrastructure", "agent", "organic"];
    const inputs = [undefined, "", "schema validation error", "timeout", "build failed"];

    for (const input of inputs) {
      const result = classifyFailureCategory(input, "feature-dev");
      expect(valid).toContain(result);
    }
  });
});
