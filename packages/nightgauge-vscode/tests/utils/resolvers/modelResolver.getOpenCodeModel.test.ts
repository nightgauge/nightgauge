/**
 * modelResolver.getOpenCodeModel.test.ts
 *
 * `getOpenCodeModel` reads the `model:` value under the `opencode:` block in
 * the merged `.nightgauge/config.yaml` / `config.local.yaml` (ADR-022 § 7,
 * machine-tier only). Covers:
 *   - the happy path, including a nested-slash model id round-tripping
 *     unchanged (e.g. `lmstudio/qwen/qwen3.8-27b`)
 *   - rejection of a leading `-` (would read as a CLI flag, e.g. `--auto`)
 *   - rejection of an unquoted/quoted value containing whitespace (`a b`)
 *   - absent file / absent block / absent key all return undefined
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md § 7
 * @see Issue #1623 - VS Code execution-adapter widening
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

import { getOpenCodeModel } from "../../../src/utils/resolvers/modelResolver";

describe("getOpenCodeModel", () => {
  let tmpRoot: string;
  const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "opencode-model-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
    warnSpy.mockClear();
  });

  afterEach(() => {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("returns undefined when no config file exists", () => {
    expect(getOpenCodeModel(tmpRoot)).toBeUndefined();
  });

  it("returns undefined when the opencode: block is absent", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `lm_studio:\n  model: some-model\n`
    );
    expect(getOpenCodeModel(tmpRoot)).toBeUndefined();
  });

  it("returns undefined when opencode: is present but model: is absent", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `opencode:\n  binary: opencode\n`
    );
    expect(getOpenCodeModel(tmpRoot)).toBeUndefined();
  });

  it("reads opencode.model and preserves nested slashes unchanged", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `opencode:\n  model: lmstudio/qwen/qwen3.8-27b\n`
    );
    expect(getOpenCodeModel(tmpRoot)).toBe("lmstudio/qwen/qwen3.8-27b");
  });

  it("does not bleed into a neighboring top-level section", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `opencode:\n  model: lmstudio/qwen/qwen3.8-27b\nlm_studio:\n  model: not-this-one\n`
    );
    expect(getOpenCodeModel(tmpRoot)).toBe("lmstudio/qwen/qwen3.8-27b");
  });

  it("rejects a value with a leading '-' (would read as a CLI flag) → undefined + warns", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `opencode:\n  model: --auto\n`
    );
    expect(getOpenCodeModel(tmpRoot)).toBeUndefined();
    expect(warnSpy).toHaveBeenCalled();
  });

  it("rejects a value containing whitespace ('a b') → undefined", () => {
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), `opencode:\n  model: a b\n`);
    expect(getOpenCodeModel(tmpRoot)).toBeUndefined();
  });

  it("config.local.yaml overrides config.yaml (machine/local tier precedence)", () => {
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.yaml"),
      `opencode:\n  model: lmstudio/base-model\n`
    );
    fs.writeFileSync(
      path.join(tmpRoot, ".nightgauge", "config.local.yaml"),
      `opencode:\n  model: lmstudio/local-override\n`
    );
    expect(getOpenCodeModel(tmpRoot)).toBe("lmstudio/local-override");
  });
});
