/**
 * Unit tests for the adapter auth pre-flight cache / single-flight / retry
 * (Issue #312).
 *
 * Covers the four contracts from the issue:
 *   - single-flight: N concurrent probes → exactly ONE runner invocation
 *   - TTL: a fresh probe re-runs only after the cached result expires
 *   - timeout-retry: a first-probe timeout retries once; a retry success passes,
 *     and a double timeout fails with the timeout-specific (not logged-out) reason
 *   - definitive negatives are NOT retried and ARE cached
 */

import { describe, it, expect, beforeEach } from "vitest";
import {
  probeAdapterAuthCached,
  resetAuthPreflightCache,
} from "../../../src/cli/adapters/authPreflightCache.js";
import { runAdapterAuthPreflight } from "../../../src/cli/adapters/runAdapterAuthPreflight.js";
import { AdapterError } from "../../../src/cli/adapters/errors.js";
import type { IncrediAdapter } from "../../../src/cli/adapters/ICliAdapter.js";

const ADAPTER: IncrediAdapter = "claude-headless";

const tick = (ms = 0): Promise<void> => new Promise((r) => setTimeout(r, ms));

/**
 * Build a fake registry whose single adapter runs `validateAuth`. Injected into
 * validateAdapterAuth via `opts.registry`, this decouples the cache tests from
 * the real CLI adapters and any subprocess.
 */
function fakeRegistry(validateAuth: () => Promise<"passed">) {
  const instance = { validateAuth: () => validateAuth() };
  return {
    get: () => instance,
    has: () => true,
  } as never;
}

beforeEach(() => {
  resetAuthPreflightCache();
});

