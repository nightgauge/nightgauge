import { execFile } from "child_process";
import { promisify } from "util";

const execFileAsync = promisify(execFile);
const CLI_TIMEOUT_MS = 10000;
const SERVER_READY_TIMEOUT_MS = 15000;
const SERVER_READY_POLL_INTERVAL_MS = 500;

export interface LmStudioModelInfo {
  id: string;
  loaded: boolean;
  maxContextLength?: number;
  currentContextLength?: number;
}

export interface LmStudioLogger {
  debug(message: string, data?: object): void;
  info(message: string, data?: object): void;
  warn(message: string, data?: object): void;
  error(message: string, error?: Error | object): void;
}

const noopLogger: LmStudioLogger = {
  debug: () => {},
  info: () => {},
  warn: () => {},
  error: () => {},
};

function normalizeManagementBaseUrl(baseUrl: string): string {
  const trimmed = baseUrl.replace(/\/+$/, "");
  if (trimmed.endsWith("/api/v1")) return trimmed;
  if (trimmed.endsWith("/v1")) return `${trimmed.slice(0, -3)}/api/v1`;
  return `${trimmed}/api/v1`;
}

function normalizeOpenAiBaseUrl(baseUrl: string): string {
  const trimmed = baseUrl.replace(/\/+$/, "");
  if (trimmed.endsWith("/api/v1")) return `${trimmed.slice(0, -7)}/v1`;
  if (trimmed.endsWith("/v1")) return trimmed;
  return `${trimmed}/v1`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function inferLoaded(model: Record<string, unknown>): boolean {
  if (typeof model.loaded === "boolean") return model.loaded;
  if (typeof model.isLoaded === "boolean") return model.isLoaded;
  if (typeof model.state === "string") return model.state.toLowerCase() === "loaded";
  if (Array.isArray(model.instances)) return model.instances.length > 0;
  return false;
}

function inferId(model: Record<string, unknown>): string | null {
  const candidates = [
    model.id,
    model.key,
    model.modelKey,
    model.model_key,
    model.identifier,
    model.path,
  ];
  for (const candidate of candidates) {
    if (typeof candidate === "string" && candidate.trim()) {
      return candidate.trim();
    }
  }
  return null;
}

function parseJsonArray(stdout: string): unknown[] {
  const parsed = JSON.parse(stdout) as unknown;
  return Array.isArray(parsed)
    ? parsed
    : isRecord(parsed) && Array.isArray(parsed.data)
      ? parsed.data
      : isRecord(parsed) && Array.isArray(parsed.models)
        ? parsed.models
        : [];
}

function parsePositiveInteger(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isInteger(value) && value > 0) return value;
  if (typeof value === "string") {
    const parsed = parseInt(value, 10);
    if (!Number.isNaN(parsed) && parsed > 0) return parsed;
  }
  return undefined;
}

function inferMaxContextLength(model: Record<string, unknown>): number | undefined {
  return parsePositiveInteger(model.max_context_length ?? model.maxContextLength);
}

function inferCurrentContextLength(model: Record<string, unknown>): number | undefined {
  if (Array.isArray(model.loaded_instances)) {
    for (const instance of model.loaded_instances) {
      if (!isRecord(instance)) continue;
      const config = isRecord(instance.config) ? instance.config : undefined;
      const current =
        parsePositiveInteger(config?.context_length) ??
        parsePositiveInteger(config?.contextLength) ??
        parsePositiveInteger(instance.context_length) ??
        parsePositiveInteger(instance.contextLength);
      if (current) return current;
    }
  }

  return parsePositiveInteger(model.context_length) ?? parsePositiveInteger(model.contextLength);
}

export class LmStudioService {
  constructor(private readonly logger: LmStudioLogger = noopLogger) {}

  async listModels(baseUrl: string, apiKey: string): Promise<LmStudioModelInfo[]> {
    this.logger.info("Listing LM Studio models", { baseUrl });
    try {
      const models = await this.listModelsViaCli();
      const enrichedModels = await this.enrichModelsViaHttp(models, baseUrl, apiKey);
      this.logger.info("Listed LM Studio models via CLI", { count: enrichedModels.length });
      return enrichedModels;
    } catch (error) {
      this.logger.warn("LM Studio CLI model listing failed; falling back to HTTP", {
        baseUrl,
        error: error instanceof Error ? error.message : String(error),
      });
      try {
        const models = await this.listModelsViaHttp(baseUrl, apiKey);
        this.logger.info("Listed LM Studio models via management HTTP", {
          count: models.length,
          baseUrl,
        });
        return models;
      } catch (httpError) {
        this.logger.warn("LM Studio management API listing failed; falling back to OpenAI API", {
          baseUrl,
          error: httpError instanceof Error ? httpError.message : String(httpError),
        });
        const models = await this.listModelsViaOpenAi(baseUrl, apiKey);
        this.logger.info("Listed LM Studio models via OpenAI HTTP", {
          count: models.length,
          baseUrl,
        });
        return models;
      }
    }
  }

