/**
 * Unit tests for RoutingPatterns
 *
 * @see routingPatterns.ts
 * @see Issue #325 - AI-Powered Epic Decomposition
 */

import { describe, it, expect } from "vitest";
import {
  tokenizeContent,
  calculateConfidence,
  matchPattern,
  matchRoutingPatterns,
  getBestMatch,
  parseAcceptanceCriteria,
  parseSubIssues,
  extractKeywords,
  generateDecomposition,
  generateSubIssueTitle,
  getUnmatchedItems,
  formatConfidence,
  getConfidenceLevel,
  groupByRepository,
  validatePatterns,
  type RoutingPattern,
  type EpicContent,
  type RoutingConfig,
} from "../../src/utils/routingPatterns";

describe("tokenizeContent", () => {
  it("should tokenize content into lowercase words", () => {
    const content = "Add User Authentication API";
    const tokens = tokenizeContent(content);

    expect(tokens).toContain("add");
    expect(tokens).toContain("user");
    expect(tokens).toContain("authentication");
    expect(tokens).toContain("api");
  });

  it("should remove markdown formatting", () => {
    const content = "## Add **bold** and `code` [link](url)";
    const tokens = tokenizeContent(content);

    expect(tokens).toContain("add");
    expect(tokens).toContain("bold");
    expect(tokens).toContain("code");
    expect(tokens).toContain("link");
    expect(tokens).not.toContain("##");
    expect(tokens).not.toContain("**");
  });

  it("should filter out single-character tokens", () => {
    const content = "a b c add user";
    const tokens = tokenizeContent(content);

    expect(tokens).not.toContain("a");
    expect(tokens).not.toContain("b");
    expect(tokens).not.toContain("c");
    expect(tokens).toContain("add");
    expect(tokens).toContain("user");
  });

  it("should handle hyphenated words", () => {
    const content = "front-end back-end e2e";
    const tokens = tokenizeContent(content);

    expect(tokens).toContain("front-end");
    expect(tokens).toContain("back-end");
    expect(tokens).toContain("e2e");
  });

  it("should handle empty content", () => {
    const tokens = tokenizeContent("");
    expect(tokens).toEqual([]);
  });
});

describe("calculateConfidence", () => {
  const pattern: RoutingPattern = {
    id: "frontend",
    keywords: ["ui", "component", "react", "css"],
    preferred_repo: "org/frontend",
  };

  it("should calculate confidence as ratio of matched keywords", () => {
    const matched = ["ui", "react"];
    const confidence = calculateConfidence(matched, pattern);

    expect(confidence).toBe(0.5); // 2 out of 4
  });

  it("should return 0 for empty pattern keywords", () => {
    const emptyPattern: RoutingPattern = {
      id: "empty",
      keywords: [],
      preferred_repo: "org/repo",
    };
    const confidence = calculateConfidence(["ui"], emptyPattern);

    expect(confidence).toBe(0);
  });

  it("should return 0 if below default threshold", () => {
    const matched = ["ui"]; // 1 out of 4 = 0.25, below 0.3 threshold
    const confidence = calculateConfidence(matched, pattern);

    expect(confidence).toBe(0);
  });

  it("should respect custom min_confidence threshold", () => {
    const strictPattern: RoutingPattern = {
      id: "strict",
      keywords: ["a", "b", "c", "d"],
      preferred_repo: "org/repo",
      min_confidence: 0.75,
    };
    const matched = ["a", "b"]; // 0.5, below 0.75 threshold
    const confidence = calculateConfidence(matched, strictPattern);

    expect(confidence).toBe(0);
  });

  it("should return confidence when above custom threshold", () => {
    const lenientPattern: RoutingPattern = {
      id: "lenient",
      keywords: ["a", "b", "c", "d"],
      preferred_repo: "org/repo",
      min_confidence: 0.2,
    };
    const matched = ["a"]; // 0.25, above 0.2 threshold
    const confidence = calculateConfidence(matched, lenientPattern);

    expect(confidence).toBe(0.25);
  });
});

