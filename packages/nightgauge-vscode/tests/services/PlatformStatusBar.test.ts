/**
 * Tests for PlatformStatusBar
 *
 * Verifies status bar updates for all state transitions and
 * correct handling of the platform.enabled = false case.
 *
 * @see Issue #1461 - Platform connection status indicator
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock vscode before imports
vi.mock("vscode", () => ({
  window: {
    createStatusBarItem: vi.fn(),
  },
  StatusBarAlignment: {
    Left: 1,
    Right: 2,
  },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
}));

import * as vscode from "vscode";
import { PlatformStatusBar } from "../../src/services/PlatformStatusBar";

describe("PlatformStatusBar", () => {
  let mockItem: {
    text: string;
    tooltip: string | vscode.MarkdownString;
    backgroundColor: vscode.ThemeColor | undefined;
    command: string | undefined;
    show: ReturnType<typeof vi.fn>;
    hide: ReturnType<typeof vi.fn>;
    dispose: ReturnType<typeof vi.fn>;
  };

  beforeEach(() => {
    vi.useFakeTimers();

    mockItem = {
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: undefined,
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    };

    vi.mocked(vscode.window.createStatusBarItem).mockReturnValue(
      mockItem as unknown as vscode.StatusBarItem
    );

    // Default: fetch succeeds with status "ok"
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok" }),
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // ---------------------------------------------------------------------------
  // Disabled state
  // ---------------------------------------------------------------------------

  it('shows "Platform: Disabled" when enabled = false', () => {
    const bar = new PlatformStatusBar({ enabled: false });
    bar.start();

    expect(mockItem.text).toBe("$(circle-slash) Platform: Disabled");
    expect(mockItem.show).toHaveBeenCalled();
    expect(global.fetch).not.toHaveBeenCalled();

    bar.dispose();
  });

  it('getState() returns "disabled" when enabled = false', () => {
    const bar = new PlatformStatusBar({ enabled: false });
    bar.start();
    expect(bar.getState()).toBe("disabled");
    bar.dispose();
  });

  // ---------------------------------------------------------------------------
  // Connected state
  // ---------------------------------------------------------------------------

  it('shows "Platform: Connected" when health returns status "ok"', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok" }),
    });

    const bar = new PlatformStatusBar({
      enabled: true,
      apiUrl: "https://api.example.com",
    });
    bar.start();

    // Advance past request timeout (5s default) so AbortController clears,
    // but not past the poll interval (60s) to avoid infinite timer recursion.
    await vi.advanceTimersByTimeAsync(6000);

    expect(mockItem.text).toBe("$(check) Platform: Connected");
    expect(mockItem.backgroundColor).toBeUndefined();
    expect(bar.getState()).toBe("connected");
    expect(bar.getLastSuccessfulPing()).toBeInstanceOf(Date);

    bar.dispose();
  });

  // ---------------------------------------------------------------------------
  // Degraded state
  // ---------------------------------------------------------------------------

  it('shows "Platform: Degraded" when health returns status "degraded"', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: "degraded" }),
    });

    const bar = new PlatformStatusBar({ enabled: true });
    bar.start();
    await vi.advanceTimersByTimeAsync(6000);

    expect(mockItem.text).toBe("$(warning) Platform: Degraded");
    expect((mockItem.backgroundColor as vscode.ThemeColor)?.id).toBe(
      "statusBarItem.warningBackground"
    );
    expect(bar.getState()).toBe("degraded");
    expect(bar.getLastSuccessfulPing()).toBeInstanceOf(Date);

    bar.dispose();
  });

  // ---------------------------------------------------------------------------
  // Offline state
  // ---------------------------------------------------------------------------

  it('shows "Platform: Offline" when fetch throws a network error', async () => {
    global.fetch = vi.fn().mockRejectedValue(new Error("Network error"));

    const bar = new PlatformStatusBar({ enabled: true });
    bar.start();
    await vi.advanceTimersByTimeAsync(6000);

    expect(mockItem.text).toBe("$(error) Platform: Offline");
    expect((mockItem.backgroundColor as vscode.ThemeColor)?.id).toBe(
      "statusBarItem.errorBackground"
    );
    expect(bar.getState()).toBe("offline");
    expect(bar.getLastSuccessfulPing()).toBeNull();

    bar.dispose();
  });

  it('shows "Platform: Offline" when response is not ok', async () => {
    global.fetch = vi.fn().mockResolvedValue({ ok: false });

    const bar = new PlatformStatusBar({ enabled: true });
    bar.start();
    await vi.advanceTimersByTimeAsync(6000);

    expect(mockItem.text).toBe("$(error) Platform: Offline");
    expect(bar.getState()).toBe("offline");

    bar.dispose();
  });

  it('shows "Platform: Offline" when response body has unknown status', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: "unknown" }),
    });

    const bar = new PlatformStatusBar({ enabled: true });
    bar.start();
    await vi.advanceTimersByTimeAsync(6000);

    expect(mockItem.text).toBe("$(error) Platform: Offline");
    expect(bar.getState()).toBe("offline");

    bar.dispose();
  });

  // ---------------------------------------------------------------------------
  // State transitions
  // ---------------------------------------------------------------------------

  it("transitions from offline to connected when health recovers", async () => {
    global.fetch = vi.fn().mockRejectedValueOnce(new Error("Network error"));

    const bar = new PlatformStatusBar({
      enabled: true,
      pollIntervalMs: 10000,
      timeoutMs: 100,
    });
    bar.start();
    await vi.advanceTimersByTimeAsync(200); // past timeoutMs, before pollIntervalMs

    expect(bar.getState()).toBe("offline");

    // Next poll — health recovers
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok" }),
    });

    await vi.advanceTimersByTimeAsync(10000); // fire poll interval
    await vi.advanceTimersByTimeAsync(200); // past timeoutMs

    expect(bar.getState()).toBe("connected");
    expect(mockItem.text).toBe("$(check) Platform: Connected");

    bar.dispose();
  });

  it("transitions from connected to degraded", async () => {
    global.fetch = vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ status: "ok" }),
      })
      .mockResolvedValue({
        ok: true,
        json: async () => ({ status: "degraded" }),
      });

    const bar = new PlatformStatusBar({
      enabled: true,
      pollIntervalMs: 10000,
      timeoutMs: 100,
    });
    bar.start();
    await vi.advanceTimersByTimeAsync(200);
    expect(bar.getState()).toBe("connected");

    await vi.advanceTimersByTimeAsync(10000); // fire poll interval
    await vi.advanceTimersByTimeAsync(200);
    expect(bar.getState()).toBe("degraded");

    bar.dispose();
  });

  // ---------------------------------------------------------------------------
  // updateOptions
  // ---------------------------------------------------------------------------

  it("hides platform (shows disabled) when updateOptions sets enabled = false", async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok" }),
    });

    const bar = new PlatformStatusBar({ enabled: true });
    bar.start();
    await vi.advanceTimersByTimeAsync(6000);
    expect(bar.getState()).toBe("connected");

    // Disable platform
    bar.updateOptions({ enabled: false });
    expect(bar.getState()).toBe("disabled");
    expect(mockItem.text).toBe("$(circle-slash) Platform: Disabled");

    bar.dispose();
  });

  // ---------------------------------------------------------------------------
  // Health URL construction
  // ---------------------------------------------------------------------------

  it("calls the correct /health URL for the configured apiUrl", async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok" }),
    });

    const bar = new PlatformStatusBar({
      enabled: true,
      apiUrl: "https://staging.example.com",
    });
    bar.start();
    await vi.advanceTimersByTimeAsync(6000);

    expect(global.fetch).toHaveBeenCalledWith(
      "https://staging.example.com/health",
      expect.objectContaining({ signal: expect.anything() })
    );

    bar.dispose();
  });

  it("strips trailing slash from apiUrl before appending /health", async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok" }),
    });

    const bar = new PlatformStatusBar({
      enabled: true,
      apiUrl: "https://api.example.com/",
    });
    bar.start();
    await vi.advanceTimersByTimeAsync(6000);

    const [url] = vi.mocked(global.fetch).mock.calls[0];
    expect(url).toBe("https://api.example.com/health");

    bar.dispose();
  });

  // ---------------------------------------------------------------------------
  // dispose
  // ---------------------------------------------------------------------------

  it("disposes the status bar item on dispose()", () => {
    const bar = new PlatformStatusBar({ enabled: false });
    bar.dispose();
    expect(mockItem.dispose).toHaveBeenCalled();
  });
});
