/**
 * Unit tests for ChangeAnalyzer
 *
 * @see changeAnalyzer.ts
 * @see Issue #216 - Complexity-Based Stage Routing
 */

import {
  extractLabels,
  detectChangeType,
  detectTaskType,
  detectFoundationTask,
  calculateComplexityScore,
  determineSkipStages,
  determineRoutingPath,
  generateRationale,
  analyzeChange,
  isHighRisk,
  type IssueLabels,
} from "../../src/utils/changeAnalyzer";

describe("extractLabels", () => {
  it("should extract size label correctly", () => {
    const labels = ["size:M", "type:feature", "priority:high"];
    const result = extractLabels(labels);

    expect(result.size).toBe("M");
    expect(result.type).toBe("feature");
    expect(result.priority).toBe("high");
  });

  it("should handle size labels with different formats", () => {
    expect(extractLabels(["size:xs"]).size).toBe("XS");
    expect(extractLabels(["size-S"]).size).toBe("S");
    expect(extractLabels(["sizeM"]).size).toBe("M");
    expect(extractLabels(["size:L"]).size).toBe("L");
    expect(extractLabels(["size:XL"]).size).toBe("XL");
  });

  it("should extract type labels with different formats", () => {
    expect(extractLabels(["type:feature"]).type).toBe("feature");
    expect(extractLabels(["bug"]).type).toBe("bug");
    expect(extractLabels(["docs"]).type).toBe("docs");
    expect(extractLabels(["documentation"]).type).toBe("docs");
    expect(extractLabels(["enhancement"]).type).toBe("feature");
    expect(extractLabels(["type:refactor"]).type).toBe("refactor");
    expect(extractLabels(["chore"]).type).toBe("chore");
    expect(extractLabels(["test"]).type).toBe("test");
    expect(extractLabels(["type:verification"]).type).toBe("verification");
    expect(extractLabels(["verification"]).type).toBe("verification");
  });

  it("should extract priority labels", () => {
    expect(extractLabels(["priority:critical"]).priority).toBe("critical");
    expect(extractLabels(["priority:high"]).priority).toBe("high");
    expect(extractLabels(["priority:medium"]).priority).toBe("medium");
    expect(extractLabels(["priority:low"]).priority).toBe("low");
  });

  it("should return null for missing labels", () => {
    const result = extractLabels(["random-label"]);

    expect(result.size).toBeNull();
    expect(result.type).toBeNull();
    expect(result.priority).toBeNull();
  });

  it("should handle empty label array", () => {
    const result = extractLabels([]);

    expect(result.all).toEqual([]);
    expect(result.size).toBeNull();
    expect(result.type).toBeNull();
    expect(result.priority).toBeNull();
  });

  it("should preserve all labels in lowercase", () => {
    const labels = ["Size:M", "PRIORITY:HIGH", "Custom-Label"];
    const result = extractLabels(labels);

    expect(result.all).toContain("size:m");
    expect(result.all).toContain("priority:high");
    expect(result.all).toContain("custom-label");
  });
});

describe("detectChangeType", () => {
  it("should detect docs type from label", () => {
    const labels: IssueLabels = {
      all: ["docs"],
      size: null,
      type: "docs",
      priority: null,
    };
    const result = detectChangeType(labels, "Some title");

    expect(result).toBe("docs");
  });

  it("should detect docs from title patterns", () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };

    expect(detectChangeType(labels, "Update README.md")).toBe("docs");
    expect(detectChangeType(labels, "Fix typo in documentation")).toBe("docs");
    expect(detectChangeType(labels, "docs: update changelog")).toBe("docs");
    expect(detectChangeType(labels, "Update docs for API")).toBe("docs");
  });

  it("should detect config from patterns", () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };

    expect(detectChangeType(labels, "Update tsconfig.json")).toBe("config");
    expect(detectChangeType(labels, "Fix .eslintrc settings")).toBe("config");
    expect(detectChangeType(labels, "Update package.json version")).toBe("config");
  });

  it("should default to code for feature-like content", () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: "feature",
      priority: null,
    };

    expect(detectChangeType(labels, "Implement user authentication")).toBe("code");
    expect(detectChangeType(labels, "Add new API endpoint for users")).toBe("code");
    expect(detectChangeType(labels, "Fix bug in login component")).toBe("code");
  });

  it("should prefer code when both docs and code indicators present", () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: "feature",
      priority: null,
    };
    const result = detectChangeType(labels, "Implement feature and update docs");

    expect(result).toBe("code");
  });
});

