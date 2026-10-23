import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  BUDGET_FIELD_MIN_RATIO,
  COST_ACCURACY_BAND_HIGH,
  COST_ACCURACY_BAND_LOW,
  DEBOUNCE_MS,
  DebouncedPatcher,
  FETCH_RETRY_DELAYS,
  FINAL_PATCH_MAX_RETRIES,
  FINAL_PATCH_RETRY_DELAYS,
  formatBudgetFieldValue,
  formatCostAccuracyValue,
  formatDuration,
  hexColor,
  reconcileRunTotalUsd,
  redactSecrets,
  retryWithBackoff,
  shortModel,
  shouldRenderBudgetField,
  truncate,
} from "../../../src/services/notifications/transport";

function makeLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };
}

// ─── Constants ─────────────────────────────────────────────────────────────

describe("transport constants", () => {
  it("exposes the canonical retry / debounce values", () => {
    expect(FETCH_RETRY_DELAYS).toEqual([200, 800]);
    expect(FINAL_PATCH_RETRY_DELAYS).toEqual([3000, 6000]);
    expect(FINAL_PATCH_MAX_RETRIES).toBe(2);
    expect(DEBOUNCE_MS).toBe(1500);
  });
});

// ─── Formatting helpers ────────────────────────────────────────────────────

describe("formatDuration", () => {
  it("formats sub-minute as seconds", () => {
    expect(formatDuration(450)).toBe("0s");
    expect(formatDuration(1500)).toBe("2s");
    expect(formatDuration(45_000)).toBe("45s");
  });
  it("formats minute+ as 'Xm Ys'", () => {
    expect(formatDuration(60_000)).toBe("1m 0s");
    expect(formatDuration(125_000)).toBe("2m 5s");
  });
});

describe("formatBudgetFieldValue", () => {
  // #333 decision G: the pre-run estimate no longer hides inside the budget
  // field — it is its own "Cost Accuracy" field. The budget field is now the
  // ratio and nothing else.
  it("renders actual/ceiling/pct only", () => {
    expect(formatBudgetFieldValue(28.259, 75.0)).toBe("$28.26 / $75.00 (38%)");
  });

  it("renders 0% when nothing has been spent", () => {
    expect(formatBudgetFieldValue(0, 75.0)).toBe("$0.0000 / $75.00 (0%)");
  });

  it("carries no pre-run estimate segment (#333 decision G)", () => {
    const result = formatBudgetFieldValue(28.259, 75.0);
    expect(result).not.toContain("Pre-run est.");
    expect(result).not.toContain("Est: $");
  });
});

describe("shouldRenderBudgetField (#333 decision F)", () => {
  it("suppresses the field when spend is below half the ceiling — it says nothing", () => {
    expect(shouldRenderBudgetField(1.518, 75.0, "productive")).toBe(false);
    expect(shouldRenderBudgetField(28.259, 75.0, "productive")).toBe(false);
  });

  it("renders the field once spend reaches half the ceiling", () => {
    expect(shouldRenderBudgetField(37.5, 75.0, "productive")).toBe(true);
    expect(shouldRenderBudgetField(74.0, 75.0, "productive")).toBe(true);
    expect(BUDGET_FIELD_MIN_RATIO).toBe(0.5);
  });

  it("always renders the field for budget-related outcomes, whatever the ratio", () => {
    expect(shouldRenderBudgetField(1.0, 75.0, "budget-ceiling")).toBe(true);
    expect(shouldRenderBudgetField(1.0, 75.0, "shipped-but-overbudget")).toBe(true);
  });

  it("never renders the field without a positive ceiling", () => {
    expect(shouldRenderBudgetField(10, 0, "budget-ceiling")).toBe(false);
  });
});

