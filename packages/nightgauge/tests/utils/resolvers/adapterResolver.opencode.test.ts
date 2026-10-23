/**
 * adapterResolver.opencode.test.ts
 *
 * Pins `opencode` as a first-class `ExecutionAdapter` across the three raw-YAML
 * shapes the line-by-line scanners in `adapterResolver.ts` parse, plus the
 * router mapping the #1713 review flagged as fail-closed.
 *
 * Before #1623:
 *   - `AdapterEnumSchema` (and therefore `VALID_ADAPTERS`) did not list
 *     "opencode", so all three YAML scanners silently dropped it and fell
 *     through to the next precedence step instead of resolving it.
 *   - `fromRouterAdapter("opencode")` threw rather than passing the id
 *     through, so an auto-router pick of opencode aborted stage dispatch.
 *
 * @see Issue #1623 - widen ExecutionAdapter to include opencode
 * @see docs/decisions/022-opencode-multi-provider-adapter.md
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
  readAdapterFallbackChainFromYaml,
  fromRouterAdapter,
} from "../../../src/utils/resolvers/adapterResolver";

const ADAPTER_ENV_KEYS = [
  "NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV",
  "NIGHTGAUGE_UI_CORE_ADAPTER",
];

function clearAdapterEnv(): void {
  for (const key of ADAPTER_ENV_KEYS) {
    delete process.env[key];
  }
}

describe("opencode as a first-class ExecutionAdapter (#1623)", () => {
  let tmpRoot: string;

  beforeEach(() => {
    clearAdapterEnv();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "adapter-resolver-opencode-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    clearAdapterEnv();
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("shape 1 — ui.core.adapter: opencode resolves with source 'global-config'", () => {
    const yaml = `ui:
  core:
    adapter: opencode
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "opencode",
      source: "global-config",
    });
  });

  it("shape 2 — pipeline.stage_adapters.<stage>: opencode resolves with source 'stage-config'", () => {
    const yaml = `pipeline:
  stage_adapters:
    feature-dev: opencode
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "opencode",
      source: "stage-config",
    });
  });

  it("shape 3 — pipeline.adapter_fallback_chain entries parse opencode", () => {
    const yaml = `pipeline:
  adapter_fallback_chain:
    - codex
    - opencode
    - gemini
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    expect(readAdapterFallbackChainFromYaml(tmpRoot)).toEqual(["codex", "opencode", "gemini"]);
  });

  it("NIGHTGAUGE_UI_CORE_ADAPTER=opencode resolves as env-sourced global adapter", () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "opencode";
    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "opencode",
      source: "global-config",
    });
  });

  it("NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV=opencode resolves with source 'env'", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV = "opencode";
    expect(resolveStageAdapter("feature-dev", tmpRoot)).toEqual({
      adapter: "opencode",
      source: "env",
    });
  });

  it("fromRouterAdapter('opencode') passes it through instead of throwing", () => {
    expect(() => fromRouterAdapter("opencode")).not.toThrow();
    expect(fromRouterAdapter("opencode")).toBe("opencode");
  });
});
