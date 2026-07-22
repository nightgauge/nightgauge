import { describe, it, expect } from "vitest";
import { fileURLToPath } from "node:url";
import {
  detectDuplicateCommandIds,
  detectDuplicateViewIds,
  collectAllWhenClauses,
  extractSetContextKeysFromContent,
  extractWhenClauseTokens,
  detectOrphanedWhenClauses,
  isDirectInvocation,
} from "../../scripts/generate-package-contributions.js";
import type { ManifestContributes } from "../../src/manifest/types.js";

// Regression guard (CI flake misdiagnosed as runner contention): importing this
// module must NEVER execute main() — the unguarded top-level call rewrote the
// real package.json on import, truncating it just as parallel vitest workers
// read it ("File is empty" JSON crash in OutputWindow.completionBadge.test.ts).
// The mere fact this test file imports the module above and the suite collects
// without corrupting package.json proves main() did not fire; these tests pin
// the guard decision so the bare `main()` can't silently return.
describe("isDirectInvocation — main() runs only when invoked directly (not on import)", () => {
  const self = fileURLToPath(import.meta.url); // a real, resolvable file

  it("returns false when there is no entry argument", () => {
    expect(isDirectInvocation(undefined, self)).toBe(false);
  });

  it("returns true when the entry path resolves to this module", () => {
    expect(isDirectInvocation(self, self)).toBe(true);
  });

  it("returns false when the entry path is a different module", () => {
    const other = fileURLToPath(new URL("../../package.json", import.meta.url));
    expect(isDirectInvocation(other, self)).toBe(false);
  });

  it("returns false (fail-safe to NOT writing) when a path cannot be resolved", () => {
    expect(isDirectInvocation("/nonexistent/entry.ts", self)).toBe(false);
  });
});

function makeMinimalManifest(overrides: Partial<ManifestContributes> = {}): ManifestContributes {
  return {
    viewsContainers: { activitybar: [] },
    views: {},
    viewsWelcome: [],
    commands: [],
    menus: {},
    keybindings: [],
    ...overrides,
  };
}

describe("detectDuplicateCommandIds", () => {
  it("returns empty array when all IDs are unique", () => {
    const commands = [
      { command: "ext.foo", title: "Foo" },
      { command: "ext.bar", title: "Bar" },
      { command: "ext.baz", title: "Baz" },
    ];
    expect(detectDuplicateCommandIds(commands)).toEqual([]);
  });

  it("detects duplicate command IDs", () => {
    const commands = [
      { command: "ext.foo", title: "Foo" },
      { command: "ext.bar", title: "Bar" },
      { command: "ext.foo", title: "Foo Again" },
    ];
    expect(detectDuplicateCommandIds(commands)).toEqual(["ext.foo"]);
  });

  it("reports each duplicate only once even with 3+ occurrences", () => {
    const commands = [
      { command: "ext.dup", title: "A" },
      { command: "ext.dup", title: "B" },
      { command: "ext.dup", title: "C" },
    ];
    expect(detectDuplicateCommandIds(commands)).toEqual(["ext.dup"]);
  });

  it("handles empty commands array", () => {
    expect(detectDuplicateCommandIds([])).toEqual([]);
  });
});

describe("detectDuplicateViewIds", () => {
  it("returns empty array when all view IDs are unique", () => {
    const views = {
      container: [
        { id: "view.a", name: "A" },
        { id: "view.b", name: "B" },
      ],
    };
    expect(detectDuplicateViewIds(views)).toEqual([]);
  });

  it("detects duplicates within the same container", () => {
    const views = {
      container: [
        { id: "view.a", name: "A" },
        { id: "view.a", name: "A dup" },
      ],
    };
    expect(detectDuplicateViewIds(views)).toEqual(["view.a"]);
  });

  it("detects duplicates across containers", () => {
    const views = {
      container1: [{ id: "view.shared", name: "A" }],
      container2: [{ id: "view.shared", name: "B" }],
    };
    expect(detectDuplicateViewIds(views)).toEqual(["view.shared"]);
  });

  it("handles empty views", () => {
    expect(detectDuplicateViewIds({})).toEqual([]);
  });
});

describe("collectAllWhenClauses", () => {
  it("collects when clauses from views, viewsWelcome, menus, and keybindings", () => {
    const manifest = makeMinimalManifest({
      views: {
        panel: [{ id: "v1", name: "V1", when: "ctx.viewWhen" }],
      },
      viewsWelcome: [{ view: "v1", contents: "Welcome", when: "ctx.welcomeWhen" }],
      menus: {
        "view/title": [{ command: "cmd.a", when: "ctx.menuWhen" }],
      },
      keybindings: [{ command: "cmd.b", key: "ctrl+k", when: "ctx.kbWhen" }],
    });

    const clauses = collectAllWhenClauses(manifest);
    expect(clauses).toEqual(["ctx.viewWhen", "ctx.welcomeWhen", "ctx.menuWhen", "ctx.kbWhen"]);
  });

  it("skips entries without when clauses", () => {
    const manifest = makeMinimalManifest({
      views: {
        panel: [{ id: "v1", name: "V1" }],
      },
      commands: [{ command: "cmd.a", title: "A" }],
    });
    expect(collectAllWhenClauses(manifest)).toEqual([]);
  });
});

