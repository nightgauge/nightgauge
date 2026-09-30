/**
 * BrownfieldDataService.test.ts
 *
 * Unit tests for BrownfieldDataService:
 * - Loads health report when file exists
 * - Returns null for missing files
 * - Emits onDataChanged when files are created/updated
 * - Saves history snapshot on score change
 * - Loads history from the checkout's brownfield-history/ (ADR-024 § 7)
 *
 * @see Issue #1163 - Brownfield Modernization Progress Dashboard
 */

import { describe, it, expect, vi, beforeEach, afterAll } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { BrownfieldDataService } from "../../src/services/BrownfieldDataService";
import { mkFakeCloneLayout } from "../helpers/cloneLayout";

// Track watcher callbacks
const watcherCallbacks: {
  onCreate: ((uri: any) => void) | null;
  onChange: ((uri: any) => void) | null;
  onDelete: ((uri: any) => void) | null;
} = { onCreate: null, onChange: null, onDelete: null };

vi.mock("vscode", () => ({
  RelativePattern: class {
    constructor(
      public base: { fsPath: string },
      public pattern: string
    ) {}
  },
  workspace: {
    createFileSystemWatcher: vi.fn(() => ({
      onDidCreate: vi.fn((cb: any) => {
        watcherCallbacks.onCreate = cb;
      }),
      onDidChange: vi.fn((cb: any) => {
        watcherCallbacks.onChange = cb;
      }),
      onDidDelete: vi.fn((cb: any) => {
        watcherCallbacks.onDelete = cb;
      }),
      dispose: vi.fn(),
    })),
  },
  EventEmitter: class {
    private listeners: Function[] = [];
    event = (listener: Function) => {
      this.listeners.push(listener);
      return { dispose: () => {} };
    };
    fire() {
      this.listeners.forEach((l) => l());
    }
    dispose() {}
  },
  Uri: {
    joinPath: vi.fn(),
    file: (p: string) => ({ fsPath: p, scheme: "file" }),
  },
}));

// Mock fs
const mockFiles: Record<string, string> = {};
vi.mock("node:fs/promises", () => ({
  default: {
    readFile: vi.fn(async (path: string) => {
      if (mockFiles[path]) return mockFiles[path];
      throw new Error("ENOENT");
    }),
    writeFile: vi.fn(async () => {}),
    mkdir: vi.fn(async () => {}),
  },
  readFile: vi.fn(async (path: string) => {
    if (mockFiles[path]) return mockFiles[path];
    throw new Error("ENOENT");
  }),
  writeFile: vi.fn(async () => {}),
  mkdir: vi.fn(async () => {}),
}));

