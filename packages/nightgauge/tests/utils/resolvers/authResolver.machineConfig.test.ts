/**
 * The extension reads the machine-tier config where the Go loader does, with
 * no Linux legacy fallback (ADR-024 § 4, § 15, #2041): an older build's
 * ~/.nightgauge/config.yaml is moved by `nightgauge doctor --fix`, never read.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({
  workspace: { getConfiguration: () => ({ get: () => undefined }) },
}));

import { machineConfigPathInUse } from "../../../src/utils/resolvers/authResolver";
import { getGlobalConfigPath } from "../../../src/utils/globalConfigResolver";

describe("machineConfigPathInUse — one machine-config location", () => {
  let home: string;
  let savedHome: string | undefined;

  beforeEach(() => {
    home = fs.mkdtempSync(path.join(os.tmpdir(), "machine-config-"));
    savedHome = process.env.HOME;
    process.env.HOME = home;
  });
  afterEach(() => {
    process.env.HOME = savedHome;
    fs.rmSync(home, { recursive: true, force: true });
  });

  it("on Linux names the XDG file even when only the legacy file exists", () => {
    const legacy = path.join(home, ".nightgauge", "config.yaml");
    fs.mkdirSync(path.dirname(legacy), { recursive: true });
    fs.writeFileSync(legacy, "owner: legacy\n");

    const got = machineConfigPathInUse({}, "linux");
    expect(got).toBe(path.join(home, ".config", "nightgauge", "config.yaml"));
    expect(got).not.toBe(legacy);
  });

  it("follows the overrides, as getGlobalConfigPath does", () => {
    const env = { NIGHTGAUGE_CONFIG_HOME: "/srv/ng-config" };
    expect(machineConfigPathInUse(env, "linux")).toBe(getGlobalConfigPath(env, "linux"));
    expect(machineConfigPathInUse({}, "darwin")).toBe(getGlobalConfigPath({}, "darwin"));
  });
});
