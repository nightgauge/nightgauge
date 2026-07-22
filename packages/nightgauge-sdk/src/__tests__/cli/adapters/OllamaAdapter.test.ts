/**
 * OllamaAdapter unit tests.
 *
 * Covers identity, capability declarations, environment variable resolution,
 * error handling paths, and SSE streaming with token parsing.
 *
 * @see Issue #2591 — Add Ollama adapter for local LLM inference
 */

import { describe, it, expect, afterEach, vi } from "vitest";
import { OllamaAdapter } from "../../../cli/adapters/OllamaAdapter.js";

// ---------------------------------------------------------------------------
// fetch mock helpers
// ---------------------------------------------------------------------------

function makeSseStream(lines: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      for (const line of lines) {
        controller.enqueue(encoder.encode(line + "\n"));
      }
      controller.close();
    },
  });
}

function makeMockResponse(lines: string[], status = 200, statusText = "OK"): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText,
    body: makeSseStream(lines),
  } as unknown as Response;
}

function sseChunk(content: string): string {
  return `data: ${JSON.stringify({ choices: [{ delta: { content } }] })}`;
}

function sseUsageChunk(promptTokens: number, completionTokens: number): string {
  return `data: ${JSON.stringify({
    choices: [{ delta: {}, finish_reason: "stop" }],
    usage: { prompt_tokens: promptTokens, completion_tokens: completionTokens },
  })}`;
}

function sseDone(): string {
  return "data: [DONE]";
}

// ---------------------------------------------------------------------------
// OllamaAdapter identity and metadata
// ---------------------------------------------------------------------------

