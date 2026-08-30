/**
 * SettingsPanel.lock.test.ts
 *
 * Tests for per-section lock behavior during pipeline execution.
 *
 * Since SettingsPanel has private methods, we test the lock behavior
 * through the observable side-effects: the HTML output (via getSettingsHtml)
 * and the handler callbacks (via message handler integration).
 *
 * @see Issue #921 - Per-section lock during pipeline execution
 */

import { describe, it, expect, vi } from "vitest";
import {
  PIPELINE_LOCKED_SECTIONS,
  SETTINGS_SECTIONS,
  getSectionForPath,
  isSectionLocked,
} from "../../../src/views/settings/types";
import { SettingsPanel } from "../../../src/views/settings/SettingsPanel";

// Mock vscode module for SettingsPanel instantiation
vi.mock("vscode", () => ({
  window: {
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showInputBox: vi.fn(),
    showErrorMessage: vi.fn(),
    createWebviewPanel: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  ViewColumn: { One: 1 },
  Uri: {
    joinPath: vi.fn((...args: any[]) => ({ fsPath: args.join("/") })),
    file: vi.fn((p: string) => ({ fsPath: p })),
  },
  commands: { executeCommand: vi.fn() },
  workspace: {
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    })),
    workspaceFolders: [{ uri: { fsPath: "/test" } }],
  },
  EventEmitter: vi.fn(function () {
    return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  }),
  RelativePattern: vi.fn(),
}));

describe("PIPELINE_LOCKED_SECTIONS constant", () => {
  it("contains exactly the expected sections", () => {
    expect(PIPELINE_LOCKED_SECTIONS).toEqual(["core", "pipeline", "commands", "routing"]);
  });

  it("all locked section IDs exist in SETTINGS_SECTIONS", () => {
    const sectionIds = SETTINGS_SECTIONS.map((s) => s.id);
    for (const lockedId of PIPELINE_LOCKED_SECTIONS) {
      expect(sectionIds).toContain(lockedId);
    }
  });

  it("is readonly", () => {
    // TypeScript enforces 'as const', but verify the array is not accidentally mutated
    expect(Object.isFrozen(PIPELINE_LOCKED_SECTIONS)).toBe(false); // as const doesn't freeze
    expect(PIPELINE_LOCKED_SECTIONS.length).toBe(4);
  });
});

