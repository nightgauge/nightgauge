import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  outcomeDisplay,
  determineAction,
  formatErrorForDiscord,
  modeDisplay,
  redactSecrets,
} from "../../src/services/DiscordService";

// Discord embed color constants (mirrored from DiscordService for assertion clarity)
const COLOR_RUNNING = 0x5865f2;
const COLOR_COMPLETE = 0x57f287;
const COLOR_WARNING = 0xfee75c;
const COLOR_NEUTRAL = 0x95a5a6;
const COLOR_FAILED = 0xed4245;

describe("outcomeDisplay", () => {
  describe('success outcomes → green + "Complete ✓"', () => {
    it("productive", () => {
      const result = outcomeDisplay("productive");
      expect(result.color).toBe(COLOR_COMPLETE);
      expect(result.label).toBe("Complete ✓");
    });

    it("verify-and-close", () => {
      const result = outcomeDisplay("verify-and-close");
      expect(result.color).toBe(COLOR_COMPLETE);
      expect(result.label).toBe("Complete ✓");
    });
  });

  describe("informational outcomes → green + distinct label", () => {
    it("already-resolved", () => {
      const result = outcomeDisplay("already-resolved");
      expect(result.color).toBe(COLOR_COMPLETE);
      expect(result.label).toBe("Already Resolved");
    });
  });

  describe("warning outcomes → yellow", () => {
    it("budget-ceiling", () => {
      const result = outcomeDisplay("budget-ceiling");
      expect(result.color).toBe(COLOR_WARNING);
      expect(result.label).toBe("Budget Ceiling");
    });
  });

  describe("neutral outcomes → grey", () => {
    it("cancelled", () => {
      const result = outcomeDisplay("cancelled");
      expect(result.color).toBe(COLOR_NEUTRAL);
      expect(result.label).toBe("Cancelled");
    });
  });

  describe('running state → blurple + "Running…"', () => {
    it("undefined (no outcome yet)", () => {
      const result = outcomeDisplay(undefined);
      expect(result.color).toBe(COLOR_RUNNING);
      expect(result.label).toBe("Running…");
    });
  });

  describe("unknown future outcome types → red fallback", () => {
    it("unrecognised string falls back to Failed ✗", () => {
      const result = outcomeDisplay("some-future-outcome-type");
      expect(result.color).toBe(COLOR_FAILED);
      expect(result.label).toBe("Failed ✗");
    });

    it("empty string falls back to Failed ✗", () => {
      const result = outcomeDisplay("");
      expect(result.color).toBe(COLOR_FAILED);
      expect(result.label).toBe("Failed ✗");
    });
  });
});

// ─── Integration tests for retry + flush behavior ─────────────────────────────
//
// These tests construct a real DiscordService with mocked dependencies and
// exercise the retry-on-failure and flush-stale-runs codepaths via the public
// event handlers (onStageStart, onStateChanged).

// Capture event listeners so tests can fire them directly
let stageStartHandler: ((e: { stage: string; issueNumber: number }) => void) | null = null;
let stageErrorHandler: ((e: { issueNumber: number }) => void) | null = null;
let stateChangedHandler: ((state: unknown) => void) | null = null;

// Mock vscode (must be before DiscordService import)
vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn(),
    })),
  },
}));

// Mock SecretStorageService to avoid singleton issues
vi.mock("../../src/services/SecretStorageService", () => ({
  SecretStorageService: {
    getInstance: () => null,
  },
  SECRET_KEYS: {
    discordWebhookUrl: "discordWebhookUrl",
  },
}));

const { DiscordService } = await import("../../src/services/DiscordService");

// ─── Factory helpers ─────────────────────────────────────────────────────────

