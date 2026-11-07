/**
 * Schema repair reads SKILL.md from the run's pinned root (#2799): the
 * ContextAssembler's workspace-root provider prefers pinnedWorkspaceRoot over
 * the currently selected repository's directory.
 */
import { describe, it, expect, vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [] },
  window: { createOutputChannel: vi.fn(() => ({ appendLine: vi.fn(), dispose: vi.fn() })) },
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
  Disposable: { from: vi.fn() },
  Uri: { file: (p: string) => ({ fsPath: p }) },
}));

import { z } from "zod";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

const logger = {
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
  debug: vi.fn(),
} as unknown as Logger;

describe("HeadlessOrchestrator schema-repair skill root (#2799)", () => {
  it("passes the pinned root, not the selected repository's directory, to loadSkillContent", async () => {
    const orch = new HeadlessOrchestrator(null, logger);
    const internals = orch as unknown as {
      pinnedWorkspaceRoot: string | undefined;
      getWorkingDirectory: () => string;
      skillLoader: { loadSkillContent: (stage: string, root?: string) => unknown };
      contextAssembler: { workspaceRootProvider: () => string };
    };
    internals.getWorkingDirectory = () => "/selected/other-repo";
    internals.pinnedWorkspaceRoot = "/pinned/run-root";

    expect(internals.contextAssembler.workspaceRootProvider()).toBe("/pinned/run-root");

    const load = vi.spyOn(internals.skillLoader, "loadSkillContent").mockReturnValue(null);
    const schema = z.object({ a: z.string() });
    const zodError = schema.safeParse({}).error!;
    await (
      internals.contextAssembler as unknown as {
        attemptContextSchemaRepair: (...a: unknown[]) => Promise<unknown>;
      }
    ).attemptContextSchemaRepair("feature-dev", 1, schema, zodError, "/tmp/x.json", 1);
    expect(load).toHaveBeenCalledWith("feature-dev", "/pinned/run-root");

    internals.pinnedWorkspaceRoot = undefined;
    expect(internals.contextAssembler.workspaceRootProvider()).toBe("/selected/other-repo");
  });
});
