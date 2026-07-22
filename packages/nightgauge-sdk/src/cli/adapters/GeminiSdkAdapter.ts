/**
 * Gemini SDK Adapter - Uses the @google/genai SDK directly.
 *
 * Requires GEMINI_API_KEY or GOOGLE_API_KEY. Does not spawn a CLI process.
 * Streams responses via generateContentStream and yields SDKMessage objects.
 *
 * @see Issue #1054 - Create GeminiSdkAdapter
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
import { AdapterError } from "./errors.js";
import { resolveAndValidateModel } from "./modelPreflight.js";

const ADAPTER_NAME = "Gemini SDK";
const GEMINI_SDK_DOCS_URL = "https://ai.google.dev/gemini-api/docs";
const DEFAULT_GEMINI_SDK_MODEL = "gemini-2.5-flash";

/**
 * Resolve the Gemini API key from environment variables.
 * Checks GEMINI_API_KEY first, then GOOGLE_API_KEY.
 */
function resolveApiKey(env: NodeJS.ProcessEnv = process.env): string | undefined {
  return env.GEMINI_API_KEY || env.GOOGLE_API_KEY;
}

export class GeminiSdkAdapter implements ICliAdapter {
  readonly name = "gemini-sdk" as const;
  readonly displayName = "Gemini SDK";
  readonly cliCommand = "gemini";
  // @google/genai generateContentStream — chat completion only, zero tool
  // handling; barred from pipeline dispatch (#57). NOTE: the Go gemini_sdk
  // adapter differs — it spawns the agentic gemini CLI.
  readonly agentic = false;

  async validateAuth(_options?: ValidateAuthOptions): Promise<"passed"> {
    // SDK adapter validates via API key presence, not CLI auth
    const apiKey = resolveApiKey();
    if (!apiKey) {
      throw new AdapterError(
        "No Gemini API key found.\n" +
          "Set one of the following environment variables:\n" +
          "  GEMINI_API_KEY — get a key at aistudio.google.com/apikey\n" +
          "  GOOGLE_API_KEY — for Vertex AI, also set GOOGLE_GENAI_USE_VERTEXAI=true\n" +
          `Docs: ${GEMINI_SDK_DOCS_URL}`,
        "AUTH_MISSING",
        ADAPTER_NAME,
        GEMINI_SDK_DOCS_URL
      );
    }
    return "passed";
  }

  async createQueryFunction(_options?: QueryFunctionOptions): Promise<SDKQueryFunction> {
    const apiKey = resolveApiKey();
    if (!apiKey) {
      throw new AdapterError(
        "No Gemini API key found.\n" +
          "Set one of the following environment variables:\n" +
          "  GEMINI_API_KEY — get a key at aistudio.google.com/apikey\n" +
          "  GOOGLE_API_KEY — for Vertex AI, also set GOOGLE_GENAI_USE_VERTEXAI=true\n" +
          `Docs: ${GEMINI_SDK_DOCS_URL}`,
        "AUTH_MISSING",
        ADAPTER_NAME,
        GEMINI_SDK_DOCS_URL
      );
    }

    // Dynamic import with variable to prevent TypeScript from resolving
    // the optional peer dependency at compile time.
    const genaiPackage = "@google/genai";
    let GoogleGenAI: new (options: { apiKey: string }) => unknown;
    try {
      const genaiModule = await import(genaiPackage);
      GoogleGenAI = genaiModule.GoogleGenAI;
    } catch {
      throw new AdapterError(
        "Failed to import @google/genai.\nFix: npm install @google/genai",
        "BINARY_NOT_FOUND",
        ADAPTER_NAME
      );
    }

    const ai = new GoogleGenAI({ apiKey }) as {
      models: {
        generateContentStream(options: { model: string; contents: string }): Promise<
          AsyncIterable<{
            text?: string;
            usageMetadata?: {
              promptTokenCount?: number;
              candidatesTokenCount?: number;
              cachedContentTokenCount?: number;
            };
          }>
        >;
      };
    };

    // Resolve + validate the configured model (#4021): tier aliases map to a
    // concrete Gemini id and an invalid id throws an actionable AdapterError
    // here, before the first generateContentStream call rather than mid-stream.
    const model =
      resolveAndValidateModel(
        "gemini-sdk",
        process.env.NIGHTGAUGE_GEMINI_MODEL ?? process.env.NIGHTGAUGE_MODEL
      ) ?? DEFAULT_GEMINI_SDK_MODEL;

    async function* queryFn(options: SDKQueryOptions): AsyncGenerator<SDKMessage> {
      const prompt = options.prompt;
      const stream = await ai.models.generateContentStream({
        model,
        contents: prompt,
      });

      let fullText = "";
      let lastUsage: {
        promptTokenCount?: number;
        candidatesTokenCount?: number;
        cachedContentTokenCount?: number;
      } = {};

      for await (const chunk of stream) {
        const text = chunk.text ?? "";
        if (text) {
          fullText += text;
          yield {
            type: "assistant",
            content: text,
          };
        }
        if (chunk.usageMetadata) {
          lastUsage = chunk.usageMetadata;
        }
      }

      // Emit result message with token usage.
      // Gemini's `promptTokenCount` is cache-INCLUSIVE (it already contains
      // `cachedContentTokenCount`). This codebase treats input_tokens and
      // cache_read_input_tokens as DISJOINT pools that sum in totalTokens()
      // (tokenEconomics.ts), so store only the non-cached remainder — clamp the
      // cached subset to the prompt total and clamp negatives. Same
      // normalization #4027 applied to Codex. (#4036)
      const geminiPrompt = Math.max(lastUsage.promptTokenCount ?? 0, 0);
      const geminiCached = Math.min(
        Math.max(lastUsage.cachedContentTokenCount ?? 0, 0),
        geminiPrompt
      );
      yield {
        type: "result",
        subtype: "success",
        content: fullText,
        usage: {
          input_tokens: geminiPrompt - geminiCached,
          output_tokens: lastUsage.candidatesTokenCount ?? 0,
          cache_read_input_tokens: geminiCached,
          cache_creation_input_tokens: 0,
        },
        total_cost_usd: 0, // Gemini SDK does not provide cost info
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
    return true;
  }
}
