import { describe, it, expect, vi, beforeEach } from "vitest";

const { mockGetConfiguration, mockOpenExternal, mockRegisterCommand, mockShowInformationMessage } =
  vi.hoisted(() => ({
    mockGetConfiguration: vi.fn(),
    mockOpenExternal: vi.fn(),
    mockRegisterCommand: vi.fn(),
    mockShowInformationMessage: vi.fn(),
  }));

vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: mockGetConfiguration,
  },
  env: {
    openExternal: mockOpenExternal,
  },
  window: {
    showInformationMessage: mockShowInformationMessage,
  },
  Uri: {
    parse: (url: string) => ({ toString: () => url, _url: url }),
  },
  commands: {
    registerCommand: mockRegisterCommand,
  },
}));

import {
  buildDashboardUrl,
  registerAuditDashboardCommands,
} from "../../src/commands/auditCommands";

describe("buildDashboardUrl", () => {
  beforeEach(() => {
    mockGetConfiguration.mockReturnValue({
      get: () => undefined,
    });
  });

  it("uses default base URL when config is not set", () => {
    const url = buildDashboardUrl("/audit");
    expect(url).toBe("https://dashboard.nightgauge.dev/audit");
  });

  it("appends accountId query param when provided", () => {
    const url = buildDashboardUrl("/audit", "acct-123");
    expect(url).toBe("https://dashboard.nightgauge.dev/audit?accountId=acct-123");
  });

  it("encodes accountId in the query param", () => {
    const url = buildDashboardUrl("/audit", "acct 456");
    expect(url).toBe("https://dashboard.nightgauge.dev/audit?accountId=acct%20456");
  });

  it("omits query param when accountId is undefined", () => {
    const url = buildDashboardUrl("/analytics", undefined);
    expect(url).toBe("https://dashboard.nightgauge.dev/analytics");
  });

  it("uses custom dashboardUrl config value", () => {
    mockGetConfiguration.mockReturnValue({
      get: (key: string) => (key === "dashboardUrl" ? "https://custom.example.com" : undefined),
    });
    const url = buildDashboardUrl("/compliance");
    expect(url).toBe("https://custom.example.com/compliance");
  });

  it("strips trailing slash from base URL", () => {
    mockGetConfiguration.mockReturnValue({
      get: (key: string) =>
        key === "dashboardUrl" ? "https://dashboard.nightgauge.dev/" : undefined,
    });
    const url = buildDashboardUrl("/cost");
    expect(url).toBe("https://dashboard.nightgauge.dev/cost");
  });
});

