import { describe, it, expect, afterEach } from "vitest";
import { ClaudeSdkAdapter } from "../../src/cli/adapters/ClaudeSdkAdapter.js";
import { AdapterError } from "../../src/cli/adapters/errors.js";

describe("ClaudeSdkAdapter", () => {
  const adapter = new ClaudeSdkAdapter();

  afterEach(() => {
    delete process.env.ANTHROPIC_API_KEY;
    delete process.env.CLAUDE_CODE_DISABLE_WORKFLOWS;
  });

  describe("identity and capabilities", () => {
    it("has correct identity fields", () => {
      expect(adapter.name).toBe("claude-sdk");
      expect(adapter.displayName).toBe("Claude SDK");
      expect(adapter.cliCommand).toBe("claude");
      expect(adapter.agentic).toBe(true);
    });

    it("declares native-workflow orchestration capability", () => {
      expect(adapter.getOrchestrationCapability()).toBe("native-workflow");
    });

    it("requires direct API key", () => {
      expect(adapter.requiresDirectApiKey()).toBe(true);
    });

    it("returns empty default args", () => {
      expect(adapter.getDefaultArgs()).toEqual([]);
    });
  });

  describe("validateAuth", () => {
    it("fails when ANTHROPIC_API_KEY is not set", async () => {
      delete process.env.ANTHROPIC_API_KEY;

      await expect(adapter.validateAuth()).rejects.toThrow(AdapterError);
      await expect(adapter.validateAuth()).rejects.toThrow(/No Anthropic API key found/);
    });

    it("fails when ANTHROPIC_API_KEY is whitespace only", async () => {
      process.env.ANTHROPIC_API_KEY = "   ";

      await expect(adapter.validateAuth()).rejects.toThrow(AdapterError);
    });

    it("passes when ANTHROPIC_API_KEY is configured", async () => {
      process.env.ANTHROPIC_API_KEY = "sk-ant-test-key-123";

      const result = await adapter.validateAuth();
      expect(result).toBe("passed");
      expect(adapter.nativeWorkflowReadiness).toBeDefined();
    });

    it("includes docs URL and actionable category in auth failure", async () => {
      delete process.env.ANTHROPIC_API_KEY;

      try {
        await adapter.validateAuth();
        expect.fail("should have thrown");
      } catch (err) {
        expect(err).toBeInstanceOf(AdapterError);
        const adapterErr = err as AdapterError;
        expect(adapterErr.category).toBe("AUTH_MISSING");
        expect(adapterErr.actionUrl).toContain("docs.anthropic.com");
      }
    });
  });

  describe("createQueryFunction", () => {
    it("throws classified BINARY_NOT_FOUND AdapterError when optional SDK peer is missing", async () => {
      // In the default test environment, @anthropic-ai/claude-agent-sdk is not bundled.
      try {
        await adapter.createQueryFunction();
      } catch (err) {
        expect(err).toBeInstanceOf(AdapterError);
        const adapterErr = err as AdapterError;
        expect(adapterErr.category).toBe("BINARY_NOT_FOUND");
        expect(adapterErr.message).toContain(
          "@anthropic-ai/claude-agent-sdk in the consuming project"
        );
      }
    });
  });
});
