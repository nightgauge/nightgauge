/**
 * ExtensionStalenessStatusItem.test.ts (#2721)
 *
 * A tagged (CI-built) extension never offers the local `dev-install.sh`
 * refresh; it states the installed version and the workspace's distance. A
 * local build keeps the refresh action.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const h = vi.hoisted(() => ({
  commands: new Map<string, () => unknown>(),
  executeCommand: vi.fn(),
  showWarningMessage: vi.fn(),
  showInformationMessage: vi.fn(),
}));

vi.mock("vscode", () => ({
  StatusBarAlignment: { Left: 1, Right: 2 },
  ThemeColor: class {
    constructor(public readonly id: string) {}
  },
  window: {
    createStatusBarItem: vi.fn(() => ({
      text: "",
      tooltip: "",
      backgroundColor: undefined as unknown,
      command: undefined as unknown,
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: h.showWarningMessage,
    showInformationMessage: h.showInformationMessage,
  },
  commands: {
    registerCommand: vi.fn((id: string, fn: () => unknown) => {
      h.commands.set(id, fn);
      return { dispose: vi.fn() };
    }),
    executeCommand: h.executeCommand,
  },
}));

import { ExtensionStalenessStatusItem } from "../../src/views/ExtensionStalenessStatusItem";
import type {
  ExtensionStalenessService,
  StalenessState,
} from "../../src/services/ExtensionStalenessService";

const REFRESH = "nightgauge.refreshExtensionFromMain";
const DETAILS = "nightgauge.showStalenessDetails";

function stale(build: { tag: string | null; version: string | null }): StalenessState {
  return {
    kind: "stale",
    buildSha: "a".repeat(40),
    currentSha: "b".repeat(40),
    build,
    commitsBehind: 7,
    criticalPathsChanged: ["internal/orchestrator/x.go"],
    otherPathsChanged: ["docs/a.md"],
  };
}

function fakeService(state: StalenessState): ExtensionStalenessService {
  return {
    getState: () => state,
    onChanged: () => ({ dispose: () => {} }),
  } as unknown as ExtensionStalenessService;
}

describe("ExtensionStalenessStatusItem (#2721)", () => {
  beforeEach(() => {
    h.commands.clear();
    vi.clearAllMocks();
  });

  it("tagged build: shows version and distance, never offers the local refresh", async () => {
    const item = new ExtensionStalenessStatusItem(
      fakeService(stale({ tag: "v0.8.1-rc.4", version: "0.8.1-rc.4" }))
    );
    expect(item.item.text).toContain("0.8.1-rc.4");
    expect(item.item.text).toContain("7 commits ahead");
    expect(String(item.item.tooltip)).not.toContain("dev-install");

    h.showInformationMessage.mockResolvedValue(undefined);
    await h.commands.get(DETAILS)!();
    expect(h.showWarningMessage).not.toHaveBeenCalled();
    const [message, options, ...actions] = h.showInformationMessage.mock.calls[0];
    expect(message).toContain("0.8.1-rc.4");
    expect(message).toContain("7 commits ahead");
    expect(message).toContain("release candidate");
    expect(options).toEqual({ modal: true });
    expect(actions).toEqual([]);
    expect(h.executeCommand).not.toHaveBeenCalled();
    item.dispose();
  });

  it("local build: offers the refresh and runs it when chosen", async () => {
    const item = new ExtensionStalenessStatusItem(fakeService(stale({ tag: null, version: null })));
    expect(item.item.text).toContain("Extension stale");

    h.showWarningMessage.mockResolvedValue("Refresh extension");
    await h.commands.get(DETAILS)!();
    expect(h.showWarningMessage.mock.calls[0]).toContain("Refresh extension");
    expect(h.executeCommand).toHaveBeenCalledWith(REFRESH);
    item.dispose();
  });
});