function makePipelineStateService() {
  return {
    onStageStart: vi.fn((cb: (e: { stage: string; issueNumber: number }) => void) => {
      stageStartHandler = cb;
      return { dispose: vi.fn() };
    }),
    onStageError: vi.fn((cb: (e: { issueNumber: number }) => void) => {
      stageErrorHandler = cb;
      return { dispose: vi.fn() };
    }),
    onStateChanged: vi.fn((cb: (state: unknown) => void) => {
      stateChangedHandler = cb;
      return { dispose: vi.fn() };
    }),
    getState: vi.fn().mockResolvedValue(null),
    getStatePath: vi.fn(() => "/repos/my-repo/.nightgauge/pipeline/state.json"),
  };
}

function makeConfigBridge(enabled = true) {
  return {
    getEffectiveConfig: vi.fn(() => ({
      config: {
        notifications: {
          discord: {
            enabled,
            webhook_env: "DISCORD_WEBHOOK_URL",
          },
        },
      },
    })),
  };
}

function makeLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };
}

const WEBHOOK_URL = "https://discord.com/api/webhooks/123456/abc-token";

function makeState(issueNumber: number, outcomeType?: string) {
  return {
    issue_number: issueNumber,
    title: `Test issue #${issueNumber}`,
    branch: `fix/issue-${issueNumber}`,
    stages: { "issue-pickup": { status: "complete" } },
    tokens: { estimated_cost_usd: 0.05 },
    outcome_type: outcomeType,
  };
}

