import { describe, it, expect, afterEach } from "vitest";
import { CopilotCliAdapter } from "../../src/cli/adapters/CopilotCliAdapter.js";
import type { PreflightCommandRunner } from "../../src/cli/codexPreflight.js";
import { AdapterError } from "../../src/cli/adapters/errors.js";
import {
  summarizeCopilotOutput,
  COPILOT_PREMIUM_REQUEST_COST_USD,
} from "../../src/cli/adapterQuery.js";
import { TokenTracker } from "../../src/tracking/TokenTracker.js";

function createRunner(
  responses: Record<string, { code: number; stdout?: string; stderr?: string }>
): PreflightCommandRunner {
  return async (command, args) => {
    const key = `${command} ${args.join(" ")}`;
    const response = responses[key];
    if (!response) {
      return { code: 1, stdout: "", stderr: `unexpected command: ${key}` };
    }
    return {
      code: response.code,
      stdout: response.stdout ?? "",
      stderr: response.stderr ?? "",
    };
  };
}

/** Version response for a valid Copilot CLI installation. */
const COPILOT_VERSION_OK = {
  "copilot --version": { code: 0, stdout: "copilot 1.0.0\n" },
};

/** CLI auth success response. */
const COPILOT_AUTH_OK = {
  "copilot auth status": { code: 0, stdout: "Logged in as user\n" },
};

/** CLI auth failure response. */
const COPILOT_AUTH_FAIL = {
  "copilot auth status": {
    code: 1,
    stderr: "not authenticated",
  },
};

/** Env vars to clean up after each test. */
const AUTH_ENV_VARS = ["GH_TOKEN", "GITHUB_TOKEN", "COPILOT_GITHUB_TOKEN"];
const CLI_ENV_VARS = ["NIGHTGAUGE_COPILOT_CLI_COMMAND", "NIGHTGAUGE_COPILOT_CLI_ARGS"];

