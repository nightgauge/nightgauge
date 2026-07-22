/**
 * Adapter Doctor command (Issue #4031).
 *
 * Surfaces per-adapter readiness on demand instead of discovering misconfig
 * mid-run. For each adapter the pipeline resolves to (per stage + the global
 * default) it reports CLI install + version, authentication, Codex MCP config,
 * and the model each stage resolves to — with an actionable fix per failure.
 *
 * Three data sources are merged (each the authority for its slice):
 *   - Go `nightgauge doctor --adapters … --json` → deterministic
 *     binary/version/MCP facts (also consumed by skill preflight).
 *   - SDK `runAdapterAuthPreflight` → auth verdict + `suggestedFix` remediation.
 *   - TS resolvers (`resolveStageAdapter`, `getStageModel`) → per-stage routing.
 */

import * as vscode from "vscode";
import { spawn } from "child_process";
import {
  runAdapterAuthPreflight,
  createDefaultPreflightRunner,
  resolveCodexModelAlias,
  PIPELINE_STAGE_ORDER,
  PHASE_REGISTRY,
  type IncrediAdapter,
  type AdapterPreflightAggregateResult,
  type PipelineStage,
} from "@nightgauge/sdk";
import type { Logger } from "../utils/logger";
import type { OutputWindow } from "../views";
import { getWorkspaceRoot } from "../config/settings";
import { BinaryResolver } from "../services/BinaryResolver";
import { resolveStageAdapter } from "../utils/resolvers/adapterResolver";
import { getStageModel } from "../utils/resolvers/stageResolver";
import { getExecutionAdapter, type ExecutionAdapter } from "../utils/resolvers/modelResolver";
import { toIncrediAdapter } from "../services/HeadlessOrchestrator";
import { AdapterDoctorPanel } from "../views/doctor/AdapterDoctorPanel";
import type {
  AdapterDoctorReport,
  AdapterReportRow,
  StageResolutionRow,
} from "../views/doctor/AdapterDoctorHtml";

/** Raw per-adapter shape from `doctor --adapters … --json` (snake_case). */
export interface GoAdapterHealth {
  adapter: string;
  kind: string;
  binary?: string;
  installed: boolean;
  path?: string;
  version?: string;
  version_ok: boolean;
  min_version?: string;
  mcp?: { config_path: string; config_present: boolean; managed_block: boolean };
  ok: boolean;
  remediation?: string;
}

interface GoDoctorResult {
  adapters?: GoAdapterHealth[];
}

/**
 * Human-readable display names per resolved SDK adapter. Values MIRROR each SDK
 * adapter class's `displayName` (the canonical source —
 * packages/nightgauge-sdk/src/cli/adapters/*Adapter.ts) so the doctor reads
 * identically to the rest of the SDK surface and introduces no new label variant.
 */
const SDK_ADAPTER_DISPLAY: Record<string, string> = {
  "claude-headless": "Claude Headless",
  "claude-sdk": "Claude SDK",
  codex: "Codex",
  gemini: "Gemini",
  "gemini-sdk": "Gemini SDK",
  ollama: "Ollama",
  "lm-studio": "LM Studio",
  copilot: "GitHub Copilot",
};

function displayName(sdkAdapter: string): string {
  return SDK_ADAPTER_DISPLAY[sdkAdapter] ?? sdkAdapter;
}

/**
 * Local HTTP adapters whose SDK `validateAuth` is a deliberate no-op (they
 * validate model/connectivity at query time, not during auth — a documented
 * adapter contract). Their auth verdict is therefore NOT a readiness signal, so
 * when the Go binary's deterministic checks (model env + claude bridge) are
 * unavailable we cannot claim them ready (#4031 review).
 */
const LOCAL_HTTP_ADAPTERS: ReadonlySet<string> = new Set(["ollama", "lm-studio"]);

/** Per-stage adapter+model resolution, before health status is attached. */
export interface StageRouting {
  stage: PipelineStage;
  adapter: ExecutionAdapter;
  sdkAdapter: IncrediAdapter;
  source: string;
  model: string; // tier, or "(auto / router)" when deferred to the router
}

/**
 * The executable pipeline stages, in canonical order. Membership is derived from
 * `PHASE_REGISTRY` (the single source of truth for which stages actually run a
 * skill — it is keyed by the executable-stage set, excluding the
 * `pipeline-start`/`pipeline-finish` boundary sentinels) and ordered by
 * `PIPELINE_STAGE_ORDER`, rather than re-encoding the sentinel names here.
 */
