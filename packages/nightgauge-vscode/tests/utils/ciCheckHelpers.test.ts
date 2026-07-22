/**
 * Tests for CI check helper functions
 *
 * @see Issue #426 - CI check gate and auto-fix retry loop
 */

import { describe, it, expect } from "vitest";
import {
  classifyCIFailure,
  isTransientCIFailure,
  parseCICheckStatus,
  extractFailedChecksFromOutput,
  canAutoFixFailures,
  getFailureTypeDescription,
  prioritizeFailuresForAutoFix,
  hasRepeatingFailures,
  type CICheckFailure,
} from "../../src/utils/ciCheckHelpers";

describe("ciCheckHelpers", () => {
  describe("classifyCIFailure", () => {
    it("should classify lint checks", () => {
      expect(classifyCIFailure("lint")).toBe("lint");
      expect(classifyCIFailure("eslint")).toBe("lint");
      expect(classifyCIFailure("lint (eslint)")).toBe("lint");
      expect(classifyCIFailure("code-quality")).toBe("lint");
      expect(classifyCIFailure("style check")).toBe("lint");
    });

    it("should classify test checks", () => {
      expect(classifyCIFailure("test")).toBe("test");
      expect(classifyCIFailure("unit tests")).toBe("test");
      expect(classifyCIFailure("test / vitest")).toBe("test");
      expect(classifyCIFailure("e2e tests")).toBe("test");
      expect(classifyCIFailure("integration-test")).toBe("test");
      expect(classifyCIFailure("coverage")).toBe("test");
    });

    it("should classify build checks", () => {
      expect(classifyCIFailure("build")).toBe("build");
      expect(classifyCIFailure("compile")).toBe("build");
      expect(classifyCIFailure("webpack build")).toBe("build");
      expect(classifyCIFailure("vite build")).toBe("build");
      expect(classifyCIFailure("bundle")).toBe("build");
    });

    it("should classify typecheck checks", () => {
      expect(classifyCIFailure("typecheck")).toBe("typecheck");
      expect(classifyCIFailure("tsc")).toBe("typecheck");
      expect(classifyCIFailure("typescript")).toBe("typecheck");
      expect(classifyCIFailure("type-check")).toBe("typecheck");
    });

    it("should classify security checks", () => {
      expect(classifyCIFailure("security")).toBe("security");
      expect(classifyCIFailure("security audit")).toBe("security");
      expect(classifyCIFailure("CodeQL")).toBe("security");
      expect(classifyCIFailure("snyk")).toBe("security");
      expect(classifyCIFailure("vulnerability scan")).toBe("security");
    });

    it("should classify format checks", () => {
      expect(classifyCIFailure("format")).toBe("format");
      expect(classifyCIFailure("prettier")).toBe("format");
      expect(classifyCIFailure("formatting check")).toBe("format");
    });

    it("should return unknown for unrecognized checks", () => {
      expect(classifyCIFailure("random-check")).toBe("unknown");
      expect(classifyCIFailure("deploy")).toBe("unknown");
      expect(classifyCIFailure("docs")).toBe("unknown");
    });

    it("should use error output for classification when check name is ambiguous", () => {
      expect(classifyCIFailure("CI", "Error: eslint found issues")).toBe("lint");
      expect(classifyCIFailure("CI", "vitest failed")).toBe("test");
    });
  });

  describe("isTransientCIFailure", () => {
    it("should return true for timed_out conclusion", () => {
      expect(isTransientCIFailure("test", "", "timed_out")).toBe(true);
    });

    it("should return true for timeout-related errors", () => {
      expect(isTransientCIFailure("test", "Operation timed out")).toBe(true);
      expect(isTransientCIFailure("test", "Request timeout")).toBe(true);
    });

    it("should return true for network errors", () => {
      expect(isTransientCIFailure("test", "Network error")).toBe(true);
      expect(isTransientCIFailure("test", "Connection refused")).toBe(true);
      expect(isTransientCIFailure("test", "ECONNRESET")).toBe(true);
    });

    it("should return true for rate limit errors", () => {
      expect(isTransientCIFailure("test", "rate limit exceeded")).toBe(true);
    });

    it("should return true for HTTP 5xx errors in output", () => {
      expect(isTransientCIFailure("test", "HTTP 502 error")).toBe(true);
      expect(isTransientCIFailure("test", "Error 503")).toBe(true);
    });

    it("should return false for normal test failures", () => {
      expect(isTransientCIFailure("test", "expect(x).toBe(y)")).toBe(false);
      expect(isTransientCIFailure("test", "assertion failed")).toBe(false);
    });

    it("should return false for lint errors", () => {
      expect(isTransientCIFailure("lint", "Unexpected token")).toBe(false);
    });
  });

  describe("parseCICheckStatus", () => {
    it("should handle empty checks array", () => {
      const status = parseCICheckStatus([]);
      expect(status.allComplete).toBe(true);
      expect(status.allPassed).toBe(true);
      expect(status.hasChecks).toBe(false);
    });

    it("should identify all passed checks", () => {
      const checks = [
        { name: "build", status: "COMPLETED", conclusion: "SUCCESS" },
        { name: "test", status: "COMPLETED", conclusion: "SUCCESS" },
      ];
      const status = parseCICheckStatus(checks);
      expect(status.allComplete).toBe(true);
      expect(status.allPassed).toBe(true);
      expect(status.passedCount).toBe(2);
      expect(status.failedCount).toBe(0);
      expect(status.hasChecks).toBe(true);
    });

    it("should identify failed checks", () => {
      const checks = [
        { name: "build", status: "COMPLETED", conclusion: "SUCCESS" },
        {
          name: "test",
          status: "COMPLETED",
          conclusion: "FAILURE",
          detailsUrl: "https://...",
        },
      ];
      const status = parseCICheckStatus(checks);
      expect(status.allComplete).toBe(true);
      expect(status.allPassed).toBe(false);
      expect(status.passedCount).toBe(1);
      expect(status.failedCount).toBe(1);
      expect(status.failures).toHaveLength(1);
      expect(status.failures[0].name).toBe("test");
    });

    it("should identify pending checks", () => {
      const checks = [
        { name: "build", status: "COMPLETED", conclusion: "SUCCESS" },
        { name: "test", status: "IN_PROGRESS" },
      ];
      const status = parseCICheckStatus(checks);
      expect(status.allComplete).toBe(false);
      expect(status.pendingCount).toBe(1);
      expect(status.passedCount).toBe(1);
    });

    it("should treat NEUTRAL and SKIPPED as passed", () => {
      const checks = [
        { name: "build", status: "COMPLETED", conclusion: "NEUTRAL" },
        { name: "test", status: "COMPLETED", conclusion: "SKIPPED" },
      ];
      const status = parseCICheckStatus(checks);
      expect(status.allPassed).toBe(true);
      expect(status.passedCount).toBe(2);
    });

    it("should classify failures correctly", () => {
      const checks = [
        { name: "lint", status: "COMPLETED", conclusion: "FAILURE" },
        { name: "test / vitest", status: "COMPLETED", conclusion: "FAILURE" },
      ];
      const status = parseCICheckStatus(checks);
      expect(status.failures[0].failureType).toBe("lint");
      expect(status.failures[1].failureType).toBe("test");
    });
  });

  describe("extractFailedChecksFromOutput", () => {
    it("should extract failed checks from tab-separated output", () => {
      const output = `build\tpass\t1m30s\thttps://github.com/.../1
test\tfail\t2m15s\thttps://github.com/.../2
lint\tfail\t0m45s\thttps://github.com/.../3`;
      const failures = extractFailedChecksFromOutput(output);
      expect(failures).toHaveLength(2);
      expect(failures[0].name).toBe("test");
      expect(failures[1].name).toBe("lint");
    });

    it("should extract failed checks from space-separated output", () => {
      const output = `build  pass  1m30s  https://github.com/.../1
test  fail  2m15s  https://github.com/.../2`;
      const failures = extractFailedChecksFromOutput(output);
      expect(failures).toHaveLength(1);
      expect(failures[0].name).toBe("test");
    });

    it("should handle empty output", () => {
      const failures = extractFailedChecksFromOutput("");
      expect(failures).toHaveLength(0);
    });

    it("should classify failure types", () => {
      const output = `lint\tfail\t1m\thttps://...`;
      const failures = extractFailedChecksFromOutput(output);
      expect(failures[0].failureType).toBe("lint");
    });
  });

  describe("canAutoFixFailures", () => {
    it("should return true for lint failures", () => {
      const failures: CICheckFailure[] = [
        {
          name: "lint",
          detailsUrl: "",
          failureType: "lint",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      expect(canAutoFixFailures(failures)).toBe(true);
    });

    it("should return true for format failures", () => {
      const failures: CICheckFailure[] = [
        {
          name: "format",
          detailsUrl: "",
          failureType: "format",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      expect(canAutoFixFailures(failures)).toBe(true);
    });

    it("should return true for test failures", () => {
      const failures: CICheckFailure[] = [
        {
          name: "test",
          detailsUrl: "",
          failureType: "test",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      expect(canAutoFixFailures(failures)).toBe(true);
    });

    it("should return false for security failures only", () => {
      const failures: CICheckFailure[] = [
        {
          name: "security",
          detailsUrl: "",
          failureType: "security",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      expect(canAutoFixFailures(failures)).toBe(false);
    });

    it("should return true if any failure is auto-fixable", () => {
      const failures: CICheckFailure[] = [
        {
          name: "security",
          detailsUrl: "",
          failureType: "security",
          isTransient: false,
          conclusion: "failure",
        },
        {
          name: "lint",
          detailsUrl: "",
          failureType: "lint",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      expect(canAutoFixFailures(failures)).toBe(true);
    });
  });

  describe("getFailureTypeDescription", () => {
    it("should return descriptions for all failure types", () => {
      expect(getFailureTypeDescription("lint")).toContain("Linting");
      expect(getFailureTypeDescription("test")).toContain("Test");
      expect(getFailureTypeDescription("build")).toContain("Build");
      expect(getFailureTypeDescription("typecheck")).toContain("Type");
      expect(getFailureTypeDescription("security")).toContain("Security");
      expect(getFailureTypeDescription("format")).toContain("Formatting");
      expect(getFailureTypeDescription("unknown")).toContain("Unknown");
    });
  });

  describe("prioritizeFailuresForAutoFix", () => {
    it("should prioritize format failures highest", () => {
      const failures: CICheckFailure[] = [
        {
          name: "test",
          detailsUrl: "",
          failureType: "test",
          isTransient: false,
          conclusion: "failure",
        },
        {
          name: "format",
          detailsUrl: "",
          failureType: "format",
          isTransient: false,
          conclusion: "failure",
        },
        {
          name: "lint",
          detailsUrl: "",
          failureType: "lint",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      const prioritized = prioritizeFailuresForAutoFix(failures);
      expect(prioritized[0].failureType).toBe("format");
      expect(prioritized[1].failureType).toBe("lint");
      expect(prioritized[2].failureType).toBe("test");
    });

    it("should prioritize security failures lowest", () => {
      const failures: CICheckFailure[] = [
        {
          name: "security",
          detailsUrl: "",
          failureType: "security",
          isTransient: false,
          conclusion: "failure",
        },
        {
          name: "lint",
          detailsUrl: "",
          failureType: "lint",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      const prioritized = prioritizeFailuresForAutoFix(failures);
      expect(prioritized[0].failureType).toBe("lint");
      expect(prioritized[1].failureType).toBe("security");
    });

    it("should not modify original array", () => {
      const failures: CICheckFailure[] = [
        {
          name: "test",
          detailsUrl: "",
          failureType: "test",
          isTransient: false,
          conclusion: "failure",
        },
        {
          name: "format",
          detailsUrl: "",
          failureType: "format",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      prioritizeFailuresForAutoFix(failures);
      expect(failures[0].failureType).toBe("test");
    });
  });

  describe("hasRepeatingFailures", () => {
    it("should return true when same check fails in both attempts", () => {
      const current: CICheckFailure[] = [
        {
          name: "test",
          detailsUrl: "",
          failureType: "test",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      const previous: CICheckFailure[] = [
        {
          name: "test",
          detailsUrl: "",
          failureType: "test",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      expect(hasRepeatingFailures(current, previous)).toBe(true);
    });

    it("should return false when different checks fail", () => {
      const current: CICheckFailure[] = [
        {
          name: "lint",
          detailsUrl: "",
          failureType: "lint",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      const previous: CICheckFailure[] = [
        {
          name: "test",
          detailsUrl: "",
          failureType: "test",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      expect(hasRepeatingFailures(current, previous)).toBe(false);
    });

    it("should return false when either array is empty", () => {
      const failures: CICheckFailure[] = [
        {
          name: "test",
          detailsUrl: "",
          failureType: "test",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      expect(hasRepeatingFailures([], failures)).toBe(false);
      expect(hasRepeatingFailures(failures, [])).toBe(false);
    });

    it("should return true when subset of checks repeat", () => {
      const current: CICheckFailure[] = [
        {
          name: "test",
          detailsUrl: "",
          failureType: "test",
          isTransient: false,
          conclusion: "failure",
        },
        {
          name: "lint",
          detailsUrl: "",
          failureType: "lint",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      const previous: CICheckFailure[] = [
        {
          name: "test",
          detailsUrl: "",
          failureType: "test",
          isTransient: false,
          conclusion: "failure",
        },
        {
          name: "build",
          detailsUrl: "",
          failureType: "build",
          isTransient: false,
          conclusion: "failure",
        },
      ];
      expect(hasRepeatingFailures(current, previous)).toBe(true);
    });
  });
});