describe("CopilotCliAdapter", () => {
  const adapter = new CopilotCliAdapter();

  afterEach(() => {
    for (const key of [...AUTH_ENV_VARS, ...CLI_ENV_VARS]) {
      delete process.env[key];
    }
  });

  // --- Identity and capabilities ---

  it("should have correct identity fields", () => {
    expect(adapter.name).toBe("copilot");
    expect(adapter.displayName).toBe("GitHub Copilot");
    expect(adapter.cliCommand).toBe("copilot");
  });

  it("declares the sdk-fanout orchestration capability", () => {
    expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
  });

  it("should return --allow-all-tools default args", () => {
    expect(adapter.getDefaultArgs()).toEqual(["--allow-all-tools"]);
  });

  it("should not require a direct API key", () => {
    expect(adapter.requiresDirectApiKey()).toBe(false);
  });

  // --- Auth: no runner (SDK direct usage) ---

  it("should pass auth without a runner (SDK direct usage)", async () => {
    const result = await adapter.validateAuth({});
    expect(result).toBe("passed");
  });

  // --- Auth: CLI not installed ---

  it("should fail when copilot CLI is not installed", async () => {
    const runner = createRunner({
      "copilot --version": { code: 1, stderr: "command not found" },
    });

    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(
      /not installed or not in PATH/
    );
  });

  // --- Auth cascade: GH_TOKEN ---

  it("should pass auth with GH_TOKEN set", async () => {
    process.env.GH_TOKEN = "ghp_test-token-123";
    const runner = createRunner({ ...COPILOT_VERSION_OK });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  // --- Auth cascade: GITHUB_TOKEN ---

  it("should pass auth with GITHUB_TOKEN set", async () => {
    process.env.GITHUB_TOKEN = "ghp_test-token-456";
    const runner = createRunner({ ...COPILOT_VERSION_OK });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  // --- Auth cascade: COPILOT_GITHUB_TOKEN ---

  it("should pass auth with COPILOT_GITHUB_TOKEN set", async () => {
    process.env.COPILOT_GITHUB_TOKEN = "ghp_copilot-token-789";
    const runner = createRunner({ ...COPILOT_VERSION_OK });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  // --- Auth cascade: CLI auth success ---

  it("should pass auth via CLI auth status when no env vars set", async () => {
    const runner = createRunner({
      ...COPILOT_VERSION_OK,
      ...COPILOT_AUTH_OK,
    });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  // --- Auth cascade: all methods fail ---

  it("should throw when no auth method is configured", async () => {
    const runner = createRunner({
      ...COPILOT_VERSION_OK,
      ...COPILOT_AUTH_FAIL,
    });

    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(
      /not authenticated/
    );
  });

  // --- Auth: skips CLI check when env var set ---

  it("should skip CLI auth check when GH_TOKEN is set", async () => {
    process.env.GH_TOKEN = "ghp_test-token";
    const calls: string[] = [];
    const runner: PreflightCommandRunner = async (command, args) => {
      const key = `${command} ${args.join(" ")}`;
      calls.push(key);
      if (key === "copilot --version") {
        return { code: 0, stdout: "copilot 1.0.0\n", stderr: "" };
      }
      return { code: 1, stdout: "", stderr: "unexpected" };
    };

    await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(calls).not.toContain("copilot auth status");
  });

  // --- createQueryFunction ---

  it("should return an async generator query function", async () => {
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });

  // --- Env var override for CLI command ---

  it("should use NIGHTGAUGE_COPILOT_CLI_COMMAND when set", async () => {
    process.env.NIGHTGAUGE_COPILOT_CLI_COMMAND = "gh";
    const queryFn = await adapter.createQueryFunction();
    // The function should be created without error — verifies env var override path
    expect(typeof queryFn).toBe("function");
  });
});

// ---------------------------------------------------------------------------
// Stats-footer parsing and real cost accounting (@see Issue #52)
//
// The GitHub Copilot CLI emits the agent response as plain text followed by a
// human-readable stats footer. These fixtures mirror the documented footer
// format (docs.github.com Copilot CLI reference; captured community samples).
// ---------------------------------------------------------------------------

/** A realistic Copilot CLI run: agent response body + default stats footer. */
function copilotRun(body: string, premiumRequests: number): string {
  return `${body}

Session ID: 221b5571-3998-47e1-b57a-552cf9078947
Started: 11/24/2025, 11:18:54 AM
Last Modified: 11/24/2025, 11:19:44 AM
Duration: 50s
Working Directory: /Users/dev/project
Usage: Total usage est: ${premiumRequests} Premium requests
Total duration (API): 12s
Total duration (wall): 50s
Total code changes: 12 lines added, 4 lines removed`;
}

describe("summarizeCopilotOutput", () => {
  it("parses the real premium-request count from the stats footer", () => {
    const summary = summarizeCopilotOutput(copilotRun("Implemented the feature.", 3));

    expect(summary.usage).toBeDefined();
    expect(summary.usage!.premium_requests).toBe(3);
    // Copilot reports no token counts — they stay 0 (honest, not fabricated).
    expect(summary.usage!.input_tokens).toBe(0);
    expect(summary.usage!.output_tokens).toBe(0);
  });

  it("derives cost from the ACTUAL premium-request count, not a flat guess", () => {
    const summary = summarizeCopilotOutput(copilotRun("Done.", 3));
    expect(summary.estimatedCostUsd).toBeCloseTo(3 * COPILOT_PREMIUM_REQUEST_COST_USD, 10);
  });

  it("records 0 premium requests (and $0) for a no-op session — not a fabricated 1", () => {
    const summary = summarizeCopilotOutput(copilotRun("Nothing to do.", 0));
    expect(summary.usage).toBeDefined();
    expect(summary.usage!.premium_requests).toBe(0);
    expect(summary.estimatedCostUsd).toBe(0);
  });

  it("extracts the session id from the footer", () => {
    const summary = summarizeCopilotOutput(copilotRun("Body.", 1));
    expect(summary.sessionId).toBe("221b5571-3998-47e1-b57a-552cf9078947");
  });

  it("strips the stats footer from displayText, leaving only the agent response", () => {
    const summary = summarizeCopilotOutput(copilotRun("Implemented the feature and ran tests.", 2));
    expect(summary.displayText).toBe("Implemented the feature and ran tests.");
    expect(summary.displayText).not.toContain("Premium requests");
    expect(summary.displayText).not.toContain("Session ID");
  });

  it("attributes the requested model as the served model when the footer omits one", () => {
    const summary = summarizeCopilotOutput(copilotRun("Body.", 1), "claude-sonnet-4.5");
    expect(summary.usage!.model).toBe("claude-sonnet-4.5");
  });

  it("prefers an explicit footer Model line over the requested model", () => {
    const output = `Body.

Session ID: abc-123
Model: gpt-4o
Usage: Total usage est: 1 Premium requests`;
    const summary = summarizeCopilotOutput(output, "claude-sonnet-4.5");
    expect(summary.usage!.model).toBe("gpt-4o");
  });

  it("leaves usage undefined and cost 0 when no footer usage line is present", () => {
    // e.g. `-s` (silent) output or an early exit — mirror Codex's unobserved
    // convention: do NOT fabricate a premium-request count.
    const summary = summarizeCopilotOutput("Just some plain text response with no footer.");
    expect(summary.usage).toBeUndefined();
    expect(summary.estimatedCostUsd).toBe(0);
    expect(summary.hasExplicitFailure).toBe(false);
  });

  it("parses a bare, singular premium-request line", () => {
    const summary = summarizeCopilotOutput("Body.\n1 premium request");
    expect(summary.usage!.premium_requests).toBe(1);
  });

  it("detects an explicit failure signal in the output", () => {
    const summary = summarizeCopilotOutput("execution halted: unable to complete task");
    expect(summary.hasExplicitFailure).toBe(true);
    expect(summary.failureReason).toContain("execution halted");
  });

  it("trims plain output for displayText", () => {
    const summary = summarizeCopilotOutput("  Some response text  ");
    expect(summary.displayText).toBe("Some response text");
  });
});

describe("TokenTracker with Copilot data", () => {
  it("should store premiumRequests from SDKUsage", () => {
    const tracker = new TokenTracker();
    tracker.record(
      "feature-dev",
      {
        type: "result",
        usage: {
          input_tokens: 1500,
          output_tokens: 800,
          premium_requests: 1,
        },
        total_cost_usd: COPILOT_PREMIUM_REQUEST_COST_USD,
      },
      3000
    );

    const usage = tracker.getStageUsage("feature-dev");
    expect(usage).toBeDefined();
    expect(usage!.premiumRequests).toBe(1);
    expect(usage!.inputTokens).toBe(1500);
    expect(usage!.outputTokens).toBe(800);
    expect(usage!.costUsd).toBe(COPILOT_PREMIUM_REQUEST_COST_USD);
  });

  it("should leave premiumRequests undefined for non-Copilot results", () => {
    const tracker = new TokenTracker();
    tracker.record(
      "feature-dev",
      {
        type: "result",
        usage: {
          input_tokens: 1000,
          output_tokens: 500,
        },
        total_cost_usd: 0.05,
      },
      2000
    );

    const usage = tracker.getStageUsage("feature-dev");
    expect(usage).toBeDefined();
    expect(usage!.premiumRequests).toBeUndefined();
  });

  it("should show premium request label in formatSummary", () => {
    const tracker = new TokenTracker();
    tracker.record(
      "feature-dev",
      {
        type: "result",
        usage: {
          input_tokens: 1500,
          output_tokens: 800,
          premium_requests: 1,
        },
        total_cost_usd: COPILOT_PREMIUM_REQUEST_COST_USD,
      },
      3000
    );

    const summary = tracker.formatSummary();
    expect(summary).toContain("1 premium req (est.)");
    expect(summary).toContain("feature-dev");
  });

  it("should not show premium request label for non-Copilot stages", () => {
    const tracker = new TokenTracker();
    tracker.record(
      "issue-pickup",
      {
        type: "result",
        usage: { input_tokens: 1000, output_tokens: 500 },
        total_cost_usd: 0.05,
      },
      1500
    );

    const summary = tracker.formatSummary();
    expect(summary).not.toContain("premium req");
  });
});
