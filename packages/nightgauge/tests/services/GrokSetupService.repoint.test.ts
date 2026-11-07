/**
 * Re-pointing a Grok plugin installed from a version-keyed extension
 * directory to the stable marketplace copy (#2802).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { resetMainChannelForTests } from "../../src/utils/logger";

const mockExecAsync = vi.hoisted(() => vi.fn());
const appended = vi.hoisted(() => [] as string[]);

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      show: vi.fn(),
      appendLine: vi.fn((line: string) => appended.push(line.replace(/^\[grok-setup\] /, ""))),
      dispose: vi.fn(),
    })),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    showWarningMessage: vi.fn(),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({ get: vi.fn().mockReturnValue(true) })),
    workspaceFolders: [],
  },
}));

vi.mock("child_process", () => ({ exec: vi.fn(), execFile: vi.fn() }));
vi.mock("util", () => ({ promisify: vi.fn(() => mockExecAsync) }));

interface Entry {
  name: string;
  source: string;
}

function cliError(message: string): Error {
  return Object.assign(new Error(`Command failed: grok\n${message}`), { stderr: message });
}

/** A stateful fake grok: `plugins` is the installed list; uninstall drops the first entry. */
function fakeGrok(opts: { plugins: Entry[]; addError?: Error; cli?: boolean }): {
  plugins: Entry[];
} {
  const state = { plugins: [...opts.plugins] };
  mockExecAsync.mockImplementation(async (cmd: unknown, args?: unknown) => {
    const argv = Array.isArray(args) ? (args as string[]) : [];
    const command = typeof cmd === "string" ? cmd : "";
    if (opts.cli === false) {
      throw Object.assign(new Error("spawn grok ENOENT"), { code: "ENOENT" });
    }
    if (command.includes("--version")) return { stdout: "grok 1.0.0", stderr: "" };
    if (argv[1] === "list") return { stdout: JSON.stringify(state.plugins), stderr: "" };
    if (argv[1] === "marketplace" && argv[2] === "add") {
      if (opts.addError) throw opts.addError;
      return { stdout: "", stderr: "" };
    }
    if (argv[1] === "uninstall") {
      state.plugins.shift();
      return { stdout: "", stderr: "" };
    }
    if (argv[1] === "install") {
      state.plugins.push({ name: "nightgauge", source: argv[2] });
      return { stdout: "", stderr: "" };
    }
    return { stdout: "", stderr: "" };
  });
  return state;
}

function makeContext(extensionPath: string, installedVersion?: string) {
  return {
    globalState: {
      get: vi.fn((key: string, def?: unknown) =>
        key === "nightgauge.grokSetup.installedVersion" ? (installedVersion ?? def) : def
      ),
      update: vi.fn().mockResolvedValue(undefined),
    },
    extensionPath,
    globalStorageUri: {
      fsPath: path.join(path.dirname(path.dirname(extensionPath)), "globalStorage"),
    },
    extension: { packageJSON: { version: "2.0.0" } },
  };
}

function calls(sub: string): string[][] {
  return mockExecAsync.mock.calls
    .map((c) => (Array.isArray(c[1]) ? (c[1] as string[]) : []))
    .filter((argv) => argv[1] === sub);
}

