import * as vscode from "vscode";
import type { TierGate } from "../platform/TierGate";
import type { LicensePreflight } from "../platform/LicensePreflight";

const DASHBOARD_ROUTES: Record<string, string> = {
  audit: "/audit",
  analytics: "/analytics",
  compliance: "/compliance",
  cost: "/cost",
};

function getDashboardBaseUrl(): string {
  return (
    vscode.workspace.getConfiguration("nightgauge").get<string>("dashboardUrl") ??
    "https://dashboard.nightgauge.dev"
  );
}

export function buildDashboardUrl(route: string, accountId?: string): string {
  const base = getDashboardBaseUrl().replace(/\/$/, "");
  const query = accountId ? `?accountId=${encodeURIComponent(accountId)}` : "";
  return `${base}${route}${query}`;
}

export function registerAuditDashboardCommands(
  getAccountId: () => string | undefined,
  tierGate?: TierGate | null,
  licensePreflight?: LicensePreflight | null
): vscode.Disposable[] {
  const open = async (route: string) => {
    const url = buildDashboardUrl(route, getAccountId());
    await vscode.env.openExternal(vscode.Uri.parse(url));
  };

  // Tier gate: the analytics dashboard is the "advanced-analytics" feature
  // (Issue #4156) — previously completely ungated, so any tier could open
  // it. Audit/compliance/cost routes are left ungated here (no FEATURE_TIER_MAP
  // entry maps to them specifically).
  const openAnalytics = async () => {
    if (tierGate && licensePreflight) {
      const preflightResult = await licensePreflight.validate();
      const gate = tierGate.check("advanced-analytics", preflightResult.tier);
      if (!gate.allowed) {
        const action = await vscode.window.showInformationMessage(
          `Analytics dashboard requires ${gate.requiredTier} tier. Upgrade to unlock advanced analytics.`,
          "View Plans"
        );
        if (action === "View Plans") {
          void vscode.env.openExternal(vscode.Uri.parse(gate.upgradeUrl));
        }
        return;
      }
    }
    await open(DASHBOARD_ROUTES.analytics);
  };

  return [
    vscode.commands.registerCommand("nightgauge.openAuditDashboard", () =>
      open(DASHBOARD_ROUTES.audit)
    ),
    vscode.commands.registerCommand("nightgauge.openAnalyticsDashboard", openAnalytics),
    vscode.commands.registerCommand("nightgauge.openComplianceReports", () =>
      open(DASHBOARD_ROUTES.compliance)
    ),
    vscode.commands.registerCommand("nightgauge.openCostForecast", () =>
      open(DASHBOARD_ROUTES.cost)
    ),
    vscode.commands.registerCommand("nightgauge.openCurrentTabInBrowser", (tabId?: string) => {
      const route = DASHBOARD_ROUTES[tabId ?? ""] ?? "/";
      if (route === DASHBOARD_ROUTES.analytics) {
        return openAnalytics();
      }
      return open(route);
    }),
  ];
}