describe("DiscordService retry and flush", () => {
  let service: InstanceType<typeof DiscordService>;
  let pss: ReturnType<typeof makePipelineStateService>;
  let logger: ReturnType<typeof makeLogger>;
  let fetchMock: ReturnType<typeof vi.fn>;
  const originalEnv = process.env.DISCORD_WEBHOOK_URL;

  beforeEach(() => {
    vi.useFakeTimers();
    stageStartHandler = null;
    stageErrorHandler = null;
    stateChangedHandler = null;

    pss = makePipelineStateService();
    const configBridge = makeConfigBridge();
    logger = makeLogger();

    service = new DiscordService(pss as any, configBridge as any, logger as any);

    // Mock global fetch
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    // Set webhook env var
    process.env.DISCORD_WEBHOOK_URL = WEBHOOK_URL;
  });

  afterEach(() => {
    service.dispose();
    vi.useRealTimers();
    vi.unstubAllGlobals();
    if (originalEnv !== undefined) {
      process.env.DISCORD_WEBHOOK_URL = originalEnv;
    } else {
      delete process.env.DISCORD_WEBHOOK_URL;
    }
  });

  // Helper: simulate issue-pickup which creates an embed
  async function simulateIssuePickup(issueNumber: number): Promise<void> {
    pss.getState.mockResolvedValue(makeState(issueNumber));

    // POST to create embed returns success
    fetchMock.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ id: `msg-${issueNumber}` }),
    });

    await service.initialize();
    stageStartHandler!({ stage: "issue-pickup", issueNumber });

    // Wait for the async handleStageStart to complete
    await vi.advanceTimersByTimeAsync(0);
  }

  // Helper: mark run as final via state change
  async function markFinal(issueNumber: number, outcomeType = "productive"): Promise<void> {
    stateChangedHandler!(makeState(issueNumber, outcomeType));
    // Let the state change handler run
    await vi.advanceTimersByTimeAsync(0);
  }

  describe("immediate final PATCH (no debounce)", () => {
    it("sends final PATCH immediately when outcome_type is set", async () => {
      await simulateIssuePickup(42);

      // Mock must be set BEFORE markFinal — PATCH fires immediately
      fetchMock.mockResolvedValueOnce({ ok: true });
      await markFinal(42);

      // POST (issue-pickup) + immediate final PATCH — no 1.5s wait needed
      expect(fetchMock).toHaveBeenCalledTimes(2);
    });
  });

  describe("retry on HTTP failure for final PATCH", () => {
    it("retries a 429 rate-limited final PATCH after 3s backoff", async () => {
      await simulateIssuePickup(42);

      // First PATCH fires immediately on markFinal — returns 429
      fetchMock.mockResolvedValueOnce({ ok: false, status: 429 });
      await markFinal(42);

      expect(fetchMock).toHaveBeenCalledTimes(2); // POST + immediate PATCH
      expect(logger.warn).toHaveBeenCalledWith(
        "DiscordService: failed to patch embed",
        expect.objectContaining({ status: 429 })
      );
      expect(logger.info).toHaveBeenCalledWith(
        "DiscordService: scheduling final patch retry",
        expect.objectContaining({ attempt: 1, delayMs: 3000 })
      );

      // Retry at 3s — returns 200 OK
      fetchMock.mockResolvedValueOnce({ ok: true });
      await vi.advanceTimersByTimeAsync(3000);

      expect(fetchMock).toHaveBeenCalledTimes(3); // POST + PATCH + retry
    });

    it("retries on network error for final PATCH", async () => {
      await simulateIssuePickup(42);

      // First PATCH fires immediately — throws network error
      fetchMock.mockRejectedValueOnce(new Error("Network timeout"));
      await markFinal(42);

      expect(logger.warn).toHaveBeenCalledWith(
        "DiscordService: network error patching embed",
        expect.objectContaining({ err: expect.any(Error) })
      );
      expect(logger.info).toHaveBeenCalledWith(
        "DiscordService: scheduling final patch retry",
        expect.objectContaining({ attempt: 1, delayMs: 3000 })
      );

      // Retry succeeds
      fetchMock.mockResolvedValueOnce({ ok: true });
      await vi.advanceTimersByTimeAsync(3000);

      expect(fetchMock).toHaveBeenCalledTimes(3);
    });

    it("gives up after max retries and logs error", async () => {
      await simulateIssuePickup(42);

      // Attempt 1 (immediate) — fails
      fetchMock.mockResolvedValueOnce({ ok: false, status: 500 });
      await markFinal(42);

      // Attempt 2 (retry 1 at 3s) — fails
      fetchMock.mockResolvedValueOnce({ ok: false, status: 500 });
      await vi.advanceTimersByTimeAsync(3000);

      expect(logger.info).toHaveBeenCalledWith(
        "DiscordService: scheduling final patch retry",
        expect.objectContaining({ attempt: 2, delayMs: 6000 })
      );

      // Attempt 3 (retry 2 at 6s) — fails
      fetchMock.mockResolvedValueOnce({ ok: false, status: 500 });
      await vi.advanceTimersByTimeAsync(6000);

      // Should have given up
      expect(logger.error).toHaveBeenCalledWith(
        "DiscordService: final patch failed after all retries — embed may be stuck",
        expect.objectContaining({ issueNumber: 42, retries: 2 })
      );
    });

    it("does NOT retry non-final PATCH failures", async () => {
      await simulateIssuePickup(42);

      // Trigger a non-final update (no outcome_type set)
      stageStartHandler!({ stage: "feature-planning", issueNumber: 42 });

      // PATCH fails with 500 (still uses 1.5s debounce for non-final)
      fetchMock.mockResolvedValueOnce({ ok: false, status: 500 });
      await vi.advanceTimersByTimeAsync(1500);

      // Should just warn, NOT schedule retry
      expect(logger.warn).toHaveBeenCalledWith(
        "DiscordService: failed to patch embed",
        expect.objectContaining({ status: 500 })
      );

      // Advance well past retry delays — no additional PATCHes should fire
      fetchMock.mockClear();
      await vi.advanceTimersByTimeAsync(10000);
      expect(fetchMock).not.toHaveBeenCalled();
    });
  });

  describe("flushStaleRuns on new issue-pickup", () => {
    it("flushes stale run when immediate final PATCH failed", async () => {
      await simulateIssuePickup(42);

      // Immediate final PATCH fails — run stays with retry scheduled
      fetchMock.mockResolvedValueOnce({ ok: false, status: 429 });
      await markFinal(42);

      // New issue starts — flushStaleRuns fires immediate PATCH for #42
      fetchMock.mockResolvedValueOnce({ ok: true }); // flush PATCH for #42
      fetchMock.mockResolvedValueOnce({
        ok: true,
        json: async () => ({ id: "msg-99" }),
      }); // POST for #99

      pss.getState.mockResolvedValue(makeState(99));
      stageStartHandler!({ stage: "issue-pickup", issueNumber: 99 });
      await vi.advanceTimersByTimeAsync(0);

      // The flush PATCH for #42 should have been called
      const patchCalls = fetchMock.mock.calls.filter(
        (call: unknown[]) => typeof call[0] === "string" && (call[0] as string).includes("msg-42")
      );
      expect(patchCalls.length).toBeGreaterThanOrEqual(1);
    });

    it("cancels retry timer when flushing stale run", async () => {
      await simulateIssuePickup(42);

      // Immediate final PATCH fails — retry timer scheduled at 3s
      fetchMock.mockResolvedValueOnce({ ok: false, status: 429 });
      await markFinal(42);

      // New issue starts — flushStaleRuns cancels retry timer, patches immediately
      fetchMock.mockResolvedValueOnce({ ok: true }); // flush PATCH for #42
      fetchMock.mockResolvedValueOnce({
        ok: true,
        json: async () => ({ id: "msg-99" }),
      });

      pss.getState.mockResolvedValue(makeState(99));
      stageStartHandler!({ stage: "issue-pickup", issueNumber: 99 });
      await vi.advanceTimersByTimeAsync(0);

      // Advance past the original 3s retry delay — should NOT fire again
      fetchMock.mockClear();
      await vi.advanceTimersByTimeAsync(5000);

      const latePatchCalls = fetchMock.mock.calls.filter(
        (call: unknown[]) => typeof call[0] === "string" && (call[0] as string).includes("msg-42")
      );
      expect(latePatchCalls.length).toBe(0);
    });
  });

  describe("embed enrichment: mode ceiling + usage-limit fallback", () => {
    it("renders Frontier + Fable ceiling and a dedicated Usage-Limit Fallback field", async () => {
      await simulateIssuePickup(42);

      // The immediate final PATCH carries the enriched embed.
      fetchMock.mockResolvedValueOnce({ ok: true });
      const finalState = {
        ...makeState(42, "productive"),
        pipeline_meta: {
          performance_mode: "frontier",
          quota_fallbacks: [{ stage: "feature-dev", from: "fable", to: "opus" }],
        },
      };

      stateChangedHandler!(finalState as any);
      await vi.advanceTimersByTimeAsync(0);

      const patchCall = fetchMock.mock.calls.find(
        (c: unknown[]) => typeof c[0] === "string" && (c[0] as string).includes("/messages/msg-42")
      );
      expect(patchCall).toBeDefined();
      const body = JSON.parse((patchCall![1] as { body: string }).body);
      const embed = body.embeds[0];

      // Title carries the Frontier badge (was blank because frontier → "Elevated")
      expect(embed.title).toContain("🚀");

      // Mode field shows Frontier + the envelope ceiling

      const modeField = embed.fields.find((f: any) => f.name === "⚙️ Mode");
      expect(modeField).toBeDefined();
      expect(modeField.value).toContain("🚀 Frontier");
      expect(modeField.value).toContain("up to Fable");

      // Dedicated usage-limit fallback field surfaces the Fable → Opus downgrade

      const fallbackField = embed.fields.find((f: any) => f.name === "⚠️ Usage-Limit Fallback");
      expect(fallbackField).toBeDefined();
      expect(fallbackField.value).toContain("Feature Dev: fable → opus");
      expect(fallbackField.value).toContain("separate Max-plan bucket");
    });
  });

  describe("embed enrichment: Budget field pre-flight estimate labeling (#267)", () => {
    it("labels the pre-flight estimate 'Pre-run est.' with an accuracy ratio, not a bare 'Est:'", async () => {
      await simulateIssuePickup(42);

      fetchMock.mockResolvedValueOnce({ ok: true });
      const finalState = {
        ...makeState(42, "productive"),
        tokens: { estimated_cost_usd: 28.259 },
        pipeline_meta: {
          budget_ceiling_usd: 75.0,
          budget_estimate_usd: 2.703,
        },
      };

      stateChangedHandler!(finalState as any);
      await vi.advanceTimersByTimeAsync(0);

      const patchCall = fetchMock.mock.calls.find(
        (c: unknown[]) => typeof c[0] === "string" && (c[0] as string).includes("/messages/msg-42")
      );
      expect(patchCall).toBeDefined();
      const body = JSON.parse((patchCall![1] as { body: string }).body);
      const embed = body.embeds[0];

      const budgetField = embed.fields.find((f: any) => f.name === "💰 Budget");
      expect(budgetField).toBeDefined();
      expect(budgetField.value).toBe(
        "$28.259 / $75.000 (38%)  ·  Pre-run est. $2.703 (actual: 10.5x)"
      );
      expect(budgetField.value).not.toContain("Est: $2.703");
    });
  });
});