describe("detectTaskType", () => {
  it("should detect verification from type:verification label", () => {
    const labels: IssueLabels = {
      all: ["type:verification"],
      size: null,
      type: "verification",
      priority: null,
    };
    const result = detectTaskType(labels, "Some title");

    expect(result).toBe("verification");
  });

  it('should detect verification from "verify" in title', () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };

    expect(detectTaskType(labels, "Verify the fix works")).toBe("verification");
    expect(detectTaskType(labels, "Confirm fix for #123")).toBe("verification");
    expect(detectTaskType(labels, "Validate implementation")).toBe("verification");
  });

  it("should detect verification from content patterns", () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };

    expect(detectTaskType(labels, "Task title", "confirm fix for #123")).toBe("verification");
    expect(detectTaskType(labels, "Task title", "verify change in PR #456")).toBe("verification");
    expect(detectTaskType(labels, "Check that everything works")).toBe("verification");
    expect(detectTaskType(labels, "Ensure that tests pass")).toBe("verification");
  });

  it("should detect docs-only from type:docs label", () => {
    const labels: IssueLabels = {
      all: ["type:docs"],
      size: null,
      type: "docs",
      priority: null,
    };
    const result = detectTaskType(labels, "Update README");

    expect(result).toBe("docs-only");
  });

  it("should detect docs-only from content patterns", () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };

    expect(detectTaskType(labels, "Update README.md")).toBe("docs-only");
    expect(detectTaskType(labels, "Fix typo in docs")).toBe("docs-only");
    expect(detectTaskType(labels, "docs: update changelog")).toBe("docs-only");
  });

  it("should detect bugfix from type:bug label", () => {
    const labels: IssueLabels = {
      all: ["bug"],
      size: null,
      type: "bug",
      priority: null,
    };
    const result = detectTaskType(labels, "Fix login issue");

    expect(result).toBe("bugfix");
  });

  it("should detect chore from type:chore label", () => {
    const labels: IssueLabels = {
      all: ["chore"],
      size: null,
      type: "chore",
      priority: null,
    };
    const result = detectTaskType(labels, "Update dependencies");

    expect(result).toBe("chore");
  });

  it("should detect chore from type:test label", () => {
    const labels: IssueLabels = {
      all: ["test"],
      size: null,
      type: "test",
      priority: null,
    };
    const result = detectTaskType(labels, "Add unit tests");

    expect(result).toBe("chore");
  });

  it("should detect refactor from type:refactor label", () => {
    const labels: IssueLabels = {
      all: ["type:refactor"],
      size: null,
      type: "refactor",
      priority: null,
    };
    const result = detectTaskType(labels, "Refactor authentication module");

    expect(result).toBe("refactor");
  });

  it("should detect feature from type:feature label", () => {
    const labels: IssueLabels = {
      all: ["type:feature"],
      size: null,
      type: "feature",
      priority: null,
    };
    const result = detectTaskType(labels, "Add user authentication");

    expect(result).toBe("feature");
  });

  it("should default to feature when no match", () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };
    const result = detectTaskType(labels, "Random issue title");

    expect(result).toBe("feature");
  });

  it("should prioritize label over content detection", () => {
    const labels: IssueLabels = {
      all: ["type:feature"],
      size: null,
      type: "feature",
      priority: null,
    };
    // Title says "verify" but label says "feature"
    const result = detectTaskType(labels, "Verify the new feature works");

    expect(result).toBe("feature");
  });
});