  async startServer(baseUrl?: string, apiKey?: string): Promise<void> {
    this.logger.info("Checking LM Studio server before start");
    if (await this.isServerReady(baseUrl, apiKey)) {
      this.logger.info("LM Studio server already running");
      return;
    }

    try {
      this.logger.info("Starting LM Studio server via CLI");
      await execFileAsync("lms", ["server", "start"], { timeout: CLI_TIMEOUT_MS });
    } catch (error) {
      this.logger.warn("LM Studio server start command returned error", {
        error: error instanceof Error ? error.message : String(error),
      });
      if (!(await this.isServerReady(baseUrl, apiKey))) {
        throw this.wrapCliError(error, "Failed to start LM Studio server");
      }
    }

    if (!(await this.waitForServerReady(baseUrl, apiKey))) {
      this.logger.error("LM Studio server did not report running after start");
      throw new Error(
        "LM Studio server did not start. Open the LM Studio app and enable the local server."
      );
    }

    this.logger.info("LM Studio server confirmed running");
  }

  async loadModel(model: string, contextLength?: number): Promise<void> {
    const args = ["load", model];
    if (contextLength && Number.isInteger(contextLength) && contextLength > 0) {
      args.push(`--context-length=${contextLength}`);
    }

    try {
      this.logger.info("Loading LM Studio model via CLI", { model, contextLength });
      await execFileAsync("lms", args);
      this.logger.info("Loaded LM Studio model", { model, contextLength });
    } catch (error) {
      this.logger.error(
        "Failed to load LM Studio model via CLI",
        error instanceof Error ? error : { model, contextLength, error: String(error) }
      );
      throw this.wrapCliError(error, `Failed to load LM Studio model '${model}'`);
    }
  }

  async isServerRunning(): Promise<boolean> {
    try {
      const result = await execFileAsync("lms", ["server", "status"], { timeout: CLI_TIMEOUT_MS });
      const status = result.stdout.trim().toLowerCase();
      if (status.includes("not running")) return false;
      const running = status.includes("running");
      this.logger.debug("LM Studio server status checked", {
        status: result.stdout.trim(),
        running,
      });
      return running;
    } catch {
      this.logger.warn("LM Studio server status check failed");
      return false;
    }
  }

  private async isServerReady(baseUrl?: string, apiKey?: string): Promise<boolean> {
    const [cliRunning, httpReachable] = await Promise.all([
      this.isServerRunning(),
      this.isHttpServerReachable(baseUrl, apiKey),
    ]);
    const ready = cliRunning || httpReachable;
    this.logger.debug("LM Studio server readiness checked", {
      cliRunning,
      httpReachable,
      ready,
      baseUrl,
    });
    return ready;
  }

  private async waitForServerReady(baseUrl?: string, apiKey?: string): Promise<boolean> {
    const deadline = Date.now() + SERVER_READY_TIMEOUT_MS;

    while (Date.now() < deadline) {
      if (await this.isServerReady(baseUrl, apiKey)) {
        return true;
      }
      await new Promise((resolve) => setTimeout(resolve, SERVER_READY_POLL_INTERVAL_MS));
    }

    return this.isServerReady(baseUrl, apiKey);
  }

  private async isHttpServerReachable(baseUrl?: string, apiKey?: string): Promise<boolean> {
    if (!baseUrl) return false;

    const openAiBaseUrl = normalizeOpenAiBaseUrl(baseUrl);
    try {
      const response = await fetch(`${openAiBaseUrl}/models`, {
        headers: apiKey ? { Authorization: `Bearer ${apiKey}` } : undefined,
      });
      const reachable = response.ok;
      this.logger.debug("LM Studio HTTP server readiness checked", {
        url: `${openAiBaseUrl}/models`,
        status: response.status,
        reachable,
      });
      return reachable;
    } catch (error) {
      this.logger.debug("LM Studio HTTP server readiness check failed", {
        url: `${openAiBaseUrl}/models`,
        error: error instanceof Error ? error.message : String(error),
      });
      return false;
    }
  }

  private async listModelsViaCli(): Promise<LmStudioModelInfo[]> {
    this.logger.debug("Listing LM Studio models via CLI");
    const [diskResult, loadedResult] = await Promise.all([
      execFileAsync("lms", ["ls", "--json"], { timeout: CLI_TIMEOUT_MS }),
      execFileAsync("lms", ["ps", "--json"], { timeout: CLI_TIMEOUT_MS }).catch(() => ({
        stdout: "[]",
        stderr: "",
      })),
    ]);

    const diskModels = parseJsonArray(diskResult.stdout);
    const loadedModels = new Set(
      parseJsonArray(loadedResult.stdout)
        .filter(isRecord)
        .map((model) => inferId(model))
        .filter((model): model is string => Boolean(model))
    );

    const models: LmStudioModelInfo[] = [];
    for (const rawModel of diskModels) {
      if (!isRecord(rawModel)) continue;
      const id = inferId(rawModel);
      if (!id) continue;
      models.push({
        id,
        loaded: loadedModels.has(id) || inferLoaded(rawModel),
      });
    }
    models.sort((a, b) => {
      if (a.loaded !== b.loaded) return a.loaded ? -1 : 1;
      return a.id.localeCompare(b.id);
    });

    this.logger.debug("LM Studio CLI model listing completed", {
      diskCount: diskModels.length,
      loadedCount: loadedModels.size,
      resultCount: models.length,
    });
    return models;
  }

