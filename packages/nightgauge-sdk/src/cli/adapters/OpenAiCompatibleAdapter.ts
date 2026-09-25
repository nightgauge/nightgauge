/**
 * OpenAI-compatible judge/eval backend (#2128, part 2).
 *
 * One generic HTTP backend for any server that speaks the OpenAI chat
 * completions API: LM Studio, Ollama's `/v1`, oMLX, vLLM, llama.cpp or a
 * hosted endpoint. It replaces the brand-specific LmStudioAdapter and
 * OllamaAdapter, which were byte-for-byte the same client with different
 * defaults. Configuration is three values:
 *
 *   - `baseUrl`   the server's OpenAI-compatible API root (`.../v1`);
 *   - `model`     the model id the server serves;
 *   - `apiKeyEnv` optional: the NAME of an environment variable holding the
 *                 key. An `Authorization` header is sent only when that
 *                 variable is set and non-empty; a local server needs none.
 *
 * Locality is decided by the declared endpoint (part 1, #2129): a loopback or
 * private base URL is local, via {@link isLocalBaseUrl}. It is chat
 * completion only, with no tool loop, so it is never agentic (#57) and serves
 * the eval, judge and summarization surfaces only.
 *
 * It is the `openai-compatible` adapter (#2128 part 3), which replaced the
 * `lm-studio` and `ollama` adapters. Its settings come from explicit config
 * or the `NIGHTGAUGE_OPENAI_COMPATIBLE_*` environment variables.
 */

import type {
  SDKMessage,
  SDKQueryFunction,
  SDKQueryOptions,
} from "../../orchestrator/StageExecutor.js";
import type {
  ICliAdapter,
  NightgaugeAdapter,
  OrchestrationCapability,
  ValidateAuthOptions,
  QueryFunctionOptions,
} from "./ICliAdapter.js";
import { throwConfigInvalid, throwModelNotFound, throwServerUnreachable } from "./errors.js";
import { isLocalBaseUrl } from "../../eval/modelRegistry.js";

/** The adapter name this backend serves. */
export type OpenAiCompatibleName = Extract<NightgaugeAdapter, "openai-compatible">;

/** Explicit configuration; any field left out is read from the environment. */
export interface OpenAiCompatibleConfig {
  baseUrl?: string;
  model?: string;
  /** Name of the environment variable holding the API key (not the key). */
  apiKeyEnv?: string;
  timeoutMs?: number;
}

/** Where the backend reads its settings from and what it defaults to. */
const SETTINGS = Object.freeze({
  displayName: "OpenAI-compatible",
  cliCommand: "openai-compatible",
  envPrefix: "NIGHTGAUGE_OPENAI_COMPATIBLE",
  /** The variable read for a key when no `<PREFIX>_API_KEY_ENV` names one. */
  defaultApiKeyEnv: "NIGHTGAUGE_OPENAI_COMPATIBLE_API_KEY",
  defaultTimeoutMs: 180_000,
  docsUrl: "https://platform.openai.com/docs/api-reference/chat",
  modelHint:
    "Load or pull the model on the server, then name it exactly as GET /v1/models lists it.",
});

/** Fully resolved settings for one query function. */
export interface ResolvedOpenAiCompatibleConfig {
  baseUrl: string;
  model: string;
  apiKeyEnv?: string;
  timeoutMs: number;
}

/**
 * Resolve the backend's settings: explicit config first, then
 * `<PREFIX>_BASE_URL`, `<PREFIX>_MODEL`, `<PREFIX>_API_KEY_ENV` and
 * `<PREFIX>_TIMEOUT_MS` (prefix `NIGHTGAUGE_OPENAI_COMPATIBLE`), then the
 * defaults. There is no default base URL: the server is always named.
 */
export function resolveOpenAiCompatibleConfig(
  config: OpenAiCompatibleConfig = {},
  env: NodeJS.ProcessEnv = process.env
): ResolvedOpenAiCompatibleConfig {
  const p = SETTINGS;
  const rawTimeout = env[`${p.envPrefix}_TIMEOUT_MS`];
  const parsedTimeout = rawTimeout ? parseInt(rawTimeout, 10) : NaN;
  let baseUrl = (config.baseUrl ?? env[`${p.envPrefix}_BASE_URL`] ?? "").trim();
  // A loop, not /\/+$/: that regex backtracks polynomially on many slashes.
  while (baseUrl.endsWith("/")) baseUrl = baseUrl.slice(0, -1);
  const apiKeyEnv =
    config.apiKeyEnv ?? (env[`${p.envPrefix}_API_KEY_ENV`] || undefined) ?? p.defaultApiKeyEnv;
  return {
    baseUrl,
    model: (config.model ?? env[`${p.envPrefix}_MODEL`] ?? "").trim(),
    apiKeyEnv,
    timeoutMs:
      config.timeoutMs ?? (Number.isNaN(parsedTimeout) ? p.defaultTimeoutMs : parsedTimeout),
  };
}