describe("calculateComplexityScore", () => {
  it("should map size labels to Fibonacci scores", () => {
    const baseLabels: Omit<IssueLabels, "size"> = {
      all: [],
      type: null,
      priority: null,
    };

    expect(calculateComplexityScore({ ...baseLabels, size: "XS" }, "code")).toBe(1);
    expect(calculateComplexityScore({ ...baseLabels, size: "S" }, "code")).toBe(2);
    expect(calculateComplexityScore({ ...baseLabels, size: "M" }, "code")).toBe(3);
    expect(calculateComplexityScore({ ...baseLabels, size: "L" }, "code")).toBe(5);
    expect(calculateComplexityScore({ ...baseLabels, size: "XL" }, "code")).toBe(8);
  });

  it("should default to M (3) when no size label", () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };
    const result = calculateComplexityScore(labels, "code");

    expect(result).toBe(3);
  });

  it("should cap docs complexity at 2", () => {
    const labels: IssueLabels = {
      all: [],
      size: "L",
      type: "docs",
      priority: null,
    };
    const result = calculateComplexityScore(labels, "docs");

    expect(result).toBe(2);
  });

  it("should cap config complexity at 2", () => {
    const labels: IssueLabels = {
      all: [],
      size: "XL",
      type: null,
      priority: null,
    };
    const result = calculateComplexityScore(labels, "config");

    expect(result).toBe(2);
  });

  it("should apply priority multipliers", () => {
    const baseLabels: Omit<IssueLabels, "priority"> = {
      all: [],
      size: "M", // Base score: 3
      type: null,
    };

    // critical: 3 * 1.5 = 4.5 -> rounds to 5
    expect(calculateComplexityScore({ ...baseLabels, priority: "critical" }, "code")).toBe(5);

    // high: 3 * 1.2 = 3.6 -> rounds to 3
    expect(calculateComplexityScore({ ...baseLabels, priority: "high" }, "code")).toBe(3);

    // low: 3 * 0.8 = 2.4 -> rounds to 2
    expect(calculateComplexityScore({ ...baseLabels, priority: "low" }, "code")).toBe(2);
  });
});

describe("determineSkipStages", () => {
  it("should skip planning for trivial size with low complexity", () => {
    const result = determineSkipStages("code", 1, "XS");

    expect(result).toContain("feature-planning");
  });

  it("should skip planning for docs-only small changes", () => {
    const result = determineSkipStages("docs", 2, "S");

    expect(result).toContain("feature-planning");
    expect(result).toContain("feature-validate");
  });

  it("should skip validation for non-code changes", () => {
    expect(determineSkipStages("docs", 3, "M")).toContain("feature-validate");
    expect(determineSkipStages("config", 3, "M")).toContain("feature-validate");
  });

  it("should not skip anything for standard code changes", () => {
    const result = determineSkipStages("code", 3, "M");

    expect(result).toEqual([]);
  });

  it("should skip planning and validate for complexity ≤ 2 even for large docs changes (Issue #1593)", () => {
    const result = determineSkipStages("docs", 2, "L");

    // Complexity ≤ 2 always skips planning + validate, regardless of size/type
    expect(result).toContain("feature-planning");
    expect(result).toContain("feature-validate");
  });
});

describe("determineSkipStages - Issue #1593 complexity-based skipping", () => {
  it("should skip planning and validate for trivial refactor (complexity ≤ 2)", () => {
    const result = determineSkipStages("code", 2, "S", "refactor");

    expect(result).toContain("feature-planning");
    expect(result).toContain("feature-validate");
  });

  it("should skip planning and validate for trivial bugfix (complexity ≤ 2)", () => {
    const result = determineSkipStages("code", 1, "XS", "bugfix");

    expect(result).toContain("feature-planning");
    expect(result).toContain("feature-validate");
  });

  it("should skip planning and validate for trivial feature (complexity ≤ 2)", () => {
    const result = determineSkipStages("code", 2, "S", "feature");

    expect(result).toContain("feature-planning");
    expect(result).toContain("feature-validate");
  });

  it("should NOT skip planning or validate for standard feature (complexity 3)", () => {
    const result = determineSkipStages("code", 3, "M", "feature");

    expect(result).not.toContain("feature-planning");
    expect(result).not.toContain("feature-validate");
  });

  it("should NOT skip planning or validate for extensive refactor (complexity 5)", () => {
    const result = determineSkipStages("code", 5, "L", "refactor");

    expect(result).not.toContain("feature-planning");
    expect(result).not.toContain("feature-validate");
  });

  it("should still skip planning for chore even at complexity 3", () => {
    const result = determineSkipStages("code", 3, "M", "chore");

    expect(result).toContain("feature-planning");
    expect(result).not.toContain("feature-validate");
  });

  it("should skip validate for docs-only at complexity 3", () => {
    const result = determineSkipStages("code", 3, "M", "docs-only");

    expect(result).toContain("feature-validate");
    expect(result).not.toContain("feature-planning");
  });

  it("should not produce duplicate skip entries for trivial chore", () => {
    const result = determineSkipStages("code", 2, "S", "chore");

    // Both complexity-based and chore-based skip planning
    expect(result.filter((s) => s === "feature-planning").length).toBe(1);
    expect(result).toContain("feature-validate");
  });
});

