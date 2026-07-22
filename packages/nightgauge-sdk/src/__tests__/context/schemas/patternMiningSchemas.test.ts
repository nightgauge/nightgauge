import { describe, expect, it } from "vitest";
import {
  PatternMiningResultSchema,
  DiscoveredPatternSchema,
  SimilarIssueSchema,
  PatternClassificationsSchema,
  PatternTypeSchema,
} from "../../../context/schemas/pattern-mining.js";

// --- PatternTypeSchema ---

describe("PatternTypeSchema", () => {
  it.each(["naming_convention", "structural", "implementation_interface", "idiom"])(
    "accepts valid pattern type: %s",
    (type: string) => {
      expect(PatternTypeSchema.safeParse(type).success).toBe(true);
    }
  );

  it("rejects invalid pattern type", () => {
    expect(PatternTypeSchema.safeParse("unknown_type").success).toBe(false);
  });
});

// --- DiscoveredPatternSchema ---

const validPattern = {
  pattern_type: "naming_convention",
  category: "file_naming",
  pattern: "Services named `*Service.ts` in `src/services/`",
  evidence: ["src/services/PhotoService.ts", "src/services/FileService.ts"],
  frequency: 12,
  example_implementations: ["src/services/PhotoService.ts:1-50"],
};

describe("DiscoveredPatternSchema", () => {
  it("parses a fully-populated canonical pattern", () => {
    const result = DiscoveredPatternSchema.safeParse(validPattern);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.pattern_type).toBe("naming_convention");
      expect(result.data.evidence).toHaveLength(2);
    }
  });

  it("accepts a minimal entry with only `pattern` populated", () => {
    // After the schema loosening (#2616 follow-up), only the human-readable
    // description is required. LLM subagents consistently produce entries
    // missing one or more of the other fields.
    const result = DiscoveredPatternSchema.safeParse({
      pattern: "Mutex-protected cache",
    });
    expect(result.success).toBe(true);
  });

  it("rejects an entry missing `pattern`", () => {
    const { pattern: _, ...withoutPattern } = validPattern;
    const result = DiscoveredPatternSchema.safeParse(withoutPattern);
    expect(result.success).toBe(false);
  });

  it("accepts single-element evidence and example_implementations arrays", () => {
    // The old schema required min(2) evidence + min(1) examples. The
    // looser schema treats these as informational — a single well-chosen
    // example is perfectly useful.
    const result = DiscoveredPatternSchema.safeParse({
      ...validPattern,
      evidence: ["src/services/PhotoService.ts"],
      example_implementations: ["src/services/PhotoService.ts:10"],
    });
    expect(result.success).toBe(true);
  });

  it("normalizes common LLM field aliases (name/description/location)", () => {
    // Real output captured from the pattern-mining subagent in planning-2628.json.
    // Previously this shape generated a wall of schema-mismatch warnings.
    const result = DiscoveredPatternSchema.safeParse({
      name: "Mutex-Protected Cache Pattern",
      location: "internal/github/project.go:15-25",
      description: "ProjectService uses sync.Mutex to protect cached field metadata",
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.pattern_type).toBe("Mutex-Protected Cache Pattern");
      expect(result.data.pattern).toBe(
        "ProjectService uses sync.Mutex to protect cached field metadata"
      );
      expect(result.data.example_implementations).toEqual(["internal/github/project.go:15-25"]);
    }
  });

  it("coerces a bare-string evidence field into a one-element array", () => {
    const result = DiscoveredPatternSchema.safeParse({
      pattern: "Something",
      evidence: "single/file.ts",
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.evidence).toEqual(["single/file.ts"]);
    }
  });

  it("still rejects non-object input (arrays, strings, null)", () => {
    expect(DiscoveredPatternSchema.safeParse("not a pattern").success).toBe(false);
    expect(DiscoveredPatternSchema.safeParse(["wrong"]).success).toBe(false);
  });
});

// --- SimilarIssueSchema ---

const validSimilarIssue = {
  issue_number: 42,
  title: "Add user photo upload",
  relevance_score: 0.85,
  pattern_overlap: ["service_pattern", "api_endpoint"],
  plan_file: ".nightgauge/plans/42-user-photo-upload.md",
};