export const EXECUTABLE_STAGES: readonly PipelineStage[] = PIPELINE_STAGE_ORDER.filter(
  (s) => s in PHASE_REGISTRY
);

/**
 * Resolve adapter + model for every executable pipeline stage using the same
 * precedence the runtime uses. Pure aside from the file/env reads inside the
 * resolvers.
 */
export function resolveStageRouting(
  workspaceRoot: string | undefined,
  env: NodeJS.ProcessEnv = process.env
): StageRouting[] {
  return EXECUTABLE_STAGES.map((stage) => {
    const decision = resolveStageAdapter(stage, workspaceRoot, env);
    const model = getStageModel(stage, workspaceRoot);
    return {
      stage,
      adapter: decision.adapter,
      sdkAdapter: toIncrediAdapter(decision.adapter, env),
      source: decision.source,
      model: model ?? "(auto / router)",
    };
  });
}

/**
 * Distinct SDK adapters to probe: every per-stage adapter plus the global
 * default (a stage may inherit it). Order-stable, deduped.
 */
export function collectDistinctSdkAdapters(
  routing: StageRouting[],
  globalSdkAdapter: IncrediAdapter
): IncrediAdapter[] {
  const seen = new Set<IncrediAdapter>();
  const out: IncrediAdapter[] = [];
  for (const a of [...routing.map((r) => r.sdkAdapter), globalSdkAdapter]) {
    if (!seen.has(a)) {
      seen.add(a);
      out.push(a);
    }
  }
  return out;
}

type SpawnImpl = typeof spawn;

/**
 * Run `doctor --adapters <list> --json` and return its adapter section. Parses
 * stdout even on a non-zero exit, since the doctor may exit 1/2 for unrelated
 * environment warnings while still emitting valid JSON (with the adapter facts).
 */
export function runGoDoctorAdapters(
  binary: string,
  adapters: string[],
  workspaceRoot: string,
  env: NodeJS.ProcessEnv = process.env,
  spawnImpl: SpawnImpl = spawn
): Promise<GoAdapterHealth[]> {
  return new Promise((resolve, reject) => {
    const proc = spawnImpl(binary, ["doctor", "--adapters", adapters.join(","), "--json"], {
      cwd: workspaceRoot,
      shell: false,
      env,
    });
    let stdout = "";
    let stderr = "";
    proc.stdout?.on("data", (d: Buffer) => (stdout += d.toString()));
    proc.stderr?.on("data", (d: Buffer) => (stderr += d.toString()));
    proc.on("error", (e: Error) => reject(new Error(`failed to spawn doctor: ${e.message}`)));
    proc.on("close", () => {
      try {
        const parsed = JSON.parse(stdout.trim()) as GoDoctorResult;
        resolve(parsed.adapters ?? []);
      } catch {
        reject(
          new Error(
            `could not parse doctor --json output${stderr ? `: ${stderr.slice(0, 200)}` : ""}`
          )
        );
      }
    });
  });
}

/**
 * Merge the Go binary/version/MCP facts with the SDK auth verdict into display
 * rows. When `binaryResolved` is false the Go facts are unavailable, so install
 * state is "unknown" and readiness falls back to the auth verdict alone (the SDK
 * auth probe itself surfaces a missing CLI as a BINARY_NOT_FOUND failure).
 */
