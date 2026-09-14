/**
 * OpenCode model catalog service (Issue #1628).
 *
 * OpenCode has no on-disk models cache to read (unlike Codex's
 * `models_cache.json` — see `CodexModelCatalogService`), so this service
 * spawns `opencode models` live and parses its stdout. Every dispatch is
 * bounded and defensive: no shell, a 10s timeout, and an env that disables
 * the catalog fetch and self-update a picker open must never trigger.
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md
 * @see Issue #1628 - vscode: OpenCode in adapter picker, settings, doctor
 *   view and model picker
 */

import { execFile } from "child_process";
import { promisify } from "util";

const execFileAsync = promisify(execFile);

/** Guards against a hung or misbehaving `opencode` binary. */
const OPENCODE_CLI_TIMEOUT_MS = 10_000;

/** Caps the catalog so a runaway `opencode models` output can never grow the
 * picker beyond a sane, human-scrollable size. */
const MAX_MODELS = 500;

/**
 * `provider/model`, one token per line, no whitespace or control characters —
 * the only shape an id may take before it reaches argv on `-m <id>`. Mirrors
 * `isValidOpenCodeModelValue` in `utils/resolvers/modelResolver.ts` (defense
 * in depth; that check runs again on the configured value independently).
 */
// eslint-disable-next-line no-control-regex -- deliberately excluding control chars from argv
const MODEL_ID_PATTERN = /^[a-z0-9][a-z0-9._-]*\/[^\s\x00-\x1f]+$/;

export interface OpenCodeModelEntry {
  /** The raw `provider/model` id, or "" for the non-selectable notice row. */
  id: string;
  /** Text to show in the picker, e.g. "lmstudio/qwen (Configured)". */
  label: string;
  /** False only for the "could not list models" notice row. */
  selectable: boolean;
}

function configuredEntry(configuredModel: string): OpenCodeModelEntry {
  return { id: configuredModel, label: `${configuredModel} (Configured)`, selectable: true };
}

const NOTICE_ENTRY: OpenCodeModelEntry = {
  id: "",
  label: "Could not list OpenCode models — check that the opencode CLI is installed",
  selectable: false,
};

export class OpenCodeModelCatalogService {
  constructor(private readonly binary: string = "opencode") {}

  /**
   * Lists `provider/model` ids from `opencode models`, with the configured
   * model (from `getOpenCodeModel`) first. Never throws: any spawn failure,
   * timeout, or empty/unparseable output degrades to the configured model
   * plus a non-selectable notice.
   */
  async listModels(configuredModel?: string): Promise<OpenCodeModelEntry[]> {
    let stdout: string;
    try {
      const result = await execFileAsync(this.binary, ["models"], {
        timeout: OPENCODE_CLI_TIMEOUT_MS,
        env: {
          ...process.env,
          OPENCODE_DISABLE_MODELS_FETCH: "1",
          OPENCODE_DISABLE_AUTOUPDATE: "1",
        },
      });
      stdout = result.stdout ?? "";
    } catch {
      return this.fallback(configuredModel);
    }

    const models = this.parseModelLines(stdout);
    if (models.length === 0) {
      return this.fallback(configuredModel);
    }

    const entries: OpenCodeModelEntry[] = [];
    if (configuredModel) {
      entries.push(configuredEntry(configuredModel));
    }
    for (const model of models) {
      if (model === configuredModel) continue;
      entries.push({ id: model, label: model, selectable: true });
    }
    return entries;
  }

  private parseModelLines(stdout: string): string[] {
    const seen = new Set<string>();
    const models: string[] = [];
    for (const rawLine of stdout.split("\n")) {
      const line = rawLine.trim();
      if (!line || seen.has(line) || !MODEL_ID_PATTERN.test(line)) continue;
      seen.add(line);
      models.push(line);
      if (models.length >= MAX_MODELS) break;
    }
    return models;
  }

  private fallback(configuredModel?: string): OpenCodeModelEntry[] {
    const entries: OpenCodeModelEntry[] = [];
    if (configuredModel) {
      entries.push(configuredEntry(configuredModel));
    }
    entries.push(NOTICE_ENTRY);
    return entries;
  }
}
