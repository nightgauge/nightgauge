import { describe, expect, it } from "vitest";
import { ValidateContextSchema } from "../../../context/schemas/validate.js";

const minimalValidate = {
  schema_version: "2.0",
  issue_number: 42,
};

describe("ValidateContextSchema — preexisting_failures", () => {
  it("accepts empty preexisting_failures array", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      preexisting_failures: [],
    });
    expect(result.success).toBe(true);
  });

  it("accepts well-formed preexisting_failures entries", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      preexisting_failures: [
        { test_file: "tests/foo.test.ts", failure_count: 1, baseline_verified: true },
        { test_file: "tests/bar.test.ts", failure_count: 3, baseline_verified: true },
      ],
    });
    expect(result.success).toBe(true);
  });

  it("rejects preexisting_failures missing failure_count", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      preexisting_failures: [{ test_file: "tests/foo.test.ts", baseline_verified: true }],
    });
    expect(result.success).toBe(false);
  });

  it("rejects preexisting_failures missing test_file", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      preexisting_failures: [{ failure_count: 1, baseline_verified: true }],
    });
    expect(result.success).toBe(false);
  });

  it("rejects preexisting_failures with failure_count of 0", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      preexisting_failures: [
        { test_file: "tests/foo.test.ts", failure_count: 0, baseline_verified: true },
      ],
    });
    expect(result.success).toBe(false);
  });

  it("rejects preexisting_failures with empty string test_file", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      preexisting_failures: [{ test_file: "", failure_count: 1, baseline_verified: true }],
    });
    expect(result.success).toBe(false);
  });

  it("coerces numeric 1 to boolean true for baseline_verified", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      preexisting_failures: [
        { test_file: "tests/foo.test.ts", failure_count: 1, baseline_verified: 1 },
      ],
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.preexisting_failures![0].baseline_verified).toBe(true);
    }
  });

  it("coerces numeric 0 to boolean false for baseline_verified", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      preexisting_failures: [
        { test_file: "tests/foo.test.ts", failure_count: 1, baseline_verified: 0 },
      ],
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.preexisting_failures![0].baseline_verified).toBe(false);
    }
  });

  it("accepts nullish preexisting_failures (omitted)", () => {
    const result = ValidateContextSchema.safeParse({ ...minimalValidate });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.preexisting_failures == null).toBe(true);
    }
  });
});

describe("ValidateContextSchema — minimum_duration_check (Issue #3041)", () => {
  it("accepts minimum_duration_check when flagged", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      minimum_duration_check: {
        flagged: true,
        actual_build_time_ms: 2000,
        p10_baseline_ms: 15000,
        warning: "Build completed suspiciously fast",
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.minimum_duration_check?.flagged).toBe(true);
      expect(result.data.minimum_duration_check?.actual_build_time_ms).toBe(2000);
      expect(result.data.minimum_duration_check?.p10_baseline_ms).toBe(15000);
    }
  });

  it("accepts minimum_duration_check when not flagged", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      minimum_duration_check: {
        flagged: false,
        actual_build_time_ms: 20000,
        p10_baseline_ms: 15000,
        warning: null,
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.minimum_duration_check?.flagged).toBe(false);
    }
  });

  it("accepts omitted minimum_duration_check", () => {
    const result = ValidateContextSchema.safeParse({ ...minimalValidate });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.minimum_duration_check == null).toBe(true);
    }
  });

  it("rejects minimum_duration_check with negative actual_build_time_ms", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      minimum_duration_check: {
        flagged: true,
        actual_build_time_ms: -1,
        p10_baseline_ms: 15000,
      },
    });
    expect(result.success).toBe(false);
  });
});