describe("analyzeChange - Issue #1593 trivial complexity for all types", () => {
  it("should skip planning and validate for trivial refactor", () => {
    const result = analyzeChange(["type:refactor", "size:S"], "Remove dead auto-tune code");

    expect(result.taskType).toBe("refactor");
    expect(result.complexityScore).toBe(2);
    expect(result.suggestedRoute).toBe("trivial");
    expect(result.skipStages).toContain("feature-planning");
    expect(result.skipStages).toContain("feature-validate");
  });

  it("should skip planning and validate for trivial bugfix", () => {
    const result = analyzeChange(["bug", "size:XS"], "Fix typo in error message");

    expect(result.taskType).toBe("bugfix");
    expect(result.complexityScore).toBe(1);
    expect(result.suggestedRoute).toBe("trivial");
    expect(result.skipStages).toContain("feature-planning");
    expect(result.skipStages).toContain("feature-validate");
  });

  it("should NOT skip for standard-complexity refactor", () => {
    const result = analyzeChange(["type:refactor", "size:M"], "Refactor authentication module");

    expect(result.taskType).toBe("refactor");
    expect(result.complexityScore).toBe(3);
    expect(result.suggestedRoute).toBe("standard");
    expect(result.skipStages).not.toContain("feature-planning");
    expect(result.skipStages).not.toContain("feature-validate");
  });
});

describe("determineRoutingPath", () => {
  it("should return trivial for docs-only XS/S changes", () => {
    const labels: IssueLabels = {
      all: [],
      size: "XS",
      type: "docs",
      priority: null,
    };
    const result = determineRoutingPath("docs", 1, labels);

    expect(result).toBe("trivial");
  });

  it("should return trivial for complexity 1-2", () => {
    const labels: IssueLabels = {
      all: [],
      size: "S",
      type: null,
      priority: null,
    };
    const result = determineRoutingPath("code", 2, labels);

    expect(result).toBe("trivial");
  });

  it("should return extensive for L/XL size", () => {
    const labels: IssueLabels = {
      all: [],
      size: "L",
      type: null,
      priority: null,
    };
    const result = determineRoutingPath("code", 5, labels);

    expect(result).toBe("extensive");
  });

  it("should return extensive for critical priority", () => {
    const labels: IssueLabels = {
      all: [],
      size: "M",
      type: null,
      priority: "critical",
    };
    const result = determineRoutingPath("code", 3, labels);

    expect(result).toBe("extensive");
  });

  it("should return extensive for complexity 5+", () => {
    const labels: IssueLabels = {
      all: [],
      size: "M",
      type: null,
      priority: "high",
    };
    const result = determineRoutingPath("code", 5, labels);

    expect(result).toBe("extensive");
  });

  it("should return standard for medium complexity code changes", () => {
    const labels: IssueLabels = {
      all: [],
      size: "M",
      type: "feature",
      priority: "medium",
    };
    const result = determineRoutingPath("code", 3, labels);

    expect(result).toBe("standard");
  });
});

describe("generateRationale", () => {
  it("should generate rationale for trivial path", () => {
    const labels: IssueLabels = {
      all: [],
      size: "XS",
      type: "docs",
      priority: null,
    };
    const result = generateRationale("trivial", "docs", 1, labels);

    expect(result).toContain("Trivial path");
    expect(result).toContain("XS size");
    expect(result).toContain("docs change");
    expect(result).toContain("Skipping planning and validation");
  });

  it("should generate rationale for extensive path", () => {
    const labels: IssueLabels = {
      all: [],
      size: "L",
      type: "feature",
      priority: "critical",
    };
    const result = generateRationale("extensive", "code", 5, labels);

    expect(result).toContain("Extensive path");
    expect(result).toContain("L size");
    expect(result).toContain("critical priority");
    expect(result).toContain("extended documentation");
  });

  it("should generate rationale for standard path", () => {
    const labels: IssueLabels = {
      all: [],
      size: "M",
      type: "feature",
      priority: "medium",
    };
    const result = generateRationale("standard", "code", 3, labels);

    expect(result).toContain("Standard path");
    expect(result).toContain("Full pipeline");
  });
});