describe("formatCostAccuracyValue (#333 decision G)", () => {
  it("reads 'on estimate' inside the neutral band", () => {
    expect(COST_ACCURACY_BAND_LOW).toBe(0.8);
    expect(COST_ACCURACY_BAND_HIGH).toBe(1.25);
    expect(formatCostAccuracyValue(4.5, 4.458)).toBe(
      "Est. $4.46 → Actual $4.50  ·  ≈ on estimate (1.0x)"
    );
    expect(formatCostAccuracyValue(8.0, 10.0)).toContain("≈ on estimate (0.8x)");
    expect(formatCostAccuracyValue(12.5, 10.0)).toContain("≈ on estimate (1.3x)");
  });

  it("renders the ratio prominently when the run blew past the estimate", () => {
    expect(formatCostAccuracyValue(14.837, 4.458)).toBe(
      "Est. $4.46 → Actual $14.84  ·  **3.3x over**"
    );
  });

  it("renders the ratio prominently when the run came in far under the estimate", () => {
    expect(formatCostAccuracyValue(1.518, 4.458)).toBe(
      "Est. $4.46 → Actual $1.52  ·  **0.3x under**"
    );
  });

  it("omits the ratio when nothing was spent — a 0x ratio is not a signal", () => {
    expect(formatCostAccuracyValue(0, 4.458)).toBe("Est. $4.46 → Actual $0.0000");
  });
});

describe("reconcileRunTotalUsd (#333 decision A / AC1)", () => {
  it("returns the reported total when there is no per-stage data to cross-check", () => {
    expect(reconcileRunTotalUsd(1.518, undefined)).toBe(1.518);
    expect(reconcileRunTotalUsd(1.518, {})).toBe(1.518);
  });

  it("returns the reported total when it is at least as large as every stage", () => {
    const logger = makeLogger();
    const total = reconcileRunTotalUsd(
      14.837,
      {
        "feature-planning": { cost_usd: 1.518 },
        "feature-dev": { cost_usd: 13.319 },
      },
      logger
    );
    expect(total).toBe(14.837);
    expect(logger.warn).not.toHaveBeenCalled();
  });

  it("renders the per-stage SUM and warns when the reported total is below a single stage", () => {
    // The #289 shape: the embed claimed $1.518 total while Feature Dev alone
    // cost $13.319. Never silently assert a total the stages contradict, and
    // never silently correct it either.
    const logger = makeLogger();
    const total = reconcileRunTotalUsd(
      1.518,
      {
        "feature-planning": { cost_usd: 1.518 },
        "feature-dev": { cost_usd: 13.319 },
      },
      logger
    );
    expect(total).toBeCloseTo(14.837, 6);
    expect(logger.warn).toHaveBeenCalledTimes(1);
    const [message, meta] = logger.warn.mock.calls[0] as [string, Record<string, number>];
    expect(message).toContain("run total");
    expect(meta.reportedTotalUsd).toBe(1.518);
    expect(meta.maxStageCostUsd).toBe(13.319);
    expect(meta.perStageSumUsd).toBeCloseTo(14.837, 6);
  });

  it("tolerates stages with no recorded cost", () => {
    expect(
      reconcileRunTotalUsd(2.0, {
        "issue-pickup": {},
        "feature-planning": { cost_usd: 1.0 },
      })
    ).toBe(2.0);
  });
});

describe("truncate", () => {
  it("returns input unchanged when within limit", () => {
    expect(truncate("hello", 10)).toBe("hello");
    expect(truncate("hello", 5)).toBe("hello");
  });
  it("appends ellipsis when truncating", () => {
    expect(truncate("hello world", 6)).toBe("hello…");
    expect(truncate("hello world", 6).length).toBe(6);
  });
});

describe("shortModel", () => {
  it("strips the claude- prefix", () => {
    expect(shortModel("claude-sonnet-4-6")).toBe("sonnet-4-6");
    expect(shortModel("claude-opus-4-7")).toBe("opus-4-7");
  });
  it("passes through models without the prefix", () => {
    expect(shortModel("gpt-5")).toBe("gpt-5");
  });
});

