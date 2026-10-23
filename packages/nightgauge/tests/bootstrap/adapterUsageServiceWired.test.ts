/**
 * adapterUsageServiceWired.test.ts
 *
 * Issue #658 shipped `AdapterUsageService` with zero production call sites —
 * its own ADR says so outright ("This PR ships a service with no production
 * consumer"), by design: #659 was the ticket scoped to wire it up. This
 * asserts that wiring actually landed and stays landed, the same way
 * tests/bootstrap/duplicateRunRecordWritersRemoved.test.ts pins a bootstrap
 * wiring fact against regression by reading the source directly — bootstrap
 * initialization is impractical to instantiate in a unit test (heavy VSCode
 * API surface, dozens of interdependent services), so there is no runnable
 * call path to exercise instead.
 *
 * @see Issue #659 - Adapter usage meter in the status bar
 * @see docs/decisions/018-adapter-usage-quota-model.md
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const SERVICES_PATH = path.resolve(__dirname, "../../src/bootstrap/services.ts");
const servicesSource = readFileSync(SERVICES_PATH, "utf-8");

describe("AdapterUsageService has a production consumer (Issue #659)", () => {
  it("is constructed via forWorkspace() in bootstrap/services.ts", () => {
    expect(servicesSource).toContain("AdapterUsageService.forWorkspace(");
  });

  it("is initialized and disposed like every other bootstrap service", () => {
    expect(servicesSource).toContain("adapterUsageService.initialize()");
    expect(servicesSource).toContain("context.subscriptions.push(adapterUsageService)");
  });

  it("feeds the status bar meter via onDidChangeUsage → showUsageSnapshot", () => {
    expect(servicesSource).toContain("adapterUsageService.onDidChangeUsage(");
    expect(servicesSource).toContain("statusBar.showUsageSnapshot(snapshot)");
  });

  it("feeds the dashboard usage panel from the same instance (Issue #661)", () => {
    // The panel and the status-bar meter must answer the same question with
    // the same number, so bootstrap hands the panel the *same* service object
    // rather than constructing a second one.
    expect(servicesSource).toContain("dashboard.setAdapterUsageService(adapterUsageService)");
    expect(servicesSource.match(/AdapterUsageService\.forWorkspace\(/g)).toHaveLength(1);
  });

  it("feeds UsageLimitsService — no second, divergent budget-alert data path", () => {
    // #683: UsageLimitsService must read the same AdapterUsageService
    // instance the status bar renders from, not DashboardState.getAggregates
    // directly — that mismatch is exactly the bug #683 fixed.
    expect(servicesSource).toContain(
      "new UsageLimitsService(adapterUsageService, notificationService)"
    );
    expect(servicesSource).not.toContain('dashboardState.getAggregates("all")');
  });
});