describe("SettingsPanel per-section lock behavior", () => {
  // #498 — this block used to carry a private copy of `getSectionForPath` /
  // `isSectionLocked`, so it asserted against the copy and stayed green through
  // any regression in the shipped mapping. Both now live in
  // `src/views/settings/types.ts` and `SettingsPanel.isSectionLocked` delegates
  // to them, so these assertions run the code the panel actually runs.
  // The rendered-HTML side is covered by SettingsHtml.lock.test.ts.

  const lockedSections = new Set(PIPELINE_LOCKED_SECTIONS);

  describe("path-to-section mapping", () => {
    it("maps simple paths to their section", () => {
      expect(getSectionForPath("pipeline.ci_timeout")).toBe("pipeline");
      expect(getSectionForPath("commands.test")).toBe("commands");
      expect(getSectionForPath("project.number")).toBe("project");
      expect(getSectionForPath("batch.max_issues")).toBe("batch");
    });

    it("maps nested paths to their section", () => {
      expect(getSectionForPath("pipeline.skip_checks.tests")).toBe("pipeline");
      expect(getSectionForPath("branch.prefixes.feature")).toBe("branch");
      expect(getSectionForPath("enforcement.dependencies.enabled")).toBe("enforcement");
    });

    it("maps ui.* paths to core section", () => {
      expect(getSectionForPath("ui.core.adapter")).toBe("core");
      expect(getSectionForPath("ui.core.auth_provider")).toBe("core");
      expect(getSectionForPath("ui.core.default_model")).toBe("core");
      expect(getSectionForPath("lm_studio.model")).toBe("core");
      expect(getSectionForPath("ollama.model")).toBe("core");
    });

    it("maps model_routing.* paths to routing section", () => {
      expect(getSectionForPath("model_routing.mode")).toBe("routing");
      expect(getSectionForPath("model_routing.complexity_thresholds.haiku_max")).toBe("routing");
    });

    it("maps pull_request and pr paths to pull_request section", () => {
      expect(getSectionForPath("pull_request.merge_strategy")).toBe("pull_request");
      expect(getSectionForPath("pr.merge_strategy")).toBe("pull_request");
    });
  });

  describe("section lock checks", () => {
    it("blocks changes to locked section paths", () => {
      expect(isSectionLocked("pipeline.ci_timeout", lockedSections)).toBe(true);
      expect(isSectionLocked("commands.test", lockedSections)).toBe(true);
      expect(isSectionLocked("ui.core.adapter", lockedSections)).toBe(true);
      expect(isSectionLocked("lm_studio.model", lockedSections)).toBe(true);
      expect(isSectionLocked("ollama.model", lockedSections)).toBe(true);
      expect(isSectionLocked("model_routing.mode", lockedSections)).toBe(true);
    });

    it("allows changes to unlocked section paths", () => {
      expect(isSectionLocked("project.number", lockedSections)).toBe(false);
      expect(isSectionLocked("pull_request.merge_strategy", lockedSections)).toBe(false);
      expect(isSectionLocked("branch.base", lockedSections)).toBe(false);
      expect(isSectionLocked("batch.max_issues", lockedSections)).toBe(false);
      expect(isSectionLocked("validation.require_tests", lockedSections)).toBe(false);
      expect(isSectionLocked("sanitization.mode", lockedSections)).toBe(false);
      expect(isSectionLocked("human_in_the_loop.auto_accept_stages", lockedSections)).toBe(false);
      expect(isSectionLocked("ralph_loop.enabled", lockedSections)).toBe(false);
      expect(isSectionLocked("automations.enabled", lockedSections)).toBe(false);
      expect(isSectionLocked("issue.auto_assign", lockedSections)).toBe(false);
      expect(isSectionLocked("enforcement.dependencies.enabled", lockedSections)).toBe(false);
    });

    it("allows all changes when no sections are locked", () => {
      const noLock = new Set<string>();
      expect(isSectionLocked("pipeline.ci_timeout", noLock)).toBe(false);
      expect(isSectionLocked("commands.test", noLock)).toBe(false);
      expect(isSectionLocked("ui.core.adapter", noLock)).toBe(false);
    });
  });

  // Unpinned-wiring guard (#498): the exported predicate above is only a real
  // guard if SettingsPanel actually calls it. Drive the panel's own private
  // `isSectionLocked` against a stand-in `this` carrying just `lockedSections`
  // — if the delegation is ever replaced by a second copy of the mapping, this
  // and the block above stop agreeing.
  describe("SettingsPanel.isSectionLocked delegates to the exported predicate", () => {
    type LockHost = { lockedSections: Set<string> };
    const panelIsSectionLocked = (
      SettingsPanel.prototype as unknown as {
        isSectionLocked: (this: LockHost, path: string) => boolean;
      }
    ).isSectionLocked;

    const host: LockHost = { lockedSections: new Set(PIPELINE_LOCKED_SECTIONS) };

    const paths = [
      "pipeline.ci_timeout",
      "commands.test",
      "ui.core.adapter",
      "lm_studio.model",
      "ollama.model",
      "model_routing.mode",
      "project.number",
      "pull_request.merge_strategy",
      "pr.merge_strategy",
      "batch.max_issues",
      "enforcement.dependencies.enabled",
    ];

    it("agrees with isSectionLocked(path, lockedSections) on every path", () => {
      for (const path of paths) {
        expect([path, panelIsSectionLocked.call(host, path)]).toEqual([
          path,
          isSectionLocked(path, host.lockedSections),
        ]);
      }
    });

    it("locks the pipeline-critical paths and leaves the rest editable", () => {
      expect(panelIsSectionLocked.call(host, "ui.core.adapter")).toBe(true);
      expect(panelIsSectionLocked.call(host, "model_routing.mode")).toBe(true);
      expect(panelIsSectionLocked.call(host, "pr.merge_strategy")).toBe(false);
      expect(panelIsSectionLocked.call(host, "project.number")).toBe(false);
    });
  });
});