describe("analyzeChange", () => {
  it("should perform complete analysis for trivial docs change", () => {
    const result = analyzeChange(["size:XS", "type:docs"], "Fix typo in README.md");

    expect(result.changeType).toBe("docs");
    expect(result.taskType).toBe("docs-only");
    expect(result.sizeLabel).toBe("XS");
    expect(result.typeLabel).toBe("docs");
    expect(result.complexityScore).toBe(1);
    expect(result.suggestedRoute).toBe("trivial");
    expect(result.skipStages).toContain("feature-planning");
    expect(result.skipStages).toContain("feature-validate");
    expect(result.estimatedTimeMinutes).toBe(6);
  });

  it("should perform complete analysis for standard feature", () => {
    const result = analyzeChange(
      ["size:M", "type:feature", "priority:medium"],
      "Add user authentication",
      "Implement login and logout functionality"
    );

    expect(result.changeType).toBe("code");
    expect(result.taskType).toBe("feature");
    expect(result.sizeLabel).toBe("M");
    expect(result.typeLabel).toBe("feature");
    expect(result.priorityLabel).toBe("medium");
    expect(result.complexityScore).toBe(3);
    expect(result.suggestedRoute).toBe("standard");
    expect(result.skipStages).toEqual([]);
    expect(result.estimatedTimeMinutes).toBe(30);
  });

  it("should perform complete analysis for extensive feature", () => {
    const result = analyzeChange(
      ["size:L", "type:feature", "priority:critical"],
      "Implement complexity-based stage routing",
      "Large feature with cross-module impact"
    );

    expect(result.changeType).toBe("code");
    expect(result.taskType).toBe("feature");
    expect(result.sizeLabel).toBe("L");
    expect(result.priorityLabel).toBe("critical");
    expect(result.suggestedRoute).toBe("extensive");
    expect(result.estimatedTimeMinutes).toBe(45);
  });

  it("should handle missing labels gracefully", () => {
    const result = analyzeChange([], "Random issue title");

    expect(result.sizeLabel).toBeNull();
    expect(result.typeLabel).toBeNull();
    expect(result.priorityLabel).toBeNull();
    expect(result.taskType).toBe("feature"); // Default
    expect(result.complexityScore).toBe(3); // Default M
    expect(result.suggestedRoute).toBe("standard");
  });

  it("should detect verification task type", () => {
    const result = analyzeChange(["type:verification", "size:S"], "Verify fix for #256");

    expect(result.taskType).toBe("verification");
    expect(result.typeLabel).toBe("verification");
  });

  it("should include task type in rationale for non-feature tasks", () => {
    const result = analyzeChange(["type:verification", "size:S"], "Verify fix for #256");

    expect(result.rationale).toContain("verification task");
  });

  it("should skip planning for chore tasks", () => {
    const result = analyzeChange(["type:chore", "size:M"], "Update dependencies");

    expect(result.taskType).toBe("chore");
    expect(result.skipStages).toContain("feature-planning");
  });

  it("should detect spike task type from label", () => {
    const result = analyzeChange(
      ["type:spike", "size:M", "priority:medium"],
      "Research multi-backend execution options"
    );

    expect(result.taskType).toBe("spike");
    expect(result.typeLabel).toBe("spike");
  });

  it("should return code change type for spike (prevents complexity cap)", () => {
    const result = analyzeChange(["type:spike", "size:M"], "Research authentication options");

    expect(result.changeType).toBe("code");
    // Complexity should NOT be capped at 2 (which would skip planning)
    expect(result.complexityScore).toBe(3);
  });

  it("should not skip planning for spike tasks", () => {
    const result = analyzeChange(["type:spike", "size:M"], "Research multi-factor authentication");

    expect(result.taskType).toBe("spike");
    expect(result.skipStages).not.toContain("feature-planning");
  });

  it("should include spike task type in rationale", () => {
    const result = analyzeChange(["type:spike", "size:M"], "Research OAuth integration");

    expect(result.rationale).toContain("spike task");
  });
});

describe("extractLabels - spike", () => {
  it("should extract type:spike label", () => {
    expect(extractLabels(["type:spike"]).type).toBe("spike");
  });

  it("should extract spike label without prefix", () => {
    expect(extractLabels(["spike"]).type).toBe("spike");
  });
});