describe("ValidateContextSchema — errorCategory (Issue #3041)", () => {
  it("accepts errorCategory: build-failed", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      validation_status: "failed",
      errorCategory: "build-failed",
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.errorCategory).toBe("build-failed");
    }
  });

  it("accepts errorCategory: tests-failed", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      validation_status: "failed",
      errorCategory: "tests-failed",
    });
    expect(result.success).toBe(true);
  });

  // The enum must stay a superset of the ERROR_CATEGORY values the
  // feature-validate skill emits. These mobile/verify-ui categories were
  // omitted originally, so a real validation failure on a Flutter/UI issue
  // parsed as a "non-fatal schema mismatch" and its failure signal was
  // dropped — letting a failed validation advance to pr-create.
  it.each(["mobile-apk-build-failed", "mobile-mcp-tests-failed", "verify-ui-gate-failed"])(
    "accepts errorCategory the skill emits: %s",
    (category) => {
      const result = ValidateContextSchema.safeParse({
        ...minimalValidate,
        validation_status: "failed",
        errorCategory: category,
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.errorCategory).toBe(category);
      }
    }
  );

  it("rejects unknown errorCategory value", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      errorCategory: "something-unknown",
    });
    expect(result.success).toBe(false);
  });

  it("accepts omitted errorCategory", () => {
    const result = ValidateContextSchema.safeParse({ ...minimalValidate });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.errorCategory == null).toBe(true);
    }
  });
});

describe("ValidateContextSchema — build.duration_ms and build.exit_code (Issue #3041)", () => {
  it("accepts build with duration_ms and exit_code", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      build: {
        ran: true,
        passed: true,
        command: "npm run build",
        duration_ms: 18500,
        exit_code: 0,
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.build?.duration_ms).toBe(18500);
      expect(result.data.build?.exit_code).toBe(0);
    }
  });

  it("accepts build without duration_ms and exit_code (backward compat)", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      build: { ran: true, passed: false, command: "npm run build" },
    });
    expect(result.success).toBe(true);
  });

  it("rejects negative duration_ms", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      build: { ran: true, passed: true, command: "npm run build", duration_ms: -1 },
    });
    expect(result.success).toBe(false);
  });
});

describe("ValidateContextSchema — mobile_mcp (Issue #24)", () => {
  it("defaults mobile_mcp to null when omitted", () => {
    const result = ValidateContextSchema.safeParse({ ...minimalValidate });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.mobile_mcp).toBe(null);
    }
  });

  it("accepts a passing mobile_mcp block with spec results", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      mobile_mcp: {
        ran: true,
        passed: true,
        specs_run: 1,
        specs_passed: 1,
        specs_failed: 0,
        results: [
          {
            spec: "sun_moon",
            platform: "android",
            device: "Pixel_9_Pro",
            status: "pass",
            assertions: [{ id: "sunrise_local_not_null", status: "pass", actual: "5:42 AM" }],
            screenshots: ["test/mobile_mcp/evidence/sun_moon/20260531T000000Z/01.png"],
          },
        ],
        evidence_dir: "test/mobile_mcp/evidence/",
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.mobile_mcp?.passed).toBe(true);
      expect(result.data.mobile_mcp?.results?.[0].status).toBe("pass");
    }
  });

  it("accepts a skipped mobile_mcp block with skipped_reason", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      mobile_mcp: {
        ran: false,
        passed: false,
        specs_run: 0,
        specs_passed: 0,
        specs_failed: 0,
        results: [],
        evidence_dir: "",
        skipped_reason: "config: validation.mobile_mcp_tests=skip",
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.mobile_mcp?.ran).toBe(false);
      expect(result.data.mobile_mcp?.skipped_reason).toBe(
        "config: validation.mobile_mcp_tests=skip"
      );
    }
  });

  it("coerces numeric 1/0 to booleans for ran/passed", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      mobile_mcp: { ran: 1, passed: 0, specs_run: 2, specs_passed: 1, specs_failed: 1 },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.mobile_mcp?.ran).toBe(true);
      expect(result.data.mobile_mcp?.passed).toBe(false);
    }
  });

  it("rejects an unknown spec status value", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      mobile_mcp: {
        ran: true,
        passed: false,
        results: [{ spec: "sun_moon", status: "flaky" }],
      },
    });
    expect(result.success).toBe(false);
  });

  it("rejects a spec result missing the spec name", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      mobile_mcp: {
        ran: true,
        passed: true,
        results: [{ status: "pass" }],
      },
    });
    expect(result.success).toBe(false);
  });

  it("rejects negative specs_run", () => {
    const result = ValidateContextSchema.safeParse({
      ...minimalValidate,
      mobile_mcp: { ran: true, passed: true, specs_run: -1 },
    });
    expect(result.success).toBe(false);
  });
});
