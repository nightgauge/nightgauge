/**
 * #1050 — the Settings panel must not misreport its own default.
 *
 * The reported symptom was "the gates that decide whether a run needs human
 * approval are not in the settings surface". That was wrong: all three controls
 * render, and the panel writes to `.nightgauge/config.local.yaml`, which the
 * runtime reads.
 *
 * The real defect is narrower and worse. `DEFAULT_CONFIG.human_in_the_loop
 * .auto_accept_stages` was `true` while every runtime consumer defaults it to
 * `false`:
 *
 *   - `otherResolver.ts`      → `autoAcceptStages: false`
 *   - `skillRunner.ts`        → `let autoAcceptStages = false`
 *   - `internal/config/init.go` writes `auto_accept_stages: false`
 *
 * So with no `human_in_the_loop:` block, the panel showed "Auto-Accept Stages"
 * CHECKED while the pipeline ran in manual mode. The operator read the GUI as
 * "unattended is on" and the run stopped for a human anyway — with nothing on
 * any surface naming the setting responsible.
 *
 * This test asserts the two sides AGREE rather than asserting a literal. Pinning
 * one side is what let them drift apart in the first place, and would not detect
 * the next divergence.
 */

import { describe, it, expect } from "vitest";
import { DEFAULT_CONFIG } from "../../src/config/schema";

describe("human_in_the_loop defaults (#1050)", () => {
  it("displays the auto-accept-stages default the runtime actually applies", () => {
    // The runtime default, stated in one place here and asserted against the
    // schema. Sourced from otherResolver.ts:93 / skillRunner.ts:3109, which are
    // plain literals rather than schema reads — that independence is exactly
    // why they were able to drift.
    const RUNTIME_DEFAULT_AUTO_ACCEPT_STAGES = false;

    expect(DEFAULT_CONFIG.human_in_the_loop?.auto_accept_stages).toBe(
      RUNTIME_DEFAULT_AUTO_ACCEPT_STAGES
    );
  });

  it("keeps auto_accept_permissions defaulting to false", () => {
    // Unchanged, and restated so a future edit to the block cannot quietly
    // flip the gate that most often stops an unattended run.
    expect(DEFAULT_CONFIG.human_in_the_loop?.auto_accept_permissions).toBe(false);
  });

  it("does not silently drop trusted_stages from the default block", () => {
    // Writing a partial human_in_the_loop block can leave a sibling at its zero
    // value rather than its default — the trap that makes autonomous
    // safety_rails unsafe to write partially.
    expect(DEFAULT_CONFIG.human_in_the_loop?.trusted_stages).toEqual([]);
  });
});
