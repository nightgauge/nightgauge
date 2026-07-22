/**
 * adapterResolver.test.ts
 *
 * Tests for `resolveStageAdapter` and `getGlobalAdapterWithSource` (Issue #3221).
 *
 * Precedence chain:
 *   env → stage-config → global-config → default
 * `auto-router` is reserved (Epic C / C4) and never returned at runtime today.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import {
  resolveStageAdapter,
  getGlobalAdapterWithSource,
  readAdapterFallbackChainFromYaml,
  readStageAdapterFallbackFromYaml,
  readDisableFallbackFromYaml,
  getEffectiveFallbackChain,
  tryAdapterFallback,
  walkAdapterFallback,
  DEFAULT_ADAPTER_FALLBACK_CHAIN,
  type AdapterSource,
  type AdapterDecision,
} from "../../../src/utils/resolvers/adapterResolver";
import type { ExecutionAdapter } from "../../../src/utils/resolvers/modelResolver";

const ADAPTER_ENV_KEYS = [
  "NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV",
  "NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_PLANNING",
  "NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_PR_CREATE",
  "NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_PR_MERGE",
  "NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_VALIDATE",
  "NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_ISSUE_PICKUP",
  "NIGHTGAUGE_UI_CORE_ADAPTER",
];

function clearAdapterEnv(): void {
  for (const key of ADAPTER_ENV_KEYS) {
    delete process.env[key];
  }
}

describe("resolveStageAdapter — env precedence (Issue #3221)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "adapter-resolver-env-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    clearAdapterEnv();
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns env adapter with source 'env' when env var is set", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV = "codex";
    const decision = resolveStageAdapter("feature-dev", tmpRoot);
    expect(decision).toEqual({ adapter: "codex", source: "env" });
  });

  it("converts kebab-case stage to UPPERCASE_UNDERSCORE env key", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_PR_CREATE = "gemini";
    const decision = resolveStageAdapter("pr-create", tmpRoot);
    expect(decision).toEqual({ adapter: "gemini", source: "env" });
  });

  it("ignores invalid env values and falls through", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV = "xyz";
    // No config file, no global override → default
    const decision = resolveStageAdapter("feature-dev", tmpRoot);
    expect(decision).toEqual({ adapter: "claude", source: "default" });
  });

  it("supports the optional `env` argument override", () => {
    const customEnv = { NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV: "copilot" };
    const decision = resolveStageAdapter("feature-dev", tmpRoot, customEnv);
    expect(decision).toEqual({ adapter: "copilot", source: "env" });
  });
});

describe("resolveStageAdapter — stage-config precedence (Issue #3221)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "adapter-resolver-stage-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    clearAdapterEnv();
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns adapter from pipeline.stage_adapters.<stage> with source 'stage-config'", () => {
    const yaml = `pipeline:
  stage_adapters:
    feature-dev: gemini
    feature-planning: codex
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "gemini",
      source: "stage-config",
    });
    expect(resolveStageAdapter("feature-planning", tmpRoot)).toEqual({
      adapter: "codex",
      source: "stage-config",
    });
  });

  it("falls through when stage missing from stage_adapters block", () => {
    const yaml = `pipeline:
  stage_adapters:
    feature-dev: gemini
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    // pr-create not configured → no global → default
    expect(resolveStageAdapter("pr-create", tmpRoot)).toEqual({
      adapter: "claude",
      source: "default",
    });
  });

  it("ignores neighboring sections like stage_models", () => {
    const yaml = `pipeline:
  stage_models:
    feature-dev: opus
  stage_cost_caps:
    feature-dev: 10
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "claude",
      source: "default",
    });
  });

  it("resolves 'ollama' from stage_adapters (now in VALID_ADAPTERS, #4030)", () => {
    // #4030 derived VALID_ADAPTERS from AdapterEnumSchema (which includes
    // ollama), closing the drift where a selected ollama silently fell back to
    // claude. ollama is now honored as a real stage-config override.
    const yaml = `pipeline:
  stage_adapters:
    feature-dev: ollama
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "ollama",
      source: "stage-config",
    });
  });
});

describe("resolveStageAdapter — global-config precedence (Issue #3221)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "adapter-resolver-global-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    clearAdapterEnv();
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns global ui.core.adapter with source 'global-config' when stage_adapters miss", () => {
    const yaml = `ui:
  core:
    adapter: copilot
pipeline:
  stage_adapters:
    pr-create: gemini
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    // pr-create is configured per-stage
    expect(resolveStageAdapter("pr-create", tmpRoot)).toEqual({
      adapter: "gemini",
      source: "stage-config",
    });
    // feature-dev falls through to global
    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "copilot",
      source: "global-config",
    });
  });

  it("uses NIGHTGAUGE_UI_CORE_ADAPTER as global when set", () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "gemini-sdk";
    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "gemini-sdk",
      source: "global-config",
    });
  });

  it("env-set global beats config-file global", () => {
    const yaml = `ui:
  core:
    adapter: copilot
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";

    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "codex",
      source: "global-config",
    });
  });

  it("config.local.yaml beats config.yaml for global", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `ui:
  core:
    adapter: copilot
`
    );
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.local.yaml"),
      `ui:
  core:
    adapter: gemini
`
    );

    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "gemini",
      source: "global-config",
    });
  });
});

describe("resolveStageAdapter — default fallthrough (Issue #3221)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "adapter-resolver-default-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    clearAdapterEnv();
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns claude/default when no env, no config, no global", () => {
    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "claude",
      source: "default",
    });
  });

  it("returns default when config file exists but has no adapter info", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  ci_timeout: 600
`
    );
    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "claude",
      source: "default",
    });
  });

  it("returns default when no workspace root can be resolved", () => {
    // No tmpRoot, no vscode workspaceFolders (mocked as undefined).
    expect(resolveStageAdapter("feature-dev")).toEqual({
      adapter: "claude",
      source: "default",
    });
  });
});

describe("resolveStageAdapter — full precedence ordering (Issue #3221)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "adapter-resolver-prec-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    clearAdapterEnv();
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("env beats stage-config beats global-config beats default", () => {
    const yaml = `ui:
  core:
    adapter: copilot
pipeline:
  stage_adapters:
    feature-dev: gemini
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    // 1. env wins outright
    process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV = "codex";
    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "codex",
      source: "env",
    });

    // 2. drop env → stage-config wins
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV;
    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "gemini",
      source: "stage-config",
    });

    // 3. unconfigured stage falls to global-config
    expect(resolveStageAdapter("pr-merge", tmpRoot)).toEqual({
      adapter: "copilot",
      source: "global-config",
    });
  });
});

describe("AdapterSource enum surface (Issue #3221, #3223)", () => {
  it("includes auto-router and fallback as reserved values (type-level reachability)", () => {
    // Type-level assertion: AdapterSource union must include all expected
    // members. Drift here (e.g. accidentally removing 'auto-router' or
    // 'fallback') is a breaking change for run-history schemas that already
    // serialize them.
    const sources = [
      "env",
      "stage-config",
      "global-config",
      "auto-router",
      "fallback",
      "default",
    ] as const satisfies readonly AdapterSource[];
    expect(sources).toHaveLength(6);
  });

  it("AdapterDecision shape is { adapter, source }", () => {
    const decision: AdapterDecision = { adapter: "claude", source: "default" };
    expect(decision.adapter).toBe("claude");
    expect(decision.source).toBe("default");
  });
});

describe("Integration — mixed-adapter pipeline (Issue #3221)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "adapter-resolver-int-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    clearAdapterEnv();
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("resolves a realistic mix where some stages override and others fall through", () => {
    // Mirrors Epic #3212's acceptance criterion: per-stage adapters mixed
    // with a global default produce the expected attribution.
    const yaml = `ui:
  core:
    adapter: claude
pipeline:
  stage_adapters:
    feature-planning: gemini
    feature-dev: codex
    pr-create: claude
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    expect(resolveStageAdapter("feature-planning", tmpRoot)).toEqual({
      adapter: "gemini",
      source: "stage-config",
    });
    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "codex",
      source: "stage-config",
    });
    expect(resolveStageAdapter("pr-create", tmpRoot)).toEqual({
      adapter: "claude",
      source: "stage-config",
    });
    // Unconfigured stages fall through to the explicit global claude
    expect(resolveStageAdapter("pr-merge", tmpRoot)).toEqual({
      adapter: "claude",
      source: "global-config",
    });
    expect(resolveStageAdapter("feature-validate", tmpRoot)).toEqual({
      adapter: "claude",
      source: "global-config",
    });
  });
});

describe("getGlobalAdapterWithSource (Issue #3221)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "adapter-resolver-global-helper-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    clearAdapterEnv();
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns configured: false when nothing is set (the case getExecutionAdapter cannot distinguish)", () => {
    // This is the bug `getExecutionAdapter` cannot tell apart from explicit
    // `ui.core.adapter: claude` — the source-aware helper must return
    // `configured: false` here so resolveStageAdapter can attribute "default".
    expect(getGlobalAdapterWithSource(tmpRoot)).toEqual({
      adapter: "claude",
      configured: false,
    });
  });

  it("returns configured: true when ui.core.adapter is explicitly set to claude", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `ui:
  core:
    adapter: claude
`
    );
    expect(getGlobalAdapterWithSource(tmpRoot)).toEqual({
      adapter: "claude",
      configured: true,
    });
  });

  it("returns configured: true when env override is set", () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "gemini";
    expect(getGlobalAdapterWithSource(tmpRoot)).toEqual({
      adapter: "gemini",
      configured: true,
    });
  });

  it("ignores invalid env values", () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "bogus";
    expect(getGlobalAdapterWithSource(tmpRoot)).toEqual({
      adapter: "claude",
      configured: false,
    });
  });
});

describe("readAdapterFallbackChainFromYaml (Issue #3223)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "adapter-fallback-yaml-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns [] when config file does not exist", () => {
    expect(readAdapterFallbackChainFromYaml(tmpRoot)).toEqual([]);
  });

  it("returns [] when adapter_fallback_chain section is absent", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  stage_adapters:
    feature-dev: gemini
`
    );
    expect(readAdapterFallbackChainFromYaml(tmpRoot)).toEqual([]);
  });

  it("returns [] when adapter_fallback_chain is empty", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
`
    );
    expect(readAdapterFallbackChainFromYaml(tmpRoot)).toEqual([]);
  });

  it("parses a list of valid adapters in order", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
    - codex
    - gemini
    - copilot
`
    );
    expect(readAdapterFallbackChainFromYaml(tmpRoot)).toEqual(["codex", "gemini", "copilot"]);
  });

  it("filters out invalid adapter names", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
    - codex
    - bogus-adapter
    - gemini
`
    );
    expect(readAdapterFallbackChainFromYaml(tmpRoot)).toEqual(["codex", "gemini"]);
  });
});

describe("tryAdapterFallback (Issue #3223)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "adapter-fallback-helper-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns null when chain is missing (default-config no-op — AC #6)", () => {
    const validate = (_a: ExecutionAdapter): string | null => null;
    expect(tryAdapterFallback("claude", validate, tmpRoot)).toBeNull();
  });

  it("returns null when chain is empty", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
`
    );
    const validate = (_a: ExecutionAdapter): string | null => null;
    expect(tryAdapterFallback("claude", validate, tmpRoot)).toBeNull();
  });

  it("returns first candidate that passes validation with source=fallback", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
    - codex
    - gemini
    - copilot
`
    );
    const validate = (a: ExecutionAdapter): string | null =>
      a === "codex" ? "codex unavailable" : null;
    const result = tryAdapterFallback("claude", validate, tmpRoot);
    expect(result).toEqual({ adapter: "gemini", source: "fallback" });
  });

  it("skips the failed adapter even when listed in the chain", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
    - claude
    - gemini
`
    );
    const validate = (_a: ExecutionAdapter): string | null => null;
    // Even though claude validates, it's the failed adapter so it's skipped.
    const result = tryAdapterFallback("claude", validate, tmpRoot);
    expect(result).toEqual({ adapter: "gemini", source: "fallback" });
  });

  it("returns null when every candidate fails validation", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
    - codex
    - gemini
`
    );
    const validate = (_a: ExecutionAdapter): string | null => "all fail";
    expect(tryAdapterFallback("claude", validate, tmpRoot)).toBeNull();
  });
});

// ============================================================================
// Issue #3231 — auth-aware fallback chain (per-stage, default, opt-out)
// ============================================================================

describe("DEFAULT_ADAPTER_FALLBACK_CHAIN (Issue #3231 / AC #1, #57)", () => {
  it("matches the AC-specified order, minus chat-only adapters (#57)", () => {
    // lm-studio was removed: the agentic gate rejects chat-completion-only
    // adapters for pipeline dispatch, so the rung would always be dead.
    expect(DEFAULT_ADAPTER_FALLBACK_CHAIN).toEqual(["claude", "codex", "gemini", "copilot"]);
  });
});

describe("readStageAdapterFallbackFromYaml (Issue #3231)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "stage-fallback-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns [] when stage_adapter_fallback section is absent", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:\n  adapter_fallback_chain:\n    - codex\n`
    );
    expect(readStageAdapterFallbackFromYaml("feature-dev", tmpRoot)).toEqual([]);
  });

  it("returns [] when the stage key is absent from stage_adapter_fallback", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  stage_adapter_fallback:
    feature-planning:
      - codex
      - gemini
`
    );
    expect(readStageAdapterFallbackFromYaml("feature-dev", tmpRoot)).toEqual([]);
  });

  it("parses the per-stage list in order", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  stage_adapter_fallback:
    feature-dev:
      - codex
      - gemini
      - copilot
    feature-planning:
      - lm-studio
`
    );
    expect(readStageAdapterFallbackFromYaml("feature-dev", tmpRoot)).toEqual([
      "codex",
      "gemini",
      "copilot",
    ]);
    expect(readStageAdapterFallbackFromYaml("feature-planning", tmpRoot)).toEqual(["lm-studio"]);
  });

  it("filters invalid adapter names", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  stage_adapter_fallback:
    feature-dev:
      - codex
      - bogus
      - gemini
`
    );
    expect(readStageAdapterFallbackFromYaml("feature-dev", tmpRoot)).toEqual(["codex", "gemini"]);
  });
});

describe("readDisableFallbackFromYaml (Issue #3231 / AC #7)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "disable-fallback-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns false when key is absent", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:\n  adapter_fallback_chain:\n    - codex\n`
    );
    expect(readDisableFallbackFromYaml(tmpRoot)).toBe(false);
  });

  it("returns true when explicitly set", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:\n  disable_fallback: true\n`
    );
    expect(readDisableFallbackFromYaml(tmpRoot)).toBe(true);
  });

  it("returns false when explicitly false", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:\n  disable_fallback: false\n`
    );
    expect(readDisableFallbackFromYaml(tmpRoot)).toBe(false);
  });
});

describe("getEffectiveFallbackChain (Issue #3231 / AC #1, #2, #7)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "effective-chain-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns the built-in default when no config is present (AC #1)", () => {
    expect(getEffectiveFallbackChain("feature-dev", tmpRoot)).toEqual(
      DEFAULT_ADAPTER_FALLBACK_CHAIN
    );
  });

  it("returns the global chain when set, overriding the default", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:\n  adapter_fallback_chain:\n    - codex\n    - gemini\n`
    );
    expect(getEffectiveFallbackChain("feature-dev", tmpRoot)).toEqual(["codex", "gemini"]);
  });

  it("per-stage override beats global (AC #2)", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
    - codex
    - gemini
  stage_adapter_fallback:
    feature-dev:
      - copilot
      - lm-studio
`
    );
    expect(getEffectiveFallbackChain("feature-dev", tmpRoot)).toEqual(["copilot", "lm-studio"]);
    // Stages without an override fall through to the global chain.
    expect(getEffectiveFallbackChain("feature-planning", tmpRoot)).toEqual(["codex", "gemini"]);
  });

  it("disable_fallback: true returns [] regardless of chain config (AC #7)", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  disable_fallback: true
  adapter_fallback_chain:
    - codex
  stage_adapter_fallback:
    feature-dev:
      - gemini
`
    );
    expect(getEffectiveFallbackChain("feature-dev", tmpRoot)).toEqual([]);
  });

  it("explicit empty global chain disables the default (operator opt-out lite)", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:\n  adapter_fallback_chain: []\n`
    );
    expect(getEffectiveFallbackChain("feature-dev", tmpRoot)).toEqual([]);
  });
});

describe("walkAdapterFallback (Issue #3231 / AC #3, #5)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "walk-fallback-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns null winner with hopsAttempted=[primary] when chain is empty", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:\n  adapter_fallback_chain: []\n`
    );
    const validate = (_a: ExecutionAdapter): string | null => "always fails";
    const result = walkAdapterFallback("claude", "claude broken", validate, tmpRoot, "feature-dev");
    expect(result.winner).toBeNull();
    expect(result.hopsAttempted).toEqual(["claude"]);
    expect(result.lastError).toBe("claude broken");
  });

  it("records every hop until a candidate validates (primary fail + 2nd-hop success)", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
    - codex
    - gemini
    - copilot
`
    );
    const validate = (a: ExecutionAdapter): string | null =>
      a === "codex" ? "codex unavailable" : null;
    const result = walkAdapterFallback("claude", "claude broken", validate, tmpRoot, "feature-dev");
    expect(result.winner).toEqual({ adapter: "gemini", source: "fallback" });
    // Primary + codex (failed) + gemini (won) — all in order.
    expect(result.hopsAttempted).toEqual(["claude", "codex", "gemini"]);
  });

  it("returns null winner and full hop list when every candidate fails", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
    - codex
    - gemini
`
    );
    const validate = (a: ExecutionAdapter): string | null => `${a} broken`;
    const result = walkAdapterFallback("claude", "claude broken", validate, tmpRoot, "feature-dev");
    expect(result.winner).toBeNull();
    expect(result.hopsAttempted).toEqual(["claude", "codex", "gemini"]);
    expect(result.lastError).toBe("gemini broken");
  });

  it("walks the built-in default chain when no config is present (AC #1)", () => {
    const validate = (a: ExecutionAdapter): string | null =>
      a === "gemini" ? null : `${a} broken`;
    const result = walkAdapterFallback("claude", "claude broken", validate, tmpRoot, "feature-dev");
    // Default chain skips the failed primary (claude), so codex is next, then gemini.
    expect(result.winner).toEqual({ adapter: "gemini", source: "fallback" });
    expect(result.hopsAttempted).toEqual(["claude", "codex", "gemini"]);
  });

  it("uses the per-stage override when set (AC #2)", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  adapter_fallback_chain:
    - codex
  stage_adapter_fallback:
    feature-dev:
      - lm-studio
`
    );
    const validate = (_a: ExecutionAdapter): string | null => null;
    const result = walkAdapterFallback("claude", "claude broken", validate, tmpRoot, "feature-dev");
    // Stage override wins — lm-studio, not codex.
    expect(result.winner).toEqual({ adapter: "lm-studio", source: "fallback" });
    expect(result.hopsAttempted).toEqual(["claude", "lm-studio"]);
  });

  it("disable_fallback: true short-circuits the walker (AC #7)", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  disable_fallback: true
  adapter_fallback_chain:
    - codex
    - gemini
`
    );
    const validate = (_a: ExecutionAdapter): string | null => null;
    const result = walkAdapterFallback("claude", "claude broken", validate, tmpRoot, "feature-dev");
    expect(result.winner).toBeNull();
    expect(result.hopsAttempted).toEqual(["claude"]);
  });

  it("primary success: caller never invokes the walker — but if invoked, returns trivial hop list", () => {
    // The dispatcher only walks when prereq fails. If a caller invokes the
    // walker after primary success (no chain), we still record the primary
    // as element 0 and return null winner with the empty default chain
    // skipped (claude is the failed primary in default chain).
    const validate = (a: ExecutionAdapter): string | null =>
      a === "claude" ? null : `${a} broken`;
    const result = walkAdapterFallback("claude", "synthetic", validate, tmpRoot, "feature-dev");
    // Default chain: ["claude", "codex", "gemini", "copilot", "lm-studio"]
    // Walker skips the failed primary (claude) and tries the rest — every
    // one fails by validate(). Result: null winner, every non-claude
    // adapter recorded.
    expect(result.winner).toBeNull();
    expect(result.hopsAttempted[0]).toBe("claude");
    expect(result.hopsAttempted).toContain("codex");
  });
});
