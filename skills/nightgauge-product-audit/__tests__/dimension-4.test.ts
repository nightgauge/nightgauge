import { describe, it, expect, vi, beforeEach } from "vitest";
import { existsSync } from "fs";
import { execFileSync } from "child_process";
import { runDimension4 } from "../src/dimensions/dimension-4.js";
import { computeParityScore, checkFeatureForClient } from "../src/matchers/feature-matcher.js";
import type { FeatureResult } from "../src/types.js";

vi.mock("fs");
vi.mock("child_process");

const mockExistsSync = vi.mocked(existsSync);
const mockExecFileSync = vi.mocked(execFileSync);

const FAKE_FEATURES_YAML = `
features:
  - name: "Epic Dashboard"
    vscode:
      patterns:
        - "class EpicDashboard"
      test_patterns:
        - "EpicDashboard.test"
    angular:
      patterns:
        - "EpicDashboardComponent"
    flutter:
      patterns:
        - "EpicListScreen"
  - name: "Queue Management"
    vscode:
      patterns:
        - "QueueManager"
      test_patterns:
        - "QueueManager.test"
    angular:
      patterns:
        - "QueueComponent"
    flutter:
      patterns:
        - "QueueScreen"
`;

beforeEach(() => {
  vi.resetAllMocks();
  mockExistsSync.mockReturnValue(false);
  mockExecFileSync.mockReturnValue("" as ReturnType<typeof execFileSync>);
});

describe("runDimension4", () => {
  it("returns early with empty features message when features.yaml missing", async () => {
    mockExistsSync.mockReturnValue(false);

    const result = await runDimension4({
      featuresYamlPath: "/nonexistent/features.yaml",
      workspaceRoot: "/fake",
    });

    expect(result.schema_version).toBe("1.0");
    expect(result.dimension).toBe("feature_parity");
    expect(result.summary).toContain("No features defined");
    expect(result.findings).toHaveLength(0);
  });

  it("loads features and returns parity matrix when features exist", async () => {
    const { readFileSync } = await import("fs");
    const mockReadFileSync = vi.mocked(readFileSync);

    // features.yaml exists, client dirs do not
    mockExistsSync.mockImplementation((p) => {
      const path = p as string;
      return path.endsWith("features.yaml");
    });
    mockReadFileSync.mockReturnValue(FAKE_FEATURES_YAML);

    const result = await runDimension4({
      featuresYamlPath: "/fake/features.yaml",
      workspaceRoot: "/fake/workspace",
      clientDirs: {
        vscode: "/nonexistent/vscode",
        angular: "/nonexistent/angular",
        flutter: "/nonexistent/flutter",
      },
    });

    expect(result.schema_version).toBe("1.0");
    expect(result.dimension).toBe("feature_parity");
    expect(result.parity_matrix).toBeDefined();
    expect(result.parity_matrix?.features).toHaveLength(2);
    expect(result.parity_matrix?.clients).toHaveProperty("vscode");
    expect(result.parity_matrix?.clients).toHaveProperty("angular");
    expect(result.parity_matrix?.clients).toHaveProperty("flutter");
  });

  it("schema conforms to AuditDimension", async () => {
    mockExistsSync.mockReturnValue(false);

    const result = await runDimension4({
      featuresYamlPath: "/nonexistent/features.yaml",
      workspaceRoot: "/fake",
    });

    expect(result).toMatchObject({
      schema_version: "1.0",
      dimension: "feature_parity",
      timestamp: expect.any(String),
      score: expect.any(Number),
      summary: expect.any(String),
      findings: expect.any(Array),
    });
  });

  it("score is within 0-100 range", async () => {
    mockExistsSync.mockReturnValue(false);
    const result = await runDimension4({
      featuresYamlPath: "/nonexistent/features.yaml",
      workspaceRoot: "/fake",
    });
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });
});