  private async listModelsViaHttp(baseUrl: string, apiKey: string): Promise<LmStudioModelInfo[]> {
    const managementBaseUrl = normalizeManagementBaseUrl(baseUrl);
    this.logger.debug("Listing LM Studio models via HTTP", { url: `${managementBaseUrl}/models` });
    const response = await fetch(`${managementBaseUrl}/models`, {
      headers: apiKey ? { Authorization: `Bearer ${apiKey}` } : undefined,
    });

    if (!response.ok) {
      throw new Error(
        `LM Studio model listing failed: HTTP ${response.status} ${response.statusText}`
      );
    }

    const body = (await response.json()) as unknown;
    const rawModels = Array.isArray(body)
      ? body
      : isRecord(body) && Array.isArray(body.data)
        ? body.data
        : isRecord(body) && Array.isArray(body.models)
          ? body.models
          : [];

    const models: LmStudioModelInfo[] = [];
    for (const rawModel of rawModels) {
      if (!isRecord(rawModel)) continue;
      const id = inferId(rawModel);
      if (!id) continue;

      const modelInfo: LmStudioModelInfo = {
        id,
        loaded: inferLoaded(rawModel),
      };
      const maxContextLength = inferMaxContextLength(rawModel);
      const currentContextLength = inferCurrentContextLength(rawModel);
      if (maxContextLength !== undefined) modelInfo.maxContextLength = maxContextLength;
      if (currentContextLength !== undefined) {
        modelInfo.currentContextLength = currentContextLength;
      }
      models.push(modelInfo);
    }
    models.sort((a, b) => {
      if (a.loaded !== b.loaded) return a.loaded ? -1 : 1;
      return a.id.localeCompare(b.id);
    });

    this.logger.debug("LM Studio HTTP model listing completed", {
      resultCount: models.length,
      baseUrl,
    });
    return models;
  }

  private async listModelsViaOpenAi(baseUrl: string, apiKey: string): Promise<LmStudioModelInfo[]> {
    const openAiBaseUrl = normalizeOpenAiBaseUrl(baseUrl);
    this.logger.debug("Listing LM Studio models via OpenAI HTTP", {
      url: `${openAiBaseUrl}/models`,
    });
    const response = await fetch(`${openAiBaseUrl}/models`, {
      headers: apiKey ? { Authorization: `Bearer ${apiKey}` } : undefined,
    });

    if (!response.ok) {
      throw new Error(
        `LM Studio OpenAI model listing failed: HTTP ${response.status} ${response.statusText}`
      );
    }

    const body = (await response.json()) as unknown;
    const rawModels = Array.isArray(body)
      ? body
      : isRecord(body) && Array.isArray(body.data)
        ? body.data
        : isRecord(body) && Array.isArray(body.models)
          ? body.models
          : [];

    const models: LmStudioModelInfo[] = [];
    for (const rawModel of rawModels) {
      if (!isRecord(rawModel)) continue;
      const id = inferId(rawModel);
      if (!id) continue;

      models.push({
        id,
        loaded: true,
        maxContextLength: inferMaxContextLength(rawModel),
        currentContextLength: inferCurrentContextLength(rawModel),
      });
    }

    models.sort((a, b) => a.id.localeCompare(b.id));
    this.logger.debug("LM Studio OpenAI HTTP model listing completed", {
      resultCount: models.length,
      baseUrl,
    });
    return models;
  }

  private async enrichModelsViaHttp(
    models: LmStudioModelInfo[],
    baseUrl: string,
    apiKey: string
  ): Promise<LmStudioModelInfo[]> {
    try {
      const metadataModels = await this.listModelsViaHttp(baseUrl, apiKey);
      const metadataById = new Map(metadataModels.map((model) => [model.id, model]));

      return models.map((model) => {
        const metadata = metadataById.get(model.id);
        if (!metadata) return model;
        return {
          ...model,
          loaded: model.loaded || metadata.loaded,
          maxContextLength: metadata.maxContextLength,
          currentContextLength: metadata.currentContextLength,
        };
      });
    } catch (error) {
      this.logger.warn("LM Studio HTTP metadata enrichment failed", {
        baseUrl,
        error: error instanceof Error ? error.message : String(error),
      });
      return models;
    }
  }

  private wrapCliError(error: unknown, prefix: string): Error {
    if (isRecord(error)) {
      const parts = [prefix];
      if (typeof error.message === "string" && error.message) parts.push(error.message);
      if (typeof error.stderr === "string" && error.stderr.trim()) parts.push(error.stderr.trim());
      return new Error(parts.join(": "));
    }
    return new Error(prefix);
  }
}

export { normalizeManagementBaseUrl, normalizeOpenAiBaseUrl };