describe("registerAuditDashboardCommands", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockGetConfiguration.mockReturnValue({ get: () => undefined });
    mockRegisterCommand.mockImplementation((_id, handler) => ({
      dispose: vi.fn(),
      _handler: handler,
    }));
    mockOpenExternal.mockResolvedValue(true);
  });

  it("registers exactly 5 commands", () => {
    registerAuditDashboardCommands(() => undefined);
    expect(mockRegisterCommand).toHaveBeenCalledTimes(5);
  });

  it("registers the four palette commands with correct ids", () => {
    registerAuditDashboardCommands(() => undefined);
    const ids = mockRegisterCommand.mock.calls.map(([id]) => id);
    expect(ids).toContain("nightgauge.openAuditDashboard");
    expect(ids).toContain("nightgauge.openAnalyticsDashboard");
    expect(ids).toContain("nightgauge.openComplianceReports");
    expect(ids).toContain("nightgauge.openCostForecast");
    expect(ids).toContain("nightgauge.openCurrentTabInBrowser");
  });

  it("openAuditDashboard calls openExternal with /audit URL", async () => {
    registerAuditDashboardCommands(() => undefined);
    const auditCall = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openAuditDashboard"
    );
    await auditCall![1]();
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://dashboard.nightgauge.dev/audit" })
    );
  });

  it("openAnalyticsDashboard calls openExternal with /analytics URL", async () => {
    registerAuditDashboardCommands(() => undefined);
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openAnalyticsDashboard"
    );
    await call![1]();
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://dashboard.nightgauge.dev/analytics" })
    );
  });

  it("openComplianceReports calls openExternal with /compliance URL", async () => {
    registerAuditDashboardCommands(() => undefined);
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openComplianceReports"
    );
    await call![1]();
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://dashboard.nightgauge.dev/compliance" })
    );
  });

  it("openCostForecast calls openExternal with /cost URL", async () => {
    registerAuditDashboardCommands(() => undefined);
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openCostForecast"
    );
    await call![1]();
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://dashboard.nightgauge.dev/cost" })
    );
  });

  it("openCurrentTabInBrowser with known tab key opens correct route", async () => {
    registerAuditDashboardCommands(() => undefined);
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openCurrentTabInBrowser"
    );
    await call![1]("compliance");
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://dashboard.nightgauge.dev/compliance" })
    );
  });

  it("openCurrentTabInBrowser with unknown tab key falls back to /", async () => {
    registerAuditDashboardCommands(() => undefined);
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openCurrentTabInBrowser"
    );
    await call![1]("unknowntab");
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://dashboard.nightgauge.dev/" })
    );
  });

  it("openCurrentTabInBrowser with no argument falls back to /", async () => {
    registerAuditDashboardCommands(() => undefined);
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openCurrentTabInBrowser"
    );
    await call![1](undefined);
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://dashboard.nightgauge.dev/" })
    );
  });

  it("includes accountId in URL when getAccountId returns a value", async () => {
    registerAuditDashboardCommands(() => "my-account");
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openAuditDashboard"
    );
    await call![1]();
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({
        _url: "https://dashboard.nightgauge.dev/audit?accountId=my-account",
      })
    );
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Tier gating for the analytics dashboard (Issue #4156)
// ─────────────────────────────────────────────────────────────────────────

describe("registerAuditDashboardCommands — advanced-analytics tier gate", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockGetConfiguration.mockReturnValue({ get: () => undefined });
    mockRegisterCommand.mockImplementation((_id, handler) => ({
      dispose: vi.fn(),
      _handler: handler,
    }));
    mockOpenExternal.mockResolvedValue(true);
  });

  function makeTierGate(allowed: boolean) {
    return {
      check: vi.fn().mockReturnValue({
        allowed,
        requiredTier: "pro",
        upgradeUrl: "https://nightgauge.dev/pricing",
      }),
    };
  }

  function makeLicensePreflight(tier: string) {
    return { validate: vi.fn().mockResolvedValue({ tier }) };
  }

  it("opens the analytics dashboard when the tier gate allows it", async () => {
    registerAuditDashboardCommands(
      () => undefined,
      makeTierGate(true) as any,
      makeLicensePreflight("pro") as any
    );
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openAnalyticsDashboard"
    );
    await call![1]();
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://dashboard.nightgauge.dev/analytics" })
    );
  });

  it("blocks the analytics dashboard and offers an upgrade prompt when the tier gate denies it", async () => {
    mockShowInformationMessage.mockResolvedValue(undefined);
    registerAuditDashboardCommands(
      () => undefined,
      makeTierGate(false) as any,
      makeLicensePreflight("community") as any
    );
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openAnalyticsDashboard"
    );
    await call![1]();
    expect(mockOpenExternal).not.toHaveBeenCalled();
    expect(mockShowInformationMessage).toHaveBeenCalled();
  });

  it("opens the upgrade page when the user picks 'View Plans' after being blocked", async () => {
    mockShowInformationMessage.mockResolvedValue("View Plans");
    registerAuditDashboardCommands(
      () => undefined,
      makeTierGate(false) as any,
      makeLicensePreflight("community") as any
    );
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openAnalyticsDashboard"
    );
    await call![1]();
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://nightgauge.dev/pricing" })
    );
  });

  it("gates the analytics route reached via openCurrentTabInBrowser too", async () => {
    mockShowInformationMessage.mockResolvedValue(undefined);
    registerAuditDashboardCommands(
      () => undefined,
      makeTierGate(false) as any,
      makeLicensePreflight("community") as any
    );
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openCurrentTabInBrowser"
    );
    await call![1]("analytics");
    expect(mockOpenExternal).not.toHaveBeenCalled();
    expect(mockShowInformationMessage).toHaveBeenCalled();
  });

  it("does not gate the audit/compliance/cost routes", async () => {
    registerAuditDashboardCommands(
      () => undefined,
      makeTierGate(false) as any,
      makeLicensePreflight("community") as any
    );
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openAuditDashboard"
    );
    await call![1]();
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://dashboard.nightgauge.dev/audit" })
    );
  });

  it("skips the gate entirely when tierGate/licensePreflight are not provided", async () => {
    registerAuditDashboardCommands(() => undefined);
    const call = mockRegisterCommand.mock.calls.find(
      ([id]) => id === "nightgauge.openAnalyticsDashboard"
    );
    await call![1]();
    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://dashboard.nightgauge.dev/analytics" })
    );
  });
});