// ─── determineAction tests ──────────────────────────────────────────────────

describe("modeDisplay", () => {
  it("frontier → Frontier 🚀 with a Fable ceiling (was mislabelled 'Elevated')", () => {
    // Regression: modeDisplay had no `frontier` case, so the one Fable-capable
    // mode fell through to the Elevated default. Fable runs showed "Elevated".
    const result = modeDisplay({ performance_mode: "frontier" });
    expect(result.label).toBe("Frontier");
    expect(result.icon).toBe("🚀");
    expect(result.ceiling).toBe("Fable");
    expect(result.modelSuffix).toBe("");
  });

  it("efficiency → Efficiency 💡, Sonnet ceiling", () => {
    const result = modeDisplay({ performance_mode: "efficiency" });
    expect(result.label).toBe("Efficiency");
    expect(result.icon).toBe("💡");
    expect(result.ceiling).toBe("Sonnet");
  });

  it("elevated → Elevated (no icon), Opus ceiling", () => {
    const result = modeDisplay({ performance_mode: "elevated" });
    expect(result.label).toBe("Elevated");
    expect(result.icon).toBe("");
    expect(result.ceiling).toBe("Opus");
  });

  it("maximum → Maximum ⚡ with the pinned model in the suffix", () => {
    const result = modeDisplay({ performance_mode: "maximum", supercharge_model: "opus" });
    expect(result.label).toBe("Maximum");
    expect(result.icon).toBe("⚡");
    expect(result.modelSuffix).toBe(" (opus)");
    expect(result.ceiling).toBe("Opus");
  });

  it("no mode + legacy is_supercharge → Maximum (pre-#3009 fallback)", () => {
    const result = modeDisplay({ is_supercharge: true });
    expect(result.label).toBe("Maximum");
    expect(result.icon).toBe("⚡");
  });

  it("undefined meta → Elevated default", () => {
    const result = modeDisplay(undefined);
    expect(result.label).toBe("Elevated");
    expect(result.ceiling).toBe("Opus");
  });
});