describe("SimilarIssueSchema", () => {
  it("parses a valid similar issue", () => {
    const result = SimilarIssueSchema.safeParse(validSimilarIssue);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.issue_number).toBe(42);
      expect(result.data.relevance_score).toBe(0.85);
    }
  });

  it("accepts null plan_file", () => {
    const result = SimilarIssueSchema.safeParse({
      ...validSimilarIssue,
      plan_file: null,
    });
    expect(result.success).toBe(true);
  });

  it("accepts missing plan_file", () => {
    const { plan_file: _, ...withoutPlanFile } = validSimilarIssue;
    const result = SimilarIssueSchema.safeParse(withoutPlanFile);
    expect(result.success).toBe(true);
  });

  it("rejects relevance_score above 1", () => {
    const result = SimilarIssueSchema.safeParse({
      ...validSimilarIssue,
      relevance_score: 1.5,
    });
    expect(result.success).toBe(false);
  });

  it("rejects relevance_score below 0", () => {
    const result = SimilarIssueSchema.safeParse({
      ...validSimilarIssue,
      relevance_score: -0.1,
    });
    expect(result.success).toBe(false);
  });

  it("rejects negative issue_number", () => {
    const result = SimilarIssueSchema.safeParse({
      ...validSimilarIssue,
      issue_number: -1,
    });
    expect(result.success).toBe(false);
  });
});

// --- PatternClassificationsSchema ---

describe("PatternClassificationsSchema", () => {
  it("parses valid classifications", () => {
    const result = PatternClassificationsSchema.safeParse({
      naming_conventions: 5,
      structural_patterns: 3,
      interface_patterns: 2,
      idioms: 1,
    });
    expect(result.success).toBe(true);
  });

  it("rejects negative counts", () => {
    const result = PatternClassificationsSchema.safeParse({
      naming_conventions: -1,
      structural_patterns: 3,
      interface_patterns: 2,
      idioms: 1,
    });
    expect(result.success).toBe(false);
  });

  it("requires all four classification fields", () => {
    const result = PatternClassificationsSchema.safeParse({
      naming_conventions: 5,
      structural_patterns: 3,
    });
    expect(result.success).toBe(false);
  });
});

// --- PatternMiningResultSchema ---

const validPatternMiningResult = {
  patterns_found: [validPattern],
  similar_issues: [validSimilarIssue],
  pattern_classifications: {
    naming_conventions: 1,
    structural_patterns: 0,
    interface_patterns: 0,
    idioms: 0,
  },
  search_queries_used: ["Service.ts"],
  coverage_ratio: 0.68,
  token_cost_estimate: 2400,
  recommendations: ["Follow service pattern from src/services/ directory"],
};

describe("PatternMiningResultSchema", () => {
  it("parses a valid pattern mining result", () => {
    const result = PatternMiningResultSchema.safeParse(validPatternMiningResult);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.patterns_found).toHaveLength(1);
      expect(result.data.similar_issues).toHaveLength(1);
      expect(result.data.coverage_ratio).toBe(0.68);
    }
  });

  it("accepts empty patterns and similar issues", () => {
    const result = PatternMiningResultSchema.safeParse({
      ...validPatternMiningResult,
      patterns_found: [],
      similar_issues: [],
      recommendations: [],
    });
    expect(result.success).toBe(true);
  });

  it("accepts extra fields via passthrough", () => {
    const result = PatternMiningResultSchema.safeParse({
      ...validPatternMiningResult,
      custom_field: "extra data",
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect((result.data as Record<string, unknown>).custom_field).toBe("extra data");
    }
  });

  it("rejects coverage_ratio above 1", () => {
    const result = PatternMiningResultSchema.safeParse({
      ...validPatternMiningResult,
      coverage_ratio: 1.5,
    });
    expect(result.success).toBe(false);
  });

  it("rejects negative token_cost_estimate", () => {
    const result = PatternMiningResultSchema.safeParse({
      ...validPatternMiningResult,
      token_cost_estimate: -100,
    });
    expect(result.success).toBe(false);
  });

  it("rejects missing required fields", () => {
    const { patterns_found: _, ...incomplete } = validPatternMiningResult;
    const result = PatternMiningResultSchema.safeParse(incomplete);
    expect(result.success).toBe(false);
  });
});
