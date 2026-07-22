/**
 * Tests for AutomationService
 *
 * Covers:
 * 1. getLogEntries — returns empty array before initialize (no logPath)
 * 2. getLogEntries — parses JSONL and returns entries newest-first
 * 3. getLogEntries — respects the limit parameter
 * 4. getLogEntries — skips malformed JSONL lines
 * 5. getLogEntries — returns empty array when file read fails
 * 6. getEntriesForIssue — filters entries by issue number
 * 7. getEntriesByAction — filters entries by action type
 * 8. isEnabled — returns false when config doesn't exist
 * 9. isEnabled — returns false when automations.enabled is false
 * 10. isEnabled — returns true when automations section has triggers
 * 11. isEnabled — returns false when no triggers defined
 * 12. dispose — disposes EventEmitter without throwing
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { AutomationService } from "../../src/services/AutomationService";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("vscode", () => {
  const listeners: Record<string, Function[]> = {};
  return {
    EventEmitter: class {
      private _handlers: Array<(v: unknown) => void> = [];
      event = (cb: (v: unknown) => void) => {
        this._handlers.push(cb);
        return { dispose: () => {} };
      };
      fire(value: unknown) {
        for (const h of this._handlers) h(value);
      }
      dispose() {}
    },
    RelativePattern: class {
      constructor(
        public base: unknown,
        public pattern: string
      ) {}
    },
    Uri: { file: vi.fn((p: string) => ({ fsPath: p })) },
    workspace: {
      createFileSystemWatcher: vi.fn(() => ({
        onDidChange: vi.fn((cb: Function) => {
          listeners["change"] = [cb];
          return { dispose: vi.fn() };
        }),
        onDidCreate: vi.fn((cb: Function) => {
          listeners["create"] = [cb];
          return { dispose: vi.fn() };
        }),
        dispose: vi.fn(),
      })),
    },
    window: {
      setStatusBarMessage: vi.fn(),
      showWarningMessage: vi.fn().mockResolvedValue(undefined),
      showInformationMessage: vi.fn(),
      showErrorMessage: vi.fn(),
    },
  };
});

const mockReadFile = vi.fn();
const mockStat = vi.fn();
const mockMkdir = vi.fn().mockResolvedValue(undefined);
const mockWriteFile = vi.fn().mockResolvedValue(undefined);
const mockOpen = vi.fn();

vi.mock("node:fs/promises", () => ({
  readFile: (...args: unknown[]) => mockReadFile(...args),
  stat: (...args: unknown[]) => mockStat(...args),
  mkdir: (...args: unknown[]) => mockMkdir(...args),
  writeFile: (...args: unknown[]) => mockWriteFile(...args),
  open: (...args: unknown[]) => mockOpen(...args),
}));

const mockResolveConfigPath = vi.fn();
const mockLogDeprecationWarning = vi.fn();

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPath: (...args: unknown[]) => mockResolveConfigPath(...args),
  logDeprecationWarning: (...args: unknown[]) => mockLogDeprecationWarning(...args),
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const WORKSPACE_ROOT = "/workspace";
const DEFAULT_LOG_PATH = `${WORKSPACE_ROOT}/.nightgauge/logs/automation.log`;

function makeEntry(
  overrides: Partial<{
    timestamp: string;
    trigger: string;
    action: string;
    status: "success" | "error";
    issue: number;
    message: string;
    dry_run: boolean;
  }> = {}
) {
  return {
    timestamp: "2024-01-01T00:00:00Z",
    trigger: "stage_complete",
    action: "post_slack",
    status: "success" as const,
    issue: 42,
    message: "Posted",
    dry_run: false,
    ...overrides,
  };
}

function makeJSONL(entries: object[]): string {
  return entries.map((e) => JSON.stringify(e)).join("\n");
}

function makeMockPipelineStateService() {
  return {
    onStageComplete: vi.fn(() => ({ dispose: vi.fn() })),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("AutomationService", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Default: config does not exist → logPath falls back to default
    mockResolveConfigPath.mockResolvedValue({ exists: false });
    mockStat.mockRejectedValue(new Error("ENOENT"));
  });

  // -------------------------------------------------------------------------
  // getLogEntries — before initialize (logPath not set)
  // -------------------------------------------------------------------------

  it("getLogEntries — returns empty array when logPath is not set (before initialize)", async () => {
    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);

    const entries = await service.getLogEntries();

    expect(entries).toEqual([]);
    expect(mockReadFile).not.toHaveBeenCalled();
  });

  // -------------------------------------------------------------------------
  // getLogEntries — after initialize
  // -------------------------------------------------------------------------

  it("getLogEntries — parses JSONL and returns entries newest-first", async () => {
    const entry1 = makeEntry({ issue: 1, timestamp: "2024-01-01T00:00:00Z" });
    const entry2 = makeEntry({ issue: 2, timestamp: "2024-01-02T00:00:00Z" });
    const entry3 = makeEntry({ issue: 3, timestamp: "2024-01-03T00:00:00Z" });

    mockReadFile.mockResolvedValue(makeJSONL([entry1, entry2, entry3]));

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);
    await service.initialize();

    const entries = await service.getLogEntries();

    expect(entries).toHaveLength(3);
    // newest-first: entry3, entry2, entry1
    expect(entries[0].issue).toBe(3);
    expect(entries[1].issue).toBe(2);
    expect(entries[2].issue).toBe(1);
  });

  it("getLogEntries — respects the limit parameter", async () => {
    const rawEntries = Array.from({ length: 5 }, (_, i) => makeEntry({ issue: i + 1 }));
    mockReadFile.mockResolvedValue(makeJSONL(rawEntries));

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);
    await service.initialize();

    const entries = await service.getLogEntries(2);

    expect(entries).toHaveLength(2);
  });

  it("getLogEntries — skips malformed JSONL lines", async () => {
    const good = makeEntry({ issue: 10 });
    const content = [JSON.stringify(good), "not-json", JSON.stringify(good)].join("\n");
    mockReadFile.mockResolvedValue(content);

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);
    await service.initialize();

    const entries = await service.getLogEntries();

    expect(entries).toHaveLength(2);
    entries.forEach((e) => expect(e.issue).toBe(10));
  });

  it("getLogEntries — returns empty array when file read fails", async () => {
    mockReadFile.mockRejectedValue(new Error("ENOENT"));

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);
    await service.initialize();

    const entries = await service.getLogEntries();

    expect(entries).toEqual([]);
  });

  // -------------------------------------------------------------------------
  // getEntriesForIssue
  // -------------------------------------------------------------------------

  it("getEntriesForIssue — filters entries by issue number", async () => {
    const e1 = makeEntry({ issue: 42, action: "post_slack" });
    const e2 = makeEntry({ issue: 99, action: "add_label" });
    const e3 = makeEntry({ issue: 42, action: "request_review" });
    mockReadFile.mockResolvedValue(makeJSONL([e1, e2, e3]));

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);
    await service.initialize();

    const entries = await service.getEntriesForIssue(42);

    expect(entries).toHaveLength(2);
    expect(entries.every((e) => e.issue === 42)).toBe(true);
  });

  // -------------------------------------------------------------------------
  // getEntriesByAction
  // -------------------------------------------------------------------------

  it("getEntriesByAction — filters entries by action type", async () => {
    const e1 = makeEntry({ action: "post_slack", issue: 1 });
    const e2 = makeEntry({ action: "add_label", issue: 2 });
    const e3 = makeEntry({ action: "post_slack", issue: 3 });
    mockReadFile.mockResolvedValue(makeJSONL([e1, e2, e3]));

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);
    await service.initialize();

    const entries = await service.getEntriesByAction("post_slack");

    expect(entries).toHaveLength(2);
    expect(entries.every((e) => e.action === "post_slack")).toBe(true);
  });

  // -------------------------------------------------------------------------
  // isEnabled
  // -------------------------------------------------------------------------

  it("isEnabled — returns false when config does not exist", async () => {
    mockResolveConfigPath.mockResolvedValue({ exists: false });

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);

    expect(await service.isEnabled()).toBe(false);
  });

  it("isEnabled — returns false when automations.enabled is false", async () => {
    mockResolveConfigPath.mockResolvedValue({
      exists: true,
      isLegacy: false,
      path: `${WORKSPACE_ROOT}/.nightgauge/config.yaml`,
    });
    mockReadFile.mockResolvedValue(
      `automations:\n  enabled: false\n  triggers:\n    - name: test\n`
    );

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);

    expect(await service.isEnabled()).toBe(false);
  });

  it("isEnabled — returns true when automations section has triggers", async () => {
    mockResolveConfigPath.mockResolvedValue({
      exists: true,
      isLegacy: false,
      path: `${WORKSPACE_ROOT}/.nightgauge/config.yaml`,
    });
    mockReadFile.mockResolvedValue(
      `automations:\n  triggers:\n    - name: test\n      trigger: stage_complete\n`
    );

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);

    expect(await service.isEnabled()).toBe(true);
  });

  it("isEnabled — returns false when no triggers defined", async () => {
    mockResolveConfigPath.mockResolvedValue({
      exists: true,
      isLegacy: false,
      path: `${WORKSPACE_ROOT}/.nightgauge/config.yaml`,
    });
    mockReadFile.mockResolvedValue(`automations:\n  dry_run: true\n`);

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);

    expect(await service.isEnabled()).toBe(false);
  });

  // -------------------------------------------------------------------------
  // initialize — default log path when config not found
  // -------------------------------------------------------------------------

  it("initialize — uses default log path when config does not exist", async () => {
    mockResolveConfigPath.mockResolvedValue({ exists: false });
    mockReadFile.mockResolvedValue("");

    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);
    await service.initialize();

    // After initialize, getLogEntries should use the default path
    await service.getLogEntries();
    expect(mockReadFile).toHaveBeenCalledWith(DEFAULT_LOG_PATH, "utf-8");
  });

  // -------------------------------------------------------------------------
  // dispose
  // -------------------------------------------------------------------------

  it("dispose — disposes EventEmitter without throwing", () => {
    const service = new AutomationService(makeMockPipelineStateService() as never, WORKSPACE_ROOT);

    expect(() => service.dispose()).not.toThrow();
  });
});
