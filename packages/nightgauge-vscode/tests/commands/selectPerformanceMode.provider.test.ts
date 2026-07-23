import { describe, expect, it, vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: undefined },
  window: {},
  commands: {},
  QuickPickItemKind: { Separator: -1 },
}));

import { getModePresentation } from "../../src/commands/selectPerformanceMode";

describe("provider-aware performance mode presentation", () => {
  it("uses Codex model names instead of Claude aliases", () => {
    const elevated = getModePresentation("elevated", "codex");
    const maximum = getModePresentation("maximum", "codex");

    expect(elevated.description).toContain("Codex");
    expect(elevated.description).toMatch(/gpt-/i);
    expect(maximum.description).toMatch(/gpt-/i);
    expect(maximum.description).not.toMatch(/\bOpus\b|\bHaiku\b|\bSonnet\b/);
  });

  it("keeps Claude presentation provider-specific", () => {
    const elevated = getModePresentation("elevated", "claude");
    expect(elevated.description).toContain("Claude");
    expect(elevated.description).toMatch(/Haiku/i);
  });
});
