/**
 * Ollama Adapter - HTTP-based adapter for Ollama's OpenAI-compatible API.
 *
 * Uses the built-in fetch API (Node.js >=18) to communicate with a local
 * Ollama server. Streams responses via OpenAI SSE and yields SDKMessage
 * objects.
 *
 * @see Issue #2591 - Add Ollama adapter for local LLM inference
 */

import type {
  SDKMessage,
  SDKQueryFunction,
  SDKQueryOptions,
} from "../../orchestrator/StageExecutor.js";
import type {
  ICliAdapter,
  OrchestrationCapability,
  ValidateAuthOptions,
  QueryFunctionOptions,
} from "./ICliAdapter.js";
import { throwConfigInvalid, throwModelNotFound, throwServerUnreachable } from "./errors.js";

const ADAPTER_NAME = "Ollama";
const OLLAMA_DOCS_URL = "https://ollama.com/library";

/**
 * Resolve the Ollama server base URL from environment.
 * Default: http://localhost:11434/v1
 */
function resolveBaseUrl(env: NodeJS.ProcessEnv = process.env): string {
  return env.NIGHTGAUGE_OLLAMA_BASE_URL ?? "http://localhost:11434/v1";
}

/**
 * Resolve the model name from environment.
 * Empty string means no model configured — will error at createQueryFunction().
 */
function resolveModel(env: NodeJS.ProcessEnv = process.env): string {
  return env.NIGHTGAUGE_OLLAMA_MODEL ?? "";
}

/**
 * Resolve the API key from environment.
 * Ollama accepts any string; 'ollama' is the documented placeholder.
 * Remote Ollama deployments may require a real API key.
 */
function resolveApiKey(env: NodeJS.ProcessEnv = process.env): string {
  return env.NIGHTGAUGE_OLLAMA_API_KEY ?? "ollama";
}

/**
 * Resolve the request timeout in milliseconds.
 * Default: 300000 (5 minutes) — local models can be slow on first load.
 */
function resolveTimeoutMs(env: NodeJS.ProcessEnv = process.env): number {
  const raw = env.NIGHTGAUGE_OLLAMA_TIMEOUT_MS;
  if (!raw) return 300_000;
  const parsed = parseInt(raw, 10);
  return isNaN(parsed) ? 300_000 : parsed;
}

export class OllamaAdapter implements ICliAdapter {
  readonly name = "ollama" as const;
  readonly displayName = "Ollama";
  readonly cliCommand = "ollama";
  // fetch/SSE chat completion only, zero tool handling; barred from
  // pipeline dispatch (#57) — remains available for eval/judge surfaces.
  readonly agentic = false;

  async validateAuth(_options?: ValidateAuthOptions): Promise<"passed"> {
    // Ollama accepts any string as API key — no auth validation needed.
    // Server connectivity and model availability are validated at query time
    // via createQueryFunction().
    return "passed";
  }

  async createQueryFunction(_options?: QueryFunctionOptions): Promise<SDKQueryFunction> {
    const baseUrl = resolveBaseUrl();
    const model = resolveModel();
    const apiKey = resolveApiKey();
    const timeoutMs = resolveTimeoutMs();

    if (!model) {
      throwConfigInvalid(
        ADAPTER_NAME,
        "NIGHTGAUGE_OLLAMA_MODEL",
        "Set NIGHTGAUGE_OLLAMA_MODEL to a model you have pulled (e.g., llama3.1, codellama).\n" +
          "Download a model first: ollama pull llama3.1",
        OLLAMA_DOCS_URL
      );
    }

    async function* queryFn(options: SDKQueryOptions): AsyncGenerator<SDKMessage> {
      const response = await fetch(`${baseUrl}/chat/completions`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${apiKey}`,
        },
        body: JSON.stringify({
          model,
          messages: [{ role: "user", content: options.prompt }],
          stream: true,
          stream_options: { include_usage: true },
        }),
        signal: AbortSignal.timeout(timeoutMs),
      });

      if (!response.ok) {
        if (response.status === 404 || response.status === 400) {
          throwModelNotFound(
            ADAPTER_NAME,
            model,
            `ollama pull ${model}`,
            "ollama serve",
            OLLAMA_DOCS_URL
          );
        }
        throwServerUnreachable(
          ADAPTER_NAME,
          baseUrl,
          `ollama serve  # (HTTP ${response.status}: ${response.statusText})`,
          OLLAMA_DOCS_URL
        );
      }

      if (!response.body) {
        throw new Error("Ollama response has no body.");
      }

      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let fullText = "";
      let lastUsage: {
        prompt_tokens?: number;
        completion_tokens?: number;
      } = {};
      let buffer = "";

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split("\n");
        // Keep the last incomplete line in the buffer
        buffer = lines.pop() ?? "";

        for (const line of lines) {
          const trimmed = line.trim();
          if (!trimmed || !trimmed.startsWith("data: ")) continue;

          const data = trimmed.slice(6); // Remove 'data: ' prefix
          if (data === "[DONE]") continue;

          let chunk: {
            choices?: Array<{
              delta?: { content?: string };
              finish_reason?: string | null;
            }>;
            usage?: {
              prompt_tokens?: number;
              completion_tokens?: number;
            };
          };
          try {
            chunk = JSON.parse(data);
          } catch {
            continue; // Skip malformed chunks
          }

          // Extract text delta
          const delta = chunk.choices?.[0]?.delta?.content;
          if (delta) {
            fullText += delta;
            yield {
              type: "assistant",
              content: delta,
            };
          }

          // Capture usage from the final chunk
          if (chunk.usage) {
            lastUsage = chunk.usage;
          }
        }
      }

      // Emit result message with token usage
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
        total_cost_usd: 0, // Local inference has no monetary cost
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