describe("determineAction", () => {
  it("returns null for success outcomes", () => {
    expect(
      determineAction({
        issue_number: 1,
        title: "",
        branch: "",
        outcome_type: "productive",
      })
    ).toBeNull();
    expect(
      determineAction({
        issue_number: 1,
        title: "",
        branch: "",
        outcome_type: "verify-and-close",
      })
    ).toBeNull();
    expect(
      determineAction({
        issue_number: 1,
        title: "",
        branch: "",
        outcome_type: "already-resolved",
      })
    ).toBeNull();
  });

  it("returns null when still running", () => {
    expect(determineAction({ issue_number: 1, title: "", branch: "" })).toBeNull();
  });

  it("recommends re-run for cancelled", () => {
    const result = determineAction({
      issue_number: 1,
      title: "",
      branch: "",
      outcome_type: "cancelled",
    });
    expect(result).toContain("Re-run");
    expect(result).toContain("preserved");
  });

  it("recommends budget increase for budget-ceiling", () => {
    const result = determineAction({
      issue_number: 1,
      title: "",
      branch: "",
      outcome_type: "budget-ceiling",
    });
    expect(result).toContain("budget");
  });

  it("recommends manual fix for build errors", () => {
    const result = determineAction({
      issue_number: 1,
      title: "",
      branch: "",
      outcome_type: "failure",
      stages: {
        "feature-dev": { status: "failed", error: "build failed: exit code 1" },
      },
    });
    expect(result).toContain("Manual fix");
  });

  it("recommends re-run for test failures without retries", () => {
    const result = determineAction({
      issue_number: 1,
      title: "",
      branch: "",
      outcome_type: "failure",
      stages: {
        "feature-validate": { status: "failed", error: "test suite failed" },
      },
      retry_count: 0,
    });
    expect(result).toContain("Re-run");
  });

  it("recommends investigation for test failures after retries", () => {
    const result = determineAction({
      issue_number: 1,
      title: "",
      branch: "",
      outcome_type: "failure",
      stages: {
        "feature-validate": { status: "failed", error: "test suite failed" },
      },
      retry_count: 2,
    });
    expect(result).toContain("manual investigation");
  });

  it("recommends re-run for rate limit errors", () => {
    const result = determineAction({
      issue_number: 1,
      title: "",
      branch: "",
      outcome_type: "failure",
      stages: {
        "feature-dev": { status: "failed", error: "rate limit exceeded" },
      },
    });
    expect(result).toContain("Transient");
    expect(result).toContain("re-run");
  });

  it("recommends manual intervention after max retries", () => {
    const result = determineAction({
      issue_number: 1,
      title: "",
      branch: "",
      outcome_type: "failure",
      stages: { "feature-dev": { status: "failed", error: "unknown error" } },
      retry_count: 3,
    });
    expect(result).toContain("Max retries");
  });
});

