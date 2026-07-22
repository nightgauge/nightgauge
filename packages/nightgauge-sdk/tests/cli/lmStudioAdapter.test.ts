/**
 * LmStudioAdapter Unit Tests
 *
 * @see Issue #2058 - Add LM Studio adapter and config contract
 * @see Issue #2054 - Add LM Studio smoke coverage and regression validation
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { LmStudioAdapter } from "../../src/cli/adapters/LmStudioAdapter.js";

// ---------------------------------------------------------------------------
// SSE stream helper
// ---------------------------------------------------------------------------

/**
 * Build a mock Response whose body is a ReadableStream that yields the
 * given SSE text in a single chunk (or as multiple chunks if an array is
 * provided for boundary-splitting tests).
 */
function makeSseResponse(sseChunks: string | string[]): Response {
  const encoder = new TextEncoder();
  const chunks = typeof sseChunks === "string" ? [sseChunks] : sseChunks;
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk));
      }
      controller.close();
    },
  });
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    body: stream,
  } as unknown as Response;
}

// ---------------------------------------------------------------------------
// Env save/restore helpers
// ---------------------------------------------------------------------------

const LM_ENV_KEYS = [
  "NIGHTGAUGE_LM_STUDIO_MODEL",
  "NIGHTGAUGE_LM_STUDIO_BASE_URL",
  "NIGHTGAUGE_LM_STUDIO_API_KEY",
  "NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS",
] as const;

type LmEnvSnapshot = Record<(typeof LM_ENV_KEYS)[number], string | undefined>;

function snapshotLmEnv(): LmEnvSnapshot {
  return Object.fromEntries(LM_ENV_KEYS.map((k) => [k, process.env[k]])) as LmEnvSnapshot;
}