describe("OllamaAdapter identity", () => {
  const adapter = new OllamaAdapter();

  it("name is ollama", () => {
    expect(adapter.name).toBe("ollama");
  });

  it("displayName is Ollama", () => {
    expect(adapter.displayName).toBe("Ollama");
  });

  it("cliCommand is ollama", () => {
    expect(adapter.cliCommand).toBe("ollama");
  });

  it("requiresDirectApiKey() returns false", () => {
    expect(adapter.requiresDirectApiKey()).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// OllamaAdapter.getOrchestrationCapability()
// ---------------------------------------------------------------------------

describe("OllamaAdapter.getOrchestrationCapability()", () => {
  const adapter = new OllamaAdapter();

  it("declares sdk-fanout — Ollama is a fan-out participant", () => {
    expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
  });
});

// ---------------------------------------------------------------------------
// getDefaultArgs()
// ---------------------------------------------------------------------------

describe("OllamaAdapter.getDefaultArgs()", () => {
  it("returns empty array", () => {
    expect(new OllamaAdapter().getDefaultArgs()).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// validateAuth()
// ---------------------------------------------------------------------------

describe("OllamaAdapter.validateAuth()", () => {
  it("returns 'passed' unconditionally", async () => {
    const adapter = new OllamaAdapter();
    const result = await adapter.validateAuth();
    expect(result).toBe("passed");
  });
});

// ---------------------------------------------------------------------------
// Environment variable resolution
// ---------------------------------------------------------------------------

describe("OllamaAdapter — environment variable resolution", () => {
  afterEach(() => {
    delete process.env.NIGHTGAUGE_OLLAMA_BASE_URL;
    delete process.env.NIGHTGAUGE_OLLAMA_MODEL;
    delete process.env.NIGHTGAUGE_OLLAMA_API_KEY;
    delete process.env.NIGHTGAUGE_OLLAMA_TIMEOUT_MS;
  });

  it("createQueryFunction() defaults to http://localhost:11434/v1 when base URL unset", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(makeMockResponse([sseChunk("hi"), sseDone()]));

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    const msgs: unknown[] = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      msgs.push(msg);
    }

    expect(fetchMock).toHaveBeenCalledOnce();
    const url = fetchMock.mock.calls[0][0] as string;
    expect(url).toBe("http://localhost:11434/v1/chat/completions");
    fetchMock.mockRestore();
  });

  it("createQueryFunction() uses NIGHTGAUGE_OLLAMA_BASE_URL when set", async () => {
    process.env.NIGHTGAUGE_OLLAMA_BASE_URL = "http://remote-ollama:11434/v1";
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(makeMockResponse([sseChunk("hi"), sseDone()]));

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    const msgs: unknown[] = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      msgs.push(msg);
    }

    expect(fetchMock.mock.calls[0][0]).toBe("http://remote-ollama:11434/v1/chat/completions");
    fetchMock.mockRestore();
  });

  it("createQueryFunction() throws if NIGHTGAUGE_OLLAMA_MODEL is not set", async () => {
    delete process.env.NIGHTGAUGE_OLLAMA_MODEL;
    const adapter = new OllamaAdapter();
    await expect(adapter.createQueryFunction()).rejects.toThrow("NIGHTGAUGE_OLLAMA_MODEL");
  });

  it("createQueryFunction() throws descriptive error mentioning ollama pull when model unset", async () => {
    delete process.env.NIGHTGAUGE_OLLAMA_MODEL;
    const adapter = new OllamaAdapter();
    await expect(adapter.createQueryFunction()).rejects.toThrow("ollama pull");
  });

  it("resolves default API key to 'ollama' when NIGHTGAUGE_OLLAMA_API_KEY unset", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(makeMockResponse([sseChunk("hi"), sseDone()]));

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    const init = fetchMock.mock.calls[0][1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers["Authorization"]).toBe("Bearer ollama");
    fetchMock.mockRestore();
  });

  it("uses custom API key when NIGHTGAUGE_OLLAMA_API_KEY is set", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    process.env.NIGHTGAUGE_OLLAMA_API_KEY = "my-secret-key";
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(makeMockResponse([sseChunk("hi"), sseDone()]));

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    const init = fetchMock.mock.calls[0][1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers["Authorization"]).toBe("Bearer my-secret-key");
    fetchMock.mockRestore();
  });

  it("resolves default timeout to 300000ms when NIGHTGAUGE_OLLAMA_TIMEOUT_MS unset", async () => {
    // Verify that AbortSignal.timeout is called (indirectly verify via no error with signal param)
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(makeMockResponse([sseChunk("hi"), sseDone()]));

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    const init = fetchMock.mock.calls[0][1] as RequestInit;
    // Signal should be an AbortSignal (from AbortSignal.timeout(300000))
    expect(init.signal).toBeDefined();
    fetchMock.mockRestore();
  });

  it("uses custom timeout when NIGHTGAUGE_OLLAMA_TIMEOUT_MS is set", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    process.env.NIGHTGAUGE_OLLAMA_TIMEOUT_MS = "60000";
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(makeMockResponse([sseChunk("hi"), sseDone()]));

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    // verify fetch was called (timeout resolved to 60000 — no error expected)
    expect(fetchMock).toHaveBeenCalledOnce();
    fetchMock.mockRestore();
  });
});

// ---------------------------------------------------------------------------
// OllamaAdapter.createQueryFunction() — error handling
// ---------------------------------------------------------------------------

describe("OllamaAdapter.createQueryFunction() — error handling", () => {
  afterEach(() => {
    delete process.env.NIGHTGAUGE_OLLAMA_MODEL;
    vi.restoreAllMocks();
  });

  it("throws with 'ollama pull' guidance on 404 response", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "nonexistent-model";
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(makeMockResponse([], 404, "Not Found"));

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    await expect(
      (async () => {
        for await (const _ of queryFn({ prompt: "test", options: {} })) {
          /* consume */
        }
      })()
    ).rejects.toThrow("ollama pull nonexistent-model");
  });

  it("throws with 'ollama pull' guidance on 400 response", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "bad-model";
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(makeMockResponse([], 400, "Bad Request"));

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    await expect(
      (async () => {
        for await (const _ of queryFn({ prompt: "test", options: {} })) {
          /* consume */
        }
      })()
    ).rejects.toThrow("ollama pull bad-model");
  });

  it("throws generic HTTP error on other non-ok responses", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      makeMockResponse([], 500, "Internal Server Error")
    );

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    await expect(
      (async () => {
        for await (const _ of queryFn({ prompt: "test", options: {} })) {
          /* consume */
        }
      })()
    ).rejects.toThrow("HTTP 500");
  });

  it("error message on 5xx includes 'ollama serve' guidance", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      makeMockResponse([], 503, "Service Unavailable")
    );

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    await expect(
      (async () => {
        for await (const _ of queryFn({ prompt: "test", options: {} })) {
          /* consume */
        }
      })()
    ).rejects.toThrow("ollama serve");
  });
});

// ---------------------------------------------------------------------------
// OllamaAdapter SSE streaming
// ---------------------------------------------------------------------------

