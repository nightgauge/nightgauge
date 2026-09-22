/**
 * Unit tests for AutoProviderRouter (Issue #3230).
 *
 * Asserts deterministic, calibration-driven adapter selection across the
 * mode × complexity × stage matrix.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import {
  AutoProviderRouter,
  type AutoRouterContext,
  type AutoRouterHistoryEntry,
  type AutoRouterMode,
  type RouterExecutionAdapter,
} from "../../src/analysis/AutoProviderRouter.js";
import { WORKFLOW_SUBSCORE_WEIGHT } from "../../src/analysis/auto-router-types.js";
import { defaultRegistry } from "../../src/cli/adapters/AdapterRegistry.js";
import type { NightgaugeAdapter } from "../../src/cli/adapters/ICliAdapter.js";
import type { ComplexityLabel } from "../../src/analysis/AutoModelSelector.js";

function makeCtx(overrides: Partial<AutoRouterContext> = {}): AutoRouterContext {
  return {
    stage: "feature-dev",
    mode: "automatic",
    complexity: "M",
    available_adapters: ["claude-sdk", "codex"],
    recent_history: [],
    ...overrides,
  };
}

describe("AutoProviderRouter — basic abstain rules", () => {
  const router = new AutoProviderRouter();

  it("abstains when available_adapters is empty", () => {
    const result = router.selectForStage("feature-dev", makeCtx({ available_adapters: [] }));
    expect(result).toBeNull();
  });

  it("abstains in manual mode regardless of candidates", () => {
    const result = router.selectForStage(
      "feature-dev",
      makeCtx({ mode: "manual", available_adapters: ["claude-sdk", "codex"] })
    );
    expect(result).toBeNull();
  });

  it("returns the only candidate with confidence 1.0 when single adapter is available", () => {
    const result = router.selectForStage("feature-dev", makeCtx({ available_adapters: ["codex"] }));
    expect(result).not.toBeNull();
    expect(result!.adapter).toBe("codex");
    expect(result!.confidence).toBe(1.0);
    expect(result!.rationale).toContain("only authenticated adapter");
  });
});

describe("AutoProviderRouter — determinism", () => {
  const router = new AutoProviderRouter();

  it("produces identical decisions across 100 invocations with identical input", () => {
    const ctx = makeCtx({
      available_adapters: ["claude-sdk", "codex", "gemini", "copilot"],
      complexity: "L",
      recent_history: [
        { adapter: "claude-sdk", model: "sonnet", cost_usd: 0.5, success: true },
        { adapter: "codex", model: "gpt-5.4", cost_usd: 0.4, success: true },
        { adapter: "gemini", model: "gemini-2.5-flash", cost_usd: 0.3, success: false },
      ],
    });

    const first = router.selectForStage("feature-dev", ctx);
    for (let i = 0; i < 100; i++) {
      const next = router.selectForStage("feature-dev", ctx);
      expect(next).toEqual(first);
    }
  });

  it("scores candidates in lexicographic order (tie-break is stable)", () => {
    // All adapters equally capable for lightweight stages; cost is neutral
    // (no history). Capability scores tie. Lexicographic tie-break should
    // favor "claude-sdk" over "codex" over "gemini" — but a tie produces
    // confidence < threshold, so the router abstains.
    const ctx = makeCtx({
      stage: "pr-create",
      available_adapters: ["gemini", "claude-sdk", "codex"],
      complexity: "S",
    });
    const result = router.selectForStage("pr-create", ctx);
    // Lightweight + ties → low confidence → abstain.
    expect(result).toBeNull();
  });
});

describe("AutoProviderRouter — mode rules", () => {
  const router = new AutoProviderRouter();

  it("hybrid mode abstains when no candidate dominates by ≥0.15", () => {
    // Two close candidates with similar capability scores.
    const ctx = makeCtx({
      mode: "hybrid",
      available_adapters: ["claude-sdk", "codex"],
      complexity: "M",
    });
    const result = router.selectForStage("feature-dev", ctx);
    // claude=0.92 vs codex=0.90 capability + equal cost/context → margin tiny
    expect(result).toBeNull();
  });

  it("automatic mode picks the highest score even with a thin margin", () => {
    // Use a confidence threshold low enough that any margin clears it.
    const ctx = makeCtx({
      mode: "automatic",
      available_adapters: ["claude-sdk", "codex"],
      complexity: "M",
      confidence_threshold: 0.001,
    });
    const result = router.selectForStage("feature-dev", ctx);
    expect(result).not.toBeNull();
    // Claude has the highest dev capability score.
    expect(result!.adapter).toBe("claude-sdk");
  });
});

describe("AutoProviderRouter — capability bias by stage", () => {
  const router = new AutoProviderRouter();

  it("classification stage favours claude even at lower threshold", () => {
    const ctx = makeCtx({
      stage: "issue-pickup",
      available_adapters: ["claude-sdk", "codex", "gemini"],
      confidence_threshold: 0.001,
    });
    const result = router.selectForStage("issue-pickup", ctx);
    expect(result).not.toBeNull();
    expect(result!.adapter).toBe("claude-sdk");
  });
});

describe("AutoProviderRouter — budget pressure", () => {
  const router = new AutoProviderRouter();

  const expensiveHistory: AutoRouterHistoryEntry[] = [
    { adapter: "claude-sdk", model: "sonnet", cost_usd: 1.0, success: true },
    { adapter: "claude-sdk", model: "sonnet", cost_usd: 1.2, success: true },
    { adapter: "codex", model: "gpt-5.4", cost_usd: 0.2, success: true },
    { adapter: "codex", model: "gpt-5.4", cost_usd: 0.25, success: true },
  ];

  it("under tight budget pressure, cheaper codex outranks claude on dev", () => {
    const ctx = makeCtx({
      mode: "automatic",
      stage: "feature-dev",
      complexity: "M",
      available_adapters: ["claude-sdk", "codex"],
      recent_history: expensiveHistory,
      remaining_budget_usd: 2.0,
      stage_estimated_cost_usd: 1.0,
      confidence_threshold: 0.001,
    });
    const result = router.selectForStage("feature-dev", ctx);
    expect(result).not.toBeNull();
    expect(result!.adapter).toBe("codex");
  });

  it("with comfortable budget headroom, claude's capability dominates", () => {
    const ctx = makeCtx({
      mode: "automatic",
      stage: "feature-dev",
      complexity: "M",
      available_adapters: ["claude-sdk", "codex"],
      recent_history: expensiveHistory,
      remaining_budget_usd: 100.0,
      stage_estimated_cost_usd: 1.0,
      confidence_threshold: 0.001,
    });
    const result = router.selectForStage("feature-dev", ctx);
    expect(result).not.toBeNull();
    expect(result!.adapter).toBe("claude-sdk");
  });
});

describe("AutoProviderRouter — weight overrides", () => {
  const router = new AutoProviderRouter();

  it("custom heavy-cost weight makes the cheap adapter win", () => {
    const ctx = makeCtx({
      mode: "automatic",
      stage: "feature-dev",
      available_adapters: ["claude-sdk", "codex"],
      recent_history: [
        { adapter: "claude-sdk", model: "sonnet", cost_usd: 1.0, success: true },
        { adapter: "codex", model: "gpt-5.4", cost_usd: 0.05, success: true },
      ],
      weights: { cost: 0.95, capability: 0.025, context_window: 0.025 },
      confidence_threshold: 0.001,
    });
    const result = router.selectForStage("feature-dev", ctx);
    expect(result).not.toBeNull();
    expect(result!.adapter).toBe("codex");
  });

  it("custom heavy-capability weight makes the most-capable adapter win", () => {
    const ctx = makeCtx({
      mode: "automatic",
      stage: "feature-dev",
      available_adapters: ["claude-sdk", "codex"],
      recent_history: [
        { adapter: "claude-sdk", model: "sonnet", cost_usd: 1.0, success: true },
        { adapter: "codex", model: "gpt-5.4", cost_usd: 0.05, success: true },
      ],
      weights: { cost: 0.025, capability: 0.95, context_window: 0.025 },
      confidence_threshold: 0.001,
    });
    const result = router.selectForStage("feature-dev", ctx);
    expect(result).not.toBeNull();
    expect(result!.adapter).toBe("claude-sdk");
  });
});

describe("AutoProviderRouter — model adapter remap", () => {
  const router = new AutoProviderRouter();

  it("remaps tier alias to a Codex-native model when Codex is picked", () => {
    const ctx = makeCtx({
      stage: "feature-dev",
      available_adapters: ["codex"],
      complexity: "L",
    });
    const result = router.selectForStage("feature-dev", ctx);
    expect(result).not.toBeNull();
    expect(result!.adapter).toBe("codex");
    // L complexity dev → opus tier → current frontier model on Codex
    expect(result!.model).toBe("gpt-5.6-sol");
  });

  it("remaps tier alias to a Gemini-native model when Gemini is picked", () => {
    const ctx = makeCtx({
      stage: "feature-dev",
      available_adapters: ["gemini"],
      complexity: "M",
    });
    const result = router.selectForStage("feature-dev", ctx);
    expect(result).not.toBeNull();
    expect(result!.adapter).toBe("gemini");
    // M complexity dev → sonnet tier → gemini-2.5-flash on Gemini
    expect(result!.model).toBe("gemini-2.5-flash");
  });
});

describe("AutoProviderRouter — mode × complexity × stage matrix", () => {
  const router = new AutoProviderRouter();
  const modes: AutoRouterMode[] = ["automatic", "hybrid", "manual"];
  const complexities: ComplexityLabel[] = ["XS", "S", "M", "L", "XL"];
  const stages = ["feature-planning", "feature-dev", "feature-validate"];
  const adapters: RouterExecutionAdapter[] = ["claude-sdk", "codex", "gemini"];

  for (const mode of modes) {
    for (const complexity of complexities) {
      for (const stage of stages) {
        it(`mode=${mode} complexity=${complexity} stage=${stage} produces a deterministic result`, () => {
          const ctx = makeCtx({
            stage,
            mode,
            complexity,
            available_adapters: adapters,
            confidence_threshold: 0.001,
          });
          const a = router.selectForStage(stage, ctx);
          const b = router.selectForStage(stage, ctx);
          expect(b).toEqual(a);
          if (mode === "manual") {
            expect(a).toBeNull();
          }
        });
      }
    }
  }
});

describe("AutoProviderRouter — confidence threshold abstain", () => {
  const router = new AutoProviderRouter();

  it("abstains when the top–second margin is below the threshold (default 0.7)", () => {
    // Two adapters with very close scores → margin << 0.7
    const ctx = makeCtx({
      mode: "automatic",
      stage: "feature-dev",
      available_adapters: ["claude-sdk", "codex"],
      complexity: "M",
    });
    const result = router.selectForStage("feature-dev", ctx);
    expect(result).toBeNull();
  });

  it("returns a decision when caller lowers the threshold", () => {
    const ctx = makeCtx({
      mode: "automatic",
      stage: "feature-dev",
      available_adapters: ["claude-sdk", "codex"],
      complexity: "M",
      confidence_threshold: 0.001,
    });
    const result = router.selectForStage("feature-dev", ctx);
    expect(result).not.toBeNull();
    expect(["claude-sdk", "codex"]).toContain(result!.adapter);
  });
});

// ── #1615 — opencode never outranks a paid adapter on priors alone ───────────

describe("AutoProviderRouter — opencode priors (#1615)", () => {
  const router = new AutoProviderRouter();
  const stages = [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ];

  function pick(stage: string, weights?: AutoRouterContext["weights"]) {
    return router.selectForStage(
      stage,
      makeCtx({
        stage,
        available_adapters: ["claude-headless", "opencode"],
        confidence_threshold: 0,
        weights,
      })
    );
  }

  for (const stage of stages) {
    it(`picks claude-headless over opencode for ${stage} with default weights`, () => {
      const result = pick(stage);
      expect(result).not.toBeNull();
      expect(result!.adapter).toBe("claude-headless");
      // opencode is scored from real table entries, not a missing-key fallback.
      expect(Number.isFinite(result!.scores!.opencode)).toBe(true);
      expect(result!.confidence).toBeGreaterThan(0);
    });

    it(`picks claude-headless over opencode for ${stage} on capability alone`, () => {
      const result = pick(stage, { cost: 0, capability: 1, context_window: 0 });
      expect(result).not.toBeNull();
      expect(result!.adapter).toBe("claude-headless");
      expect(Number.isFinite(result!.scores!.opencode)).toBe(true);
      expect(result!.confidence).toBeGreaterThan(0);
    });
  }
});

// ── #1645 — opencode scored by the resolved model's window (ADR 023) ────────

describe("AutoProviderRouter — opencode resolved-window scoring (#1645)", () => {
  const router = new AutoProviderRouter();

  function scoreOpencode(overrides: Partial<AutoRouterContext> = {}) {
    const ctx = makeCtx({
      available_adapters: ["claude-headless", "opencode"],
      confidence_threshold: 0,
      weights: { cost: 0, capability: 0, context_window: 1, workflow: 0 },
      ...overrides,
    });
    const result = router.selectForStage(ctx.stage, ctx);
    expect(result).not.toBeNull();
    return result!.scores!.opencode!;
  }

  it("scores a 131072-window local Qwen fixture differently from a 200000-window one", () => {
    const small = scoreOpencode({ opencode_context_window: 131_072 });
    const big = scoreOpencode({ opencode_context_window: 200_000 });
    // Both saturate to 1.0 once STAGE_EXPECTED_TOKENS is comfortably covered
    // by either window, so use a stage whose expected volume sits BETWEEN
    // the two windows to actually see them diverge.
    const smallOverStage = scoreOpencode({
      stage: "feature-dev", // STAGE_EXPECTED_TOKENS["feature-dev"] = 150_000
      opencode_context_window: 131_072,
    });
    const bigOverStage = scoreOpencode({
      stage: "feature-dev",
      opencode_context_window: 200_000,
    });
    expect(smallOverStage).toBeLessThan(1.0);
    expect(bigOverStage).toBe(1.0);
    expect(bigOverStage).toBeGreaterThan(smallOverStage);
    // Sanity: both resolved windows differ from the static 32_000 placeholder
    // in a way that would be indistinguishable from each other pre-#1645.
    expect(small).toBeGreaterThan(0);
    expect(big).toBeGreaterThan(0);
  });

  it("reverting to the static ADAPTER_CONTEXT_WINDOW_TOKENS constant would fail this assertion", () => {
    // Pinned against the real registry id so a revert to the static 32_000
    // placeholder changes the score and turns this red.
    const resolved = scoreOpencode({
      stage: "feature-dev",
      opencode_model: "anthropic/claude-sonnet-5", // registry context_window: 1_000_000
    });
    const placeholderOnly = scoreOpencode({ stage: "feature-dev" }); // no override: static 32_000
    expect(resolved).toBe(1.0);
    expect(placeholderOnly).toBeLessThan(1.0);
  });

  it("scores the registry value for a resolvable anthropic/<id> dispatch string", () => {
    const got = scoreOpencode({
      stage: "feature-dev",
      opencode_model: "anthropic/claude-sonnet-5",
    });
    // claude-sonnet-5's registry context_window (1_000_000) comfortably
    // covers feature-dev's 150_000 expected tokens: saturates to 1.0.
    expect(got).toBe(1.0);
  });

  it("falls back to the static placeholder for an unresolvable opencode_model", () => {
    const got = scoreOpencode({
      stage: "feature-dev",
      opencode_model: "lmstudio/some-unknown-local-model",
    });
    const staticOnly = scoreOpencode({ stage: "feature-dev" });
    expect(got).toBe(staticOnly);
  });

  it("grok still reads its fixed 500000 window, unaffected by the opencode fields", () => {
    const ctx = makeCtx({
      stage: "feature-dev",
      available_adapters: ["grok", "opencode"],
      confidence_threshold: 0,
      weights: { cost: 0, capability: 0, context_window: 1, workflow: 0 },
      opencode_context_window: 131_072,
    });
    const result = router.selectForStage(ctx.stage, ctx);
    expect(result).not.toBeNull();
    // grok's window (500_000) comfortably covers feature-dev's 150_000
    // expected tokens regardless of anything opencode-specific on ctx.
    expect(result!.scores!.grok).toBe(1.0);
  });
});

// ── #3912 — RouterExecutionAdapter derived from NightgaugeAdapter ────────────────

describe("AutoProviderRouter — RouterExecutionAdapter derivation (#3912)", () => {
  it("every router adapter id is a real registered NightgaugeAdapter (no bare 'claude')", () => {
    // Compile-time: `RouterExecutionAdapter[]` is assignable to
    // `NightgaugeAdapter[]` — they are the same union. Runtime: each id resolves
    // to a registered adapter, so the capability hook can be called on it.
    const adapters: RouterExecutionAdapter[] = [
      "claude-sdk",
      "claude-headless",
      "codex",
      "gemini",
      "gemini-sdk",
      "lm-studio",
      "ollama",
      "copilot",
    ];
    const asNightgauge: NightgaugeAdapter[] = adapters;
    for (const adapter of asNightgauge) {
      expect(defaultRegistry.has(adapter)).toBe(true);
    }
  });

  it("disambiguates the two Claude backends — both score identically", () => {
    const router = new AutoProviderRouter();
    const sdk = router.selectForStage(
      "feature-dev",
      makeCtx({ available_adapters: ["claude-sdk"], complexity: "M" })
    );
    const headless = router.selectForStage(
      "feature-dev",
      makeCtx({ available_adapters: ["claude-headless"], complexity: "M" })
    );
    expect(sdk!.adapter).toBe("claude-sdk");
    expect(headless!.adapter).toBe("claude-headless");
    // Same model pick — the two backends are capability-equivalent.
    expect(sdk!.model).toBe(headless!.model);
  });
});

// ── #3912 — workflow sub-score ───────────────────────────────────────────────

describe("AutoProviderRouter — workflow sub-score (#3912)", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("is the production consumer of getOrchestrationCapability() when requires_workflow", () => {
    const router = new AutoProviderRouter();
    const claudeSpy = vi.spyOn(defaultRegistry.get("claude-sdk"), "getOrchestrationCapability");
    const codexSpy = vi.spyOn(defaultRegistry.get("codex"), "getOrchestrationCapability");

    router.selectForStage(
      "feature-dev",
      makeCtx({
        requires_workflow: true,
        available_adapters: ["claude-sdk", "codex"],
        confidence_threshold: 0,
      })
    );

    expect(claudeSpy).toHaveBeenCalled();
    expect(codexSpy).toHaveBeenCalled();
  });

  it("does NOT consult the capability hook when requires_workflow is false", () => {
    const router = new AutoProviderRouter();
    const claudeSpy = vi.spyOn(defaultRegistry.get("claude-sdk"), "getOrchestrationCapability");

    router.selectForStage(
      "feature-dev",
      makeCtx({
        requires_workflow: false,
        available_adapters: ["claude-sdk", "codex"],
        confidence_threshold: 0,
      })
    );

    expect(claudeSpy).not.toHaveBeenCalled();
  });

  it("prefers a native-workflow adapter over an sdk-fanout one when requires_workflow", () => {
    const router = new AutoProviderRouter();
    // gemini is sdk-fanout with a 1M context window; claude-sdk is
    // native-workflow. With the workflow dimension active, claude wins.
    const decision = router.selectForStage(
      "feature-planning",
      makeCtx({
        stage: "feature-planning",
        requires_workflow: true,
        available_adapters: ["claude-sdk", "gemini"],
        confidence_threshold: 0,
      })
    );
    expect(decision).not.toBeNull();
    expect(decision!.adapter).toBe("claude-sdk");
    expect(decision!.rationale).toContain("workflow=");
  });

  it("keeps Codex routable for a workflow-eligible stage when Claude is unavailable", () => {
    const router = new AutoProviderRouter();
    const decision = router.selectForStage(
      "feature-dev",
      makeCtx({
        requires_workflow: true,
        available_adapters: ["codex", "ollama"],
        confidence_threshold: 0,
      })
    );
    expect(decision).not.toBeNull();
    // Codex (sdk-fanout, strong capability + large window) out-scores Ollama.
    expect(decision!.adapter).toBe("codex");
  });

  it("ranks native-workflow above codex but keeps codex scored, not zeroed", () => {
    const router = new AutoProviderRouter();
    const decision = router.selectForStage(
      "feature-dev",
      makeCtx({
        requires_workflow: true,
        available_adapters: ["claude-sdk", "codex"],
        confidence_threshold: 0,
      })
    );
    expect(decision!.adapter).toBe("claude-sdk");
    expect(decision!.scores!.codex).toBeGreaterThan(0);
  });
});

// ── #3912 — non-workflow routing is unchanged ────────────────────────────────

describe("AutoProviderRouter — non-workflow routing unchanged (#3912)", () => {
  const router = new AutoProviderRouter();

  it("requires_workflow:false yields an identical decision to omitting the flag", () => {
    const adapters: RouterExecutionAdapter[] = ["claude-sdk", "codex", "gemini"];
    const omitted = router.selectForStage(
      "feature-dev",
      makeCtx({ available_adapters: adapters, confidence_threshold: 0 })
    );
    const explicitFalse = router.selectForStage(
      "feature-dev",
      makeCtx({ available_adapters: adapters, requires_workflow: false, confidence_threshold: 0 })
    );
    expect(explicitFalse).toEqual(omitted);
  });

  it("omits the workflow fragment from the rationale for non-workflow routing", () => {
    const decision = router.selectForStage(
      "feature-dev",
      makeCtx({
        available_adapters: ["claude-sdk", "gemini"],
        requires_workflow: false,
        confidence_threshold: 0,
      })
    );
    expect(decision?.rationale ?? "").not.toContain("workflow=");
  });

  it("reserves a fractional WORKFLOW_SUBSCORE_WEIGHT in (0, 1)", () => {
    expect(WORKFLOW_SUBSCORE_WEIGHT).toBeGreaterThan(0);
    expect(WORKFLOW_SUBSCORE_WEIGHT).toBeLessThan(1);
  });
});