function restoreLmEnv(snapshot: LmEnvSnapshot): void {
  for (const key of LM_ENV_KEYS) {
    if (snapshot[key] === undefined) {
      delete process.env[key];
    } else {
      process.env[key] = snapshot[key];
    }
  }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("LmStudioAdapter", () => {
  const adapter = new LmStudioAdapter();

  describe("identity", () => {
    it("name is lm-studio", () => {
      expect(adapter.name).toBe("lm-studio");
    });

    it("displayName is LM Studio", () => {
      expect(adapter.displayName).toBe("LM Studio");
    });

    it("cliCommand is lm-studio", () => {
      expect(adapter.cliCommand).toBe("lm-studio");
    });
  });

  describe("orchestration capability", () => {
    it("declares sdk-fanout", () => {
      expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
    });
  });

  describe("requiresDirectApiKey", () => {
    it("returns false", () => {
      expect(adapter.requiresDirectApiKey()).toBe(false);
    });
  });

  describe("getDefaultArgs", () => {
    it("returns empty array", () => {
      expect(adapter.getDefaultArgs()).toEqual([]);
    });
  });

  describe("createQueryFunction", () => {
    it("throws when NIGHTGAUGE_LM_STUDIO_MODEL is not set", async () => {
      // Temporarily clear the env var to ensure model is empty
      const originalModel = process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
      delete process.env.NIGHTGAUGE_LM_STUDIO_MODEL;

      try {
        await expect(adapter.createQueryFunction()).rejects.toThrow(/NIGHTGAUGE_LM_STUDIO_MODEL/);
      } finally {
        // Restore original value
        if (originalModel !== undefined) {
          process.env.NIGHTGAUGE_LM_STUDIO_MODEL = originalModel;
        }
      }
    });

    it("returns a Promise when model is set", () => {
      const originalModel = process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
      process.env.NIGHTGAUGE_LM_STUDIO_MODEL = "test-model";

      try {
        const result = adapter.createQueryFunction();
        // Resolves to an async generator function (no server needed)
        expect(result).toBeInstanceOf(Promise);
      } finally {
        if (originalModel !== undefined) {
          process.env.NIGHTGAUGE_LM_STUDIO_MODEL = originalModel;
        } else {
          delete process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
        }
      }
    });
  });

  describe("validateAuth", () => {
    it("resolves to passed without a server", async () => {
      await expect(adapter.validateAuth()).resolves.toBe("passed");
    });
  });

  // -------------------------------------------------------------------------
  // Request construction tests (fetch-mocked)
  // -------------------------------------------------------------------------

  describe("request construction", () => {
    let envSnapshot: LmEnvSnapshot;

    beforeEach(() => {
      envSnapshot = snapshotLmEnv();
      process.env.NIGHTGAUGE_LM_STUDIO_MODEL = "test-model";
      delete process.env.NIGHTGAUGE_LM_STUDIO_BASE_URL;
      delete process.env.NIGHTGAUGE_LM_STUDIO_API_KEY;
      delete process.env.NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS;
    });

    afterEach(() => {
      vi.restoreAllMocks();
      restoreLmEnv(envSnapshot);
    });

    it("POSTs to correct endpoint from default base URL", async () => {
      const fetchSpy = vi
        .spyOn(global, "fetch")
        .mockResolvedValue(makeSseResponse("data: [DONE]\n\n") as Response);

      const queryFn = await adapter.createQueryFunction();
      for await (const _ of queryFn({ prompt: "test" })) {
        /* drain */
      }

      expect(fetchSpy).toHaveBeenCalledOnce();
      const [url] = fetchSpy.mock.calls[0] as [string, RequestInit];
      expect(url).toBe("http://localhost:1234/v1/chat/completions");
    });

    it("uses POST method", async () => {
      const fetchSpy = vi
        .spyOn(global, "fetch")
        .mockResolvedValue(makeSseResponse("data: [DONE]\n\n") as Response);

      const queryFn = await adapter.createQueryFunction();
      for await (const _ of queryFn({ prompt: "test" })) {
        /* drain */
      }

      const [, options] = fetchSpy.mock.calls[0] as [string, RequestInit];
      expect(options.method).toBe("POST");
    });

    it("uses Authorization header with default api key", async () => {
      const fetchSpy = vi
        .spyOn(global, "fetch")
        .mockResolvedValue(makeSseResponse("data: [DONE]\n\n") as Response);

      const queryFn = await adapter.createQueryFunction();
      for await (const _ of queryFn({ prompt: "test" })) {
        /* drain */
      }

      const [, options] = fetchSpy.mock.calls[0] as [string, RequestInit];
      const headers = options.headers as Record<string, string>;
      expect(headers["Authorization"]).toBe("Bearer lm-studio");
    });

    it("uses custom NIGHTGAUGE_LM_STUDIO_BASE_URL when set", async () => {
      process.env.NIGHTGAUGE_LM_STUDIO_BASE_URL = "http://myserver:5678/v1";
      const fetchSpy = vi
        .spyOn(global, "fetch")
        .mockResolvedValue(makeSseResponse("data: [DONE]\n\n") as Response);

      const queryFn = await adapter.createQueryFunction();
      for await (const _ of queryFn({ prompt: "test" })) {
        /* drain */
      }

      const [url] = fetchSpy.mock.calls[0] as [string, RequestInit];
      expect(url).toBe("http://myserver:5678/v1/chat/completions");
    });

    it("uses custom NIGHTGAUGE_LM_STUDIO_API_KEY when set", async () => {
      process.env.NIGHTGAUGE_LM_STUDIO_API_KEY = "my-custom-key";
      const fetchSpy = vi
        .spyOn(global, "fetch")
        .mockResolvedValue(makeSseResponse("data: [DONE]\n\n") as Response);

      const queryFn = await adapter.createQueryFunction();
      for await (const _ of queryFn({ prompt: "test" })) {
        /* drain */
      }

      const [, options] = fetchSpy.mock.calls[0] as [string, RequestInit];
      const headers = options.headers as Record<string, string>;
      expect(headers["Authorization"]).toBe("Bearer my-custom-key");
    });

    it("sends correct request body: model, messages, stream, stream_options", async () => {
      const fetchSpy = vi
        .spyOn(global, "fetch")
        .mockResolvedValue(makeSseResponse("data: [DONE]\n\n") as Response);

      const queryFn = await adapter.createQueryFunction();
      for await (const _ of queryFn({ prompt: "hello world" })) {
        /* drain */
      }

      const [, options] = fetchSpy.mock.calls[0] as [string, RequestInit];
      const body = JSON.parse(options.body as string);
      expect(body.model).toBe("test-model");
      expect(body.messages).toEqual([{ role: "user", content: "hello world" }]);
      expect(body.stream).toBe(true);
      expect(body.stream_options).toEqual({ include_usage: true });
    });

    it("sets AbortSignal.timeout in the fetch options", async () => {
      const fetchSpy = vi
        .spyOn(global, "fetch")
        .mockResolvedValue(makeSseResponse("data: [DONE]\n\n") as Response);

      const queryFn = await adapter.createQueryFunction();
      for await (const _ of queryFn({ prompt: "test" })) {
        /* drain */
      }

      const [, options] = fetchSpy.mock.calls[0] as [string, RequestInit];
      expect(options.signal).toBeInstanceOf(AbortSignal);
    });

    it("uses NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS for the timeout signal", async () => {
      process.env.NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS = "5000";
      const timeoutSpy = vi.spyOn(AbortSignal, "timeout");
      vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse("data: [DONE]\n\n") as Response);

      const queryFn = await adapter.createQueryFunction();
      for await (const _ of queryFn({ prompt: "test" })) {
        /* drain */
      }

      expect(timeoutSpy).toHaveBeenCalledWith(5000);
    });
  });

  // -------------------------------------------------------------------------
  // SSE response parsing tests
  // -------------------------------------------------------------------------

  describe("SSE response parsing", () => {
    let envSnapshot: LmEnvSnapshot;

    beforeEach(() => {
      envSnapshot = snapshotLmEnv();
      process.env.NIGHTGAUGE_LM_STUDIO_MODEL = "test-model";
      delete process.env.NIGHTGAUGE_LM_STUDIO_BASE_URL;
      delete process.env.NIGHTGAUGE_LM_STUDIO_API_KEY;
      delete process.env.NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS;
    });

    afterEach(() => {
      vi.restoreAllMocks();
      restoreLmEnv(envSnapshot);
    });

    it("yields assistant messages for each delta", async () => {
      const sse = [
        'data: {"choices":[{"delta":{"content":"hello"}}]}\n',
        "\n",
        'data: {"choices":[{"delta":{"content":" world"}}]}\n',
        "\n",
        "data: [DONE]\n",
        "\n",
      ].join("");
      vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

      const queryFn = await adapter.createQueryFunction();
      const messages = [];
      for await (const msg of queryFn({ prompt: "test" })) {
        messages.push(msg);
      }

      const assistantMessages = messages.filter((m) => m.type === "assistant");
      expect(assistantMessages).toHaveLength(2);
      expect(assistantMessages[0].content).toBe("hello");
      expect(assistantMessages[1].content).toBe(" world");
    });

    it("yields result message with mapped token usage", async () => {
      const sse = [
        'data: {"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}\n',
        "\n",
        "data: [DONE]\n",
        "\n",
      ].join("");
      vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

      const queryFn = await adapter.createQueryFunction();
      const messages = [];
      for await (const msg of queryFn({ prompt: "test" })) {
        messages.push(msg);
      }

      const result = messages.find((m) => m.type === "result") as Record<string, unknown>;
      expect(result).toBeDefined();
      const usage = result.usage as Record<string, number>;
      expect(usage.input_tokens).toBe(10);
      expect(usage.output_tokens).toBe(5);
      expect(usage.cache_read_input_tokens).toBe(0);
      expect(usage.cache_creation_input_tokens).toBe(0);
    });

    it("sets total_cost_usd to 0 for local inference", async () => {
      const sse = 'data: {"choices":[{"delta":{"content":"x"}}]}\n\ndata: [DONE]\n\n';
      vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

      const queryFn = await adapter.createQueryFunction();
      const messages = [];
      for await (const msg of queryFn({ prompt: "test" })) {
        messages.push(msg);
      }

      const result = messages.find((m) => m.type === "result") as Record<string, unknown>;
      expect(result).toBeDefined();
      expect(result.total_cost_usd).toBe(0);
    });

    it("sets model in result message to configured model name", async () => {
      const sse = 'data: {"choices":[{"delta":{"content":"x"}}]}\n\ndata: [DONE]\n\n';
      vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

      const queryFn = await adapter.createQueryFunction();
      const messages = [];
      for await (const msg of queryFn({ prompt: "test" })) {
        messages.push(msg);
      }

      const result = messages.find((m) => m.type === "result") as Record<string, unknown>;
      expect(result).toBeDefined();
      expect(result.model).toBe("test-model");
    });

    it("skips [DONE] line without yielding an extra message", async () => {
      const sse = "data: [DONE]\n\n";
      vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

      const queryFn = await adapter.createQueryFunction();
      const messages = [];
      for await (const msg of queryFn({ prompt: "test" })) {
        messages.push(msg);
      }

      // Only the result message should be emitted — no assistant messages
      expect(messages.filter((m) => m.type === "assistant")).toHaveLength(0);
      expect(messages.filter((m) => m.type === "result")).toHaveLength(1);
    });

    it("skips malformed JSON chunks without throwing", async () => {
      const sse = [
        "data: not-valid-json\n",
        "\n",
        'data: {"choices":[{"delta":{"content":"ok"}}]}\n',
        "\n",
        "data: [DONE]\n",
        "\n",
      ].join("");
      vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

      const queryFn = await adapter.createQueryFunction();
      const messages = [];
      for await (const msg of queryFn({ prompt: "test" })) {
        messages.push(msg);
      }

      const assistantMessages = messages.filter((m) => m.type === "assistant");
      expect(assistantMessages).toHaveLength(1);
      expect(assistantMessages[0].content).toBe("ok");
    });

    it("handles chunks split across buffer boundaries", async () => {
      // Simulate a delta chunk split across two reads:
      // First read ends mid-JSON (no newline yet), second read completes it.
      const deltaJson = '{"choices":[{"delta":{"content":"split"}}]}';
      const firstChunk = `data: ${deltaJson}`; // no trailing newline
      const secondChunk = `\n\ndata: [DONE]\n\n`; // completes the line

      vi.spyOn(global, "fetch").mockResolvedValue(
        makeSseResponse([firstChunk, secondChunk]) as Response
      );

      const queryFn = await adapter.createQueryFunction();
      const messages = [];
      for await (const msg of queryFn({ prompt: "test" })) {
        messages.push(msg);
      }

      const assistantMessages = messages.filter((m) => m.type === "assistant");
      expect(assistantMessages).toHaveLength(1);
      expect(assistantMessages[0].content).toBe("split");
    });
  });

  // -------------------------------------------------------------------------
  // Error handling tests
  // -------------------------------------------------------------------------

  describe("error handling", () => {
    let envSnapshot: LmEnvSnapshot;

    beforeEach(() => {
      envSnapshot = snapshotLmEnv();
      process.env.NIGHTGAUGE_LM_STUDIO_MODEL = "test-model";
      delete process.env.NIGHTGAUGE_LM_STUDIO_BASE_URL;
      delete process.env.NIGHTGAUGE_LM_STUDIO_API_KEY;
      delete process.env.NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS;
    });

    afterEach(() => {
      vi.restoreAllMocks();
      restoreLmEnv(envSnapshot);
    });

    it("throws model-not-loaded error on HTTP 404", async () => {
      vi.spyOn(global, "fetch").mockResolvedValue({
        ok: false,
        status: 404,
        statusText: "Not Found",
      } as Response);

      const queryFn = await adapter.createQueryFunction();

      await expect(async () => {
        for await (const _ of queryFn({ prompt: "test" })) {
          /* drain */
        }
      }).rejects.toThrow(/Model.*not found/i);
    });

    it("throws model-not-loaded error on HTTP 400", async () => {
      vi.spyOn(global, "fetch").mockResolvedValue({
        ok: false,
        status: 400,
        statusText: "Bad Request",
      } as Response);

      const queryFn = await adapter.createQueryFunction();

      await expect(async () => {
        for await (const _ of queryFn({ prompt: "test" })) {
          /* drain */
        }
      }).rejects.toThrow(/Model.*not found/i);
    });

    it("throws generic HTTP error for other non-ok status codes", async () => {
      vi.spyOn(global, "fetch").mockResolvedValue({
        ok: false,
        status: 500,
        statusText: "Internal Server Error",
      } as Response);

      const queryFn = await adapter.createQueryFunction();

      await expect(async () => {
        for await (const _ of queryFn({ prompt: "test" })) {
          /* drain */
        }
      }).rejects.toThrow(/HTTP 500/);
    });

    it("throws when response body is null", async () => {
      vi.spyOn(global, "fetch").mockResolvedValue({
        ok: true,
        status: 200,
        statusText: "OK",
        body: null,
      } as unknown as Response);

      const queryFn = await adapter.createQueryFunction();

      await expect(async () => {
        for await (const _ of queryFn({ prompt: "test" })) {
          /* drain */
        }
      }).rejects.toThrow(/no body/i);
    });
  });
});
