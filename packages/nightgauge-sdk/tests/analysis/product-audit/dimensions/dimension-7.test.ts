/**
 * Unit tests for Dimension 7: Dependencies
 *
 * Tests npm audit JSON parsing, version mismatch detection,
 * and cross-repo aggregation.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ── extractMajorVersion (via dimension-7 internal logic) ─────────────────────

// We test the version mismatch logic by exercising runDimension7 with mocked fs/exec

import { runDimension7 } from "../../../../src/analysis/product-audit/dimensions/dimension-7-dependencies.js";
import * as fs from "fs";
import * as childProcess from "child_process";

vi.mock("fs");
vi.mock("child_process");

const mockFs = vi.mocked(fs);
const mockExecSync = vi.mocked(childProcess.execSync);

describe("runDimension7", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockFs.existsSync.mockReturnValue(false);
  });

  it("marks repo as missing when root does not exist", async () => {
    const result = await runDimension7([{ name: "missing-repo", root: "/does/not/exist" }]);
    expect(result.repos_missing).toContain("missing-repo");
    expect(result.findings).toHaveLength(0);
  });

  it("skips npm audit when no package.json found", async () => {
    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      return p.toString() === "/repo";
    });
    mockFs.readdirSync.mockReturnValue([] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation(() => {
      throw new Error("not found");
    });

    const result = await runDimension7([{ name: "test", root: "/repo" }], "/config");
    expect(mockExecSync).not.toHaveBeenCalled();
    expect(result.findings).toHaveLength(0);
  });

  it("parses npm audit JSON and generates findings for critical vulnerabilities", async () => {
    const auditOutput = JSON.stringify({
      vulnerabilities: {
        lodash: {
          name: "lodash",
          severity: "critical",
          via: [{ cve: "CVE-2021-23337" }],
          range: "<4.17.21",
          nodes: [],
          fixAvailable: true,
        },
      },
    });

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo" || s === "/repo/package.json";
    });
    mockFs.readdirSync.mockReturnValue([] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      if (s === "/config/shared-dependencies.json") {
        return JSON.stringify({ ecosystems: { npm: { deps: [] } } });
      }
      throw new Error(`unexpected: ${s}`);
    });
    mockExecSync.mockReturnValue(auditOutput as unknown as Buffer);

    const result = await runDimension7([{ name: "nightgauge", root: "/repo" }], "/config");

    expect(result.findings.length).toBeGreaterThanOrEqual(1);
    const finding = result.findings[0];
    expect(finding.severity).toBe("critical");
    expect(finding.is_vulnerability).toBe(true);
    expect(finding.package_or_dependency).toBe("lodash");
    expect(finding.cve_ids).toContain("CVE-2021-23337");
    expect(finding.ecosystem).toBe("npm");
  });

  it("skips low-severity vulnerabilities", async () => {
    const auditOutput = JSON.stringify({
      vulnerabilities: {
        "some-pkg": {
          name: "some-pkg",
          severity: "low",
          via: [],
          range: "<1.0.0",
          nodes: [],
          fixAvailable: false,
        },
      },
    });

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo" || s === "/repo/package.json";
    });
    mockFs.readdirSync.mockReturnValue([] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation(() => {
      throw new Error("not found");
    });
    mockExecSync.mockReturnValue(auditOutput as unknown as Buffer);

    const result = await runDimension7([{ name: "test", root: "/repo" }], "/nonexistent");
    expect(result.findings).toHaveLength(0);
  });

  it("handles npm audit returning non-zero exit with stdout JSON", async () => {
    const auditOutput = JSON.stringify({
      vulnerabilities: {
        express: {
          name: "express",
          severity: "high",
          via: [],
          range: "<4.18.0",
          nodes: [],
          fixAvailable: true,
        },
      },
    });

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo" || s === "/repo/package.json";
    });
    mockFs.readdirSync.mockReturnValue([] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation(() => {
      throw new Error("not found");
    });

    // Simulate npm audit non-zero exit: throws with stdout
    mockExecSync.mockImplementation(() => {
      const err = new Error("exit 1") as Error & { stdout: string };
      err.stdout = auditOutput;
      throw err;
    });

    const result = await runDimension7([{ name: "test", root: "/repo" }], "/nonexistent");
    expect(result.findings.length).toBeGreaterThanOrEqual(1);
    expect(result.findings[0].package_or_dependency).toBe("express");
  });

  it("warns when npm audit produces no output", async () => {
    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo" || s === "/repo/package.json";
    });
    mockFs.readdirSync.mockReturnValue([] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation(() => {
      throw new Error("not found");
    });

    mockExecSync.mockImplementation(() => {
      const err = new Error("exit 2") as Error & {
        stdout: string;
        stderr: string;
      };
      err.stdout = "";
      err.stderr = "npm ERR! network timeout";
      throw err;
    });

    const result = await runDimension7([{ name: "test", root: "/repo" }], "/nonexistent");
    expect(result.warnings.some((w) => w.includes("npm audit failed"))).toBe(true);
  });

  it("detects version mismatch in shared dependencies", async () => {
    const sharedDepsConfig = JSON.stringify({
      ecosystems: {
        npm: {
          deps: [
            {
              name: "typescript",
              expected_major: 5,
              rationale: "TS 5 required",
              repos: ["repo-a", "repo-b"],
            },
          ],
        },
      },
    });

    const pkgA = JSON.stringify({ dependencies: { typescript: "^4.9.0" } });
    const pkgB = JSON.stringify({ dependencies: { typescript: "^5.4.0" } });

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return [
        "/repo-a",
        "/repo-b",
        "/repo-a/package.json",
        "/repo-b/package.json",
        "/config/shared-dependencies.json",
      ].includes(s);
    });
    mockFs.readdirSync.mockReturnValue([] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      if (s === "/config/shared-dependencies.json") return sharedDepsConfig;
      if (s === "/repo-a/package.json") return pkgA;
      if (s === "/repo-b/package.json") return pkgB;
      throw new Error(`unexpected: ${s}`);
    });
    mockExecSync.mockReturnValue("{}" as unknown as Buffer);

    const result = await runDimension7(
      [
        { name: "repo-a", root: "/repo-a" },
        { name: "repo-b", root: "/repo-b" },
      ],
      "/config"
    );

    const mismatchFinding = result.findings.find(
      (f) => f.category === "OUTDATED_DEPENDENCY" && f.package_or_dependency === "typescript"
    );
    expect(mismatchFinding).toBeDefined();
    expect(mismatchFinding?.detail).toContain("typescript");
  });

  it("sorts findings with critical first", async () => {
    const auditOutput = JSON.stringify({
      vulnerabilities: {
        "pkg-high": {
          name: "pkg-high",
          severity: "high",
          via: [],
          range: "<1",
          nodes: [],
          fixAvailable: false,
        },
        "pkg-critical": {
          name: "pkg-critical",
          severity: "critical",
          via: [],
          range: "<1",
          nodes: [],
          fixAvailable: false,
        },
      },
    });

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo" || s === "/repo/package.json";
    });
    mockFs.readdirSync.mockReturnValue([] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation(() => {
      throw new Error("not found");
    });
    mockExecSync.mockReturnValue(auditOutput as unknown as Buffer);

    const result = await runDimension7([{ name: "test", root: "/repo" }], "/nonexistent");
    expect(result.findings[0].severity).toBe("critical");
  });
});