/** Request headers: `Authorization` only when the named key variable is set. */
export function buildOpenAiCompatibleHeaders(
  apiKeyEnv: string | undefined,
  env: NodeJS.ProcessEnv = process.env
): Record<string, string> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  const key = apiKeyEnv ? env[apiKeyEnv] : undefined;
  if (key && key.trim()) headers.Authorization = `Bearer ${key.trim()}`;
  return headers;
}

type Usage = { prompt_tokens?: number; completion_tokens?: number };

export class OpenAiCompatibleAdapter implements ICliAdapter {
  readonly name: OpenAiCompatibleName = "openai-compatible";
  readonly displayName: string = SETTINGS.displayName;
  readonly cliCommand: string = SETTINGS.cliCommand;
  // Chat completion only, zero tool handling: barred from pipeline dispatch
  // (#57); serves the eval, judge and summarization surfaces.
  readonly agentic = false;

  constructor(
    private readonly config: OpenAiCompatibleConfig = {},
    private readonly env: NodeJS.ProcessEnv = process.env
  ) {}

  /** The settings a query function would use now. */
  resolveConfig(): ResolvedOpenAiCompatibleConfig {
    return resolveOpenAiCompatibleConfig(this.config, this.env);
  }

  /**
   * Whether the configured endpoint is a server the operator runs, decided by
   * its base URL (#2128 part 1), never by the adapter's brand.
   */
  isLocal(): boolean {
    return isLocalBaseUrl(this.resolveConfig().baseUrl);
  }

  async validateAuth(_options?: ValidateAuthOptions): Promise<"passed"> {
    // A key is optional; reachability and the model are checked at query time.
    return "passed";
  }

  async createQueryFunction(_options?: QueryFunctionOptions): Promise<SDKQueryFunction> {
    const preset = SETTINGS;
    const label = preset.displayName;
    const { baseUrl, model, apiKeyEnv, timeoutMs } = this.resolveConfig();
    const env = this.env;

    if (!baseUrl) {
      throwConfigInvalid(
        label,
        `${preset.envPrefix}_BASE_URL`,
        `Set ${preset.envPrefix}_BASE_URL to the server's OpenAI-compatible API root, such as http://localhost:1234/v1.`,
        preset.docsUrl
      );
    }
    if (!model) {
      throwConfigInvalid(
        label,
        `${preset.envPrefix}_MODEL`,
        `Set ${preset.envPrefix}_MODEL to a model the server at ${baseUrl} serves.\n${preset.modelHint}`,
        preset.docsUrl
      );
    }

    async function* queryFn(options: SDKQueryOptions): AsyncGenerator<SDKMessage> {
      let response: Response;
      try {
        response = await fetch(`${baseUrl}/chat/completions`, {
          method: "POST",
          headers: buildOpenAiCompatibleHeaders(apiKeyEnv, env),
          body: JSON.stringify({
            model,
            messages: [{ role: "user", content: options.prompt }],
            stream: true,
            stream_options: { include_usage: true },
          }),
          signal: AbortSignal.timeout(timeoutMs),
        });
      } catch (err) {
        throwServerUnreachable(
          label,
          baseUrl,
          `Start the server at ${baseUrl} (${err instanceof Error ? err.message : String(err)})`,
          preset.docsUrl
        );
      }

      if (!response.ok) {
        if (response.status === 404 || response.status === 400) {
          throwModelNotFound(
            label,
            model,
            `Load or pull '${model}' on the server at ${baseUrl}`,
            undefined,
            preset.docsUrl
          );
        }
        throwServerUnreachable(
          label,
          baseUrl,
          `${label} returned HTTP ${response.status}: ${response.statusText}`,
          preset.docsUrl
        );
      }

      if (!response.body) {
        throw new Error(`${label} response has no body.`);
      }

      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let fullText = "";
      let lastUsage: Usage = {};
      let buffer = "";

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split("\n");
        buffer = lines.pop() ?? "";

        for (const line of lines) {
          const trimmed = line.trim();
          if (!trimmed.startsWith("data: ")) continue;
          const data = trimmed.slice(6);
          if (data === "[DONE]") continue;

          let chunk: { choices?: Array<{ delta?: { content?: string } }>; usage?: Usage };
          try {
            chunk = JSON.parse(data);
          } catch {
            continue; // skip malformed chunks
          }
          const delta = chunk.choices?.[0]?.delta?.content;
          if (delta) {
            fullText += delta;
            yield { type: "assistant", content: delta };
          }
          if (chunk.usage) lastUsage = chunk.usage;
        }
      }

      yield {
        type: "result",
        subtype: "success",
        content: fullText,
        usage: {
          input_tokens: lastUsage.prompt_tokens ?? 0,
          output_tokens: lastUsage.completion_tokens ?? 0,
          cache_read_input_tokens: 0,
          cache_creation_input_tokens: 0,
        },
        // A local server has no monetary cost; a hosted one is not priced here.
        total_cost_usd: 0,
        model,
      };
    }

    return queryFn;
  }

  getDefaultArgs(): string[] {
    return [];
  }

  getOrchestrationCapability(): OrchestrationCapability {
    return "sdk-fanout";
  }

  requiresDirectApiKey(): boolean {
    return false;
  }
}
