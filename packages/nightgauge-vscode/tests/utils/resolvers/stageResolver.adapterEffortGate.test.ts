/**
 * stageResolver.adapterEffortGate.test.ts (#569)
 *
 * `checkAdapterEffortSupported` — the registry effort gate for ADAPTER
 * dispatches. The extension's only effort enforcement used to be gated
 * `adapter === "claude"`, so codex/gemini/grok/copilot dispatches bypassed the
 * registry entirely; a provider-global `xhigh` against a model topping out at
 * `high` reproduced #532's failure signature (exit 1 in seconds, no work,
 * nothing classified). The gate now consults the registry for every adapter.
 *
 * Tests run against the REAL registry — a hand-authored fixture would
 * reintroduce the second authority this issue deletes (same discipline as
 * stageResolver.effortAuthority.test.ts for #336).
 */

import { describe, it, expect } from "vitest";
import { checkAdapterEffortSupported } from "../../../src/utils/resolvers/stageResolver";

describe("checkAdapterEffortSupported — registry gate for adapter dispatches (#569)", () => {
  it("rejects xhigh against grok-4.5 (tops out at high) — the #532 repro", () => {
    const result = checkAdapterEffortSupported("grok", "xhigh", "grok-4.5", "feature-dev");
    expect(result.ok).toBe(false);
    // The reason names the model, the requested effort, and the declared ladder.
    expect(result.reason).toContain("grok-4.5");
    expect(result.reason).toContain('"xhigh"');
    expect(result.reason).toContain("low, medium, high");
    expect(result.reason).toContain('stage "feature-dev"');
    expect(result.supported).toEqual(["low", "medium", "high"]);
  });

  it("accepts every rung the resolved model declares", () => {
    for (const effort of ["low", "medium", "high", "xhigh"]) {
      expect(checkAdapterEffortSupported("grok", effort, "grok-4.6").ok).toBe(true);
    }
    for (const effort of ["low", "medium", "high"]) {
      expect(checkAdapterEffortSupported("codex", effort, "gpt-5.4").ok).toBe(true);
    }
  });

  it("enforces AFTER normalization: none/minimal collapse to low (#523)", () => {
    // grok-4.5 declares low, so the grok-native sub-low rungs are legal…
    expect(checkAdapterEffortSupported("grok", "none", "grok-4.5").ok).toBe(true);
    expect(checkAdapterEffortSupported("grok", "minimal", "grok-4.5").ok).toBe(true);
    // …and against a no-axis model they are rejected like any explicit effort.
    const noAxis = checkAdapterEffortSupported("grok", "none", "grok-build-0.1");
    expect(noAxis.ok).toBe(false);
  });

  it("resolves band names to the provider's served model before enforcing", () => {
    // sonnet → grok-4.6 for the grok adapter, which declares xhigh…
    expect(checkAdapterEffortSupported("grok", "xhigh", "sonnet").ok).toBe(true);
    // …but not max, above every current xai ladder.
    const overMax = checkAdapterEffortSupported("grok", "max", "sonnet");
    expect(overMax.ok).toBe(false);
    expect(overMax.reason).toContain("grok-4.6");
  });

  it("rejects ANY explicit effort against supported_efforts: [] (#336)", () => {
    // `[]` is a positive declaration — no effort axis. Unlike
    // assertEffortSupported (whose emission gate makes `[]` unreachable with a
    // flag in hand), an explicit adapter effort here WOULD reach the CLI, so
    // dropping it silently is exactly the downgrade #75 forbids.
    const result = checkAdapterEffortSupported("grok", "low", "grok-build-0.1");
    expect(result.ok).toBe(false);
    expect(result.reason).toContain("no effort axis");
  });

  it("passes with a warning for a model with no registry descriptor (#336)", () => {
    const unknown = checkAdapterEffortSupported("grok", "xhigh", "some-local-model");
    expect(unknown.ok).toBe(true);
    expect(unknown.warning).toContain("no registry descriptor");
    // No model at all (CLI default) → same unverifiable pass-through.
    const noModel = checkAdapterEffortSupported("grok", "xhigh", undefined);
    expect(noModel.ok).toBe(true);
    expect(noModel.warning).toBeTruthy();
  });

  it("passes with a warning for values outside the Nightgauge ladder", () => {
    // The adapter's own CLI-syntax filter drops these; the warning keeps the
    // drop from being silent.
    const result = checkAdapterEffortSupported("grok", "banana", "grok-4.5");
    expect(result.ok).toBe(true);
    expect(result.warning).toContain('"banana"');
  });

  it("nothing to enforce when no effort is requested", () => {
    expect(checkAdapterEffortSupported("grok", undefined, "grok-4.5")).toEqual({ ok: true });
    expect(checkAdapterEffortSupported("grok", "  ", "grok-4.5")).toEqual({ ok: true });
  });

  it("consults the registry for every adapter, not just claude", () => {
    // codex: gpt-5.4 declares [low, medium, high] — xhigh fails closed.
    const codex = checkAdapterEffortSupported("codex", "xhigh", "gpt-5.4", "feature-dev");
    expect(codex.ok).toBe(false);
    expect(codex.reason).toContain("gpt-5.4");
    // gemini: gemini-2.5-pro declares [low, medium, high] — max fails closed.
    expect(checkAdapterEffortSupported("gemini", "max", "gemini-2.5-pro").ok).toBe(false);
    // copilot: gpt-4o declares [low, medium, high] — high passes.
    expect(checkAdapterEffortSupported("copilot", "high", "gpt-4o").ok).toBe(true);
    // claude: the haiku band declares [] — explicit adapter effort rejected.
    expect(checkAdapterEffortSupported("claude", "high", "claude-haiku-4-5-20251001").ok).toBe(
      false
    );
  });
});
