/**
 * RuntimeWriteTier.test.ts
 *
 * Issue #1516 — a runtime/UI toggle must never rewrite the committed team
 * config. Two properties are pinned here:
 *
 *   1. The value lands in the tier it belongs in — `.nightgauge/config.local.yaml`
 *      for an ordinary key, `~/.nightgauge/config.yaml` for a machine-tier key —
 *      and the merged view reads it back.
 *   2. `.nightgauge/config.yaml` comes out byte-identical, header comments and
 *      blank lines included. When an explicit team-tier write does happen, only
 *      the changed key moves; every other comment survives.
 *
 * `NightgaugeYamlService` reads through `vscode.workspace.fs` but resolves its
 * tier paths with node's `fs`, so the mock below is backed by a real temp
 * directory rather than an in-memory map. `NIGHTGAUGE_CONFIG_HOME` points the
 * machine tier at that temp directory too, so the developer's real
 * `~/.nightgauge/config.yaml` is never touched.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

const { mockReadFile, mockWriteFile, mockDelete, mockCreateDirectory } = vi.hoisted(() => {
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const nodeFs = require("fs") as typeof import("fs");
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const nodePath = require("path") as typeof import("path");
  return {
    mockReadFile: vi.fn(async (uri: { fsPath: string }) => {
      if (!nodeFs.existsSync(uri.fsPath)) {
        const err = new Error(`FileNotFound: ${uri.fsPath}`) as Error & { code: string };
        err.code = "FileNotFound";
        throw err;
      }
      return nodeFs.readFileSync(uri.fsPath);
    }),
    mockWriteFile: vi.fn(async (uri: { fsPath: string }, data: Uint8Array) => {
      nodeFs.mkdirSync(nodePath.dirname(uri.fsPath), { recursive: true });
      nodeFs.writeFileSync(uri.fsPath, Buffer.from(data));
    }),
    mockDelete: vi.fn(async (uri: { fsPath: string }) => {
      if (nodeFs.existsSync(uri.fsPath)) nodeFs.rmSync(uri.fsPath);
    }),
    mockCreateDirectory: vi.fn(async (uri: { fsPath: string }) => {
      nodeFs.mkdirSync(uri.fsPath, { recursive: true });
    }),
  };
});

vi.mock("vscode", () => ({
  window: {
    showInformationMessage: vi.fn(),
    showWarningMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  Uri: {
    file: vi.fn((p: string) => ({ fsPath: p, toString: () => p })),
    joinPath: vi.fn((...args: unknown[]) => ({ fsPath: String(args.join("/")) })),
  },
  workspace: {
    fs: {
      readFile: mockReadFile,
      writeFile: mockWriteFile,
      delete: mockDelete,
      createDirectory: mockCreateDirectory,
    },
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    })),
    workspaceFolders: [],
  },
  EventEmitter: vi.fn(function EventEmitterMock() {
    return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  }),
  RelativePattern: vi.fn(),
  FileSystemError: class FileSystemError extends Error {
    code = "FileNotFound";
  },
  StatusBarAlignment: { Left: 1 },
  ViewColumn: { One: 1 },
}));

import { NightgaugeYamlService } from "../../../src/views/settings/NightgaugeYamlService";
import { runtimeWriteTierFor, isMachineTierPath } from "../../../src/views/settings/tierRouting";
import { serializePreservingComments } from "../../../src/views/settings/yamlDocumentWriter";

/** Mirrors the shape of the repository's committed team config. */
const TEAM_BODY = `# Safe public-core dogfood defaults.
#
# Autonomous execution and issue discovery are disabled by default so cloning
# the repository never authorizes work.
owner: nightgauge
repo: nightgauge

pipeline:
  budget_preset: conservative
  worktree_base: .worktrees

human_in_the_loop:
  auto_accept_stages: false
`;

let tmpRoot: string;
let teamPath: string;
let localPath: string;
let machinePath: string;
let previousConfigHome: string | undefined;
let service: NightgaugeYamlService;

beforeEach(() => {
  tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-1516-"));
  const workspace = path.join(tmpRoot, "workspace");
  const machineDir = path.join(tmpRoot, "machine");
  fs.mkdirSync(path.join(workspace, ".nightgauge"), { recursive: true });
  fs.mkdirSync(machineDir, { recursive: true });

  teamPath = path.join(workspace, ".nightgauge", "config.yaml");
  localPath = path.join(workspace, ".nightgauge", "config.local.yaml");
  machinePath = path.join(machineDir, "config.yaml");
  fs.writeFileSync(teamPath, TEAM_BODY);

  previousConfigHome = process.env.NIGHTGAUGE_CONFIG_HOME;
  process.env.NIGHTGAUGE_CONFIG_HOME = machineDir;

  service = new NightgaugeYamlService(workspace);
});

afterEach(() => {
  service.dispose();
  if (previousConfigHome === undefined) {
    delete process.env.NIGHTGAUGE_CONFIG_HOME;
  } else {
    process.env.NIGHTGAUGE_CONFIG_HOME = previousConfigHome;
  }
  fs.rmSync(tmpRoot, { recursive: true, force: true });
});