export function mergeAdapterRows(
  sdkAdapters: IncrediAdapter[],
  goHealth: GoAdapterHealth[],
  auth: AdapterPreflightAggregateResult,
  binaryResolved: boolean
): AdapterReportRow[] {
  const goByName = new Map(goHealth.map((g) => [g.adapter, g]));
  return sdkAdapters.map((a) => {
    const g = goByName.get(a);
    const authResult = auth.results[a];
    const authOk = authResult?.ok ?? false;
    const authReason = authResult && !authResult.ok ? authResult.reason : undefined;

    const remediations: string[] = [];
    if (binaryResolved && g && !g.ok && g.remediation) remediations.push(g.remediation);
    const failure = auth.failures.find((f) => f.adapter === a);
    if (failure) remediations.push(failure.suggestedFix);

    const installed = binaryResolved ? (g?.installed ?? false) : false;
    const versionOk = binaryResolved ? (g?.version_ok ?? true) : true;

    // Readiness:
    //  - Go present → it checked install/version; combine with the auth verdict.
    //  - Go absent + local HTTP adapter → its auth probe is a no-op, so we cannot
    //    confirm the model env / claude bridge; do NOT claim ready.
    //  - Go absent + CLI/SDK adapter → the auth probe IS a real signal (a CLI
    //    adapter can't pass `… auth status` unless installed; SDK adapters check
    //    their API key in validateAuth), so auth alone is a sound readiness proxy.
    let ok: boolean;
    if (binaryResolved) {
      ok = installed && versionOk && authOk;
    } else if (LOCAL_HTTP_ADAPTERS.has(a)) {
      ok = false;
      remediations.push(
        "Cannot verify readiness without the nightgauge binary — install it or set nightgauge.backend.binaryPath."
      );
    } else {
      ok = authOk;
    }

    return {
      sdkAdapter: a,
      displayName: displayName(a),
      kind: g?.kind ?? "unknown",
      binary: g?.binary,
      installed,
      path: g?.path,
      version: g?.version,
      versionOk,
      minVersion: g?.min_version,
      mcp: g?.mcp
        ? {
            configPath: g.mcp.config_path,
            configPresent: g.mcp.config_present,
            managedBlock: g.mcp.managed_block,
          }
        : undefined,
      authOk,
      authReason,
      remediations: dedupe(remediations),
      ok,
    };
  });
}

function dedupe(items: string[]): string[] {
  return Array.from(new Set(items.filter(Boolean)));
}

/** Attach health status + concrete Codex model to each stage routing row. */
export function finalizeStageRows(
  routing: StageRouting[],
  rows: AdapterReportRow[],
  binaryResolved: boolean
): StageResolutionRow[] {
  const rowByAdapter = new Map(rows.map((r) => [r.sdkAdapter, r]));
  return routing.map((r) => {
    const row = rowByAdapter.get(r.sdkAdapter);
    let status: StageResolutionRow["status"];
    if (!row) status = "unknown";
    else if (row.ok) status = "ok";
    else if (!binaryResolved && !row.authOk) status = "error";
    else status = row.authOk ? "warn" : "error";

    const codexModel = r.sdkAdapter === "codex" ? resolveCodexModelAlias(r.model) : undefined;

    return {
      stage: r.stage,
      adapter: r.adapter,
      sdkAdapter: r.sdkAdapter,
      source: r.source,
      model: r.model,
      codexModel,
      status,
    };
  });
}

/** Injectable dependencies so the orchestrator can be unit-tested. */
export interface BuildReportDeps {
  workspaceRoot: string | undefined;
  env?: NodeJS.ProcessEnv;
  resolveBinary: () => Promise<string | null>;
  runGoAdapters: (binary: string, adapters: string[], cwd: string) => Promise<GoAdapterHealth[]>;
  runAuth: (adapters: IncrediAdapter[]) => Promise<AdapterPreflightAggregateResult>;
  globalAdapter: () => ExecutionAdapter;
  now: () => string;
  /** Override per-stage routing resolution (defaults to the live resolvers). */
  resolveRouting?: (workspaceRoot: string | undefined, env: NodeJS.ProcessEnv) => StageRouting[];
}

/**
 * Orchestrate the full report: resolve per-stage routing, probe the distinct
 * adapters via Go (binary/version/MCP) + SDK (auth), and merge.
 */
export async function buildAdapterDoctorReport(
  deps: BuildReportDeps
): Promise<AdapterDoctorReport> {
  const env = deps.env ?? process.env;
  const routing = (deps.resolveRouting ?? resolveStageRouting)(deps.workspaceRoot, env);
  const globalSdkAdapter = toIncrediAdapter(deps.globalAdapter(), env);
  const sdkAdapters = collectDistinctSdkAdapters(routing, globalSdkAdapter);

  const notes: string[] = [];
  const cwd = deps.workspaceRoot ?? process.cwd();

  // Auth (SDK, in-process) and binary/version/MCP (Go) probed concurrently.
  const authPromise = deps.runAuth(sdkAdapters).catch((err): AdapterPreflightAggregateResult => {
    notes.push(`Auth probe failed: ${err instanceof Error ? err.message : String(err)}`);
    return { ok: false, results: {}, failures: [] };
  });

  const binary = await deps.resolveBinary();
  let goHealth: GoAdapterHealth[] = [];
  let binaryResolved = false;
  if (!binary) {
    notes.push(
      "nightgauge Go binary not found — binary/version/MCP facts are unavailable. Install it or set nightgauge.backend.binaryPath."
    );
  } else {
    try {
      goHealth = await deps.runGoAdapters(binary, sdkAdapters, cwd);
      binaryResolved = true;
    } catch (err) {
      notes.push(
        `Adapter binary/version/MCP probe failed: ${err instanceof Error ? err.message : String(err)}`
      );
    }
  }

  const auth = await authPromise;
  const rows = mergeAdapterRows(sdkAdapters, goHealth, auth, binaryResolved);
  const stages = finalizeStageRows(routing, rows, binaryResolved);

  return { rows, stages, generatedAt: deps.now(), binaryResolved, notes };
}

