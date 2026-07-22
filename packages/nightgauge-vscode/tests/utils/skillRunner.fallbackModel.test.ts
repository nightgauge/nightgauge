/**
 * skillRunner.fallbackModel.test.ts
 *
 * Verifies resolveFallbackModel — the Fable-aware `--fallback-model` chooser.
 * Fable has a separate Max-plan usage bucket from Opus/Sonnet, so a Fable run
 * defaults its CLI fallback to Opus (a user-configured non-Fable fallback wins),
 * while a non-Fable run keeps whatever was configured.
 */
import { describe, it, expect, vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }] },
}));

import { resolveFallbackModel } from "../../src/utils/skillRunner";

describe("resolveFallbackModel", () => {
  it("Fable with no configured fallback → Opus", () => {
    expect(resolveFallbackModel("fable", undefined)).toBe("opus");
  });

  it("Fable with a configured non-Fable fallback → honors the config", () => {
    expect(resolveFallbackModel("fable", "sonnet")).toBe("sonnet");
    expect(resolveFallbackModel("fable", "opus")).toBe("opus");
  });

  it("Fable configured to fall back to Fable → Opus (never Fable)", () => {
    expect(resolveFallbackModel("fable", "fable")).toBe("opus");
  });

  it("non-Fable run keeps the configured fallback (or none)", () => {
    expect(resolveFallbackModel("opus", undefined)).toBeUndefined();
    expect(resolveFallbackModel("sonnet", "haiku")).toBe("haiku");
    expect(resolveFallbackModel("haiku", undefined)).toBeUndefined();
  });
});
