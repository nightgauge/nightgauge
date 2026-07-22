/**
 * adapterResolver.autoRouter.test.ts
 *
 * Tests for Step 2.5 — AutoProviderRouter integration in `resolveStageAdapter`
 * (Issue #3230).
 *
 * Asserts:
 * - Env override beats router (precedence preserved)
 * - `pipeline.stage_adapters.<stage>` config beats router (precedence preserved)
 * - Router is invoked with the right context when neither override applies
 * - When router abstains, fall-through to global / default works
 * - When `pipeline.auto_router.enabled: false`, router is never invoked
 * - Returned decision carries `source: "auto-router"` and `rationale` populated
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

import { AutoProviderRouter, type AutoRouterDecision } from "@nightgauge/sdk";

import {
  resolveStageAdapter,
  type AutoRouterOptions,
  _resetAutoRouterForTests,
} from "../../../src/utils/resolvers/adapterResolver";

const ENV_KEYS = [
  "NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV",
  "NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_PLANNING",
  "NIGHTGAUGE_UI_CORE_ADAPTER",
];

function clearEnv(): void {
  for (const key of ENV_KEYS) {
    delete process.env[key];
  }
}

function makeStubRouter(decision: AutoRouterDecision | null): AutoProviderRouter {
  return {
    selectForStage: vi.fn(() => decision),
  } as unknown as AutoProviderRouter;
}

describe("resolveStageAdapter Step 2.5 (auto-router) — Issue #3230", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearEnv();
    _resetAutoRouterForTests();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "auto-router-wiring-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    clearEnv();
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns auto-router source + rationale on confident pick", () => {
    const stub = makeStubRouter({
      adapter: "codex",
      model: "gpt-5.4",
      rationale: "adapter=codex selected for stage=feature-dev (test rationale)",
      confidence: 0.8,
    });
    const enumerate = vi.fn(() => ["claude" as const, "codex" as const]);
    const options: AutoRouterOptions = {
      enumerateAvailableAdapters: enumerate,
      complexity: "M",
      mode: "automatic",
      router: stub,
    };
    const decision = resolveStageAdapter("feature-dev", tmpRoot, process.env, options);
    expect(decision.adapter).toBe("codex");
    expect(decision.source).toBe("auto-router");
    expect(decision.rationale).toContain("test rationale");
    expect(decision.routerModel).toBe("gpt-5.4");
    expect(stub.selectForStage).toHaveBeenCalledTimes(1);
    expect(enumerate).toHaveBeenCalledTimes(1);
  });

  it("env override beats router (precedence preserved)", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV = "gemini";
    const stub = makeStubRouter({
      adapter: "codex",
      model: "gpt-5.4",
      rationale: "router pick",
      confidence: 0.9,
    });
    const options: AutoRouterOptions = {
      enumerateAvailableAdapters: () => ["claude", "codex"],
      complexity: "M",
      router: stub,
    };
    const decision = resolveStageAdapter("feature-dev", tmpRoot, process.env, options);
    expect(decision.source).toBe("env");
    expect(decision.adapter).toBe("gemini");
    // Router must not be consulted when env is set.
    expect(stub.selectForStage).not.toHaveBeenCalled();
  });

  it("stage-config beats router (precedence preserved)", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  stage_adapters:
    feature-dev: gemini
`
    );
    const stub = makeStubRouter({
      adapter: "codex",
      model: "gpt-5.4",
      rationale: "router pick",
      confidence: 0.9,
    });
    const options: AutoRouterOptions = {
      enumerateAvailableAdapters: () => ["claude", "codex"],
      complexity: "M",
      router: stub,
    };
    const decision = resolveStageAdapter("feature-dev", tmpRoot, process.env, options);
    expect(decision.source).toBe("stage-config");
    expect(decision.adapter).toBe("gemini");
    expect(stub.selectForStage).not.toHaveBeenCalled();
  });

  it("falls through to default when router abstains and no global config", () => {
    const stub = makeStubRouter(null); // abstain
    const options: AutoRouterOptions = {
      enumerateAvailableAdapters: () => ["claude", "codex"],
      complexity: "M",
      router: stub,
    };
    const decision = resolveStageAdapter("feature-dev", tmpRoot, process.env, options);
    expect(decision.source).toBe("default");
    expect(decision.adapter).toBe("claude");
    expect(stub.selectForStage).toHaveBeenCalledTimes(1);
  });

  it("falls through to global-config when router abstains and global is configured", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `ui:
  core:
    adapter: gemini
`
    );
    const stub = makeStubRouter(null);
    const options: AutoRouterOptions = {
      enumerateAvailableAdapters: () => ["claude", "codex"],
      complexity: "M",
      router: stub,
    };
    const decision = resolveStageAdapter("feature-dev", tmpRoot, process.env, options);
    expect(decision.source).toBe("global-config");
    expect(decision.adapter).toBe("gemini");
  });

  it("does not invoke router when pipeline.auto_router.enabled is false", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `pipeline:
  auto_router:
    enabled: false
`
    );
    const stub = makeStubRouter({
      adapter: "codex",
      model: "gpt-5.4",
      rationale: "router pick",
      confidence: 0.9,
    });
    const options: AutoRouterOptions = {
      enumerateAvailableAdapters: () => ["claude", "codex"],
      complexity: "M",
      router: stub,
    };
    const decision = resolveStageAdapter("feature-dev", tmpRoot, process.env, options);
    expect(decision.source).toBe("default");
    expect(stub.selectForStage).not.toHaveBeenCalled();
  });

  it("does not invoke router when no adapters are available", () => {
    const stub = makeStubRouter({
      adapter: "codex",
      model: "gpt-5.4",
      rationale: "router pick",
      confidence: 0.9,
    });
    const options: AutoRouterOptions = {
      enumerateAvailableAdapters: () => [],
      complexity: "M",
      router: stub,
    };
    const decision = resolveStageAdapter("feature-dev", tmpRoot, process.env, options);
    expect(decision.source).toBe("default");
    expect(stub.selectForStage).not.toHaveBeenCalled();
  });

  it("bypasses router entirely when autoRouterOptions is omitted", () => {
    const decision = resolveStageAdapter("feature-dev", tmpRoot);
    expect(decision.source).toBe("default");
    expect(decision.adapter).toBe("claude");
    expect(decision.rationale).toBeUndefined();
  });
});
