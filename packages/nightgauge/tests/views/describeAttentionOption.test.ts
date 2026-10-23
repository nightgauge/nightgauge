/**
 * The option descriptions the Action Center shows under each button.
 *
 * Issue #405 — the fleet-halt card's Retry options carry
 * `then: "autonomous.resume"`, and resuming the halted fleet IS the
 * consequence the operator is choosing: clearing a cooldown on a paused fleet
 * dispatches nothing. The description has to say so, or the button that lifts
 * a fleet-wide halt reads like a per-issue cooldown reset.
 */

import { describe, it, expect } from "vitest";
import { describeAttentionOption } from "../../src/views/attention/attentionTreeItems";
import type { AttentionOption } from "../../src/services/IpcClientBase";

const option = (verb: string, args?: Record<string, unknown>): AttentionOption => ({
  id: "opt",
  label: "Opt",
  verb,
  args,
});

describe("describeAttentionOption (#405)", () => {
  it("names the resume consequence on the Retry option", () => {
    const got = describeAttentionOption(
      option("autonomous.clearIssueFailures", {
        key: "octocat/acme#405",
        then: "autonomous.resume",
      })
    );
    expect(got).toContain("cooldown");
    expect(got).toMatch(/resumes the halted fleet/i);
  });

  it("names it on Retry with escalation too", () => {
    const got = describeAttentionOption(
      option("run.retryWithEscalation", { tier: "opus", then: "autonomous.resume" })
    );
    expect(got).toContain("opus");
    expect(got).toMatch(/resumes the halted fleet/i);
  });

  it("leaves the rescan and bare variants unchanged", () => {
    expect(
      describeAttentionOption(
        option("autonomous.clearIssueFailures", { then: "autonomous.rescan" })
      )
    ).toBe("Clears the failure cooldown and triggers a rescan.");
    expect(describeAttentionOption(option("autonomous.clearIssueFailures"))).toBe(
      "Clears the failure cooldown for this issue."
    );
    expect(describeAttentionOption(option("run.retryWithEscalation", { tier: "opus" }))).toBe(
      "Retries with the model escalated to opus."
    );
  });
});