describe("hexColor", () => {
  it("converts a 24-bit RGB int to a CSS hex string", () => {
    expect(hexColor(0x57f287)).toBe("#57f287");
    expect(hexColor(0xed4245)).toBe("#ed4245");
  });
  it("zero-pads small values to 6 chars", () => {
    expect(hexColor(0x0000ff)).toBe("#0000ff");
    expect(hexColor(1)).toBe("#000001");
  });
  it("clamps out-of-range integers", () => {
    expect(hexColor(-1)).toBe("#000000");
    expect(hexColor(0x1000000)).toBe("#ffffff");
  });
});

// ─── Secret redaction ──────────────────────────────────────────────────────

describe("redactSecrets", () => {
  it("redacts a JWT", () => {
    const jwt =
      "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c";
    const result = redactSecrets(`token: ${jwt}`);
    expect(result).not.toContain(jwt);
    expect(result).toContain("[REDACTED:JWT]");
  });

  it("redacts a GitHub PAT", () => {
    const result = redactSecrets("GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz1234");
    expect(result).not.toContain("ghp_abcdefghijklmnopqrstuvwxyz1234");
    expect(result).toContain("[REDACTED");
  });

  it("redacts a bare GitLab PAT (glpat-)", () => {
    // Bare token, not in a KEY=value assignment — exercises the dedicated
    // glpat- pattern added in #170 (previously uncovered). Built by
    // concatenation so the fixture itself is not a contiguous glpat- literal
    // that would trip the credential scanner; it is a real glpat- shape at
    // runtime.
    const gitlabPat = "glpat-" + "N3FwABCDEFGHIJKLMNOP";
    const result = redactSecrets(`push failed authing with ${gitlabPat} token`);
    expect(result).not.toContain(gitlabPat);
    expect(result).toContain("[REDACTED:GITLAB_TOKEN]");
  });

  it("redacts SECRET=value assignments", () => {
    const result = redactSecrets("LICENSE_HMAC_SECRET=supersecretvaluedonotleak");
    expect(result).toContain("LICENSE_HMAC_SECRET=[REDACTED]");
    expect(result).not.toContain("supersecretvaluedonotleak");
  });

  it("redacts PEM blocks across newlines", () => {
    const raw = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADAN\nABC123\n-----END PRIVATE KEY-----";
    expect(redactSecrets(raw)).toBe("[REDACTED:PEM_BLOCK]");
  });

  it("returns empty input unchanged", () => {
    expect(redactSecrets("")).toBe("");
  });
});

// ─── retryWithBackoff ──────────────────────────────────────────────────────

describe("retryWithBackoff", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("returns the response on first success without retrying", async () => {
    const logger = makeLogger();
    const fetchFn = vi.fn().mockResolvedValue({ ok: true } as Response);

    const promise = retryWithBackoff(fetchFn, {
      delays: [200, 800],
      logger: logger as never,
      label: "TestService",
    });
    const res = await promise;

    expect(res.ok).toBe(true);
    expect(fetchFn).toHaveBeenCalledTimes(1);
    expect(logger.info).not.toHaveBeenCalled();
  });

  it("retries on non-ok response and succeeds on subsequent attempt", async () => {
    const logger = makeLogger();
    const fetchFn = vi
      .fn()
      .mockResolvedValueOnce({ ok: false, status: 429 } as Response)
      .mockResolvedValueOnce({ ok: true } as Response);

    const promise = retryWithBackoff(fetchFn, {
      delays: [200],
      logger: logger as never,
      label: "TestService",
    });

    await vi.advanceTimersByTimeAsync(200);
    const res = await promise;

    expect(res.ok).toBe(true);
    expect(fetchFn).toHaveBeenCalledTimes(2);
    expect(logger.info).toHaveBeenCalledWith(
      "TestService: fetch failed, retrying",
      expect.objectContaining({ attempt: 1, delayMs: 200 })
    );
  });

  it("retries on thrown errors", async () => {
    const logger = makeLogger();
    const fetchFn = vi
      .fn()
      .mockRejectedValueOnce(new Error("network down"))
      .mockResolvedValueOnce({ ok: true } as Response);

    const promise = retryWithBackoff(fetchFn, {
      delays: [200],
      logger: logger as never,
      label: "TestService",
    });

    await vi.advanceTimersByTimeAsync(200);
    const res = await promise;

    expect(res.ok).toBe(true);
    expect(fetchFn).toHaveBeenCalledTimes(2);
    expect(logger.info).toHaveBeenCalledWith(
      "TestService: fetch error, retrying",
      expect.objectContaining({ attempt: 1, delayMs: 200 })
    );
  });

  it("throws the last error after exhausting retries", async () => {
    const logger = makeLogger();
    const fetchFn = vi.fn().mockResolvedValue({ ok: false, status: 500 } as Response);

    const promise = retryWithBackoff(fetchFn, {
      delays: [200, 800],
      logger: logger as never,
      label: "TestService",
    });

    // Suppress the unhandled rejection warning while we drive the timers.
    const caught = promise.catch((e) => e);
    await vi.advanceTimersByTimeAsync(200 + 800);
    const err = await caught;

    expect(err).toBeInstanceOf(Error);
    expect((err as Error).message).toBe("HTTP 500");
    // 3 attempts total: initial + 2 retries
    expect(fetchFn).toHaveBeenCalledTimes(3);
  });
});

