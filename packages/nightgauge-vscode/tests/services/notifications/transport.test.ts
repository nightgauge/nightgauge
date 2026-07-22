import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  DEBOUNCE_MS,
  DebouncedPatcher,
  FETCH_RETRY_DELAYS,
  FINAL_PATCH_MAX_RETRIES,
  FINAL_PATCH_RETRY_DELAYS,
  formatBudgetFieldValue,
  formatCost,
  formatDuration,
  hexColor,
  redactSecrets,
  retryWithBackoff,
  shortModel,
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

describe("formatCost", () => {
  it("renders three-decimal USD", () => {
    expect(formatCost(0)).toBe("$0.000");
    expect(formatCost(0.123)).toBe("$0.123");
    expect(formatCost(1.5)).toBe("$1.500");
  });
});

describe("formatBudgetFieldValue", () => {
  it("renders actual/ceiling/pct with no estimate segment when none was recorded", () => {
    expect(formatBudgetFieldValue(28.259, 75.0)).toBe("$28.259 / $75.000 (38%)");
  });

  it("renders actual/ceiling/pct with no estimate segment when estimate is 0", () => {
    expect(formatBudgetFieldValue(28.259, 75.0, 0)).toBe("$28.259 / $75.000 (38%)");
  });

  it("labels the pre-flight estimate as 'Pre-run est.' (not bare 'Est:') and shows its accuracy vs actual (#267)", () => {
    // Regression for #267: Discord/Mattermost completion embeds used to show
    // a bare "Est: $2.703" right next to the actual cost, reading as a second
    // (wrong) actual figure. It must be unambiguously labeled as a pre-run
    // prediction and show how far off it was.
    const result = formatBudgetFieldValue(28.259, 75.0, 2.703);
    expect(result).toBe("$28.259 / $75.000 (38%)  ·  Pre-run est. $2.703 (actual: 10.5x)");
    expect(result).not.toContain("Est: $");
  });

  it("shows an under-1x ratio when the actual cost came in below the estimate", () => {
    const result = formatBudgetFieldValue(3.0, 75.0, 10.0);
    expect(result).toBe("$3.000 / $75.000 (4%)  ·  Pre-run est. $10.000 (actual: 0.3x)");
  });

  it("omits the accuracy ratio (but keeps the pre-run label) when actual cost is 0", () => {
    expect(formatBudgetFieldValue(0, 75.0, 2.703)).toBe(
      "$0.000 / $75.000 (0%)  ·  Pre-run est. $2.703"
    );
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
