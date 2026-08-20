/**
 * Tests for the "Nightgauge: Show Diagnostics" command (#749).
 *
 * The load-bearing assertion here is the acceptance criterion itself: no
 * secret value — a signed-in session token or a license key — can reach the
 * diagnostics snapshot, only the *kind* of credential in use. Everything
 * else (connectivity line, binary path/version, last error per platform
 * surface) is verified against the shape the command promises to report.
 *
 * @see Issue #749 — consolidate six output channels, add Show Diagnostics
 * @see Issue #742 — the Go daemon holds a session JWT or a license key
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const mockExecFileAsync = vi.hoisted(() => vi.fn());

vi.mock("child_process", () => ({ execFile: vi.fn() }));
vi.mock("util", () => ({ promisify: vi.fn(() => mockExecFileAsync) }));

vi.mock("../../src/platform/TokenStorage", () => ({
  TokenStorage: { getInstance: vi.fn() },
}));

vi.mock("../../src/services/SecretStorageService", () => ({
  SecretStorageService: { getInstance: vi.fn() },
  SECRET_KEYS: { platformLicenseKey: "nightgauge.platform.licenseKey" },
}));

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: vi.fn() },
}));

vi.mock("../../src/services/IpcClientBase", () => ({
  IpcClientBase: { getLastTransportErrors: vi.fn(() => new Map()) },
}));

vi.mock("vscode", () => ({
  commands: { registerCommand: vi.fn((_id: string, handler: unknown) => ({ handler })) },
  window: { showErrorMessage: vi.fn() },
}));

import { TokenStorage } from "../../src/platform/TokenStorage";
import { SecretStorageService } from "../../src/services/SecretStorageService";
import { BinaryResolver } from "../../src/services/BinaryResolver";
import { IpcClientBase } from "../../src/services/IpcClientBase";
import { buildDiagnosticsSnapshot } from "../../src/commands/showDiagnostics";

// A real-shaped GitHub PAT and a real-shaped JWT — exactly what redactSecrets
// is built to catch. If either string ever appears verbatim in the snapshot,
// the test fails.
const FAKE_SESSION_JWT =
  "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U";
const FAKE_LICENSE_KEY = `ghp_${"a".repeat(36)}`;

function mockPlatformStatusBarItem(overrides?: {
  displayState?: string;
  connectionDetails?: string;
}) {
  return {
    getDisplayState: vi.fn(() => overrides?.displayState ?? "connected"),
    getConnectionDetails: vi.fn(
      () => overrides?.connectionDetails ?? "Connected\nURL: https://nightgauge.dev"
    ),
  } as never;
}

describe("buildDiagnosticsSnapshot", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(BinaryResolver.fromVSCode).mockReturnValue({
      resolve: vi.fn().mockResolvedValue("/usr/local/bin/nightgauge"),
    } as never);
    mockExecFileAsync.mockResolvedValue({ stdout: "nightgauge 1.2.3\n", stderr: "" });
    vi.mocked(IpcClientBase.getLastTransportErrors).mockReturnValue(new Map());
  });

  it("never prints the session token — only that a session is in use", async () => {
    vi.mocked(TokenStorage.getInstance).mockReturnValue({
      retrieve: vi.fn().mockResolvedValue(FAKE_SESSION_JWT),
    } as never);
    vi.mocked(SecretStorageService.getInstance).mockReturnValue({
      getSecret: vi.fn().mockResolvedValue(undefined),
    } as never);

    const lines = await buildDiagnosticsSnapshot({
      platformStatusBarItem: mockPlatformStatusBarItem(),
    });
    const joined = lines.join("\n");

    expect(joined).not.toContain(FAKE_SESSION_JWT);
    expect(joined).toContain("Credential in use: session (signed in)");
  });

  it("never prints the license key — only that a license key is in use", async () => {
    vi.mocked(TokenStorage.getInstance).mockReturnValue({
      retrieve: vi.fn().mockResolvedValue(null),
    } as never);
    vi.mocked(SecretStorageService.getInstance).mockReturnValue({
      getSecret: vi.fn().mockResolvedValue(FAKE_LICENSE_KEY),
    } as never);

    const lines = await buildDiagnosticsSnapshot({
      platformStatusBarItem: mockPlatformStatusBarItem(),
    });
    const joined = lines.join("\n");

    expect(joined).not.toContain(FAKE_LICENSE_KEY);
    expect(joined).toContain("Credential in use: license key");
  });

  it("reports 'none configured' when neither credential is present", async () => {
    vi.mocked(TokenStorage.getInstance).mockReturnValue({
      retrieve: vi.fn().mockResolvedValue(null),
    } as never);
    vi.mocked(SecretStorageService.getInstance).mockReturnValue({
      getSecret: vi.fn().mockResolvedValue(undefined),
    } as never);

    const lines = await buildDiagnosticsSnapshot({
      platformStatusBarItem: mockPlatformStatusBarItem(),
    });

    expect(lines.join("\n")).toContain("Credential in use: none configured");
  });

  it("redacts a secret that leaked into a transport error message", async () => {
    vi.mocked(TokenStorage.getInstance).mockReturnValue({
      retrieve: vi.fn().mockResolvedValue(null),
    } as never);
    vi.mocked(SecretStorageService.getInstance).mockReturnValue({
      getSecret: vi.fn().mockResolvedValue(undefined),
    } as never);
    vi.mocked(IpcClientBase.getLastTransportErrors).mockReturnValue(
      new Map([
        [
          "platform.getCostAnalytics",
          {
            status: 401,
            message: `unauthorized: Bearer ${FAKE_LICENSE_KEY}`,
            timestamp: "2026-08-19T00:00:00.000Z",
          },
        ],
      ])
    );

    const lines = await buildDiagnosticsSnapshot({
      platformStatusBarItem: mockPlatformStatusBarItem(),
    });
    const joined = lines.join("\n");

    expect(joined).not.toContain(FAKE_LICENSE_KEY);
    expect(joined).toContain("[REDACTED:GH_TOKEN]");
    expect(joined).toContain("platform.getCostAnalytics");
    expect(joined).toContain("status 401");
  });

  it("reports the Go binary path and version", async () => {
    vi.mocked(TokenStorage.getInstance).mockReturnValue({
      retrieve: vi.fn().mockResolvedValue(null),
    } as never);
    vi.mocked(SecretStorageService.getInstance).mockReturnValue({
      getSecret: vi.fn().mockResolvedValue(undefined),
    } as never);

    const lines = await buildDiagnosticsSnapshot({
      platformStatusBarItem: mockPlatformStatusBarItem(),
    });
    const joined = lines.join("\n");

    expect(joined).toContain("Go binary path: /usr/local/bin/nightgauge");
    expect(joined).toContain("Go binary version: nightgauge 1.2.3");
  });

  it("reports the binary as not found when resolution fails", async () => {
    vi.mocked(BinaryResolver.fromVSCode).mockReturnValue({
      resolve: vi.fn().mockResolvedValue(null),
    } as never);
    vi.mocked(TokenStorage.getInstance).mockReturnValue({
      retrieve: vi.fn().mockResolvedValue(null),
    } as never);
    vi.mocked(SecretStorageService.getInstance).mockReturnValue({
      getSecret: vi.fn().mockResolvedValue(undefined),
    } as never);

    const lines = await buildDiagnosticsSnapshot({
      platformStatusBarItem: mockPlatformStatusBarItem(),
    });
    const joined = lines.join("\n");

    expect(joined).toContain("Go binary path: not found");
    expect(joined).toContain("unknown (binary not found)");
  });

  it("lists every platform surface with 'no error recorded' when nothing failed", async () => {
    vi.mocked(TokenStorage.getInstance).mockReturnValue({
      retrieve: vi.fn().mockResolvedValue(null),
    } as never);
    vi.mocked(SecretStorageService.getInstance).mockReturnValue({
      getSecret: vi.fn().mockResolvedValue(undefined),
    } as never);

    const lines = await buildDiagnosticsSnapshot({
      platformStatusBarItem: mockPlatformStatusBarItem(),
    });
    const joined = lines.join("\n");

    for (const endpoint of [
      "platform.getAnalyticsHealth",
      "platform.getAnalyticsTrends",
      "platform.getCostAnalytics",
      "platform.getAnalyticsRuns",
      "platform.auditListReports",
      "platform.getUsageSummary",
    ]) {
      expect(joined).toContain(endpoint);
    }
    expect(joined).toContain("no error recorded this session");
  });

  it("falls back gracefully when no platform status bar item was wired up", async () => {
    vi.mocked(TokenStorage.getInstance).mockReturnValue({
      retrieve: vi.fn().mockResolvedValue(null),
    } as never);
    vi.mocked(SecretStorageService.getInstance).mockReturnValue({
      getSecret: vi.fn().mockResolvedValue(undefined),
    } as never);

    const lines = await buildDiagnosticsSnapshot({ platformStatusBarItem: null });

    expect(lines.join("\n")).toContain("platform surface not initialized");
  });
});