describe("detectFoundationTask", () => {
  it("should return true: type:chore + scaffold title", () => {
    const labels: IssueLabels = {
      all: ["chore"],
      size: null,
      type: "chore",
      priority: null,
    };
    expect(detectFoundationTask(labels, "Scaffold the CLI package")).toBe(true);
  });

  it("should return true: type:chore + setup title", () => {
    const labels: IssueLabels = {
      all: ["chore"],
      size: null,
      type: "chore",
      priority: null,
    };
    expect(detectFoundationTask(labels, "Setup ESLint configuration")).toBe(true);
  });

  it("should return true: type:chore + initialize title", () => {
    const labels: IssueLabels = {
      all: ["chore"],
      size: null,
      type: "chore",
      priority: null,
    };
    expect(detectFoundationTask(labels, "Initialize the database schema")).toBe(true);
  });

  it("should return true: type:chore + bootstrap title", () => {
    const labels: IssueLabels = {
      all: ["chore"],
      size: null,
      type: "chore",
      priority: null,
    };
    expect(detectFoundationTask(labels, "Bootstrap the frontend app")).toBe(true);
  });

  it("should return true: type:chore + configure title", () => {
    const labels: IssueLabels = {
      all: ["chore"],
      size: null,
      type: "chore",
      priority: null,
    };
    expect(detectFoundationTask(labels, "Configure TypeScript compiler options")).toBe(true);
  });

  it('should return true: strong phrase "initialize monorepo" without chore label', () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };
    expect(detectFoundationTask(labels, "Initialize monorepo structure")).toBe(true);
  });

  it('should return true: strong phrase "setup typescript" without chore label', () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };
    expect(detectFoundationTask(labels, "Setup TypeScript for the project")).toBe(true);
  });

  it('should return true: strong phrase "configure github actions" without chore label', () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };
    expect(detectFoundationTask(labels, "Configure GitHub Actions CI pipeline")).toBe(true);
  });

  it("should return false: type:chore + non-scaffold title", () => {
    const labels: IssueLabels = {
      all: ["chore"],
      size: null,
      type: "chore",
      priority: null,
    };
    expect(detectFoundationTask(labels, "Fix login bug")).toBe(false);
  });

  it("should return false: type:feature + scaffold title (no chore, no strong phrase)", () => {
    const labels: IssueLabels = {
      all: ["type:feature"],
      size: null,
      type: "feature",
      priority: null,
    };
    expect(detectFoundationTask(labels, "Scaffold user profile page")).toBe(false);
  });

  it("should return false: empty labels + generic title", () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };
    expect(detectFoundationTask(labels, "Add user login feature")).toBe(false);
  });
});

describe("determineSkipStages - foundationTask=true (#1318)", () => {
  it("should skip both feature-planning and feature-validate when foundationTask=true", () => {
    const result = determineSkipStages("code", 3, "M", "chore", true);

    expect(result).toContain("feature-planning");
    expect(result).toContain("feature-validate");
  });

  it("should not produce duplicates when taskType=chore (already skips planning) and foundationTask=true", () => {
    const result = determineSkipStages("code", 3, "M", "chore", true);

    expect(result.filter((s) => s === "feature-planning").length).toBe(1);
    expect(result.filter((s) => s === "feature-validate").length).toBe(1);
  });

  it("should skip both stages for feature taskType + foundationTask=true (strong phrase match)", () => {
    const result = determineSkipStages("code", 3, "M", "feature", true);

    expect(result).toContain("feature-planning");
    expect(result).toContain("feature-validate");
  });

  it("should not skip validate for standard feature when foundationTask=false", () => {
    const result = determineSkipStages("code", 3, "M", "feature", false);

    expect(result).not.toContain("feature-planning");
    expect(result).not.toContain("feature-validate");
  });
});

