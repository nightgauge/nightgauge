/**
 * mergedConfigReader.test.ts
 *
 * Pins the single synchronous entry point for effective config reads:
 * global (machine) → project → local → env, matching the Go binary's
 * LoadMerged and the async configMergeEngine. Every utils/resolvers/* getter
 * and the skillRunner auto-accept loader consume this, so these tests are
 * the tier-behavior contract for the whole sync read path.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { parse } from "yaml";
import {
  readEffectiveConfigTextSync,
  clearMergedConfigCacheForTests,
} from "../../src/utils/mergedConfigReader";
import { resolveConfigPathSync } from "../../src/utils/configPathResolver";
import { getHumanInTheLoopConfig } from "../../src/utils/resolvers/otherResolver";

function writeFileEnsured(filePath: string, content: string): void {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(filePath, content, "utf-8");
}

describe("mergedConfigReader", () => {
  let root: string;
  let machineDir: string;
  let savedConfigHome: string | undefined;

  const projectPath = () => path.join(root, ".nightgauge", "config.yaml");
  const localPath = () => path.join(root, ".nightgauge", "config.local.yaml");
  const machinePath = () => path.join(machineDir, "config.yaml");

  const readMerged = () => {
    clearMergedConfigCacheForTests();
    return parse(readEffectiveConfigTextSync(resolveConfigPathSync(root)));
  };

  beforeEach(() => {
    root = fs.mkdtempSync(path.join(os.tmpdir(), "merged-config-root-"));
    machineDir = fs.mkdtempSync(path.join(os.tmpdir(), "merged-config-machine-"));
    savedConfigHome = process.env.NIGHTGAUGE_CONFIG_HOME;
    process.env.NIGHTGAUGE_CONFIG_HOME = machineDir;
    clearMergedConfigCacheForTests();
  });

  afterEach(() => {
    if (savedConfigHome === undefined) {
      delete process.env.NIGHTGAUGE_CONFIG_HOME;
    } else {
      process.env.NIGHTGAUGE_CONFIG_HOME = savedConfigHome;
    }
    clearMergedConfigCacheForTests();
    fs.rmSync(root, { recursive: true, force: true });
    fs.rmSync(machineDir, { recursive: true, force: true });
  });

  it("returns the project config unchanged in value when it is the only tier", () => {
    writeFileEnsured(projectPath(), "owner: acme\npipeline:\n  max_concurrent: 3\n");
    const merged = readMerged();
    expect(merged.owner).toBe("acme");
    expect(merged.pipeline.max_concurrent).toBe(3);
  });

  it("merges the machine tier UNDER the project tier (project wins)", () => {
    writeFileEnsured(
      machinePath(),
      "pipeline:\n  budget_preset: standard\nlm_studio:\n  model: local-model\n"
    );
    writeFileEnsured(projectPath(), "owner: acme\npipeline:\n  budget_preset: generous\n");
    const merged = readMerged();
    // Project value shadows machine.
    expect(merged.pipeline.budget_preset).toBe("generous");
    // Machine-only keys surface.
    expect(merged.lm_studio.model).toBe("local-model");
  });

  it("merges the local tier OVER the project tier (local wins)", () => {
    writeFileEnsured(
      projectPath(),
      "owner: acme\npipeline:\n  architecture_approval:\n    enabled: true\n"
    );
    writeFileEnsured(localPath(), "pipeline:\n  architecture_approval:\n    enabled: false\n");
    const merged = readMerged();
    expect(merged.pipeline.architecture_approval.enabled).toBe(false);
    expect(merged.owner).toBe("acme");
  });

  it("applies NIGHTGAUGE_* env overrides over all file tiers", () => {
    writeFileEnsured(projectPath(), "owner: acme\npipeline:\n  auto_fix: true\n");
    writeFileEnsured(localPath(), "pipeline:\n  auto_fix: true\n");
    process.env.NIGHTGAUGE_PIPELINE_AUTO_FIX = "false";
    try {
      const merged = readMerged();
      expect(merged.pipeline.auto_fix).toBe(false);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_AUTO_FIX;
    }
  });

  it("replaces arrays wholesale instead of concatenating", () => {
    writeFileEnsured(
      projectPath(),
      "owner: acme\nhuman_in_the_loop:\n  trusted_stages:\n    - issue-pickup\n"
    );
    writeFileEnsured(
      localPath(),
      "human_in_the_loop:\n  trusted_stages:\n    - feature-dev\n    - pr-create\n"
    );
    const merged = readMerged();
    expect(merged.human_in_the_loop.trusted_stages).toEqual(["feature-dev", "pr-create"]);
  });

  it("falls back to the raw project text when the project tier is unparseable", () => {
    const rawBroken = "owner: acme\n\t\tbad:\n  - [unclosed\n";
    writeFileEnsured(projectPath(), rawBroken);
    writeFileEnsured(localPath(), "pipeline:\n  auto_fix: false\n");
    clearMergedConfigCacheForTests();
    const text = readEffectiveConfigTextSync(resolveConfigPathSync(root));
    expect(text).toBe(rawBroken);
  });

  it("skips an unparseable local tier but still merges the rest", () => {
    writeFileEnsured(projectPath(), "owner: acme\n");
    writeFileEnsured(machinePath(), "notifications:\n  discord:\n    enabled: true\n");
    writeFileEnsured(localPath(), "\t\t[[[not yaml\n");
    const merged = readMerged();
    expect(merged.owner).toBe("acme");
    expect(merged.notifications.discord.enabled).toBe(true);
  });

  it("invalidates the cache when a tier file changes", () => {
    writeFileEnsured(projectPath(), "owner: acme\npipeline:\n  max_concurrent: 2\n");
    const first = parse(readEffectiveConfigTextSync(resolveConfigPathSync(root)));
    expect(first.pipeline.max_concurrent).toBe(2);

    // A local override appears — the next read must see it without any
    // explicit cache clear.
    writeFileEnsured(localPath(), "pipeline:\n  max_concurrent: 5\n");
    const second = parse(readEffectiveConfigTextSync(resolveConfigPathSync(root)));
    expect(second.pipeline.max_concurrent).toBe(5);
  });

  it("resolves tiers from the path's OWN root — the worktree contract", () => {
    // Simulate a worktree: a separate directory with its own committed
    // config + copied local config. The machine tier applies to both roots.
    writeFileEnsured(machinePath(), "github_user: operator\n");
    const worktree = fs.mkdtempSync(path.join(os.tmpdir(), "merged-config-worktree-"));
    try {
      writeFileEnsured(
        path.join(worktree, ".nightgauge", "config.yaml"),
        "owner: acme\npipeline:\n  budget_preset: generous\n"
      );
      writeFileEnsured(
        path.join(worktree, ".nightgauge", "config.local.yaml"),
        "pipeline:\n  architecture_approval:\n    enabled: false\n"
      );
      clearMergedConfigCacheForTests();
      const merged = parse(readEffectiveConfigTextSync(resolveConfigPathSync(worktree)));
      expect(merged.owner).toBe("acme");
      expect(merged.github_user).toBe("operator");
      expect(merged.pipeline.architecture_approval.enabled).toBe(false);
    } finally {
      fs.rmSync(worktree, { recursive: true, force: true });
    }
  });

  it("end-to-end: a resolver getter honors machine and local tiers", () => {
    // human_in_the_loop set true in the committed config, disabled via the
    // gitignored local tier — the exact "dark factory kill switch without a
    // commit" flow.
    writeFileEnsured(
      projectPath(),
      "owner: acme\nhuman_in_the_loop:\n  auto_accept_stages: true\n  auto_accept_permissions: true\n"
    );
    clearMergedConfigCacheForTests();
    expect(getHumanInTheLoopConfig(root).autoAcceptPermissions).toBe(true);

    writeFileEnsured(localPath(), "human_in_the_loop:\n  auto_accept_permissions: false\n");
    clearMergedConfigCacheForTests();
    const hitl = getHumanInTheLoopConfig(root);
    expect(hitl.autoAcceptPermissions).toBe(false);
    // Keys the local tier does not touch keep their project value.
    expect(hitl.autoAcceptStages).toBe(true);
  });
});