// ─── formatErrorForDiscord — stream-JSON envelope extraction ──────────────────

describe("formatErrorForDiscord", () => {
  it("passes through plain text unchanged", () => {
    const result = formatErrorForDiscord("build failed: exit code 1");
    expect(result).toBe("build failed: exit code 1");
  });

  it("trims surrounding whitespace on plain text", () => {
    const result = formatErrorForDiscord("   some error text   \n");
    expect(result).toBe("some error text");
  });

  it("returns empty string for null / undefined / empty input", () => {
    expect(formatErrorForDiscord(undefined)).toBe("");
    expect(formatErrorForDiscord(null)).toBe("");
    expect(formatErrorForDiscord("")).toBe("");
    expect(formatErrorForDiscord("   ")).toBe("");
  });

  it("extracts string content from a user → tool_result envelope", () => {
    const envelope = JSON.stringify({
      type: "user",
      message: {
        role: "user",
        content: [
          {
            tool_use_id: "toolu_01NECCjcUa7L983Mc7kM632A",
            type: "tool_result",
            content:
              " error • The method 'close' isn't defined for the class 'StreamSubscription'.",
          },
        ],
      },
    });
    const result = formatErrorForDiscord(envelope);
    expect(result).toContain("The method 'close' isn't defined");
    expect(result).not.toContain("tool_use_id");
    expect(result).not.toContain("tool_result");
  });

  it("extracts array-of-text-blocks content from a tool_result envelope", () => {
    const envelope = JSON.stringify({
      type: "user",
      message: {
        content: [
          {
            type: "tool_result",
            content: [
              { type: "text", text: "First error line" },
              { type: "text", text: "Second error line" },
            ],
          },
        ],
      },
    });
    const result = formatErrorForDiscord(envelope);
    expect(result).toBe("First error line\nSecond error line");
  });

  it("summarises a system task_notification stopped envelope", () => {
    const envelope = JSON.stringify({
      type: "system",
      subtype: "task_notification",
      task_id: "b4v5yqkka",
      tool_use_id: "toolu_01WFG14BVhze81vrdbJj3eSj",
      status: "stopped",
      summary: "subprocess received SIGKILL after 180 seconds",
      exit_code: 137,
    });
    const result = formatErrorForDiscord(envelope);
    expect(result).toContain("Stopped");
    expect(result).toContain("SIGKILL");
    expect(result).toContain("137");
    expect(result).not.toContain("task_id");
  });

  it("summarises a task_notification without exit code", () => {
    const envelope = JSON.stringify({
      type: "system",
      subtype: "task_notification",
      status: "stopped",
    });
    const result = formatErrorForDiscord(envelope);
    expect(result).toContain("Stopped");
  });

  it("extracts text blocks from an assistant envelope", () => {
    const envelope = JSON.stringify({
      type: "assistant",
      message: {
        model: "claude-sonnet-4-6",
        id: "msg_01AFVMbEB8kYGZXb7fV5p18D",
        type: "message",
        role: "assistant",
        content: [
          {
            type: "text",
            text: '<!-- phase:start name="ptc-detection" -->\nDetecting PTC events...',
          },
        ],
      },
    });
    const result = formatErrorForDiscord(envelope);
    expect(result).toContain("phase:start");
    expect(result).toContain("Detecting PTC events");
    expect(result).not.toContain("msg_01AFVMbEB8kYGZXb7fV5p18D");
    expect(result).not.toContain("claude-sonnet-4-6");
  });

  it("summarises tool_use blocks in assistant envelopes", () => {
    const envelope = JSON.stringify({
      type: "assistant",
      message: {
        content: [
          { type: "text", text: "Running command" },
          { type: "tool_use", name: "Bash", id: "toolu_xyz", input: { command: "ls" } },
        ],
      },
    });
    const result = formatErrorForDiscord(envelope);
    expect(result).toContain("Running command");
    expect(result).toContain("Used tool: Bash");
    expect(result).not.toContain("toolu_xyz");
  });

  it("extracts from the last tool_result when multiple are present in one envelope", () => {
    const envelope = JSON.stringify({
      type: "user",
      message: {
        content: [
          { type: "tool_result", content: "first result" },
          { type: "tool_result", content: "final error" },
        ],
      },
    });
    const result = formatErrorForDiscord(envelope);
    expect(result).toBe("final error");
  });

  it("handles mixed JSONL — concatenates extractable envelopes", () => {
    const line1 = JSON.stringify({
      type: "assistant",
      message: { content: [{ type: "text", text: "Starting work" }] },
    });
    const line2 = JSON.stringify({
      type: "user",
      message: {
        content: [{ type: "tool_result", content: "compile failed" }],
      },
    });
    const line3 = JSON.stringify({
      type: "system",
      subtype: "task_notification",
      status: "stopped",
      exit_code: 137,
    });
    const result = formatErrorForDiscord(`${line1}\n${line2}\n${line3}`);
    expect(result).toContain("Starting work");
    expect(result).toContain("compile failed");
    expect(result).toContain("Stopped");
    expect(result).toContain("137");
  });

  it("ignores JSONL lines that fail to parse", () => {
    const good = JSON.stringify({
      type: "user",
      message: { content: [{ type: "tool_result", content: "real error" }] },
    });
    const result = formatErrorForDiscord(`${good}\n{malformed json\n${good}`);
    expect(result).toContain("real error");
  });

  it("falls back to truncated JSON when nothing extractable", () => {
    const envelope = JSON.stringify({
      type: "some_unknown_envelope",
      data: "a".repeat(800),
    });
    const result = formatErrorForDiscord(envelope);
    // Should be truncated (less than ~510 chars: 500 + ellipsis) and not empty
    expect(result.length).toBeLessThanOrEqual(510);
    expect(result.length).toBeGreaterThan(0);
    expect(result).toContain("some_unknown_envelope");
  });

  it("clamps extracted output at MAX_ERROR_EXTRACT_LENGTH (1500)", () => {
    const longText = "x".repeat(3000);
    const envelope = JSON.stringify({
      type: "user",
      message: { content: [{ type: "tool_result", content: longText }] },
    });
    const result = formatErrorForDiscord(envelope);
    expect(result.length).toBeLessThanOrEqual(1500);
    expect(result.endsWith("…")).toBe(true);
  });

  it("does not break when message.content is missing", () => {
    const envelope = JSON.stringify({ type: "user", message: {} });
    const result = formatErrorForDiscord(envelope);
    // No extractable content — falls back to truncated JSON, not empty
    expect(result.length).toBeGreaterThan(0);
  });

  it("does not break when envelope is malformed JSON", () => {
    const result = formatErrorForDiscord("{not-valid-json");
    // Non-JSON-parseable string starting with { still treated as JSON branch
    // → falls back to truncated raw input
    expect(result.length).toBeGreaterThan(0);
    expect(result.length).toBeLessThanOrEqual(510);
  });

  it("redacts PEM private key blocks from plain-text errors", () => {
    const raw =
      "compose error: JWT_PRIVATE_KEY: -----BEGIN PRIVATE KEY-----\nMIIEvQIBADAN\nABC123\n-----END PRIVATE KEY----- plus tail";
    const result = formatErrorForDiscord(raw);
    expect(result).not.toContain("BEGIN PRIVATE KEY");
    expect(result).not.toContain("MIIEvQIBADAN");
    expect(result).toContain("[REDACTED:PEM_BLOCK]");
  });

  it("redacts PEM blocks with literal-\\n encoding (compose-style)", () => {
    const raw =
      "JWT_PRIVATE_KEY: '-----BEGIN PRIVATE KEY-----\\nMIIEvQIB\\nblahblah\\n-----END PRIVATE KEY-----'";
    const result = formatErrorForDiscord(raw);
    expect(result).not.toContain("MIIEvQIB");
    expect(result).toContain("[REDACTED:PEM_BLOCK]");
  });

  it("redacts secrets inside extracted tool_result content", () => {
    const envelope = JSON.stringify({
      type: "user",
      message: {
        content: [
          {
            type: "tool_result",
            content: "build failed: GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz1234 leaked in env",
          },
        ],
      },
    });
    const result = formatErrorForDiscord(envelope);
    expect(result).not.toContain("ghp_abcdefghijklmnopqrstuvwxyz1234");
    expect(result).toContain("[REDACTED");
  });
});

