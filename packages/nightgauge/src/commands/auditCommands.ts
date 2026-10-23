import * as vscode from "vscode";
import type { TierGate } from "../platform/TierGate";
import type { LicensePreflight } from "../platform/LicensePreflight";

const DASHBOARD_ROUTES: Record<string, string> = {
  audit: "/audit",
  analytics: "/analytics",
  compliance: "/compliance",
  // NOT "/cost". The dashboard SPA has no such route — its router declares
  // `analytics/forecast` with breadcrumb "Cost Forecast", and its own nav links
  // `{label:"Cost Forecast", route:"/analytics/forecast"}`. The catch-all is
  // `{path:"**", redirectTo:"/login"}`, so "/cost" bounced a signed-in user to
  // the login page — a dead command that looked wired up. Verified against the
  // deployed bundle, not assumed.
  cost: "/analytics/forecast",
  // NOT "/account", and not on the marketing host either — the old links were
  // wrong twice (#1018). `https://nightgauge.dev/account` pointed at the
  // marketing site, which has no such page, and the dashboard SPA declares no
  // `account` route either, so even the corrected host would hit the same
  // `{path:"**", redirectTo:"/login"}` catch-all that "/cost" hit above.
  // `/billing` is the real subscription surface. Its `billingGuard` degrades to
  // `/dashboard` for a user without `billing:read` rather than bouncing to
  // login. Verified against the dashboard's own app.routes.ts, not assumed.
  account: "/billing",
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

/**
 * The subscription/account surface on the hosted dashboard.
 *
 * Exists so the six call sites that used to hardcode a marketing-host URL share
 * one definition with the audit/analytics/compliance links, and so a route
 * change is a one-line edit rather than a six-site sweep (#1018).
 */
export function buildAccountUrl(): string {
  return buildDashboardUrl(DASHBOARD_ROUTES.account);
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