const read = (p: string): string => fs.readFileSync(p, "utf-8");

describe("runtime writes never target the committed team config (#1516)", () => {
  it("leaves .nightgauge/config.yaml byte-identical", async () => {
    const result = await service.writeRuntimeValue("human_in_the_loop.auto_accept_stages", true);

    expect(result.success).toBe(true);
    expect(read(teamPath)).toBe(TEAM_BODY);
  });

  it("lands the value in the local tier", async () => {
    await service.writeRuntimeValue("human_in_the_loop.auto_accept_stages", true);

    const local = read(localPath);
    expect(local).toContain("auto_accept_stages: true");
    // Only the addressed key — the team file's other blocks are not copied in.
    expect(local).not.toContain("budget_preset");
    expect(local).not.toContain("owner:");
  });

  it("routes a machine-tier key to the machine file, not the workspace", async () => {
    expect(isMachineTierPath("ui.core.default_model")).toBe(true);
    expect(runtimeWriteTierFor("ui.core.default_model")).toBe("global");

    const result = await service.writeRuntimeValue("ui.core.default_model", "opus");

    expect(result.success).toBe(true);
    expect(read(teamPath)).toBe(TEAM_BODY);
    expect(fs.existsSync(localPath)).toBe(false);
    expect(read(machinePath)).toContain("default_model: opus");
  });

  it("routes a key nested under a machine-tier key to the machine file too", () => {
    expect(isMachineTierPath("platform.license_key")).toBe(true);
    expect(runtimeWriteTierFor("platform.license_key")).toBe("global");
    expect(runtimeWriteTierFor("human_in_the_loop.auto_accept_stages")).toBe("local");
    expect(runtimeWriteTierFor("pipeline.budget_preset")).toBe("local");
  });

  it("never resolves a runtime write to the project tier", () => {
    for (const key of [
      "human_in_the_loop.auto_accept_stages",
      "pipeline.budget_preset",
      "platform.license_key",
      "owner",
    ]) {
      expect(runtimeWriteTierFor(key)).not.toBe("project");
    }
  });

  it("makes the merged view read the value written to the local tier", async () => {
    const before = await service.readMerged();
    expect(before.config.human_in_the_loop?.auto_accept_stages).toBe(false);

    await service.writeRuntimeValue("human_in_the_loop.auto_accept_stages", true);

    const after = await service.readMerged();
    expect(after.config.human_in_the_loop?.auto_accept_stages).toBe(true);
    // …and the team file still carries the safe default.
    expect(read(teamPath)).toBe(TEAM_BODY);
  });

  it("rejects a prototype-polluting key path", async () => {
    const result = await service.writeRuntimeValue("__proto__.polluted", true);
    expect(result.success).toBe(false);
    expect(({} as Record<string, unknown>)["polluted"]).toBeUndefined();
  });
});

describe("explicit team-tier writes preserve comments", () => {
  it("keeps the header and blank lines when the user edits the team tier", async () => {
    const result = await service.write({ pipeline: { budget_preset: "generous" } }, "project");

    expect(result.success).toBe(true);
    const out = read(teamPath);
    expect(out).toContain("# Safe public-core dogfood defaults.");
    expect(out).toContain("# the repository never authorizes work.");
    expect(out).toContain("budget_preset: generous");
    expect(out).toContain("worktree_base: .worktrees");
    // Blank-line separation between top-level blocks survives.
    expect(out).toContain("\n\npipeline:");
  });

  it("is a no-op on disk when the write changes nothing", async () => {
    const result = await service.write({}, "project");

    expect(result.success).toBe(true);
    expect(read(teamPath)).toBe(TEAM_BODY);
  });
});

describe("serializePreservingComments", () => {
  it("falls back to a plain render when there is no existing file", () => {
    const out = serializePreservingComments(null, { a: { b: 1 } });
    expect(out).toContain("a:");
    expect(out).toContain("b: 1");
  });

  it("falls back to a plain render when the existing text does not parse", () => {
    const out = serializePreservingComments("a: [unclosed\n", { a: 1 });
    expect(out.trim()).toBe("a: 1");
  });

  it("removes a key the caller dropped, and prunes the emptied parent", () => {
    const existing = "project:\n  number: 3\ntop: 1\n";
    const out = serializePreservingComments(existing, { top: 1 });
    expect(out).not.toContain("number");
    expect(out).not.toContain("project:");
    expect(out).toContain("top: 1");
  });

  it("preserves a comment attached to an unchanged sibling", () => {
    const existing = "# header\n\n# why this matters\nkeep: 1\nchange: 2\n";
    const out = serializePreservingComments(existing, { keep: 1, change: 3 });
    expect(out).toContain("# header");
    expect(out).toContain("# why this matters");
    expect(out).toContain("keep: 1");
    expect(out).toContain("change: 3");
  });
});
