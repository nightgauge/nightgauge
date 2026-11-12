/**
 * acReconcileRegen.test.ts
 *
 * Issue #3228 — the orchestrator, not the planning model, rebuilds
 * ac-reconcile-{N}.json from the issue's current body before feature-planning
 * dispatches. A re-run that reused an earlier report judged 9 of 13 criteria.
 * A failed regeneration fails the stage; the old report is never a fallback.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";

// tests/setup.ts stubs this module for every other suite; this one tests it.
vi.unmock("../../src/services/acReconcileRegen");

const calls: string[][] = [];
let failVerb: string | null = null;

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (_bin: string, args: string[]) => {
    calls.push(args);
    if (failVerb && args.includes(failVerb)) {
      return Promise.reject(new Error(`${failVerb}: HTTP 502`));
    }
    if (args[0] === "forge") {
      return Promise.resolve({ stdout: '{"body":"- [ ] A\\n- [ ] B\\n"}', stderr: "" });
    }
    return Promise.resolve({ stdout: "", stderr: "" });
  };
  return { ...actual, execFile: execFileMock };
});

import { regenerateAcReconcileReport } from "../../src/services/acReconcileRegen";

const params = {
  binary: "/fake/nightgauge",
  issueNumber: 42,
  cwd: "/tmp",
  repoSlug: "acme/widget",
  outPath: "/tmp/ws-3228/pipeline/ac-reconcile-42.json",
};

describe("regenerateAcReconcileReport (#3228)", () => {
  beforeEach(() => {
    calls.length = 0;
    failVerb = null;
  });

  it("reads the vouched body and writes the report with the binary", async () => {
    expect(await regenerateAcReconcileReport(params)).toBeNull();
    expect(calls[0]).toEqual([
      "forge",
      "issue",
      "view",
      "42",
      "--repo",
      "acme/widget",
      "--json",
      "--vouched",
    ]);
    const reconcile = calls[1];
    expect(reconcile.slice(0, 3)).toEqual(["preflight", "ac-reconcile", "42"]);
    expect(reconcile).toContain("--notify-empty");
    expect(reconcile[reconcile.indexOf("--out") + 1]).toBe(params.outPath);
  });

  it("fails when the body cannot be read, without running the reconcile", async () => {
    failVerb = "view";
    const err = await regenerateAcReconcileReport(params);
    expect(err?.message).toMatch(/ac-reconcile regeneration failed: .*HTTP 502/);
    expect(calls.some((a) => a.includes("ac-reconcile"))).toBe(false);
  });

  it("fails when the report cannot be written", async () => {
    failVerb = "ac-reconcile";
    const err = await regenerateAcReconcileReport(params);
    expect(err?.message).toMatch(/ac-reconcile regeneration failed/);
  });

  it("fails when no binary was resolved", async () => {
    const err = await regenerateAcReconcileReport({ ...params, binary: null });
    expect(err?.message).toMatch(/binary not found/);
    expect(calls).toHaveLength(0);
  });
});
