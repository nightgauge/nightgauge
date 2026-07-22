/**
 * #3237 Fixture: orphaned pipeline-state recovery (Issue #3238)
 *
 * Reproduces the precipitating incident exactly:
 *   - Branch present (e.g. feat/3237-…)
 *   - No context files in .nightgauge/pipeline/
 *   - Board status Ready
 *   - run-state.json absent
 *
 * Pre-Gap-1 the orchestrator hard-failed with a programmer-facing missing
 * input error. Post-Gap-1 the orchestrator surfaces a structured
 * recoverable error with the choice {restart, manual-pickup}.
 *
 * The integration concern is *not* the actual orchestrator wiring (that
 * lives in HeadlessOrchestrator and the Go scheduler) — it is the contract
 * RunStateManager.detectResume() exposes for them to consume.
 */
import { describe, it, expect, beforeEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import { RunStateManager } from "../../src/context/RunStateManager.js";

async function tmpDir(): Promise<string> {
  return await fs.mkdtemp(path.join(os.tmpdir(), "fixture-3237-"));
}

describe("#3237 fixture — orphaned state without run-state.json", () => {
  let dir: string;
  let mgr: RunStateManager;

  beforeEach(async () => {
    dir = await tmpDir();
    mgr = new RunStateManager(dir);
  });

  it("surfaces orphaned + restart/manual-pickup when only a branch is present", async () => {
    const det = await mgr.detectResume({
      branch: "feat/3237-precipitating-incident",
      hasContextFiles: false,
    });
    expect(det.kind).toBe("orphaned");
    if (det.kind === "orphaned") {
      expect(det.choices).toEqual(["restart", "manual-pickup"]);
      expect(det.branch).toBe("feat/3237-precipitating-incident");
      expect(det.hasContextFiles).toBe(false);
    }
  });

  it("surfaces orphaned when context files exist but no run-state.json", async () => {
    // Pre-Gap-1: a previous pipeline left context files but never wrote
    // run-state.json. ADR-002 says we should not silently migrate — surface
    // a recoverable orphaned record with the same choices.
    await fs.writeFile(path.join(dir, "issue-3237.json"), "{}", "utf-8");
    const det = await mgr.detectResume({
      branch: "feat/3237-precipitating-incident",
      hasContextFiles: true,
    });
    expect(det.kind).toBe("orphaned");
    if (det.kind === "orphaned") {
      expect(det.hasContextFiles).toBe(true);
      expect(det.choices).toEqual(["restart", "manual-pickup"]);
    }
  });

  it("does NOT surface orphaned when no branch and no context files (truly fresh)", async () => {
    const det = await mgr.detectResume({});
    expect(det.kind).toBe("fresh");
  });

  it("the choices array is stable for the recovery UX (Gap 2)", async () => {
    // The recovery UX consumes choices verbatim. Asserting their exact
    // ordering and content here makes any drift a hard failure rather than
    // a subtle UX bug at the next interface boundary.
    const det = await mgr.detectResume({ branch: "feat/x", hasContextFiles: false });
    if (det.kind !== "orphaned") {
      throw new Error(`expected orphaned, got ${det.kind}`);
    }
    expect(det.choices.length).toBe(2);
    expect(det.choices[0]).toBe("restart");
    expect(det.choices[1]).toBe("manual-pickup");
  });
});