describe("matchPattern", () => {
  const frontendPattern: RoutingPattern = {
    id: "frontend",
    keywords: ["ui", "component", "react", "css", "styling"],
    preferred_repo: "org/frontend",
  };

  it("should match content with pattern keywords", () => {
    const content = "Create UI component with React styling";
    const match = matchPattern(content, frontendPattern);

    expect(match).not.toBeNull();
    expect(match?.pattern_id).toBe("frontend");
    expect(match?.preferred_repo).toBe("org/frontend");
    expect(match?.matched_keywords).toContain("ui");
    expect(match?.matched_keywords).toContain("component");
    expect(match?.matched_keywords).toContain("react");
    expect(match?.matched_keywords).toContain("styling");
  });

  it("should return null when no keywords match", () => {
    const content = "Add database migration scripts";
    const match = matchPattern(content, frontendPattern);

    expect(match).toBeNull();
  });

  it("should return null when matches below threshold", () => {
    const content = "Add UI"; // Only 1 keyword, 1/5 = 0.2 < 0.3 threshold
    const match = matchPattern(content, frontendPattern);

    expect(match).toBeNull();
  });

  it("should match case-insensitively", () => {
    const content = "CREATE UI COMPONENT";
    const match = matchPattern(content, frontendPattern);

    expect(match).not.toBeNull();
    expect(match?.matched_keywords).toContain("ui");
    expect(match?.matched_keywords).toContain("component");
  });

  it("should match substring keywords", () => {
    const content = "Add button component for styling";
    const match = matchPattern(content, frontendPattern);

    expect(match).not.toBeNull();
    expect(match?.matched_keywords).toContain("component");
    expect(match?.matched_keywords).toContain("styling");
  });
});

describe("matchRoutingPatterns", () => {
  const patterns: RoutingPattern[] = [
    {
      id: "frontend",
      keywords: ["ui", "component", "react"],
      preferred_repo: "org/frontend",
    },
    {
      id: "backend",
      keywords: ["api", "endpoint", "service"],
      preferred_repo: "org/backend",
    },
    {
      id: "shared",
      keywords: ["types", "utils", "common"],
      preferred_repo: "org/shared",
    },
  ];

  it("should return matches sorted by confidence", () => {
    // Matches both frontend and backend, but more frontend keywords
    const content = "Create UI component with API integration";
    const matches = matchRoutingPatterns(content, patterns);

    expect(matches.length).toBeGreaterThan(0);
    // Should be sorted by confidence
    for (let i = 1; i < matches.length; i++) {
      expect(matches[i - 1].confidence).toBeGreaterThanOrEqual(matches[i].confidence);
    }
  });

  it("should return empty array when no patterns match", () => {
    const content = "Update documentation";
    const matches = matchRoutingPatterns(content, patterns);

    expect(matches).toEqual([]);
  });

  it("should return multiple matches when applicable", () => {
    const content = "Create shared types for API endpoints and UI components";
    const matches = matchRoutingPatterns(content, patterns);

    // Should match multiple patterns
    expect(matches.length).toBeGreaterThanOrEqual(2);
  });
});

describe("getBestMatch", () => {
  const patterns: RoutingPattern[] = [
    {
      id: "frontend",
      keywords: ["ui", "component", "react", "css"],
      preferred_repo: "org/frontend",
    },
    {
      id: "backend",
      keywords: ["api", "endpoint"],
      preferred_repo: "org/backend",
    },
  ];

  it("should return the highest confidence match", () => {
    const content = "Create UI component with React and CSS";
    const match = getBestMatch(content, patterns);

    expect(match?.pattern_id).toBe("frontend");
  });

  it("should return null when no matches", () => {
    const content = "Update documentation";
    const match = getBestMatch(content, patterns);

    expect(match).toBeNull();
  });
});

