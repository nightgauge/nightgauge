import { describe, it, expect, vi, beforeEach } from "vitest";
import { existsSync, readdirSync } from "fs";
import { runDimension3 } from "../src/dimensions/dimension-3.js";

// Mock fs module — dimension-3 calls parsers which use existsSync/readFileSync
vi.mock("fs");
// Mock child_process to avoid real greps
vi.mock("child_process", () => ({ execSync: vi.fn().mockReturnValue("") }));

const mockExistsSync = vi.mocked(existsSync);
const mockReaddirSync = vi.mocked(readdirSync);

beforeEach(() => {
  vi.resetAllMocks();
  // Default: all directories exist
  mockExistsSync.mockReturnValue(false);
  mockReaddirSync.mockReturnValue([]);
});

describe("runDimension3", () => {
  it("returns clean result when no repos found", async () => {
    // No repos discovered
    mockExistsSync.mockReturnValue(false);

    const result = await runDimension3({
      workspaceRoot: "/fake/workspace",
    });

    expect(result.schema_version).toBe("1.0");
    expect(result.dimension).toBe("documentation_accuracy");
    expect(result.findings).toBeInstanceOf(Array);
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
    expect(typeof result.timestamp).toBe("string");
  });

  it("uses repoPaths overrides", async () => {
    // Override with fake paths that don't exist
    mockExistsSync.mockImplementation((p) => {
      const path = p as string;
      return (
        path.includes("/fake/nightgauge") &&
        !path.includes("ECOSYSTEM") &&
        !path.includes("package.json") &&
        !path.includes("README") &&
        !path.includes("CONTRIBUTING") &&
        !path.includes("routes") &&
        !path.includes("src") &&
        !path.includes("api")
      );
    });

    const result = await runDimension3({
      workspaceRoot: "/fake/workspace",
      repoPaths: {
        nightgauge: "/fake/nightgauge",
      },
    });

    expect(result.schema_version).toBe("1.0");
    expect(result.findings).toBeInstanceOf(Array);
  });

  it("schema conforms to AuditDimension", async () => {
    mockExistsSync.mockReturnValue(false);

    const result = await runDimension3({ workspaceRoot: "/fake" });

    expect(result).toMatchObject({
      schema_version: "1.0",
      dimension: "documentation_accuracy",
      timestamp: expect.any(String),
      score: expect.any(Number),
      summary: expect.any(String),
      findings: expect.any(Array),
    });
    // No parity_matrix for dimension 3
    expect(result.parity_matrix).toBeUndefined();
  });

  it("score is within 0-100 range", async () => {
    mockExistsSync.mockReturnValue(false);
    const result = await runDimension3({ workspaceRoot: "/fake" });
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });
});
