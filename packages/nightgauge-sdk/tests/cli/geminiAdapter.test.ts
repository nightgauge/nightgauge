import { describe, it, expect, afterEach } from "vitest";
import { GeminiAdapter } from "../../src/cli/adapters/GeminiAdapter.js";
import type { PreflightCommandRunner } from "../../src/cli/codexPreflight.js";
import { AdapterError } from "../../src/cli/adapters/errors.js";

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

/** Version response for a valid Gemini CLI installation. */
const GEMINI_VERSION_OK = {
  "gemini --version": { code: 0, stdout: "gemini 0.29.5\n" },
};

/** gcloud auth success response. */
const GCLOUD_AUTH_OK = {
  "gcloud auth print-access-token": { code: 0, stdout: "ya29.fake-token\n" },
};

/** gcloud auth failure response. */
const GCLOUD_AUTH_FAIL = {
  "gcloud auth print-access-token": {
    code: 1,
    stderr: "ERROR: (gcloud.auth.print-access-token) not logged in",
  },
};

/** gcloud auth timeout response (exit code 124). */
const GCLOUD_AUTH_TIMEOUT = {
  "gcloud auth print-access-token": {
    code: 124,
    stderr: "Command timed out",
  },
};

/** Env vars to clean up after each test. */
const AUTH_ENV_VARS = [
  "GEMINI_API_KEY",
  "GOOGLE_API_KEY",
  "GOOGLE_GENAI_USE_VERTEXAI",
  "NIGHTGAUGE_GEMINI_MODEL",
  "NIGHTGAUGE_MODEL",
];