describe("parseAcceptanceCriteria", () => {
  it("should parse checkbox items from body", () => {
    const body = `
## Acceptance Criteria

- [ ] User can log in with email
- [ ] User can log out
- [x] Session is persisted
    `;
    const criteria = parseAcceptanceCriteria(body);

    expect(criteria).toHaveLength(3);
    expect(criteria).toContain("User can log in with email");
    expect(criteria).toContain("User can log out");
    expect(criteria).toContain("Session is persisted");
  });

  it("should handle asterisk bullet points", () => {
    const body = `
* [ ] First item
* [x] Second item
    `;
    const criteria = parseAcceptanceCriteria(body);

    expect(criteria).toHaveLength(2);
  });

  it("should handle indented checkboxes", () => {
    const body = `
    - [ ] Indented checkbox
        - [ ] Double indented
    `;
    const criteria = parseAcceptanceCriteria(body);

    expect(criteria).toHaveLength(2);
  });

  it("should return empty array for no checkboxes", () => {
    const body = "Just some text without checkboxes";
    const criteria = parseAcceptanceCriteria(body);

    expect(criteria).toEqual([]);
  });
});

describe("parseSubIssues", () => {
  it("should parse numbered list items", () => {
    const body = `
## Sub-Issues

1. Implement authentication service
2. Create login component
3. Add session management
    `;
    const subIssues = parseSubIssues(body);

    expect(subIssues).toContain("Implement authentication service");
    expect(subIssues).toContain("Create login component");
    expect(subIssues).toContain("Add session management");
  });

  it("should filter out reproduction steps", () => {
    const body = `
## Steps to Reproduce

1. Go to login page
2. Click login button
3. Navigate to dashboard
    `;
    const subIssues = parseSubIssues(body);

    // Should filter out action verbs like "Go to", "Click", "Navigate"
    expect(subIssues).toEqual([]);
  });

  it("should handle mixed content", () => {
    const body = `
1. Implement API endpoint
2. Go to settings (this should be filtered)
3. Create database schema
    `;
    const subIssues = parseSubIssues(body);

    expect(subIssues).toContain("Implement API endpoint");
    expect(subIssues).toContain("Create database schema");
    expect(subIssues).not.toContain("Go to settings (this should be filtered)");
  });
});

describe("extractKeywords", () => {
  it("should extract frontend keywords", () => {
    const content = "Add UI component with React and CSS styling";
    const keywords = extractKeywords(content);

    expect(keywords).toContain("ui");
    expect(keywords).toContain("component");
    expect(keywords).toContain("react");
    expect(keywords).toContain("css");
    expect(keywords).toContain("styling");
  });

  it("should extract backend keywords", () => {
    const content = "Create API endpoint for authentication service";
    const keywords = extractKeywords(content);

    expect(keywords).toContain("api");
    expect(keywords).toContain("endpoint");
    expect(keywords).toContain("authentication");
    expect(keywords).toContain("service");
  });

  it("should extract shared/common keywords", () => {
    const content = "Add shared types and utils module";
    const keywords = extractKeywords(content);

    expect(keywords).toContain("shared");
    expect(keywords).toContain("types");
    expect(keywords).toContain("utils");
    expect(keywords).toContain("module");
  });

  it("should return unique keywords", () => {
    const content = "API api API endpoint endpoint";
    const keywords = extractKeywords(content);

    // Should not have duplicates
    const uniqueKeywords = [...new Set(keywords)];
    expect(keywords).toEqual(uniqueKeywords);
  });
});

