/**
 * Canonical adapter-config reader for the SDK CLI (#54).
 *
 * Reads the same `.nightgauge/config.yaml` keys the VSCode resolver and the
 * Go binary parse — `pipeline.stage_adapters.<stage>`, `ui.core.adapter`,
 * and `pipeline.adapter_fallback_chain` — so all three layers share ONE
 * schema. A sibling `config.local.yaml` overlays the project file per key
 * (matching the VSCode reader's local-over-project precedence). Reads are
 * best-effort: a missing or unparseable file yields an empty config, never
 * an error — adapter resolution then falls through to the env/auto-select
 * rungs.
 */

import { readFileSync } from "node:fs";
import { join } from "node:path";
import * as yaml from "yaml";

/** The adapter-relevant subset of .nightgauge/config.yaml. */
export interface AdapterFileConfig {
  /** pipeline.stage_adapters.<stage> — per-stage adapter names. */
  stageAdapters: Record<string, string>;
  /** ui.core.adapter — the global execution-adapter default. */
  globalAdapter?: string;
  /** pipeline.adapter_fallback_chain — parsed for schema completeness. */
  fallbackChain?: string[];
}

function readTier(path: string): Partial<AdapterFileConfig> {
  let parsed: unknown;
  try {
    parsed = yaml.parse(readFileSync(path, "utf-8"));
  } catch {
    return {};
  }
  if (!parsed || typeof parsed !== "object") return {};
  const root = parsed as Record<string, unknown>;

  const out: Partial<AdapterFileConfig> = {};
  const pipeline = root.pipeline;
  if (pipeline && typeof pipeline === "object") {
    const p = pipeline as Record<string, unknown>;
    if (p.stage_adapters && typeof p.stage_adapters === "object") {
      const entries = Object.entries(p.stage_adapters as Record<string, unknown>).filter(
        ([, v]) => typeof v === "string" && (v as string).trim() !== ""
      ) as Array<[string, string]>;
      if (entries.length > 0) out.stageAdapters = Object.fromEntries(entries);
    }
    if (Array.isArray(p.adapter_fallback_chain)) {
      const chain = p.adapter_fallback_chain.filter((v): v is string => typeof v === "string");
      if (chain.length > 0) out.fallbackChain = chain;
    }
  }
  const ui = root.ui;
  if (ui && typeof ui === "object") {
    const core = (ui as Record<string, unknown>).core;
    if (core && typeof core === "object") {
      const adapter = (core as Record<string, unknown>).adapter;
      if (typeof adapter === "string" && adapter.trim() !== "") {
        out.globalAdapter = adapter.trim();
      }
    }
  }
  return out;
}

/**
 * Read the canonical adapter config from `<cwd>/.nightgauge/config.yaml`,
 * overlaid per key by `config.local.yaml` when present.
 */
export function readAdapterFileConfig(cwd: string): AdapterFileConfig {
  const dir = join(cwd, ".nightgauge");
  const project = readTier(join(dir, "config.yaml"));
  const local = readTier(join(dir, "config.local.yaml"));
  return {
    stageAdapters: { ...(project.stageAdapters ?? {}), ...(local.stageAdapters ?? {}) },
    globalAdapter: local.globalAdapter ?? project.globalAdapter,
    fallbackChain: local.fallbackChain ?? project.fallbackChain,
  };
}