/**
 * Resolve a GITHUB_TOKEN best-effort so the Go doctor's env checks don't error.
 * Async + `execFile` (no shell, arg array) so it never blocks the extension host
 * event loop — the lint rule bans the synchronous `execSync` form here (#2884).
 */
async function withGitHubToken(env: NodeJS.ProcessEnv): Promise<NodeJS.ProcessEnv> {
  if (env.GITHUB_TOKEN) return env;
  try {
    const { execFile } = await import("child_process");
    const { promisify } = await import("util");
    const execFileAsync = promisify(execFile);
    const { stdout } = await execFileAsync("gh", ["auth", "token"], { timeout: 5_000 });
    const token = stdout.trim();
    return token ? { ...env, GITHUB_TOKEN: token } : env;
  } catch {
    return env;
  }
}

/**
 * Build the production dependency bag (live resolvers, Go binary, SDK auth).
 */
function liveDeps(workspaceRoot: string | undefined): BuildReportDeps {
  return {
    workspaceRoot,
    env: process.env,
    resolveBinary: () => BinaryResolver.fromVSCode().resolve(),
    runGoAdapters: async (binary, adapters, cwd) =>
      runGoDoctorAdapters(binary, adapters, cwd, await withGitHubToken(process.env)),
    // Inject a real subprocess runner so CLI adapters (codex/claude/copilot)
    // actually probe auth (`codex login status`, …) instead of short-circuiting
    // to "passed" — the no-runner default would make the auth column meaningless
    // (#4031 review). validateAdapterAuth bounds each probe with its own timeout.
    // bypassCache: the Doctor's "Re-run checks" must reflect LIVE CLI state, so
    // it forces a fresh probe instead of reading the short-TTL cache the
    // pipeline auth gate shares (#312). The fresh result still refreshes that
    // shared cache for later pipeline runs.
    runAuth: (adapters) =>
      runAdapterAuthPreflight(adapters, {
        runner: createDefaultPreflightRunner(),
        cwd: workspaceRoot ?? process.cwd(),
        bypassCache: true,
      }),
    globalAdapter: () => getExecutionAdapter(workspaceRoot),
    now: () => new Date().toLocaleString(),
  };
}

/**
 * Register the `nightgauge.adapterDoctor` command. Opens the Adapter Doctor
 * webview with a freshly computed report; the panel's "Re-run checks" button
 * recomputes via the same builder.
 */
export function registerAdapterDoctorCommand(
  _context: vscode.ExtensionContext,
  logger: Logger,
  outputWindow?: OutputWindow
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.adapterDoctor", async () => {
    const workspaceRoot = getWorkspaceRoot() ?? undefined;
    const build = () => buildAdapterDoctorReport(liveDeps(workspaceRoot));

    await vscode.window.withProgress(
      {
        location: vscode.ProgressLocation.Notification,
        title: "Adapter Doctor: checking adapters…",
      },
      async () => {
        try {
          const report = await build();
          logger.info("Adapter Doctor report generated", {
            adapters: report.rows.length,
            ready: report.rows.filter((r) => r.ok).length,
            binaryResolved: report.binaryResolved,
          });
          AdapterDoctorPanel.show(report, build);
        } catch (err) {
          const message = err instanceof Error ? err.message : String(err);
          logger.error("Adapter Doctor failed", { error: message });
          outputWindow?.appendLine(`Adapter Doctor failed: ${message}`, "error");
          void vscode.window.showErrorMessage(`Adapter Doctor failed: ${message}`);
        }
      }
    );
  });
}