describe("redactSecrets", () => {
  it("redacts a JWT", () => {
    const jwt =
      "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c";
    const result = redactSecrets(`token: ${jwt}`);
    expect(result).not.toContain(jwt);
    expect(result).toContain("[REDACTED:JWT]");
  });

  it("redacts an OpenAI key", () => {
    const result = redactSecrets("OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyz");
    expect(result).not.toContain("sk-abcdefghijklmnop");
    expect(result).toContain("[REDACTED");
  });

  it("redacts a Stripe live key", () => {
    const result = redactSecrets("key=sk_live_abcdef1234567890ABCDEF");
    expect(result).not.toContain("sk_live_abcdef");
  });

  it("redacts AWS access key id", () => {
    const result = redactSecrets("aws=AKIAIOSFODNN7EXAMPLE end");
    expect(result).toContain("[REDACTED:AWS_ACCESS_KEY]");
    expect(result).not.toContain("AKIAIOSFODNN7EXAMPLE");
  });

  it("redacts SECRET=value assignments while keeping the key name", () => {
    const result = redactSecrets("LICENSE_HMAC_SECRET=supersecretvaluedonotleak");
    expect(result).toContain("LICENSE_HMAC_SECRET=[REDACTED]");
    expect(result).not.toContain("supersecretvaluedonotleak");
  });

  it("passes through harmless text unchanged", () => {
    const result = redactSecrets("build failed at step 3 of 7");
    expect(result).toBe("build failed at step 3 of 7");
  });

  it("handles empty input", () => {
    expect(redactSecrets("")).toBe("");
  });
});
