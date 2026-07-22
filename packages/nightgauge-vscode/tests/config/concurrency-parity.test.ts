/**
 * concurrency-parity.test.ts
 *
 * Go↔TS parity fixture tests for the unified concurrency model (#3781/#3789).
 *
 * Each test here has a matching Go test in internal/config/cap_parity_test.go.
 * When a test diverges between the two files, the resolvers have drifted.
 *
 * Fixture A:
 *   concurrency:
 *     workspace_max: 4
 *     per_repo_max: 2
 *     repository_overrides:
 *       flutter: 3
 *
 * Fixture B:
 *   concurrency: {}  (all defaults)
 *
 * Go counterpart: internal/config/cap_parity_test.go TestCapForRepoParity_FixtureA/B
 */

import { describe, it, expect } from "vitest";

vi.mock("vscode", () => ({}));

import {
  createSequentialRepoConfigService,
  DEFAULT_PER_REPO_MAX,
  DEFAULT_WORKSPACE_MAX,
  type ConfigBridgeLike,
  type RuntimeStateStoreLike,
  type MachineYamlWriterLike,
} from "../../src/utils/sequentialRepoConfig";

function makeMockRuntimeStore(): RuntimeStateStoreLike {
  return { delete: async () => {} };
}

function makeMockYamlWriter(): MachineYamlWriterLike {
  return { writeGlobal: async () => ({ success: true }) };
}

function makeBridge(concurrency?: {
  workspace_max?: number;
  per_repo_max?: number;
  repository_overrides?: Record<string, number | undefined>;
}): ConfigBridgeLike {
  return { getEffectiveConfig: () => ({ config: { concurrency } }) };
}

describe("Go↔TS parity — FixtureA (workspace_max:4, per_repo_max:2, flutter override:3)", () => {
  // Mirrors TestCapForRepoParity_FixtureA in internal/config/cap_parity_test.go
  const svc = createSequentialRepoConfigService(
    makeMockRuntimeStore(),
    makeBridge({ workspace_max: 4, per_repo_max: 2, repository_overrides: { flutter: 3 } }),
    makeMockYamlWriter()
  );

  it("readWorkspaceMax returns 4", () => {
    expect(svc.readWorkspaceMax()).toBe(4);
  });

  it('resolveRepoConcurrencyCap("flutter") returns 3 (explicit override)', () => {
    expect(svc.resolveRepoConcurrencyCap("flutter")).toBe(3);
  });

  it('resolveRepoConcurrencyCap("nightgauge/flutter") returns 3 (short-name suffix match)', () => {
    expect(svc.resolveRepoConcurrencyCap("nightgauge/flutter")).toBe(3);
  });

  it('resolveRepoConcurrencyCap("other-repo") returns 2 (per_repo_max)', () => {
    expect(svc.resolveRepoConcurrencyCap("other-repo")).toBe(2);
  });
});

describe("Go↔TS parity — FixtureB (empty concurrency block — all defaults)", () => {
  // Mirrors TestCapForRepoParity_FixtureB in internal/config/cap_parity_test.go
  const svc = createSequentialRepoConfigService(
    makeMockRuntimeStore(),
    makeBridge({}),
    makeMockYamlWriter()
  );

  it(`readWorkspaceMax returns DEFAULT_WORKSPACE_MAX (${DEFAULT_WORKSPACE_MAX})`, () => {
    expect(svc.readWorkspaceMax()).toBe(DEFAULT_WORKSPACE_MAX);
  });

  it(`resolveRepoConcurrencyCap("any-repo") returns DEFAULT_PER_REPO_MAX (${DEFAULT_PER_REPO_MAX})`, () => {
    expect(svc.resolveRepoConcurrencyCap("any-repo")).toBe(DEFAULT_PER_REPO_MAX);
  });
});