describe("probeAdapterAuthCached — single-flight (#312)", () => {
  it("collapses a concurrent burst to exactly one probe", async () => {
    let calls = 0;
    const registry = fakeRegistry(async () => {
      calls++;
      await tick(20); // stay pending so all four callers overlap
      return "passed";
    });

    const results = await Promise.all(
      [0, 1, 2, 3].map(() => probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" }))
    );

    expect(calls).toBe(1);
    expect(results).toHaveLength(4);
    for (const r of results) {
      expect(r.ok).toBe(true);
    }
  });

  it("re-probes once the in-flight probe settles (no permanent pinning)", async () => {
    let calls = 0;
    const registry = fakeRegistry(async () => {
      calls++;
      return "passed";
    });

    // ttlMs:0 disables the TTL cache, isolating single-flight from TTL reuse.
    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" }, { ttlMs: 0 });
    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" }, { ttlMs: 0 });

    expect(calls).toBe(2);
  });
});

describe("probeAdapterAuthCached — TTL cache (#312)", () => {
  it("serves the cached result inside the TTL and re-probes after it expires", async () => {
    let calls = 0;
    const registry = fakeRegistry(async () => {
      calls++;
      return "passed";
    });
    const clock = { t: 1_000 };
    const cacheOpts = { ttlMs: 60_000, now: () => clock.t };

    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" }, cacheOpts);
    expect(calls).toBe(1);

    // Within the TTL → cache hit, no new probe.
    clock.t += 30_000;
    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" }, cacheOpts);
    expect(calls).toBe(1);

    // Past the TTL → fresh probe.
    clock.t += 40_000;
    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" }, cacheOpts);
    expect(calls).toBe(2);
  });

  it("keys the cache by cwd (config root)", async () => {
    let calls = 0;
    const registry = fakeRegistry(async () => {
      calls++;
      return "passed";
    });

    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo-a" });
    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo-b" });

    expect(calls).toBe(2);
  });

  it("bypassCache forces a fresh probe but refreshes the shared cache", async () => {
    let calls = 0;
    const registry = fakeRegistry(async () => {
      calls++;
      return "passed";
    });

    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" }); // populate
    expect(calls).toBe(1);

    // Doctor-style bypass: skips the read side, so it probes again…
    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" }, { bypassCache: true });
    expect(calls).toBe(2);

    // …and the fresh result refreshed the cache, so a normal caller hits it.
    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" });
    expect(calls).toBe(2);
  });
});

describe("probeAdapterAuthCached — timeout retry (#312)", () => {
  it("retries once on a first-probe timeout and passes when the retry succeeds", async () => {
    let calls = 0;
    const registry = fakeRegistry(async () => {
      calls++;
      if (calls === 1) {
        await tick(60); // first attempt hangs past the initial timeout
      }
      return "passed";
    });

    const result = await probeAdapterAuthCached(
      ADAPTER,
      { registry, cwd: "/repo" },
      { initialTimeoutMs: 20, retryTimeoutMs: 200, retryDelayMs: 0, ttlMs: 0 }
    );

    expect(result.ok).toBe(true);
    expect(calls).toBe(2); // one timed-out attempt + one successful retry
  });

  it("fails with the timeout-specific reason when both attempts time out", async () => {
    const registry = fakeRegistry(async () => {
      await tick(80); // always hangs past both timeouts
      return "passed";
    });

    const result = await probeAdapterAuthCached(
      ADAPTER,
      { registry, cwd: "/repo" },
      { initialTimeoutMs: 20, retryTimeoutMs: 30, retryDelayMs: 0, ttlMs: 0 }
    );

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.category).toBe("TIMEOUT");
      // Distinguishes "probe timed out" from "not authenticated".
      expect(result.reason).toMatch(/timed out/i);
      expect(result.reason).toMatch(/retry/i);
      expect(result.reason).not.toMatch(/logged out session/); // it explicitly says NOT logged out
    }
  });

  it("does NOT retry a definitive negative and caches it", async () => {
    let calls = 0;
    const registry = fakeRegistry(async () => {
      calls++;
      throw new AdapterError(
        "CLI is not authenticated. Run `claude auth login`.",
        "AUTH_MISSING",
        ADAPTER
      );
    });

    const first = await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" });
    expect(first.ok).toBe(false);
    expect(calls).toBe(1); // no retry for a definitive negative

    // Cached: a second caller inside the TTL reuses it without re-probing.
    const second = await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" });
    expect(second.ok).toBe(false);
    expect(calls).toBe(1);
  });

  it("never caches a timeout result (transient → always re-probed)", async () => {
    let calls = 0;
    const registry = fakeRegistry(async () => {
      calls++;
      await tick(80);
      return "passed";
    });
    const opts = { initialTimeoutMs: 15, retryTimeoutMs: 20, retryDelayMs: 0, ttlMs: 60_000 };

    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" }, opts);
    await probeAdapterAuthCached(ADAPTER, { registry, cwd: "/repo" }, opts);

    // 2 probes each retried once → 4 attempts; a cached timeout would have
    // stopped the second probe at 2.
    expect(calls).toBe(4);
  });
});

describe("runAdapterAuthPreflight — burst + timeout surfacing (#312)", () => {
  it("a 4-slot burst costs exactly one probe and all callers get the verdict", async () => {
    let calls = 0;
    const registry = fakeRegistry(async () => {
      calls++;
      await tick(20);
      return "passed";
    });

    const results = await Promise.all(
      [0, 1, 2, 3].map(() => runAdapterAuthPreflight([ADAPTER], { registry, cwd: "/repo" }))
    );

    expect(calls).toBe(1);
    for (const r of results) {
      expect(r.ok).toBe(true);
    }
  });

  it("flags a double-timeout failure as timedOut so callers route it to retryable infra", async () => {
    const registry = fakeRegistry(async () => {
      await tick(80);
      return "passed";
    });

    const result = await runAdapterAuthPreflight([ADAPTER], {
      registry,
      cwd: "/repo",
      initialTimeoutMs: 15,
      retryTimeoutMs: 20,
      retryDelayMs: 0,
      ttlMs: 0,
    });

    expect(result.ok).toBe(false);
    expect(result.failures).toHaveLength(1);
    expect(result.failures[0].timedOut).toBe(true);
    expect(result.failures[0].reason).toMatch(/timed out/i);
  });
});
