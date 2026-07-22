/**
 * CodexMcpProvisioner — makes the pipeline's MCP servers reachable from Codex
 * stages by translating them into `[mcp_servers.<name>]` tables inside a managed
 * block in Codex's global config (`$CODEX_HOME/config.toml`, default
 * `~/.codex/config.toml`).
 *
 * Why the global config (not a project `.codex/config.toml`):
 *  - Auth-safe: Codex auth lives in a SEPARATE file (`auth.json`); writing
 *    `config.toml` never touches it.
 *  - No trust gate: a project-scoped `.codex/config.toml` is honored only for
 *    "trusted projects"; the global config is always read.
 *  - Commit-safe: it lives outside any repo worktree, so the pipeline's pr-create
 *    stage can never accidentally stage/commit it.
 *
 * Unlike AGENTS.md (a per-repo, possibly-committed file that is stripped on
 * cleanup), the managed MCP block is intentionally PERSISTED: it is idempotent
 * and deterministic from `.mcp.json`, so re-running produces identical bytes and
 * there is nothing to clean up — mirroring how `codex mcp add` persists servers.
 *
 * User-defined `[mcp_servers.*]` tables outside the managed block are always
 * preserved; on a name clash the user's definition wins and ours is skipped.
 *
 * @see Issue #4025 - Codex MCP provisioning via ~/.codex/config.toml
 */

import * as fs from "node:fs";
import * as fsPromises from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import {
  readPipelineMcpServers,
  computeNextCodexConfig,
  type PipelineMcpServer,
} from "./codexMcpConfig.js";

/** Options for provisioning Codex MCP config. */
export interface CodexMcpOptions {
  /** Workspace root holding `.mcp.json` / `.claude/settings.json`. */
  workspaceRoot: string;
  /** Adapter name (provisioning is a no-op unless this is `"codex"`). */
  adapter: string;
  /**
   * Override the Codex home directory (where `config.toml` lives). Defaults to
   * `$CODEX_HOME`, then `~/.codex`. Primarily for tests.
   */
  codexHome?: string;
  /**
   * Pre-resolved servers (primarily for tests). When omitted, servers are read
   * from the workspace's `.mcp.json` / `.claude/settings.json`.
   */
  servers?: Record<string, PipelineMcpServer>;
}

/** Outcome of a provisioning run, for logging/telemetry. */
export interface CodexMcpResult {
  /** Absolute path to the config file written (or that would be written). */
  configPath: string;
  /** Server names written into the managed block this run. */
  provisioned: string[];
  /** Server names skipped because the user already defined them outside the block. */
  skippedCollisions: string[];
  /** Whether the file content actually changed (false ⇒ already up to date). */
  changed: boolean;
}

/** Config gate, read from `.nightgauge/config.yaml` `pipeline.codex_mcp`. */
export interface CodexMcpConfig {
  /** Whether MCP provisioning for Codex is enabled (default: true). */
  enabled?: boolean;
}

/**
 * Resolve Codex's home directory: `$CODEX_HOME` if set, else `~/.codex`.
 * Exported so callers/tests share the exact resolution Codex itself uses.
 */
export function resolveCodexHome(override?: string): string {
  if (override && override.length > 0) return override;
  const fromEnv = process.env.CODEX_HOME;
  if (fromEnv && fromEnv.length > 0) return fromEnv;
  return path.join(os.homedir(), ".codex");
}

export class CodexMcpProvisioner {
  private isCodexAdapter(adapter: string): boolean {
    return adapter === "codex";
  }

  /**
   * Whether provisioning should run for this call. Skips when the adapter is not
   * Codex, when programmatically disabled (`config.enabled === false`), or when
   * the operator set `NIGHTGAUGE_CODEX_MCP_DISABLED=true` — the latter is the
   * user-facing opt-out enforced at the spawn call sites (which do not plumb
   * config.yaml), mirroring the `NIGHTGAUGE_CODEX_RESUME_ENABLED` pattern.
   */
  private isEnabled(adapter: string, config?: CodexMcpConfig): boolean {
    if (!this.isCodexAdapter(adapter) || config?.enabled === false) return false;
    if (process.env.NIGHTGAUGE_CODEX_MCP_DISABLED === "true") return false;
    return true;
  }

  private resolve(options: CodexMcpOptions): {
    configPath: string;
    servers: Record<string, PipelineMcpServer>;
  } {
    const codexHome = resolveCodexHome(options.codexHome);
    const servers = options.servers ?? readPipelineMcpServers(options.workspaceRoot);
    return { configPath: path.join(codexHome, "config.toml"), servers };
  }

  private readExisting(configPath: string): string | null {
    try {
      return fs.readFileSync(configPath, "utf-8");
    } catch {
      return null;
    }
  }

  /**
   * Provision the managed MCP block into Codex's config (async).
   * @returns the result, or null when skipped (non-Codex adapter / disabled).
   */
  async provision(
    options: CodexMcpOptions,
    config?: CodexMcpConfig
  ): Promise<CodexMcpResult | null> {
    if (!this.isEnabled(options.adapter, config)) {
      return null;
    }
    const { configPath, servers } = this.resolve(options);
    const existing = this.readExisting(configPath);
    const { next, provisioned, skippedCollisions } = computeNextCodexConfig(existing, servers);

    const changed = next !== (existing ?? "");
    if (changed) {
      await fsPromises.mkdir(path.dirname(configPath), { recursive: true });
      await fsPromises.writeFile(configPath, next, "utf-8");
    }
    return { configPath, provisioned, skippedCollisions, changed };
  }

  /**
   * Synchronous variant for the skillRunner spawn path (which is synchronous).
   * Same semantics as {@link provision}.
   */
  provisionSync(options: CodexMcpOptions, config?: CodexMcpConfig): CodexMcpResult | null {
    if (!this.isEnabled(options.adapter, config)) {
      return null;
    }
    const { configPath, servers } = this.resolve(options);
    const existing = this.readExisting(configPath);
    const { next, provisioned, skippedCollisions } = computeNextCodexConfig(existing, servers);

    const changed = next !== (existing ?? "");
    if (changed) {
      fs.mkdirSync(path.dirname(configPath), { recursive: true });
      fs.writeFileSync(configPath, next, "utf-8");
    }
    return { configPath, provisioned, skippedCollisions, changed };
  }
}