// ─── DebouncedPatcher ──────────────────────────────────────────────────────

describe("DebouncedPatcher", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("schedules a single fn after the requested delay", async () => {
    const patcher = new DebouncedPatcher();
    const fn = vi.fn();
    patcher.schedule(42, fn, 1500);

    expect(patcher.has(42)).toBe(true);
    expect(fn).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1500);
    expect(fn).toHaveBeenCalledTimes(1);
    expect(patcher.has(42)).toBe(false);
  });

  it("coalesces back-to-back schedules into one fire (debounce)", async () => {
    const patcher = new DebouncedPatcher();
    const fn = vi.fn();
    patcher.schedule(42, fn, 1500);
    await vi.advanceTimersByTimeAsync(500);
    patcher.schedule(42, fn, 1500); // resets the timer
    await vi.advanceTimersByTimeAsync(500);
    expect(fn).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1000);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("a later schedule replaces a pending retry timer", async () => {
    const patcher = new DebouncedPatcher();
    const retryFn = vi.fn();
    const debounceFn = vi.fn();
    // Schedule retry at 3s
    patcher.schedule(42, retryFn, 3000);
    // 1s later, a fresh debounced update arrives — should cancel the retry
    await vi.advanceTimersByTimeAsync(1000);
    patcher.schedule(42, debounceFn, 1500);

    await vi.advanceTimersByTimeAsync(1500);
    expect(debounceFn).toHaveBeenCalledTimes(1);
    expect(retryFn).not.toHaveBeenCalled();

    // Even after the original 3s window passes, the retry never fires.
    await vi.advanceTimersByTimeAsync(2000);
    expect(retryFn).not.toHaveBeenCalled();
  });

  it("cancel() prevents the fn from firing", async () => {
    const patcher = new DebouncedPatcher();
    const fn = vi.fn();
    patcher.schedule(42, fn, 1500);
    patcher.cancel(42);
    await vi.advanceTimersByTimeAsync(2000);
    expect(fn).not.toHaveBeenCalled();
    expect(patcher.has(42)).toBe(false);
  });

  it("cancel() is a no-op when no timer is scheduled", () => {
    const patcher = new DebouncedPatcher();
    expect(() => patcher.cancel(42)).not.toThrow();
  });

  it("dispose() cancels every pending timer", async () => {
    const patcher = new DebouncedPatcher();
    const fn1 = vi.fn();
    const fn2 = vi.fn();
    patcher.schedule(1, fn1, 1500);
    patcher.schedule(2, fn2, 1500);

    patcher.dispose();
    await vi.advanceTimersByTimeAsync(2000);

    expect(fn1).not.toHaveBeenCalled();
    expect(fn2).not.toHaveBeenCalled();
    expect(patcher.has(1)).toBe(false);
    expect(patcher.has(2)).toBe(false);
  });

  it("dispose() is idempotent", () => {
    const patcher = new DebouncedPatcher();
    patcher.dispose();
    expect(() => patcher.dispose()).not.toThrow();
  });
});
