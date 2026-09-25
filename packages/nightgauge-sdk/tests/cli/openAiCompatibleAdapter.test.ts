/**
 * OpenAiCompatibleAdapter — the generic eval/judge backend (#2128, part 2).
 *
 * Covers the request shape, the Authorization header sent only when the named
 * key variable is set, error handling, and locality decided by the declared
 * base URL (part 1's isLocalBaseUrl).
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import {
  OpenAiCompatibleAdapter,
  buildOpenAiCompatibleHeaders,
  resolveOpenAiCompatibleConfig,
} from "../../src/cli/adapters/OpenAiCompatibleAdapter.js";
import type { SDKMessage } from "../../src/orchestrator/StageExecutor.js";

function sseResponse(lines: string[], init: ResponseInit = { status: 200 }): Response {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      const enc = new TextEncoder();
      for (const l of lines) controller.enqueue(enc.encode(l));
      controller.close();
    },
  });
  return new Response(body, init);
}

const OK_STREAM = [
  'data: {"choices":[{"delta":{"content":"Hel"}}]}\n',
  'data: {"choices":[{"delta":{"content":"lo"}}]}\n',
  "data: not-json\n",
  'data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2}}\n',
  "data: [DONE]\n",
];

async function collect(adapter: OpenAiCompatibleAdapter): Promise<SDKMessage[]> {
  const q = await adapter.createQueryFunction();
  const out: SDKMessage[] = [];
  for await (const m of q({ prompt: "judge this" } as never)) out.push(m);
  return out;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("resolveOpenAiCompatibleConfig", () => {
  it("prefers explicit config over the environment and trims a trailing slash", () => {
    const r = resolveOpenAiCompatibleConfig(
      "lm-studio",
      { baseUrl: "http://127.0.0.1:8000/v1/", model: "m" },
      { NIGHTGAUGE_LM_STUDIO_BASE_URL: "http://other/v1" }
    );
    expect(r.baseUrl).toBe("http://127.0.0.1:8000/v1");
    expect(r.model).toBe("m");
    expect(r.apiKeyEnv).toBe("NIGHTGAUGE_LM_STUDIO_API_KEY");
  });

  it("reads base_url, model and the key variable's name from the environment", () => {
    const r = resolveOpenAiCompatibleConfig("lm-studio", undefined, {
      NIGHTGAUGE_LM_STUDIO_BASE_URL: "https://api.example.com/v1",
      NIGHTGAUGE_LM_STUDIO_MODEL: "judge-1",
      NIGHTGAUGE_LM_STUDIO_API_KEY_ENV: "MY_KEY",
    });
    expect(r).toMatchObject({
      baseUrl: "https://api.example.com/v1",
      model: "judge-1",
      apiKeyEnv: "MY_KEY",
    });
  });

  it("keeps the lm-studio and ollama preset defaults", () => {
    expect(resolveOpenAiCompatibleConfig("lm-studio", {}, {}).baseUrl).toBe(
      "http://localhost:1234/v1"
    );
    expect(resolveOpenAiCompatibleConfig("ollama", {}, {}).baseUrl).toBe(
      "http://localhost:11434/v1"
    );
    expect(resolveOpenAiCompatibleConfig("ollama", {}, {}).timeoutMs).toBe(300_000);
  });
});

describe("buildOpenAiCompatibleHeaders", () => {
  it("sends no Authorization header when no key variable is named", () => {
    expect(buildOpenAiCompatibleHeaders(undefined, { X: "k" })).toEqual({
      "Content-Type": "application/json",
    });
  });

  it("sends no Authorization header when the named variable is unset or empty", () => {
    expect(buildOpenAiCompatibleHeaders("MY_KEY", {})).not.toHaveProperty("Authorization");
    expect(buildOpenAiCompatibleHeaders("MY_KEY", { MY_KEY: "  " })).not.toHaveProperty(
      "Authorization"
    );
  });

  it("sends a bearer token when the named variable is set", () => {
    expect(buildOpenAiCompatibleHeaders("MY_KEY", { MY_KEY: "sk-1" }).Authorization).toBe(
      "Bearer sk-1"
    );
  });
});

describe("OpenAiCompatibleAdapter query", () => {
  it("posts a streamed chat completion and yields text plus a usage result", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(sseResponse(OK_STREAM));
    const adapter = new OpenAiCompatibleAdapter(
      "lm-studio",
      { baseUrl: "http://localhost:9000/v1", model: "judge", apiKeyEnv: "JUDGE_KEY" },
      { JUDGE_KEY: "secret" }
    );
    const msgs = await collect(adapter);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("http://localhost:9000/v1/chat/completions");
    expect(init?.method).toBe("POST");
    expect((init?.headers as Record<string, string>).Authorization).toBe("Bearer secret");
    expect(JSON.parse(String(init?.body))).toEqual({
      model: "judge",
      messages: [{ role: "user", content: "judge this" }],
      stream: true,
      stream_options: { include_usage: true },
    });

    expect(msgs.filter((m) => m.type === "assistant").map((m) => m.content)).toEqual(["Hel", "lo"]);
    const result = msgs.at(-1)!;
    expect(result).toMatchObject({
      type: "result",
      content: "Hello",
      model: "judge",
      total_cost_usd: 0,
      usage: { input_tokens: 7, output_tokens: 2 },
    });
  });

  it("omits the Authorization header when the key variable is not set", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(sseResponse(OK_STREAM));
    const adapter = new OpenAiCompatibleAdapter(
      "lm-studio",
      { baseUrl: "http://localhost:9000/v1", model: "judge", apiKeyEnv: "JUDGE_KEY" },
      {}
    );
    await collect(adapter);
    expect(fetchMock.mock.calls[0][1]?.headers).not.toHaveProperty("Authorization");
  });

  it("fails fast on a missing base URL or model", async () => {
    await expect(
      new OpenAiCompatibleAdapter("lm-studio", { baseUrl: "" }, {}).createQueryFunction()
    ).rejects.toThrow(/NIGHTGAUGE_LM_STUDIO_BASE_URL/);
    await expect(
      new OpenAiCompatibleAdapter(
        "lm-studio",
        { baseUrl: "http://localhost:1/v1" },
        {}
      ).createQueryFunction()
    ).rejects.toThrow(/NIGHTGAUGE_LM_STUDIO_MODEL/);
  });

  it("reports an unknown model on HTTP 404", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("nope", { status: 404 }));
    const adapter = new OpenAiCompatibleAdapter(
      "lm-studio",
      { baseUrl: "http://localhost:9000/v1", model: "missing" },
      {}
    );
    await expect(collect(adapter)).rejects.toThrow(/missing/);
  });

  it("reports an unreachable server on HTTP 500 and on a network error", async () => {
    const cfg = { baseUrl: "http://localhost:9000/v1", model: "judge" };
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response("boom", { status: 500, statusText: "Internal Server Error" })
    );
    await expect(collect(new OpenAiCompatibleAdapter("lm-studio", cfg, {}))).rejects.toThrow(
      /localhost:9000/
    );

    vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new TypeError("fetch failed"));
    await expect(collect(new OpenAiCompatibleAdapter("lm-studio", cfg, {}))).rejects.toThrow(
      /fetch failed/
    );
  });
});

describe("OpenAiCompatibleAdapter locality (part-1 logic)", () => {
  const local = (baseUrl: string) =>
    new OpenAiCompatibleAdapter("lm-studio", { baseUrl, model: "m" }, {}).isLocal();

  it("is local for loopback and private base URLs", () => {
    expect(local("http://localhost:1234/v1")).toBe(true);
    expect(local("http://127.0.0.1:8000/v1")).toBe(true);
    expect(local("http://192.168.1.20:8080/v1")).toBe(true);
  });

  it("is not local for a hosted endpoint, whatever its name", () => {
    expect(local("https://api.openai.com/v1")).toBe(false);
    expect(local("https://lmstudio.example.com/v1")).toBe(false);
  });
});