describe("computeParityScore", () => {
  it("returns 0 for empty results", () => {
    expect(computeParityScore([])).toBe(0);
  });

  it("returns 1.0 for all FULL", () => {
    const results: FeatureResult[] = [
      {
        feature: "F1",
        client: "vscode",
        status: "FULL",
        confidence: 1,
        evidence: [],
      },
      {
        feature: "F2",
        client: "vscode",
        status: "FULL",
        confidence: 1,
        evidence: [],
      },
    ];
    expect(computeParityScore(results)).toBe(1.0);
  });

  it("returns 0.5 for all PARTIAL", () => {
    const results: FeatureResult[] = [
      {
        feature: "F1",
        client: "vscode",
        status: "PARTIAL",
        confidence: 0.7,
        evidence: [],
      },
      {
        feature: "F2",
        client: "vscode",
        status: "PARTIAL",
        confidence: 0.7,
        evidence: [],
      },
    ];
    expect(computeParityScore(results)).toBe(0.5);
  });

  it("returns 0 for all MISSING", () => {
    const results: FeatureResult[] = [
      {
        feature: "F1",
        client: "vscode",
        status: "MISSING",
        confidence: 0,
        evidence: [],
      },
    ];
    expect(computeParityScore(results)).toBe(0);
  });

  it("computes mixed score correctly", () => {
    // 2 FULL + 2 PARTIAL + 2 MISSING = (2 + 0.5*2 + 0) / 6 = 3/6 = 0.5
    const results: FeatureResult[] = [
      {
        feature: "F1",
        client: "vscode",
        status: "FULL",
        confidence: 1,
        evidence: [],
      },
      {
        feature: "F2",
        client: "vscode",
        status: "FULL",
        confidence: 1,
        evidence: [],
      },
      {
        feature: "F3",
        client: "vscode",
        status: "PARTIAL",
        confidence: 0.5,
        evidence: [],
      },
      {
        feature: "F4",
        client: "vscode",
        status: "PARTIAL",
        confidence: 0.5,
        evidence: [],
      },
      {
        feature: "F5",
        client: "vscode",
        status: "MISSING",
        confidence: 0,
        evidence: [],
      },
      {
        feature: "F6",
        client: "vscode",
        status: "MISSING",
        confidence: 0,
        evidence: [],
      },
    ];
    expect(computeParityScore(results)).toBeCloseTo(0.5);
  });
});

describe("checkFeatureForClient", () => {
  it("returns MISSING when client dir not found", () => {
    mockExistsSync.mockReturnValue(false);

    const result = checkFeatureForClient(
      {
        name: "Epic Dashboard",
        vscode: { patterns: ["class EpicDashboard"] },
      },
      "vscode",
      "/nonexistent/dir"
    );

    expect(result.status).toBe("MISSING");
    expect(result.confidence).toBe(0);
  });

  it("returns MISSING when no client definition", () => {
    mockExistsSync.mockReturnValue(true);

    const result = checkFeatureForClient(
      { name: "Epic Dashboard" }, // no vscode definition
      "vscode",
      "/fake/dir"
    );

    expect(result.status).toBe("MISSING");
  });

  it("returns FULL when code and test patterns both match", () => {
    mockExistsSync.mockReturnValue(true);
    // Two grep calls: one for code pattern, one for test pattern
    mockExecFileSync
      .mockReturnValueOnce("/fake/dir/EpicDashboard.ts\n" as ReturnType<typeof execFileSync>)
      .mockReturnValueOnce("/fake/dir/EpicDashboard.test.ts\n" as ReturnType<typeof execFileSync>);

    const result = checkFeatureForClient(
      {
        name: "Epic Dashboard",
        vscode: {
          patterns: ["class EpicDashboard"],
          test_patterns: ["EpicDashboard.test"],
        },
      },
      "vscode",
      "/fake/dir"
    );

    expect(result.status).toBe("FULL");
    expect(result.confidence).toBeGreaterThan(0);
  });

  it("returns PARTIAL when only code patterns match", () => {
    mockExistsSync.mockReturnValue(true);
    // Code pattern matches, test pattern does not
    mockExecFileSync
      .mockReturnValueOnce(
        "/fake/dir/EpicDashboard.ts\n/fake/dir/other.ts\n" as ReturnType<typeof execSync>
      )
      .mockReturnValueOnce("" as ReturnType<typeof execFileSync>);

    const result = checkFeatureForClient(
      {
        name: "Epic Dashboard",
        vscode: {
          patterns: ["class EpicDashboard"],
          test_patterns: ["EpicDashboard.test"],
        },
      },
      "vscode",
      "/fake/dir"
    );

    expect(result.status).toBe("PARTIAL");
  });

  it("returns STUB when exactly one code match and no tests", () => {
    mockExistsSync.mockReturnValue(true);
    // Single code match
    mockExecFileSync
      .mockReturnValueOnce("/fake/dir/one.ts\n" as ReturnType<typeof execFileSync>)
      .mockReturnValueOnce("" as ReturnType<typeof execFileSync>);

    const result = checkFeatureForClient(
      {
        name: "Epic Dashboard",
        vscode: {
          patterns: ["class EpicDashboard"],
          test_patterns: ["EpicDashboard.test"],
        },
      },
      "vscode",
      "/fake/dir"
    );

    expect(result.status).toBe("STUB");
  });
});