describe("extractSetContextKeysFromContent", () => {
  it("extracts keys from single-quote setContext calls", () => {
    const input = `
      vscode.commands.executeCommand('setContext', 'nightgauge.pipelineRunning', true);
      vscode.commands.executeCommand('setContext', 'nightgauge.pipelineActive', false);
    `;
    const keys = extractSetContextKeysFromContent(input);
    expect(keys).toContain("nightgauge.pipelineRunning");
    expect(keys).toContain("nightgauge.pipelineActive");
  });

  it("extracts keys from double-quote setContext calls", () => {
    const input = `
      vscode.commands.executeCommand("setContext", "nightgauge.hasFocus", true);
    `;
    const keys = extractSetContextKeysFromContent(input);
    expect(keys).toContain("nightgauge.hasFocus");
  });

  it("extracts keys from multi-line setContext calls", () => {
    const input = `
      vscode.commands.executeCommand(
        'setContext',
        'nightgauge.pipelinePaused',
        true
      );
    `;
    const keys = extractSetContextKeysFromContent(input);
    expect(keys).toContain("nightgauge.pipelinePaused");
  });

  it("returns empty array for no matches", () => {
    expect(extractSetContextKeysFromContent("no context keys here")).toEqual([]);
  });
});

describe("extractWhenClauseTokens", () => {
  it("extracts nightgauge.* tokens from when clauses", () => {
    const clauses = ["nightgauge.pipelineRunning && !nightgauge.pipelinePaused"];
    const tokens = extractWhenClauseTokens(clauses);
    expect(tokens).toContain("nightgauge.pipelineRunning");
    expect(tokens).toContain("nightgauge.pipelinePaused");
    expect(tokens).toHaveLength(2);
  });

  it("does not extract tokens from escaped regex patterns in when clauses", () => {
    // view =~ /^nightgauge\\.pipeline/ uses escaped dots — not a context key
    const clauses = ["view =~ /^nightgauge\\\\.pipeline/"];
    const tokens = extractWhenClauseTokens(clauses);
    expect(tokens).toEqual([]);
  });

  it("handles dotted context keys like concurrentSlotActive.0", () => {
    const clauses = ["nightgauge.concurrentSlotActive.0"];
    const tokens = extractWhenClauseTokens(clauses);
    expect(tokens).toContain("nightgauge.concurrentSlotActive.0");
  });
});

describe("detectOrphanedWhenClauses", () => {
  it("returns empty when all tokens match known keys or view IDs", () => {
    const manifest = makeMinimalManifest({
      views: {
        panel: [
          {
            id: "nightgauge.myView",
            name: "My",
            when: "nightgauge.isReady",
          },
        ],
      },
      menus: {
        "view/title": [{ command: "cmd.a", when: "view == nightgauge.myView" }],
      },
    });
    const knownKeys = ["nightgauge.isReady"];
    expect(detectOrphanedWhenClauses(manifest, knownKeys)).toEqual([]);
  });

  it("detects orphaned context keys not in known set", () => {
    const manifest = makeMinimalManifest({
      views: {
        panel: [{ id: "v1", name: "V1", when: "nightgauge.unknownKey" }],
      },
    });
    const orphaned = detectOrphanedWhenClauses(manifest, []);
    expect(orphaned).toContain("nightgauge.unknownKey");
  });

  it("does not flag view IDs as orphaned", () => {
    const manifest = makeMinimalManifest({
      views: {
        panel: [{ id: "nightgauge.pipelineView", name: "Pipeline" }],
      },
      menus: {
        "view/title": [
          {
            command: "cmd.a",
            when: "view == nightgauge.pipelineView",
          },
        ],
      },
    });
    expect(detectOrphanedWhenClauses(manifest, [])).toEqual([]);
  });
});

describe("real manifest validation", () => {
  it("real manifest has no duplicate command IDs", async () => {
    const { MANIFEST_CONTRIBUTES } = await import("../../src/manifest/index.js");
    const dups = detectDuplicateCommandIds(MANIFEST_CONTRIBUTES.commands);
    expect(dups).toEqual([]);
  });

  it("real manifest has no duplicate view IDs", async () => {
    const { MANIFEST_CONTRIBUTES } = await import("../../src/manifest/index.js");
    const dups = detectDuplicateViewIds(MANIFEST_CONTRIBUTES.views);
    expect(dups).toEqual([]);
  });

  it("real manifest has expected number of commands", async () => {
    const { MANIFEST_CONTRIBUTES } = await import("../../src/manifest/index.js");
    expect(MANIFEST_CONTRIBUTES.commands.length).toBeGreaterThanOrEqual(70);
  });
});
