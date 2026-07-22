/**
 * LicensePreflight unit tests.
 *
 * Tests all acceptance criteria scenarios:
 * 1. Valid license → allowed, tier from response
 * 2. Invalid license → blocked
 * 3. Offline / IPC error → community tier (allow)
 * 4. Timeout (5s) → community tier (allow)
 * 5. No license key configured → community tier (allow)
 * 6. Caching — second call within TTL returns cached result
 * 7. clearCache() — after clearing, next call re-validates
 *
 * @see Issue #1470 - Integrate license validation call at pipeline preflight
 * @see Issue #2090 - Migrate to IPC
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock vscode (required because MachineFingerprint imports vscode)
vi.mock("vscode", () => {
  class MockEventEmitter<T> {
    private listeners: Array<(e: T) => void> = [];
    event = (listener: (e: T) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          const idx = this.listeners.indexOf(listener);
          if (idx >= 0) this.listeners.splice(idx, 1);
        },
      };
    };
    fire = (data: T) => {
      for (const listener of this.listeners) {
        listener(data);
      }
    };
    dispose = vi.fn();
  }
  return {
    EventEmitter: MockEventEmitter,
    Disposable: { from: vi.fn() },
    env: { machineId: "test-machine-id" },
  };
});

import { LicensePreflight, type LicensePreflightResult } from "../../src/platform/LicensePreflight";
import type { IpcClient } from "../../src/services/IpcClient";
import type { MachineFingerprint } from "../../src/platform/MachineFingerprint";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function createMockMachineFingerprint(): MachineFingerprint {
  return { getMachineId: vi.fn().mockReturnValue("test-machine-id") } as any;
}

function createMockIpcClient(): {
  client: IpcClient;
  platformValidateLicense: ReturnType<typeof vi.fn>;
} {
  const platformValidateLicense = vi.fn();
  const client = {
    platformValidateLicense,
  } as unknown as IpcClient;
  return { client, platformValidateLicense };
}

const validLicenseResponse = {
  valid: true,
  tier: "pro",
};

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("LicensePreflight", () => {
  let machineFingerprint: MachineFingerprint;
  let mockClient: ReturnType<typeof createMockIpcClient>;

  beforeEach(() => {
    vi.clearAllMocks();
    machineFingerprint = createMockMachineFingerprint();
    mockClient = createMockIpcClient();
  });

  // 1. Valid license
  it("returns allowed=true with tier from response for valid license", async () => {
    mockClient.platformValidateLicense.mockResolvedValue(validLicenseResponse);

    const preflight = new LicensePreflight(
      mockClient.client,
      machineFingerprint,
      () => "ib_live_abc123"
    );

    const result = await preflight.validate();

    expect(result.allowed).toBe(true);
    expect(result.tier).toBe("pro");
    expect(result.reason).toBeUndefined();
    expect(mockClient.platformValidateLicense).toHaveBeenCalledOnce();
  });

  // 2. Invalid license (valid=false from Go IPC)
  it("returns allowed=false when license is invalid", async () => {
    mockClient.platformValidateLicense.mockResolvedValue({
      valid: false,
      tier: "community",
    });

    const preflight = new LicensePreflight(
      mockClient.client,
      machineFingerprint,
      () => "ib_live_abc123"
    );

    const result = await preflight.validate();

    expect(result.allowed).toBe(false);
    expect(result.tier).toBe("community");
    expect(result.reason).toBeDefined();
    expect(result.actionUrl).toContain("renew");
  });

  // 3. Offline / IPC error → community tier
  it("degrades to community tier on IPC error", async () => {
    mockClient.platformValidateLicense.mockRejectedValue(new Error("IPC call failed"));

    const preflight = new LicensePreflight(
      mockClient.client,
      machineFingerprint,
      () => "ib_live_abc123"
    );

    const result = await preflight.validate();

    expect(result.allowed).toBe(true);
    expect(result.tier).toBe("community");
    expect(result.offline).toBe(true);
  });

  // 4. Timeout → community tier
  it("degrades to community tier on timeout", async () => {
    // Create a promise that never resolves (will be timed out)
    mockClient.platformValidateLicense.mockReturnValue(
      new Promise(() => {}) // Never resolves
    );

    const preflight = new LicensePreflight(
      mockClient.client,
      machineFingerprint,
      () => "ib_live_abc123"
    );

    const result = await preflight.validate();

    expect(result.allowed).toBe(true);
    expect(result.tier).toBe("community");
    expect(result.offline).toBe(true);
  }, 10_000); // Allow extra time for the 5s timeout

  // 5. No license key configured → community tier
  it("returns community tier when no license key is configured", async () => {
    const preflight = new LicensePreflight(mockClient.client, machineFingerprint, () => undefined);

    const result = await preflight.validate();

    expect(result.allowed).toBe(true);
    expect(result.tier).toBe("community");
    // Should not call validateLicense when no key
    expect(mockClient.platformValidateLicense).not.toHaveBeenCalled();
  });

  // 6. Caching — second call returns cached result without re-calling IPC
  it("returns cached result on second call within TTL", async () => {
    mockClient.platformValidateLicense.mockResolvedValue(validLicenseResponse);

    const preflight = new LicensePreflight(
      mockClient.client,
      machineFingerprint,
      () => "ib_live_abc123"
    );

    const first = await preflight.validate();
    const second = await preflight.validate();

    expect(first).toEqual(second);
    expect(mockClient.platformValidateLicense).toHaveBeenCalledOnce();
  });

  // 7. clearCache() — after clearing, next call re-validates
  it("re-validates after clearCache()", async () => {
    mockClient.platformValidateLicense.mockResolvedValue(validLicenseResponse);

    const preflight = new LicensePreflight(
      mockClient.client,
      machineFingerprint,
      () => "ib_live_abc123"
    );

    await preflight.validate();
    expect(mockClient.platformValidateLicense).toHaveBeenCalledOnce();

    preflight.clearCache();
    await preflight.validate();
    expect(mockClient.platformValidateLicense).toHaveBeenCalledTimes(2);
  });

  // Additional: tier override bypasses IPC call
  it("returns overridden tier when tier_override is set", async () => {
    const preflight = new LicensePreflight(
      mockClient.client,
      machineFingerprint,
      () => "ib_live_abc123",
      () => "enterprise"
    );

    const result = await preflight.validate();

    expect(result.allowed).toBe(true);
    expect(result.tier).toBe("enterprise");
    expect(mockClient.platformValidateLicense).not.toHaveBeenCalled();
  });

  // =========================================================================
  // cacheUntil field tests (#1476)
  // =========================================================================

  describe("cacheUntil field", () => {
    it("returns cacheUntil within CACHE_TTL_MS from now for valid license", async () => {
      mockClient.platformValidateLicense.mockResolvedValue(validLicenseResponse);

      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();

      expect(result.cacheUntil).toBeDefined();
      const expectedMax = Date.now() + 5 * 60 * 1000 + 1000; // 1s tolerance
      expect(new Date(result.cacheUntil).getTime()).toBeLessThanOrEqual(expectedMax);
    });

    it("cache hit — returns cached result within cacheUntil window", async () => {
      mockClient.platformValidateLicense.mockResolvedValue(validLicenseResponse);

      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const first = await preflight.validate();
      const second = await preflight.validate();

      expect(second.cacheUntil).toBe(first.cacheUntil);
      expect(mockClient.platformValidateLicense).toHaveBeenCalledOnce();
    });

    it("cache miss — re-validates after clearCache()", async () => {
      mockClient.platformValidateLicense.mockResolvedValue(validLicenseResponse);

      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      await preflight.validate();
      expect(mockClient.platformValidateLicense).toHaveBeenCalledOnce();

      preflight.clearCache();
      const second = await preflight.validate();

      expect(mockClient.platformValidateLicense).toHaveBeenCalledTimes(2);
      expect(second.cacheUntil).toBeDefined();
    });

    it("community result includes cacheUntil when no license key configured", async () => {
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => undefined
      );

      const result = await preflight.validate();

      expect(result.tier).toBe("community");
      expect(result.cacheUntil).toBeDefined();
    });

    it("error path includes cacheUntil for IPC errors", async () => {
      mockClient.platformValidateLicense.mockRejectedValue(new Error("IPC error"));

      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();

      expect(result.allowed).toBe(true);
      expect(result.tier).toBe("community");
      expect(result.cacheUntil).toBeDefined();
      const cacheUntilMs = new Date(result.cacheUntil).getTime();
      expect(cacheUntilMs).toBeGreaterThan(Date.now());
    });
  });

  // Additional: validates IPC call includes machine info
  it("sends licenseKey, machineId, hostname, and platform to IPC", async () => {
    mockClient.platformValidateLicense.mockResolvedValue(validLicenseResponse);

    const preflight = new LicensePreflight(
      mockClient.client,
      machineFingerprint,
      () => "ib_live_abc123"
    );

    await preflight.validate();

    expect(mockClient.platformValidateLicense).toHaveBeenCalledWith(
      "ib_live_abc123",
      "test-machine-id",
      expect.any(String),
      expect.any(String)
    );
  });

  // =========================================================================
  // Status propagation (#4156)
  // =========================================================================

  describe("status propagation", () => {
    it("reports status=active for a valid pro license", async () => {
      mockClient.platformValidateLicense.mockResolvedValue({
        valid: true,
        tier: "pro",
        status: "active",
      });
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();
      expect(result.status).toBe("active");
      expect(result.allowed).toBe(true);
    });

    it("reports status=revoked and a support actionUrl for a revoked license", async () => {
      mockClient.platformValidateLicense.mockResolvedValue({
        valid: false,
        tier: "",
        status: "revoked",
      });
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();
      expect(result.allowed).toBe(false);
      expect(result.status).toBe("revoked");
      expect(result.actionUrl).toContain("github.com/nightgauge/nightgauge/issues");
      expect(result.reason).toContain("revoked");
    });

    it("reports status=suspended and a support actionUrl for a suspended license", async () => {
      mockClient.platformValidateLicense.mockResolvedValue({
        valid: false,
        tier: "",
        status: "suspended",
      });
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();
      expect(result.allowed).toBe(false);
      expect(result.status).toBe("suspended");
      expect(result.actionUrl).toContain("github.com/nightgauge/nightgauge/issues");
    });

    it("reports status=expired and a renew actionUrl for an expired license", async () => {
      mockClient.platformValidateLicense.mockResolvedValue({
        valid: false,
        tier: "",
        status: "expired",
      });
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();
      expect(result.allowed).toBe(false);
      expect(result.status).toBe("expired");
      expect(result.actionUrl).toContain("renew");
    });

    it("falls back to expired-style messaging when status is missing on an invalid response", async () => {
      mockClient.platformValidateLicense.mockResolvedValue({
        valid: false,
        tier: "",
      });
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();
      expect(result.status).toBe("expired");
      expect(result.actionUrl).toContain("renew");
    });

    it("propagates machineBound and machineCount from the response", async () => {
      mockClient.platformValidateLicense.mockResolvedValue({
        valid: true,
        tier: "pro",
        status: "active",
        machineBound: true,
        machineCount: 3,
      });
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();
      expect(result.machineBound).toBe(true);
      expect(result.machineCount).toBe(3);
    });

    it("defaults machineBound/machineCount to false/0 when absent", async () => {
      mockClient.platformValidateLicense.mockResolvedValue(validLicenseResponse);
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();
      expect(result.machineBound).toBe(false);
      expect(result.machineCount).toBe(0);
    });

    it("propagates expiresAt from the response instead of always null", async () => {
      const expiresAt = "2027-01-01T00:00:00Z";
      mockClient.platformValidateLicense.mockResolvedValue({
        valid: true,
        tier: "pro",
        status: "active",
        expiresAt,
      });
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();
      expect(result.expiresAt).toBe(expiresAt);
    });

    it("reports status=community for community tier (no license key)", async () => {
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => undefined
      );
      const result = await preflight.validate();
      expect(result.status).toBe("community");
    });

    it("reports status=community (not a confirmed status) when degrading on IPC error", async () => {
      mockClient.platformValidateLicense.mockRejectedValue(new Error("IPC error"));
      const preflight = new LicensePreflight(
        mockClient.client,
        machineFingerprint,
        () => "ib_live_abc123"
      );

      const result = await preflight.validate();
      expect(result.status).toBe("community");
      expect(result.offline).toBe(true);
    });
  });
});
