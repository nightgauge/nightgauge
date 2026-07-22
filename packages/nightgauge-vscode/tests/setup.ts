/**
 * Test setup - mocks for VSCode API
 */
import * as os from "node:os";
import * as path from "node:path";
import { vi } from "vitest";
import { DEFAULT_CONFIG } from "../src/config/schema";

// Hermetic config tiers: resolver reads merge the machine tier
// (~/.nightgauge/config.yaml on macOS) via mergedConfigReader. Tests must
// never observe the developer's real machine config — that would make results
// machine-dependent (green in CI, red locally, or vice versa). Point the
// global-config resolver at a directory that does not exist; tests that
// exercise the machine tier explicitly set NIGHTGAUGE_CONFIG_HOME to
// their own fixture directory.
process.env.NIGHTGAUGE_CONFIG_HOME ??= path.join(os.tmpdir(), "nightgauge-tests-no-machine-tier");

// #4044: the HeadlessOrchestrator auth gate now injects a real preflight runner
// that probes CLI auth (codex login status / claude auth status). Unit tests run
// in environments without that auth (and must not depend on it), so skip the
// gate suite-wide via the documented env escape hatch. A test that specifically
// exercises the gate (the #4044 regression test) deletes this for its case.
process.env.NIGHTGAUGE_SKIP_AUTH_PREFLIGHT = "1";

// Create a shared mock ConfigBridge instance
export const mockConfigBridgeInstance = {
  isInitialized: vi.fn().mockReturnValue(true),
  getUI: vi.fn().mockReturnValue(DEFAULT_CONFIG.ui),
  onConfigChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
  getEffectiveConfig: vi.fn().mockReturnValue({ config: DEFAULT_CONFIG }),
  getPipeline: vi.fn().mockReturnValue(DEFAULT_CONFIG.pipeline),
  getLmStudio: vi.fn().mockReturnValue(DEFAULT_CONFIG.lm_studio),
};

/**
 * Helper to set custom UI config values for tests.
 * Call this in beforeEach to customize ConfigBridge return values.
 *
 * @param uiOverrides - Partial UI config to merge with defaults
 */
export function setMockUIConfig(uiOverrides: Partial<typeof DEFAULT_CONFIG.ui>) {
  const mergedUI = {
    ...DEFAULT_CONFIG.ui,
    ...uiOverrides,
    // Deep merge nested objects
    ready_items: {
      ...DEFAULT_CONFIG.ui?.ready_items,
      ...uiOverrides?.ready_items,
      filters: {
        ...DEFAULT_CONFIG.ui?.ready_items?.filters,
        ...uiOverrides?.ready_items?.filters,
      },
    },
    project_board: {
      ...DEFAULT_CONFIG.ui?.project_board,
      ...uiOverrides?.project_board,
    },
    sidebar: {
      ...DEFAULT_CONFIG.ui?.sidebar,
      ...uiOverrides?.sidebar,
    },
    pipeline: {
      ...DEFAULT_CONFIG.ui?.pipeline,
      ...uiOverrides?.pipeline,
    },
    output_window: {
      ...DEFAULT_CONFIG.ui?.output_window,
      ...uiOverrides?.output_window,
    },
  };
  mockConfigBridgeInstance.getUI.mockReturnValue(mergedUI);
}

/**
 * Reset ConfigBridge mock to defaults.
 * Call this in beforeEach to ensure clean state.
 */
export function resetMockConfigBridge() {
  mockConfigBridgeInstance.isInitialized.mockReturnValue(true);
  mockConfigBridgeInstance.getUI.mockReturnValue(DEFAULT_CONFIG.ui);
  mockConfigBridgeInstance.getLmStudio.mockReturnValue(DEFAULT_CONFIG.lm_studio);
  mockConfigBridgeInstance.onConfigChanged.mockReturnValue({
    dispose: vi.fn(),
  });
}

// Mock ConfigBridge service
vi.mock("../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(() => mockConfigBridgeInstance),
    resetInstance: vi.fn(),
  },
}));

// Mock vscode module
vi.mock("vscode", () => ({
  ProgressLocation: {
    SourceControl: 1,
    Window: 10,
    Notification: 15,
  },
  TreeItemCollapsibleState: {
    None: 0,
    Collapsed: 1,
    Expanded: 2,
  },
  TreeItem: class TreeItem {
    label: string;
    collapsibleState: number;
    iconPath?: any;
    contextValue?: string;
    description?: string;
    tooltip?: any;
    command?: any;

    constructor(label: string, collapsibleState: number = 0) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }
  },
  ThemeIcon: class ThemeIcon {
    id: string;
    color?: any;

    constructor(id: string, color?: any) {
      this.id = id;
      this.color = color;
    }
  },
  ThemeColor: class ThemeColor {
    id: string;

    constructor(id: string) {
      this.id = id;
    }
  },
  MarkdownString: class MarkdownString {
    value: string = "";
    isTrusted?: boolean;

    appendMarkdown(value: string) {
      this.value += value;
      return this;
    }
  },
  Uri: {
    parse: (value: string) => ({
      scheme: "https",
      authority: "",
      path: value,
      query: "",
      fragment: "",
      fsPath: value,
      toString: () => value,
    }),
    file: (path: string) => ({
      scheme: "file",
      authority: "",
      path,
      query: "",
      fragment: "",
      fsPath: path,
      toString: () => path,
    }),
  },
  EventEmitter: class EventEmitter {
    event: any = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
  RelativePattern: class RelativePattern {
    constructor(
      public base: string,
      public pattern: string
    ) {}
  },
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn(),
    })),
    onDidChangeConfiguration: vi.fn(),
    workspaceFolders: undefined,
    createFileSystemWatcher: vi.fn(() => ({
      onDidCreate: vi.fn(),
      onDidChange: vi.fn(),
      onDidDelete: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  window: {
    createTreeView: vi.fn(),
    createStatusBarItem: vi.fn(() => ({
      text: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    // Return resolved Promises so caller `.then(...)` chains don't throw
    // TypeError on undefined. Several callers (e.g. IpcClientBase backend
    // restart prompt) chain `.then` directly on the returned thenable.
    showErrorMessage: vi.fn(() => Promise.resolve(undefined)),
    showWarningMessage: vi.fn(() => Promise.resolve(undefined)),
    showInformationMessage: vi.fn(() => Promise.resolve(undefined)),
    onDidChangeActiveTextEditor: vi.fn(() => ({ dispose: vi.fn() })),
    // Terminal API — used by the Codex interactive TUI path (#4024).
    terminals: [],
    createTerminal: vi.fn(() => ({
      name: "mock-terminal",
      show: vi.fn(),
      sendText: vi.fn(),
      dispose: vi.fn(),
      exitStatus: undefined,
    })),
    onDidCloseTerminal: vi.fn(() => ({ dispose: vi.fn() })),
  },
  commands: {
    executeCommand: vi.fn(),
    registerCommand: vi.fn((command: string, handler: any) => ({
      dispose: vi.fn(),
    })),
  },
  TreeItemCheckboxState: {
    Checked: 1,
    Unchecked: 0,
  },
  StatusBarAlignment: {
    Left: 1,
    Right: 2,
  },
  DataTransferItem: class DataTransferItem {
    value: any;
    constructor(value: any) {
      this.value = value;
    }
  },
  extensions: {
    getExtension: vi.fn(() => undefined),
  },
}));