describe("BrownfieldDataService", () => {
  let service: BrownfieldDataService;
  // A temp root with a fixed layout (no git); the reports dir the watcher
  // needs is created under it, never in the real checkout.
  const workspaceRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-brownfield-"));
  const CHECKOUT = mkFakeCloneLayout(workspaceRoot).checkout;
  const REPORTS = path.join(CHECKOUT, "reports");

  afterAll(() => fs.rmSync(workspaceRoot, { recursive: true, force: true }));

  beforeEach(() => {
    // Clear mock files
    Object.keys(mockFiles).forEach((k) => delete mockFiles[k]);
    watcherCallbacks.onCreate = null;
    watcherCallbacks.onChange = null;
    watcherCallbacks.onDelete = null;

    service = new BrownfieldDataService(workspaceRoot);
  });

  it("returns null for missing health report", async () => {
    const result = await service.loadHealth();
    expect(result).toBeNull();
  });

  it("loads health report when file exists", async () => {
    const healthData = {
      schema_version: "1.0",
      assessment_date: "2026-02-21",
      summary: {
        overall_health_score: 72,
        status: "good",
        dimensions_assessed: 6,
        dimensions_skipped: 0,
      },
      dimensions: {},
      top_recommendations: [],
      created_at: "2026-02-21T00:00:00Z",
    };
    mockFiles[path.join(REPORTS, "health-report.json")] = JSON.stringify(healthData);

    const result = await service.loadHealth();
    expect(result).not.toBeNull();
    expect(result!.summary.overall_health_score).toBe(72);
    expect(result!.summary.status).toBe("good");
  });

  it("returns null for missing security audit", async () => {
    const result = await service.loadSecurity();
    expect(result).toBeNull();
  });

  it("loads security audit when file exists", async () => {
    const securityData = {
      schema_version: "1.0",
      assessment_date: "2026-02-21",
      summary: {
        overall_security_score: 85,
        status: "good",
        dimensions_assessed: 7,
        dimensions_skipped: 0,
        total_findings: 3,
        findings_by_severity: {
          critical: 0,
          high: 1,
          medium: 2,
          low: 0,
          info: 0,
        },
      },
      dimensions: {},
      top_recommendations: [],
      created_at: "2026-02-21T00:00:00Z",
    };
    mockFiles[path.join(REPORTS, "security-audit.json")] = JSON.stringify(securityData);

    const result = await service.loadSecurity();
    expect(result).not.toBeNull();
    expect(result!.summary.overall_security_score).toBe(85);
  });

  it("returns null for missing modernization plan", async () => {
    const result = await service.loadPlan();
    expect(result).toBeNull();
  });

  it("returns null for missing dep modernize report", async () => {
    const result = await service.loadDeps();
    expect(result).toBeNull();
  });

  it("returns empty array for missing history", async () => {
    const result = await service.loadHistory();
    expect(result).toEqual([]);
  });

  it("loads history from JSON file", async () => {
    const history = [
      {
        timestamp: "2026-02-20T00:00:00Z",
        health_score: 60,
        security_score: 70,
        tasks_completed: 5,
        tasks_total: 20,
      },
    ];
    mockFiles[path.join(CHECKOUT, "brownfield-history", "brownfield-snapshots.json")] =
      JSON.stringify(history);

    const result = await service.loadHistory();
    expect(result).toHaveLength(1);
    expect(result[0].health_score).toBe(60);
  });

  it("loadAll returns dashboard data with hasAnyData=false when no files exist", async () => {
    const result = await service.loadAll();
    expect(result.hasAnyData).toBe(false);
    expect(result.health).toBeNull();
    expect(result.security).toBeNull();
    expect(result.plan).toBeNull();
    expect(result.deps).toBeNull();
  });

  it("loadAll returns hasAnyData=true when health report exists", async () => {
    const healthData = {
      schema_version: "1.0",
      assessment_date: "2026-02-21",
      summary: {
        overall_health_score: 50,
        status: "fair",
        dimensions_assessed: 6,
        dimensions_skipped: 0,
      },
      dimensions: {},
      top_recommendations: [],
      created_at: "2026-02-21T00:00:00Z",
    };
    mockFiles[path.join(REPORTS, "health-report.json")] = JSON.stringify(healthData);

    const result = await service.loadAll();
    expect(result.hasAnyData).toBe(true);
    expect(result.health).not.toBeNull();
  });

  it("emits onDataChanged when watcher fires", () => {
    let fired = false;
    service.onDataChanged(() => {
      fired = true;
    });

    // Simulate file creation
    if (watcherCallbacks.onCreate) {
      watcherCallbacks.onCreate({});
    }

    expect(fired).toBe(true);
  });

  it("watches the report files in the checkout's reports/ directory", async () => {
    const vscode = await import("vscode");
    const calls = vi.mocked(vscode.workspace.createFileSystemWatcher).mock.calls;
    const pattern = calls[calls.length - 1][0] as unknown as { base: { fsPath: string } };
    expect(pattern.base.fsPath).toBe(REPORTS);
    expect(fs.existsSync(REPORTS)).toBe(true);
  });

  it("reports no data and watches nothing when the root is not a git checkout", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.workspace.createFileSystemWatcher).mockClear();
    const unusable = new BrownfieldDataService("relative/root");
    expect(vscode.workspace.createFileSystemWatcher).not.toHaveBeenCalled();
    expect(await unusable.loadHealth()).toBeNull();
    expect(await unusable.loadHistory()).toEqual([]);
    unusable.dispose();
  });

  it("disposes watchers on dispose", () => {
    expect(() => service.dispose()).not.toThrow();
  });
});
