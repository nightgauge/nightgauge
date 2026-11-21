import { describe, it, expect } from "vitest";
import {
  MIN_NATIVE_WORKFLOW_VERSION,
  ULTRACODE_KEYWORD_RENAME_VERSION,
  DISABLE_WORKFLOWS_ENV,
  parseVersion,
  supportsNativeWorkflow,
  ultracodeKeyword,
  isNativeWorkflowDisabledByEnv,
  detectClaudeCliVersion,
  preflightNativeWorkflow,
  mapNativeUsage,
  runClaudeNativeWorkflow,
  NativeWorkflowUnavailableError,
} from "../../src/cli/adapters/ClaudeNativeWorkflow.js";
import type { PreflightCommandRunner } from "../../src/cli/codexPreflight.js";
import type { WorkflowSpec } from "../../src/workflow/WorkflowSpec.js";

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

describe("ClaudeNativeWorkflow (#3910)", () => {
  describe("constants", () => {
    it("pins the expected floor and rename versions", () => {
      expect(MIN_NATIVE_WORKFLOW_VERSION).toBe("2.1.154");
      expect(ULTRACODE_KEYWORD_RENAME_VERSION).toBe("2.1.160");
      expect(DISABLE_WORKFLOWS_ENV).toBe("CLAUDE_CODE_DISABLE_WORKFLOWS");
    });
  });

  describe("parseVersion", () => {
    it("parses major.minor.patch versions", () => {
      expect(parseVersion("2.1.154")).toEqual([2, 1, 154]);
      expect(parseVersion("1.0.0")).toEqual([1, 0, 0]);
    });

    it("parses major.minor with default patch of 0", () => {
      expect(parseVersion("2.1")).toEqual([2, 1, 0]);
    });

    it("handles leading prefixes and pre-release suffixes", () => {
      expect(parseVersion("v2.1.160-beta.1")).toEqual([2, 1, 160]);
      expect(parseVersion("claude 2.1.154 (Darwin)")).toEqual([2, 1, 154]);
    });

    it("returns null for non-numeric or empty strings", () => {
      expect(parseVersion("")).toBeNull();
      expect(parseVersion(undefined)).toBeNull();
      expect(parseVersion(null)).toBeNull();
      expect(parseVersion("not-a-version")).toBeNull();
    });
  });

  describe("supportsNativeWorkflow", () => {
    it("returns true at or above floor (>= 2.1.154)", () => {
      expect(supportsNativeWorkflow("2.1.154")).toBe(true);
      expect(supportsNativeWorkflow("2.1.160")).toBe(true);
      expect(supportsNativeWorkflow("2.2.0")).toBe(true);
      expect(supportsNativeWorkflow("3.0.0")).toBe(true);
    });

    it("returns false below floor (< 2.1.154)", () => {
      expect(supportsNativeWorkflow("2.1.153")).toBe(false);
      expect(supportsNativeWorkflow("2.0.99")).toBe(false);
      expect(supportsNativeWorkflow("1.0.0")).toBe(false);
    });

    it("fails closed on unparseable or undefined versions", () => {
      expect(supportsNativeWorkflow(undefined)).toBe(false);
      expect(supportsNativeWorkflow(null)).toBe(false);
      expect(supportsNativeWorkflow("invalid")).toBe(false);
    });
  });

  describe("ultracodeKeyword", () => {
    it('returns "ultracode" at or above 2.1.160', () => {
      expect(ultracodeKeyword("2.1.160")).toBe("ultracode");
      expect(ultracodeKeyword("2.2.0")).toBe("ultracode");
    });

    it('returns "workflow" between 2.1.154 and 2.1.160', () => {
      expect(ultracodeKeyword("2.1.154")).toBe("workflow");
      expect(ultracodeKeyword("2.1.159")).toBe("workflow");
    });

    it('defaults to "workflow" on unparseable versions', () => {
      expect(ultracodeKeyword(undefined)).toBe("workflow");
      expect(ultracodeKeyword("invalid")).toBe("workflow");
    });
  });

  describe("isNativeWorkflowDisabledByEnv", () => {
    it("recognizes disabling flags", () => {
      expect(isNativeWorkflowDisabledByEnv({ [DISABLE_WORKFLOWS_ENV]: "true" })).toBe(true);
      expect(isNativeWorkflowDisabledByEnv({ [DISABLE_WORKFLOWS_ENV]: "1" })).toBe(true);
      expect(isNativeWorkflowDisabledByEnv({ [DISABLE_WORKFLOWS_ENV]: "yes" })).toBe(true);
      expect(isNativeWorkflowDisabledByEnv({ [DISABLE_WORKFLOWS_ENV]: "TRUE" })).toBe(true);
    });

    it("returns false for non-disabling values", () => {
      expect(isNativeWorkflowDisabledByEnv({})).toBe(false);
      expect(isNativeWorkflowDisabledByEnv({ [DISABLE_WORKFLOWS_ENV]: "false" })).toBe(false);
      expect(isNativeWorkflowDisabledByEnv({ [DISABLE_WORKFLOWS_ENV]: "0" })).toBe(false);
      expect(isNativeWorkflowDisabledByEnv({ [DISABLE_WORKFLOWS_ENV]: "no" })).toBe(false);
    });
  });

  describe("detectClaudeCliVersion", () => {
    it("reads version string when runner exits 0", async () => {
      const runner = createRunner({
        "claude --version": { code: 0, stdout: "claude 2.1.160\n" },
      });
      const version = await detectClaudeCliVersion(runner, "/tmp");
      expect(version).toBe("2.1.160");
    });

    it("returns null when runner fails or is undefined", async () => {
      expect(await detectClaudeCliVersion(undefined, "/tmp")).toBeNull();

      const failingRunner = createRunner({
        "claude --version": { code: 1, stderr: "not found" },
      });
      expect(await detectClaudeCliVersion(failingRunner, "/tmp")).toBeNull();
    });
  });

  describe("preflightNativeWorkflow", () => {
    it("returns disabled-by-env when env flag is set", () => {
      const result = preflightNativeWorkflow("2.1.160", {
        env: { [DISABLE_WORKFLOWS_ENV]: "true" },
      });
      expect(result.ready).toBe(false);
      expect(result.reason).toBe("disabled-by-env");
    });

    it("returns disabled-by-config when configDisabled is set", () => {
      const result = preflightNativeWorkflow("2.1.160", {
        configDisabled: true,
        env: {},
      });
      expect(result.ready).toBe(false);
      expect(result.reason).toBe("disabled-by-config");
    });

    it("returns version-undetectable when version is null", () => {
      const result = preflightNativeWorkflow(null, { env: {} });
      expect(result.ready).toBe(false);
      expect(result.reason).toBe("version-undetectable");
    });

    it("returns version-below-floor when version < 2.1.154", () => {
      const result = preflightNativeWorkflow("2.1.150", { env: {} });
      expect(result.ready).toBe(false);
      expect(result.reason).toBe("version-below-floor");
    });

    it("returns ready true when version >= 2.1.154 and no kill-switch", () => {
      const result = preflightNativeWorkflow("2.1.160", { env: {} });
      expect(result.ready).toBe(true);
      expect(result.detectedVersion).toBe("2.1.160");
      expect(result.reason).toBeUndefined();
    });
  });

  describe("mapNativeUsage", () => {
    it("returns zeroed usage with estimated: true when report is undefined", () => {
      const usage = mapNativeUsage(undefined);
      expect(usage.inputTokens).toBe(0);
      expect(usage.outputTokens).toBe(0);
      expect(usage.costUsd).toBe(0);
      expect(usage.estimated).toBe(true);
    });

    it("maps reported tokens and preserves exact estimate flag", () => {
      const usage = mapNativeUsage({
        inputTokens: 1000,
        outputTokens: 500,
        costUsd: 0.05,
        estimated: false,
      });
      expect(usage.inputTokens).toBe(1000);
      expect(usage.outputTokens).toBe(500);
      expect(usage.costUsd).toBe(0.05);
      expect(usage.estimated).toBe(false);
    });
  });

  describe("runClaudeNativeWorkflow", () => {
    const validSpec: WorkflowSpec = {
      schemaVersion: 4,
      runId: "run-1",
      phases: [
        {
          name: "phase-1",
          agents: [
            {
              agentId: "agent-1",
              prompt: "test step",
            },
          ],
        },
      ],
      ceiling: {
        maxConcurrent: 2,
        maxTotal: 5,
      },
    };

    const mockSink = {
      emit: () => {},
    };

    it("throws on an invalid WorkflowSpec up front", async () => {
      const invalidSpec: WorkflowSpec = {
        schemaVersion: 99 as unknown as 4,
        runId: "invalid-run",
        phases: [],
        ceiling: {
          maxConcurrent: 0,
          maxTotal: 0,
        },
      };

      await expect(
        runClaudeNativeWorkflow(invalidSpec, mockSink, {
          surface: "agent-sdk",
          detectedVersion: "2.1.160",
        })
      ).rejects.toThrow(/invalid WorkflowSpec/);
    });

    it("throws NativeWorkflowUnavailableError when disabled by env", async () => {
      await expect(
        runClaudeNativeWorkflow(validSpec, mockSink, {
          surface: "agent-sdk",
          detectedVersion: "2.1.160",
          env: { [DISABLE_WORKFLOWS_ENV]: "1" },
        })
      ).rejects.toThrow(NativeWorkflowUnavailableError);
    });

    it("throws NativeWorkflowUnavailableError when disabled by config", async () => {
      await expect(
        runClaudeNativeWorkflow(validSpec, mockSink, {
          surface: "agent-sdk",
          detectedVersion: "2.1.160",
          configDisabled: true,
          env: {},
        })
      ).rejects.toThrow(NativeWorkflowUnavailableError);
    });

    it("throws NativeWorkflowUnavailableError when version is undetectable", async () => {
      await expect(
        runClaudeNativeWorkflow(validSpec, mockSink, {
          surface: "agent-sdk",
          detectedVersion: null,
          env: {},
        })
      ).rejects.toThrow(NativeWorkflowUnavailableError);
    });

    it("throws NativeWorkflowUnavailableError when version is below floor", async () => {
      await expect(
        runClaudeNativeWorkflow(validSpec, mockSink, {
          surface: "agent-sdk",
          detectedVersion: "2.1.150",
          env: {},
        })
      ).rejects.toThrow(NativeWorkflowUnavailableError);
    });
  });
});