describe("generateDecomposition", () => {
  const config: RoutingConfig = {
    patterns: [
      {
        id: "frontend",
        keywords: ["ui", "component", "react", "button", "form"],
        preferred_repo: "org/frontend",
      },
      {
        id: "backend",
        keywords: ["api", "endpoint", "service", "database"],
        preferred_repo: "org/backend",
      },
    ],
    default_repository: "org/main",
    ai_fallback: true,
  };

  it("should generate suggestions for acceptance criteria", () => {
    const epic: EpicContent = {
      title: "User Authentication",
      body: "",
      acceptance_criteria: ["Create login form component", "Add authentication API endpoint"],
    };

    const suggestions = generateDecomposition(epic, config);

    expect(suggestions).toHaveLength(2);

    const formSuggestion = suggestions.find((s) => s.source.includes("login form"));
    expect(formSuggestion?.suggested_repo).toBe("org/frontend");

    const apiSuggestion = suggestions.find((s) => s.source.includes("API"));
    expect(apiSuggestion?.suggested_repo).toBe("org/backend");
  });

  it("should use default repository for unmatched items", () => {
    const epic: EpicContent = {
      title: "Random Task",
      body: "",
      acceptance_criteria: ["Update documentation"],
    };

    const suggestions = generateDecomposition(epic, config);

    expect(suggestions).toHaveLength(1);
    expect(suggestions[0].suggested_repo).toBe("org/main");
    expect(suggestions[0].confidence).toBe(0.1);
  });

  it("should set suggestion_source correctly", () => {
    const epic: EpicContent = {
      title: "Epic",
      body: "",
      acceptance_criteria: ["Create UI button component"],
    };

    const suggestions = generateDecomposition(epic, config);

    expect(suggestions[0].suggestion_source).toBe("pattern");
  });

  it("should deduplicate items from multiple sources", () => {
    const epic: EpicContent = {
      title: "Epic",
      body: "1. Create API endpoint",
      acceptance_criteria: ["Create API endpoint"],
    };

    const suggestions = generateDecomposition(epic, config);

    expect(suggestions).toHaveLength(1);
  });
});

describe("generateSubIssueTitle", () => {
  it("should capitalize first letter", () => {
    const title = generateSubIssueTitle("add user authentication");
    expect(title).toBe("Add user authentication");
  });

  it("should remove markdown formatting", () => {
    const title = generateSubIssueTitle("Add **bold** and `code`");
    expect(title).toBe("Add bold and code");
  });

  it("should truncate long titles", () => {
    const longDesc =
      "This is a very long description that should be truncated because it exceeds the maximum length for a title";
    const title = generateSubIssueTitle(longDesc);

    expect(title.length).toBeLessThanOrEqual(70);
    expect(title).toMatch(/\.\.\.$/);
  });

  it("should not truncate short titles", () => {
    const title = generateSubIssueTitle("Short title");
    expect(title).toBe("Short title");
  });
});

describe("getUnmatchedItems", () => {
  it("should return items not in suggestions", () => {
    const epic: EpicContent = {
      title: "Epic",
      body: "",
      acceptance_criteria: ["Item 1", "Item 2", "Item 3"],
    };

    const suggestions = [
      {
        title: "Item 1",
        source: "Item 1",
        suggested_repo: "org/repo",
        confidence: 0.8,
        suggestion_source: "pattern" as const,
      },
    ];

    const unmatched = getUnmatchedItems(epic, suggestions);

    expect(unmatched).toContain("Item 2");
    expect(unmatched).toContain("Item 3");
    expect(unmatched).not.toContain("Item 1");
  });

  it("should return empty array when all items matched", () => {
    const epic: EpicContent = {
      title: "Epic",
      body: "",
      acceptance_criteria: ["Item 1"],
    };

    const suggestions = [
      {
        title: "Item 1",
        source: "Item 1",
        suggested_repo: "org/repo",
        confidence: 0.8,
        suggestion_source: "pattern" as const,
      },
    ];

    const unmatched = getUnmatchedItems(epic, suggestions);

    expect(unmatched).toEqual([]);
  });
});

describe("formatConfidence", () => {
  it("should format as percentage", () => {
    expect(formatConfidence(0.78)).toBe("78%");
    expect(formatConfidence(1.0)).toBe("100%");
    expect(formatConfidence(0.333)).toBe("33%");
  });

  it("should round to nearest integer", () => {
    expect(formatConfidence(0.785)).toBe("79%");
    expect(formatConfidence(0.784)).toBe("78%");
  });
});

describe("getConfidenceLevel", () => {
  it("should return high for >= 0.7", () => {
    expect(getConfidenceLevel(0.7)).toBe("high");
    expect(getConfidenceLevel(0.9)).toBe("high");
    expect(getConfidenceLevel(1.0)).toBe("high");
  });

  it("should return medium for 0.4-0.69", () => {
    expect(getConfidenceLevel(0.4)).toBe("medium");
    expect(getConfidenceLevel(0.5)).toBe("medium");
    expect(getConfidenceLevel(0.69)).toBe("medium");
  });

  it("should return low for < 0.4", () => {
    expect(getConfidenceLevel(0.39)).toBe("low");
    expect(getConfidenceLevel(0.1)).toBe("low");
    expect(getConfidenceLevel(0)).toBe("low");
  });
});

