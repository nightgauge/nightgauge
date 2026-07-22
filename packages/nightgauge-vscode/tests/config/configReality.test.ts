/**
 * Reality tests for .nightgauge/config.yaml
 *
 * These tests load the actual config file from this repository and verify that
 * the parser and path resolver handle its hybrid format correctly. They fail
 * when format changes break the parser — providing an early warning before
 * CI or users are affected.
 *
 * Skip condition: config file absent (shallow CI clone, fork without config).
 *
 * @see Issue #1937 - Config reality tests: verify actual config.yaml parses correctly
 */

import { describe, it, expect } from "vitest";
import * as path from "path";
import { existsSync } from "fs";
import {
  resolveConfigPath,
  getRepoIdentity,
  CONFIG_DIR,
  CONFIG_FILE_NAME,
} from "../../src/utils/configPathResolver";

// Resolve repo root from vitest CWD (packages/nightgauge-vscode/)
const repoRoot = path.resolve(process.cwd(), "..", "..");
const configPath = path.join(repoRoot, CONFIG_DIR, CONFIG_FILE_NAME);
const configExists = existsSync(configPath);

describe.skipIf(!configExists)("Config reality tests — .nightgauge/config.yaml", () => {
  it("resolveConfigPath finds the real config file as non-legacy", async () => {
    const result = await resolveConfigPath(repoRoot);

    expect(result.exists).toBe(true);
    expect(result.isLegacy).toBe(false);
    expect(result.path).toBe(configPath);
  });

  it("getRepoIdentity reads owner and repo from real config", async () => {
    const identity = await getRepoIdentity(repoRoot);

    expect(identity).not.toBeNull();
    expect(identity!.owner).toBe("nightgauge");
    expect(identity!.repo).toBe("nightgauge");
  });
});
