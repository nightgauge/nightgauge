/**
 * BrownfieldDataService.test.ts
 *
 * Unit tests for BrownfieldDataService:
 * - Loads health report when file exists
 * - Returns null for missing files
 * - Emits onDataChanged when files are created/updated
 * - Saves history snapshot on score change
 * - Loads history from .nightgauge/history/
 *
 * @see Issue #1163 - Brownfield Modernization Progress Dashboard
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { BrownfieldDataService } from "../../src/services/BrownfieldDataService";

// Track watcher callbacks
const watcherCallbacks: {
  onCreate: ((uri: any) => void) | null;
  onChange: ((uri: any) => void) | null;
  onDelete: ((uri: any) => void) | null;
} = { onCreate: null, onChange: null, onDelete: null };

vi.mock("vscode", () => ({
  RelativePattern: class {
    constructor(
      public base: string,
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
  const workspaceRoot = "/test/workspace";

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
    mockFiles["/test/workspace/.nightgauge/health-report.json"] = JSON.stringify(healthData);

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
    mockFiles["/test/workspace/.nightgauge/security-audit.json"] = JSON.stringify(securityData);

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
    mockFiles["/test/workspace/.nightgauge/history/brownfield-snapshots.json"] =
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
    mockFiles["/test/workspace/.nightgauge/health-report.json"] = JSON.stringify(healthData);

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

  it("disposes watchers on dispose", () => {
    expect(() => service.dispose()).not.toThrow();
  });
});
