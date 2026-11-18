/**
 * dashboardMessageGuard.test.ts
 *
 * Per-type validation of Dashboard webview messages.
 */

import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import {
  DASHBOARD_EXECUTABLE_COMMANDS,
  checkDashboardMessage,
} from "../../../src/views/dashboard/dashboardMessageGuard";

const DASHBOARD_SRC = join(__dirname, "..", "..", "..", "src", "views", "dashboard");

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = join(dir, name);
    return statSync(full).isDirectory() ? walk(full) : full.endsWith(".ts") ? [full] : [];
  });
}

describe("checkDashboardMessage", () => {
  it("rejects executeCommand for a command outside the allowlist", () => {
    for (const command of ["nightgauge.resetPipeline", "workbench.action.terminal.new", ""]) {
      expect(checkDashboardMessage({ type: "executeCommand", command }).ok).toBe(false);
    }
  });

  it.each(DASHBOARD_EXECUTABLE_COMMANDS)("accepts executeCommand %s", (command) => {
    expect(checkDashboardMessage({ type: "executeCommand", command }).ok).toBe(true);
  });

  it("allowlists exactly the commands the Dashboard webview sends", () => {
    const sent = new Set<string>();
    for (const file of walk(DASHBOARD_SRC)) {
      const body = readFileSync(file, "utf8");
      for (const m of body.matchAll(/type:\s*'executeCommand',\s*command:\s*'([^']+)'/g)) {
        sent.add(m[1]);
      }
    }
    expect([...sent].sort()).toEqual([...DASHBOARD_EXECUTABLE_COMMANDS].sort());
  });

  it("rejects an unknown type and a malformed message", () => {
    expect(checkDashboardMessage({ type: "nope" })).toMatchObject({
      ok: false,
      reason: "unknown-type",
    });
    expect(checkDashboardMessage({ type: "__proto__" })).toMatchObject({ ok: false });
    expect(checkDashboardMessage({})).toMatchObject({ ok: false, reason: "malformed" });
    expect(checkDashboardMessage(null)).toMatchObject({ ok: false, reason: "malformed" });
    expect(checkDashboardMessage([])).toMatchObject({ ok: false, reason: "malformed" });
  });

  it.each([
    [{ type: "openSlotOutput", slotIndex: -1 }],
    [{ type: "openSlotOutput", slotIndex: "1" }],
    [{ type: "setScope", scope: "everything" }],
    [{ type: "export", format: "json", target: "elsewhere" }],
    [{ type: "selectRun", issueNumber: Number.NaN }],
    [{ type: "auditFilter", filters: { dateFrom: 1 } }],
    [{ type: "retentionUpdate", retentionDays: 99999 }],
    [{ type: "mergeDependabotPR", prNodeId: "" }],
    [{ type: "firewallFilter", filter: "eventType", value: { x: 1 } }],
  ])("rejects invalid fields %j", (msg) => {
    expect(checkDashboardMessage(msg)).toMatchObject({ ok: false, reason: "invalid-fields" });
  });

  it.each([
    [{ type: "refresh" }],
    [{ type: "openSlotOutput", slotIndex: 0 }],
    [{ type: "setScope", scope: "all" }],
    [{ type: "export", format: "csv", target: "current" }],
    [{ type: "export", format: "json", target: { issueNumber: 3, runId: "r" } }],
    [{ type: "loadRunDetails", issueNumber: 7, startedAt: "2026-01-01T00:00:00Z" }],
    [
      {
        type: "auditFilter",
        filters: { dateFrom: "a", dateTo: "b", actionFilter: "", userFilter: "" },
      },
    ],
    [{ type: "firewallFilter", filter: "category", value: ["x"] }],
    [{ type: "mergeDependabotPR", prNodeId: "PR_1", owner: "o", repo: "r" }],
    [{ type: "openInBrowser" }],
    [{ type: "trendsDateRangeChange", range: "90d" }],
  ])("accepts the shape the webview sends %j", (msg) => {
    expect(checkDashboardMessage(msg).ok).toBe(true);
  });
});