describe("GrokSetupService re-point (#2802)", () => {
  let tmpRoot: string;
  let extensionPath: string;
  let stable: string;
  let stablePlugin: string;
  let stalePlugin: string;
  let prevGrokHome: string | undefined;

  beforeEach(async () => {
    vi.clearAllMocks();
    appended.length = 0;
    resetMainChannelForTests();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-grok-repoint-"));
    extensionPath = path.join(tmpRoot, "extensions", "nightgauge.nightgauge-1.0.0");
    const dist = path.join(extensionPath, "dist");
    stalePlugin = path.join(dist, "claude-plugins", "nightgauge");
    fs.mkdirSync(path.join(stalePlugin, "skills", "issue-create"), { recursive: true });
    fs.writeFileSync(path.join(stalePlugin, "skills", "issue-create", "SKILL.md"), "# x\n");
    fs.mkdirSync(path.join(dist, ".claude-plugin"), { recursive: true });
    fs.writeFileSync(path.join(dist, ".claude-plugin", "marketplace.json"), "{}");
    fs.mkdirSync(path.join(dist, "skills", "nightgauge-issue-create"), { recursive: true });
    fs.writeFileSync(path.join(dist, "skills", "nightgauge-issue-create", "SKILL.md"), "# s\n");
    stable = path.join(tmpRoot, "globalStorage", "claude-marketplace");
    stablePlugin = path.join(stable, "claude-plugins", "nightgauge");
    prevGrokHome = process.env.GROK_HOME;
    process.env.GROK_HOME = path.join(tmpRoot, "grok-home");
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Install" as never);
  });

  afterEach(() => {
    if (prevGrokHome === undefined) delete process.env.GROK_HOME;
    else process.env.GROK_HOME = prevGrokHome;
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  async function runSetup(installedVersion?: string, viaActivation = false): Promise<void> {
    if (installedVersion) {
      // An earlier version installed the skills, so activation re-syncs.
      const skill = path.join(tmpRoot, "grok-home", "skills", "nightgauge-issue-create");
      fs.mkdirSync(skill, { recursive: true });
      fs.writeFileSync(path.join(skill, "SKILL.md"), "# old\n");
      fs.writeFileSync(path.join(skill, ".nightgauge-installed"), "x");
    }
    const { GrokSetupService } = await import("../../src/services/GrokSetupService");
    const service = new GrokSetupService(makeContext(extensionPath, installedVersion) as never);
    if (viaActivation) await service.checkAndPromptSetup();
    else await service.showSetupPrompt();
  }

  const warnings = (): string[] => appended.filter((l) => l.startsWith("WARNING"));
  const already = (): Error => cliError(`Error: Marketplace source already configured: ${stable}`);

  it("fresh install adds the stable source and installs from it, silently", async () => {
    const state = fakeGrok({ plugins: [] });
    await runSetup();
    expect(calls("marketplace")).toContainEqual(["plugin", "marketplace", "add", stable]);
    expect(calls("install")).toEqual([["plugin", "install", stablePlugin, "--trust"]]);
    expect(state.plugins).toEqual([{ name: "nightgauge", source: stablePlugin }]);
    expect(warnings()).toEqual([]);
  });

  it("re-points a version-keyed entry by uninstall then install from stable", async () => {
    const state = fakeGrok({
      plugins: [{ name: "nightgauge", source: stalePlugin }],
      addError: already(),
    });
    await runSetup();
    expect(calls("uninstall")).toEqual([["plugin", "uninstall", "nightgauge", "--confirm"]]);
    expect(calls("install")).toEqual([["plugin", "install", stablePlugin, "--trust"]]);
    expect(state.plugins).toEqual([{ name: "nightgauge", source: stablePlugin }]);
    expect(calls("marketplace").some((a) => a[2] === "remove")).toBe(false);
    expect(appended).toContain(`✓ Grok plugin repointed to ${stablePlugin}`);
    expect(warnings()).toEqual([]);
  });

  it("is a no-op when only the stable entry exists", async () => {
    fakeGrok({ plugins: [{ name: "nightgauge", source: stablePlugin }], addError: already() });
    await runSetup();
    expect(calls("install")).toEqual([]);
    expect(calls("uninstall")).toEqual([]);
    expect(calls("update")).toEqual([]);
    expect(warnings()).toEqual([]);
  });

  it("warns on a genuine marketplace add failure", async () => {
    fakeGrok({ plugins: [], addError: cliError("network unreachable") });
    await runSetup();
    expect(
      warnings().some((l) => l.startsWith("WARNING: grok plugin marketplace add failed"))
    ).toBe(true);
  });

  it("keeps uninstalling until no stale entry remains, then reinstalls stable", async () => {
    const state = fakeGrok({
      plugins: [
        { name: "nightgauge", source: stablePlugin },
        { name: "nightgauge", source: stalePlugin },
      ],
      addError: already(),
    });
    await runSetup();
    expect(calls("uninstall")).toHaveLength(2);
    expect(state.plugins).toEqual([{ name: "nightgauge", source: stablePlugin }]);
    expect(warnings()).toEqual([]);
  });

  it("re-points on the activation upgrade re-sync", async () => {
    const state = fakeGrok({ plugins: [{ name: "nightgauge", source: stalePlugin }] });
    await runSetup("1.0.0", true);
    expect(calls("uninstall")).toHaveLength(1);
    expect(state.plugins).toEqual([{ name: "nightgauge", source: stablePlugin }]);
  });

  it("leaves a stable entry alone on the upgrade re-sync", async () => {
    fakeGrok({ plugins: [{ name: "nightgauge", source: stablePlugin }] });
    await runSetup("1.0.0", true);
    expect(calls("list")).toHaveLength(1);
    expect(calls("install")).toEqual([]);
    expect(calls("uninstall")).toEqual([]);
  });

  it("issues no plugin calls on the upgrade re-sync without the grok CLI", async () => {
    fakeGrok({ plugins: [], cli: false });
    await runSetup("1.0.0", true);
    expect(mockExecAsync.mock.calls.filter((c) => Array.isArray(c[1]))).toEqual([]);
  });
});