describe("analyzeChange - foundationTask routing (#1318)", () => {
  it("should set foundationTask=true and skip both stages for type:chore + scaffold title", () => {
    const result = analyzeChange(["type:chore", "size:M"], "Initialize npm workspaces");

    expect(result.foundationTask).toBe(true);
    expect(result.skipStages).toContain("feature-planning");
    expect(result.skipStages).toContain("feature-validate");
  });

  it("should set foundationTask=false for type:chore + non-scaffold title (only planning skipped)", () => {
    const result = analyzeChange(["type:chore", "size:M"], "Remove legacy dead code");

    expect(result.foundationTask).toBe(false);
    expect(result.skipStages).toContain("feature-planning");
    expect(result.skipStages).not.toContain("feature-validate");
  });

  it("should set foundationTask=true for strong phrase without chore label and skip both stages", () => {
    const result = analyzeChange(["type:feature", "size:M"], "Setup TypeScript compiler");

    expect(result.foundationTask).toBe(true);
    expect(result.skipStages).toContain("feature-planning");
    expect(result.skipStages).toContain("feature-validate");
  });

  it("should set foundationTask=false for type:feature + regular title (no stages skipped)", () => {
    const result = analyzeChange(["type:feature", "size:M"], "Add user login");

    expect(result.foundationTask).toBe(false);
    expect(result.skipStages).not.toContain("feature-planning");
    expect(result.skipStages).not.toContain("feature-validate");
  });
});

describe("detectTaskType - spike content detection", () => {
  it('should detect spike from "research" in title', () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };

    expect(detectTaskType(labels, "Research OAuth2 implementation")).toBe("spike");
  });

  it('should detect spike from "investigate" in title', () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };

    expect(detectTaskType(labels, "Investigate performance bottleneck")).toBe("spike");
  });

  it('should detect spike from "proof of concept" in body', () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };

    expect(
      detectTaskType(labels, "New architecture", "Build a proof of concept for the new design")
    ).toBe("spike");
  });

  it('should detect spike from "feasibility" in title', () => {
    const labels: IssueLabels = {
      all: [],
      size: null,
      type: null,
      priority: null,
    };

    expect(detectTaskType(labels, "Feasibility study for Bedrock integration")).toBe("spike");
  });

  it("should prioritize label over content detection", () => {
    const labels: IssueLabels = {
      all: ["type:feature"],
      size: null,
      type: "feature",
      priority: null,
    };

    // Title says "research" but label says "feature"
    expect(detectTaskType(labels, "Research new authentication approach")).toBe("feature");
  });
});

describe("isHighRisk + RISK_FLOOR (#4093)", () => {
  const lbl = (all: string[]): IssueLabels => ({ all, size: null, type: null, priority: null });

  it("flags high blast-radius labels", () => {
    expect(isHighRisk(lbl(["component:security"])).high).toBe(true);
    expect(isHighRisk(lbl(["component:billing"])).high).toBe(true);
    expect(isHighRisk(lbl(["area:db-migration"])).high).toBe(true);
    expect(isHighRisk(lbl(["public-api"])).high).toBe(true);
    expect(isHighRisk(lbl(["needs-credential-rotation"])).high).toBe(true);
  });

  it("honors the explicit escape hatch", () => {
    expect(isHighRisk(lbl(["risk:high"])).high).toBe(true);
    expect(isHighRisk(lbl(["risk-high"])).high).toBe(true);
  });

  it("returns false for benign labels and de-dupes reasons", () => {
    expect(isHighRisk(lbl(["type:feature", "size:M"])).high).toBe(false);
    expect(isHighRisk(lbl(["component:billing", "component:billing"])).reasons).toEqual([
      "component:billing",
    ]);
  });

  it("forces extensive route + no skips for a high-risk low-complexity issue", () => {
    // Baseline: tiny feature, no risk label → trivial + skips.
    const baseline = analyzeChange(["type:feature", "size:XS"], "tweak copy");
    expect(baseline.suggestedRoute).toBe("trivial");
    expect(baseline.skipStages.length).toBeGreaterThan(0);
    expect(baseline.riskHigh).toBe(false);

    // Same change in a high-risk area → extensive, nothing skipped.
    const risky = analyzeChange(["type:feature", "size:XS", "component:security"], "tweak copy");
    expect(risky.riskHigh).toBe(true);
    expect(risky.suggestedRoute).toBe("extensive");
    expect(risky.skipStages).toEqual([]);
    expect(risky.riskReasons).toContain("component:security");
  });

  it("determineRoutingPath / determineSkipStages honor the highRisk flag", () => {
    expect(determineRoutingPath("code", 1, lbl(["size:XS"]), true)).toBe("extensive");
    expect(determineSkipStages("code", 1, "XS", "feature", false, true)).toEqual([]);
  });
});
