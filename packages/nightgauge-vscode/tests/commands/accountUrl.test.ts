/**
 * Issue #1018: "View Account" opened a 404.
 *
 * The links were wrong twice. `https://nightgauge.dev/account` names the
 * MARKETING host, which has no such page — and the dashboard SPA declares no
 * `account` route either, so even the corrected host would have hit the
 * `{path:"**", redirectTo:"/login"}` catch-all. That is the identical failure
 * "/cost" hit and that DASHBOARD_ROUTES already carries a comment about.
 *
 * The regression guard that matters is not "buildAccountUrl returns a string" —
 * it is that no source file goes back to hardcoding a host. Six call sites drifted
 * from the one function that knows where the dashboard lives; a unit test on the
 * function cannot see that happen.
 */

import { describe, it, expect, vi } from "vitest";
import * as fs from "fs";
import * as path from "path";

vi.mock("vscode", () => ({
  workspace: { getConfiguration: () => ({ get: () => undefined }) },
}));

import { buildAccountUrl, buildDashboardUrl } from "../../src/commands/auditCommands";

const SRC = path.join(__dirname, "..", "..", "src");

function walk(dir: string, out: string[] = []): string[] {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(p, out);
    else if (entry.name.endsWith(".ts")) out.push(p);
  }
  return out;
}

describe("account URL (#1018)", () => {
  it("resolves to the dashboard's billing route, not a marketing-host page", () => {
    const url = buildAccountUrl();
    expect(url).toBe("https://dashboard.nightgauge.dev/billing");
    // The two things that were wrong, asserted separately so a future change
    // that fixes one and breaks the other cannot pass.
    expect(url).not.toContain("nightgauge.dev/account");
    expect(url.startsWith(buildDashboardUrl(""))).toBe(true);
  });

  it("honours a configured dashboard host", async () => {
    const vscode = await import("vscode");
    vi.spyOn(vscode.workspace, "getConfiguration").mockReturnValue({
      get: () => "https://dash.example.test/",
    } as never);
    // The trailing slash must not double up.
    expect(buildAccountUrl()).toBe("https://dash.example.test/billing");
    vi.restoreAllMocks();
  });

  it("no source file hardcodes the marketing-host account URL", () => {
    const offenders = walk(SRC)
      .filter((f) => {
        const body = fs.readFileSync(f, "utf-8");
        // Strip comments so the explanatory note in auditCommands.ts — which
        // deliberately names the bad URL — is not counted as a violation.
        const code = body
          .replace(/\/\*[\s\S]*?\*\//g, "")
          .split("\n")
          .filter((l) => !l.trim().startsWith("//"))
          .join("\n");
        return code.includes("nightgauge.dev/account");
      })
      .map((f) => path.relative(SRC, f));

    expect(
      offenders,
      "these files hardcode the marketing-host account URL instead of calling " +
        "buildAccountUrl(); the dashboard's catch-all redirects it to /login"
    ).toEqual([]);
  });
});