describe("GeminiAdapter", () => {
  const adapter = new GeminiAdapter();

  afterEach(() => {
    for (const key of AUTH_ENV_VARS) {
      delete process.env[key];
    }
  });

  // --- Identity and capabilities (unchanged) ---

  it("should have correct identity fields", () => {
    expect(adapter.name).toBe("gemini");
    expect(adapter.displayName).toBe("Gemini");
    expect(adapter.cliCommand).toBe("gemini");
  });

  it("declares the sdk-fanout orchestration capability", () => {
    expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
  });

  it("should not require a direct API key", () => {
    expect(adapter.requiresDirectApiKey()).toBe(false);
  });

  it("should return stream-json default args", () => {
    expect(adapter.getDefaultArgs()).toEqual(["--output-format", "stream-json"]);
  });

  // --- Model routing (#53): NIGHTGAUGE_GEMINI_MODEL → --model ---

  it("creates a query function when NIGHTGAUGE_GEMINI_MODEL is a valid Gemini model", async () => {
    process.env.NIGHTGAUGE_GEMINI_MODEL = "gemini-2.5-pro";
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });

  it("resolves a tier keyword through the Gemini tier map", async () => {
    process.env.NIGHTGAUGE_GEMINI_MODEL = "sonnet";
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });

  it("falls back to NIGHTGAUGE_MODEL when NIGHTGAUGE_GEMINI_MODEL is unset", async () => {
    process.env.NIGHTGAUGE_MODEL = "opus";
    const queryFn = await adapter.createQueryFunction();
    expect(typeof queryFn).toBe("function");
  });

  it("fails fast on an invalid Gemini model before spawning the CLI", async () => {
    process.env.NIGHTGAUGE_GEMINI_MODEL = "gemini-99-ultra";
    await expect(adapter.createQueryFunction()).rejects.toThrow(AdapterError);
    await expect(adapter.createQueryFunction()).rejects.toThrow(/not valid for the Gemini/);
  });

  // --- Auth: no runner (SDK direct usage) ---

  it("should pass auth without a runner (SDK direct usage)", async () => {
    const result = await adapter.validateAuth({});
    expect(result).toBe("passed");
  });

  // --- Auth: CLI not installed ---

  it("should fail when gemini CLI is not installed", async () => {
    const runner = createRunner({
      "gemini --version": { code: 1, stderr: "command not found" },
    });

    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(
      /not installed or not in PATH/
    );
  });

  // --- Auth cascade: GEMINI_API_KEY ---

  it("should pass auth with GEMINI_API_KEY set", async () => {
    process.env.GEMINI_API_KEY = "test-api-key-123";
    const runner = createRunner({ ...GEMINI_VERSION_OK });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  it("should skip gcloud when GEMINI_API_KEY is set", async () => {
    process.env.GEMINI_API_KEY = "test-api-key-123";
    const calls: string[] = [];
    const runner: PreflightCommandRunner = async (command, args) => {
      const key = `${command} ${args.join(" ")}`;
      calls.push(key);
      if (key === "gemini --version") {
        return { code: 0, stdout: "gemini 0.29.5\n", stderr: "" };
      }
      return { code: 1, stdout: "", stderr: "unexpected" };
    };

    await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(calls).not.toContain("gcloud auth print-access-token");
  });

  // --- Auth cascade: Vertex AI env vars ---

  it("should pass auth with Vertex AI env vars", async () => {
    process.env.GOOGLE_GENAI_USE_VERTEXAI = "true";
    process.env.GOOGLE_API_KEY = "vertex-key-456";
    const runner = createRunner({ ...GEMINI_VERSION_OK });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  it("should require both Vertex AI vars (missing GOOGLE_API_KEY falls through to gcloud)", async () => {
    process.env.GOOGLE_GENAI_USE_VERTEXAI = "true";
    // GOOGLE_API_KEY not set — should fall through to gcloud
    const runner = createRunner({
      ...GEMINI_VERSION_OK,
      ...GCLOUD_AUTH_OK,
    });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  it('should require GOOGLE_GENAI_USE_VERTEXAI to be "true"', async () => {
    process.env.GOOGLE_GENAI_USE_VERTEXAI = "false";
    process.env.GOOGLE_API_KEY = "vertex-key-456";
    // Falls through to gcloud since USE_VERTEXAI is not "true"
    const runner = createRunner({
      ...GEMINI_VERSION_OK,
      ...GCLOUD_AUTH_OK,
    });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  // --- Auth cascade: gcloud OAuth ---

  it("should pass auth with gcloud OAuth", async () => {
    // No env vars set — falls through to gcloud
    const runner = createRunner({
      ...GEMINI_VERSION_OK,
      ...GCLOUD_AUTH_OK,
    });

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(result).toBe("passed");
  });

  // --- Auth cascade: all methods fail ---

  it("should throw when no auth method is configured", async () => {
    const runner = createRunner({
      ...GEMINI_VERSION_OK,
      ...GCLOUD_AUTH_FAIL,
    });

    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(
      /No Gemini authentication detected/
    );
  });

  it("should include actionable hints in auth failure message", async () => {
    const runner = createRunner({
      ...GEMINI_VERSION_OK,
      ...GCLOUD_AUTH_FAIL,
    });

    try {
      await adapter.validateAuth({ runner, cwd: "/tmp" });
      expect.fail("should have thrown");
    } catch (err) {
      const message = (err as Error).message;
      expect(message).toContain("GEMINI_API_KEY");
      expect(message).toContain("GOOGLE_API_KEY");
      expect(message).toContain("gcloud auth login");
    }
  });

  // --- Auth cascade: gcloud timeout ---

  it("should throw timeout-specific error when gcloud times out", async () => {
    const runner = createRunner({
      ...GEMINI_VERSION_OK,
      ...GCLOUD_AUTH_TIMEOUT,
    });

    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(AdapterError);
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).rejects.toThrow(/timed out/);
  });

  // --- Version warnings (updated to include auth env var so tests pass the cascade) ---

  it("should warn but not block on older version", async () => {
    process.env.GEMINI_API_KEY = "test-key";
    const warnings: string[] = [];
    const originalWarn = console.warn;
    console.warn = (...args: unknown[]) => {
      warnings.push(args.join(" "));
    };

    try {
      const runner = createRunner({
        "gemini --version": { code: 0, stdout: "gemini 0.0.1\n" },
      });

      const result = await adapter.validateAuth({ runner, cwd: "/tmp" });
      expect(result).toBe("passed");
      expect(warnings.some((w) => w.includes("WARNING") && w.includes("0.0.1"))).toBe(true);
    } finally {
      console.warn = originalWarn;
    }
  });

  it("should not warn when version meets minimum", async () => {
    process.env.GEMINI_API_KEY = "test-key";
    const warnings: string[] = [];
    const originalWarn = console.warn;
    console.warn = (...args: unknown[]) => {
      warnings.push(args.join(" "));
    };

    try {
      const runner = createRunner({
        "gemini --version": { code: 0, stdout: "gemini 0.29.5\n" },
      });

      await adapter.validateAuth({ runner, cwd: "/tmp" });
      expect(warnings.filter((w) => w.includes("[gemini-adapter]"))).toHaveLength(0);
    } finally {
      console.warn = originalWarn;
    }
  });
});