describe("groupByRepository", () => {
  it("should group suggestions by repository", () => {
    const suggestions = [
      {
        title: "Item 1",
        source: "Item 1",
        suggested_repo: "org/frontend",
        confidence: 0.8,
        suggestion_source: "pattern" as const,
      },
      {
        title: "Item 2",
        source: "Item 2",
        suggested_repo: "org/backend",
        confidence: 0.7,
        suggestion_source: "pattern" as const,
      },
      {
        title: "Item 3",
        source: "Item 3",
        suggested_repo: "org/frontend",
        confidence: 0.6,
        suggestion_source: "pattern" as const,
      },
    ];

    const groups = groupByRepository(suggestions);

    expect(groups.get("org/frontend")).toHaveLength(2);
    expect(groups.get("org/backend")).toHaveLength(1);
  });

  it("should return empty map for no suggestions", () => {
    const groups = groupByRepository([]);
    expect(groups.size).toBe(0);
  });
});

describe("validatePatterns", () => {
  it("should pass for valid patterns", () => {
    const patterns: RoutingPattern[] = [
      {
        id: "frontend",
        keywords: ["ui", "component"],
        preferred_repo: "org/frontend",
      },
      {
        id: "backend",
        keywords: ["api", "endpoint"],
        preferred_repo: "org/backend",
      },
    ];

    const result = validatePatterns(patterns);

    expect(result.valid).toBe(true);
    expect(result.errors).toEqual([]);
  });

  it("should fail for missing id", () => {
    const patterns = [
      {
        id: "",
        keywords: ["ui"],
        preferred_repo: "org/repo",
      },
    ] as RoutingPattern[];

    const result = validatePatterns(patterns);

    expect(result.valid).toBe(false);
    expect(result.errors).toContain("Pattern at index 0: missing or empty 'id'");
  });

  it("should fail for duplicate ids", () => {
    const patterns: RoutingPattern[] = [
      {
        id: "frontend",
        keywords: ["ui"],
        preferred_repo: "org/frontend",
      },
      {
        id: "frontend",
        keywords: ["api"],
        preferred_repo: "org/backend",
      },
    ];

    const result = validatePatterns(patterns);

    expect(result.valid).toBe(false);
    expect(result.errors.some((e) => e.includes("duplicate id"))).toBe(true);
  });

  it("should fail for empty keywords", () => {
    const patterns: RoutingPattern[] = [
      {
        id: "empty",
        keywords: [],
        preferred_repo: "org/repo",
      },
    ];

    const result = validatePatterns(patterns);

    expect(result.valid).toBe(false);
    expect(result.errors.some((e) => e.includes("non-empty array"))).toBe(true);
  });

  it("should fail for missing preferred_repo", () => {
    const patterns = [
      {
        id: "test",
        keywords: ["ui"],
        preferred_repo: "",
      },
    ] as RoutingPattern[];

    const result = validatePatterns(patterns);

    expect(result.valid).toBe(false);
    expect(result.errors.some((e) => e.includes("missing or empty 'preferred_repo'"))).toBe(true);
  });

  it("should fail for invalid min_confidence", () => {
    const patterns: RoutingPattern[] = [
      {
        id: "test",
        keywords: ["ui"],
        preferred_repo: "org/repo",
        min_confidence: 1.5,
      },
    ];

    const result = validatePatterns(patterns);

    expect(result.valid).toBe(false);
    expect(result.errors.some((e) => e.includes("between 0 and 1"))).toBe(true);
  });

  it("should pass for valid min_confidence", () => {
    const patterns: RoutingPattern[] = [
      {
        id: "test",
        keywords: ["ui"],
        preferred_repo: "org/repo",
        min_confidence: 0.5,
      },
    ];

    const result = validatePatterns(patterns);

    expect(result.valid).toBe(true);
  });
});