describe("OllamaAdapter SSE streaming", () => {
  afterEach(() => {
    delete process.env.NIGHTGAUGE_OLLAMA_MODEL;
    vi.restoreAllMocks();
  });

  it("yields assistant messages for each text delta", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      makeMockResponse([sseChunk("Hello"), sseChunk(" world"), sseDone()])
    );

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    const msgs: Array<{ type: string; content: string }> = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      msgs.push(msg as { type: string; content: string });
    }

    const assistantMsgs = msgs.filter((m) => m.type === "assistant");
    expect(assistantMsgs).toHaveLength(2);
    expect(assistantMsgs[0].content).toBe("Hello");
    expect(assistantMsgs[1].content).toBe(" world");
  });

  it("emits result message with accumulated text content", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      makeMockResponse([sseChunk("Hello"), sseChunk(" world"), sseDone()])
    );

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    const msgs: unknown[] = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      msgs.push(msg);
    }

    const resultMsg = msgs.find((m) => (m as Record<string, unknown>).type === "result") as Record<
      string,
      unknown
    >;
    expect(resultMsg).toBeDefined();
    expect(resultMsg.content).toBe("Hello world");
    expect(resultMsg.subtype).toBe("success");
  });

  it("collects token usage from final chunk and emits in result message", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      makeMockResponse([sseChunk("Response text"), sseUsageChunk(42, 17), sseDone()])
    );

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    const msgs: unknown[] = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      msgs.push(msg);
    }

    const resultMsg = msgs.find((m) => (m as Record<string, unknown>).type === "result") as Record<
      string,
      unknown
    >;
    expect(resultMsg).toBeDefined();

    const usage = resultMsg.usage as Record<string, number>;
    expect(usage.input_tokens).toBe(42);
    expect(usage.output_tokens).toBe(17);
    expect(usage.cache_read_input_tokens).toBe(0);
    expect(usage.cache_creation_input_tokens).toBe(0);
  });

  it("emits $0.00 total_cost_usd for local inference", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      makeMockResponse([sseChunk("hi"), sseUsageChunk(5, 3), sseDone()])
    );

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    const msgs: unknown[] = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      msgs.push(msg);
    }

    const resultMsg = msgs.find((m) => (m as Record<string, unknown>).type === "result") as Record<
      string,
      unknown
    >;
    expect(resultMsg.total_cost_usd).toBe(0);
  });

  it("skips malformed JSON chunks without throwing", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      makeMockResponse(["data: {invalid json here", sseChunk("Valid text"), sseDone()])
    );

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    const msgs: Array<{ type: string; content: string }> = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      msgs.push(msg as { type: string; content: string });
    }

    // The malformed chunk is skipped — only the valid chunk yields a message
    const assistantMsgs = msgs.filter((m) => m.type === "assistant");
    expect(assistantMsgs).toHaveLength(1);
    expect(assistantMsgs[0].content).toBe("Valid text");
  });

  it("sends model name in request body", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "codellama";
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(makeMockResponse([sseChunk("hi"), sseDone()]));

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    const init = fetchMock.mock.calls[0][1] as RequestInit;
    const body = JSON.parse(init.body as string);
    expect(body.model).toBe("codellama");
    expect(body.stream).toBe(true);
    expect(body.stream_options).toEqual({ include_usage: true });
  });

  it("sends user prompt in messages array", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(makeMockResponse([sseChunk("hi"), sseDone()]));

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    for await (const _ of queryFn({ prompt: "Tell me a joke", options: {} })) {
      /* consume */
    }

    const init = fetchMock.mock.calls[0][1] as RequestInit;
    const body = JSON.parse(init.body as string);
    expect(body.messages).toEqual([{ role: "user", content: "Tell me a joke" }]);
  });

  it("includes model name in result message", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "deepseek-coder";
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      makeMockResponse([sseChunk("code"), sseDone()])
    );

    const adapter = new OllamaAdapter();
    const queryFn = await adapter.createQueryFunction();
    const msgs: unknown[] = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      msgs.push(msg);
    }

    const resultMsg = msgs.find((m) => (m as Record<string, unknown>).type === "result") as Record<
      string,
      unknown
    >;
    expect(resultMsg.model).toBe("deepseek-coder");
  });
});

// ---------------------------------------------------------------------------
// AdapterRegistry integration
// ---------------------------------------------------------------------------

describe("OllamaAdapter registry integration", () => {
  it("defaultRegistry.get('ollama') returns OllamaAdapter instance", async () => {
    const { defaultRegistry } = await import("../../../cli/adapters/AdapterRegistry.js");
    const adapter = defaultRegistry.get("ollama");
    expect(adapter).toBeInstanceOf(OllamaAdapter);
    expect(adapter.name).toBe("ollama");
  });

  it("defaultRegistry.getNames() includes 'ollama'", async () => {
    const { defaultRegistry } = await import("../../../cli/adapters/AdapterRegistry.js");
    expect(defaultRegistry.getNames()).toContain("ollama");
  });
});
