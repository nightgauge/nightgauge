/**
 * GeminiSdkAdapter Unit Tests
 *
 * @see Issue #1054 - Create GeminiSdkAdapter
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { GeminiSdkAdapter } from "../../src/cli/adapters/GeminiSdkAdapter.js";

describe("GeminiSdkAdapter", () => {
  const adapter = new GeminiSdkAdapter();

  describe("identity", () => {
    it("name is gemini-sdk", () => {
      expect(adapter.name).toBe("gemini-sdk");
    });

    it("displayName is Gemini SDK", () => {
      expect(adapter.displayName).toBe("Gemini SDK");
    });

    it("cliCommand is gemini", () => {
      expect(adapter.cliCommand).toBe("gemini");
    });
  });

  describe("orchestration capability", () => {
    it("declares sdk-fanout", () => {
      expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
    });
  });

  describe("requiresDirectApiKey", () => {
    it("returns true", () => {
      expect(adapter.requiresDirectApiKey()).toBe(true);
    });
  });

  describe("validateAuth", () => {
    beforeEach(() => {
      process.env.GEMINI_API_KEY = "test-gemini-key";
    });

    afterEach(() => {
      delete process.env.GEMINI_API_KEY;
    });

    it("returns passed without a runner", async () => {
      const result = await adapter.validateAuth();
      expect(result).toBe("passed");
    });
  });

  describe("getDefaultArgs", () => {
    it("returns empty array", () => {
      expect(adapter.getDefaultArgs()).toEqual([]);
    });
  });

  describe("createQueryFunction", () => {
    it("returns a Promise", () => {
      // We only verify the method is callable and returns a Promise.
      // We do NOT await it because @google/genai may not be installed
      // in the test environment. Catch the expected rejection to prevent
      // unhandled promise warnings.
      const result = adapter.createQueryFunction();
      result.catch(() => {});
      expect(result).toBeInstanceOf(Promise);
    });
  });
});
