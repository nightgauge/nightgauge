/**
 * Unit tests for severityMapping — Finding-to-Issue label utilities
 *
 * Tests pure mapping functions that translate Finding severity and
 * HealthDimension values into GitHub issue labels for the Finding-to-Issue
 * engine (Issue #1102).
 */

import { describe, it, expect } from "vitest";
import {
  SEVERITY_ORDER,
  severityMeetsThreshold,
  severityToPriorityLabel,
  severityToSizeLabel,
  dimensionToComponentLabel,
  severityToTypeLabel,
  findingToLabels,
} from "../../../src/analysis/health/severityMapping.js";
import type { Severity, HealthDimension, Finding } from "../../../src/analysis/health/types.js";

// ── Test helpers ───────────────────────────────────────────────────

function makeFinding(overrides: Partial<Finding> = {}): Finding {
  return {
    id: "finding-001",
    dimension: "reliability",
    severity: "medium",
    title: "Test finding",
    description: "A test finding description",
    impact: "Moderate impact on pipeline reliability",
    recommendation: "Address the underlying issue",
    evidence: { sampleSize: 10 },
    confidence: "medium",
    ...overrides,
  };
}

// ── SEVERITY_ORDER ────────────────────────────────────────────────

describe("SEVERITY_ORDER", () => {
  it("assigns expected numeric rank to each severity level", () => {
    expect(SEVERITY_ORDER["critical"]).toBe(4);
    expect(SEVERITY_ORDER["high"]).toBe(3);
    expect(SEVERITY_ORDER["medium"]).toBe(2);
    expect(SEVERITY_ORDER["low"]).toBe(1);
    expect(SEVERITY_ORDER["info"]).toBe(0);
  });

  it("ranks critical strictly above all others", () => {
    const others: Severity[] = ["high", "medium", "low", "info"];
    others.forEach((s) => {
      expect(SEVERITY_ORDER["critical"]).toBeGreaterThan(SEVERITY_ORDER[s]);
    });
  });

  it("maintains strict total order: critical > high > medium > low > info", () => {
    const ordered: Severity[] = ["critical", "high", "medium", "low", "info"];
    for (let i = 0; i < ordered.length - 1; i++) {
      expect(SEVERITY_ORDER[ordered[i]]).toBeGreaterThan(SEVERITY_ORDER[ordered[i + 1]]);
    }
  });
});

// ── severityMeetsThreshold ────────────────────────────────────────

describe("severityMeetsThreshold", () => {
  it("returns true when severity equals the threshold", () => {
    const severities: Severity[] = ["critical", "high", "medium", "low", "info"];
    severities.forEach((s) => {
      expect(severityMeetsThreshold(s, s)).toBe(true);
    });
  });

  const meetsCases: Array<{
    severity: Severity;
    threshold: Severity;
    expected: boolean;
    label: string;
  }> = [
    // Severity clearly above threshold
    {
      severity: "critical",
      threshold: "high",
      expected: true,
      label: "critical >= high",
    },
    {
      severity: "critical",
      threshold: "medium",
      expected: true,
      label: "critical >= medium",
    },
    {
      severity: "critical",
      threshold: "low",
      expected: true,
      label: "critical >= low",
    },
    {
      severity: "critical",
      threshold: "info",
      expected: true,
      label: "critical >= info",
    },
    {
      severity: "high",
      threshold: "medium",
      expected: true,
      label: "high >= medium",
    },
    {
      severity: "high",
      threshold: "low",
      expected: true,
      label: "high >= low",
    },
    {
      severity: "high",
      threshold: "info",
      expected: true,
      label: "high >= info",
    },
    {
      severity: "medium",
      threshold: "low",
      expected: true,
      label: "medium >= low",
    },
    {
      severity: "medium",
      threshold: "info",
      expected: true,
      label: "medium >= info",
    },
    {
      severity: "low",
      threshold: "info",
      expected: true,
      label: "low >= info",
    },
    // Severity clearly below threshold
    {
      severity: "info",
      threshold: "low",
      expected: false,
      label: "info < low",
    },
    {
      severity: "info",
      threshold: "medium",
      expected: false,
      label: "info < medium",
    },
    {
      severity: "info",
      threshold: "high",
      expected: false,
      label: "info < high",
    },
    {
      severity: "info",
      threshold: "critical",
      expected: false,
      label: "info < critical",
    },
    {
      severity: "low",
      threshold: "medium",
      expected: false,
      label: "low < medium",
    },
    {
      severity: "low",
      threshold: "high",
      expected: false,
      label: "low < high",
    },
    {
      severity: "low",
      threshold: "critical",
      expected: false,
      label: "low < critical",
    },
    {
      severity: "medium",
      threshold: "high",
      expected: false,
      label: "medium < high",
    },
    {
      severity: "medium",
      threshold: "critical",
      expected: false,
      label: "medium < critical",
    },
    {
      severity: "high",
      threshold: "critical",
      expected: false,
      label: "high < critical",
    },
    // Boundary: adjacent levels
    {
      severity: "high",
      threshold: "high",
      expected: true,
      label: "high == high (boundary)",
    },
    {
      severity: "medium",
      threshold: "medium",
      expected: true,
      label: "medium == medium (boundary)",
    },
  ];

  meetsCases.forEach(({ severity, threshold, expected, label }) => {
    it(`${label} → ${expected}`, () => {
      expect(severityMeetsThreshold(severity, threshold)).toBe(expected);
    });
  });
});

// ── severityToPriorityLabel ───────────────────────────────────────

describe("severityToPriorityLabel", () => {
  const cases: Array<{ severity: Severity; expected: string }> = [
    { severity: "critical", expected: "priority:critical" },
    { severity: "high", expected: "priority:high" },
    { severity: "medium", expected: "priority:medium" },
    { severity: "low", expected: "priority:low" },
    { severity: "info", expected: "priority:low" },
  ];

  cases.forEach(({ severity, expected }) => {
    it(`maps ${severity} → ${expected}`, () => {
      expect(severityToPriorityLabel(severity)).toBe(expected);
    });
  });

  it("maps both low and info to the same priority label", () => {
    expect(severityToPriorityLabel("low")).toBe(severityToPriorityLabel("info"));
  });
});

// ── severityToSizeLabel ───────────────────────────────────────────

describe("severityToSizeLabel", () => {
  const cases: Array<{ severity: Severity; expected: string }> = [
    { severity: "critical", expected: "size:M" },
    { severity: "high", expected: "size:S" },
    { severity: "medium", expected: "size:S" },
    { severity: "low", expected: "size:XS" },
    { severity: "info", expected: "size:XS" },
  ];

  cases.forEach(({ severity, expected }) => {
    it(`maps ${severity} → ${expected}`, () => {
      expect(severityToSizeLabel(severity)).toBe(expected);
    });
  });

  it("maps high and medium to the same size label", () => {
    expect(severityToSizeLabel("high")).toBe(severityToSizeLabel("medium"));
  });

  it("maps low and info to the same size label", () => {
    expect(severityToSizeLabel("low")).toBe(severityToSizeLabel("info"));
  });
});

// ── severityToTypeLabel ───────────────────────────────────────────

describe("severityToTypeLabel", () => {
  const cases: Array<{ severity: Severity; expected: string }> = [
    { severity: "critical", expected: "type:fix" },
    { severity: "high", expected: "type:fix" },
    { severity: "medium", expected: "type:chore" },
    { severity: "low", expected: "type:chore" },
    { severity: "info", expected: "type:chore" },
  ];

  cases.forEach(({ severity, expected }) => {
    it(`maps ${severity} → ${expected}`, () => {
      expect(severityToTypeLabel(severity)).toBe(expected);
    });
  });

  it("maps critical and high to the same type label", () => {
    expect(severityToTypeLabel("critical")).toBe(severityToTypeLabel("high"));
  });

  it("maps medium, low, and info to the same type label", () => {
    expect(severityToTypeLabel("medium")).toBe(severityToTypeLabel("low"));
    expect(severityToTypeLabel("low")).toBe(severityToTypeLabel("info"));
  });
});

// ── dimensionToComponentLabel ─────────────────────────────────────

describe("dimensionToComponentLabel", () => {
  const cases: Array<{ dimension: HealthDimension; expected: string }> = [
    {
      dimension: "token-economics",
      expected: "component:health-token-economics",
    },
    { dimension: "cost-health", expected: "component:health-cost-health" },
    {
      dimension: "stage-effectiveness",
      expected: "component:health-stage-effectiveness",
    },
    { dimension: "model-routing", expected: "component:health-model-routing" },
    { dimension: "reliability", expected: "component:health-reliability" },
    {
      dimension: "learning-effectiveness",
      expected: "component:health-learning-effectiveness",
    },
    {
      dimension: "pipeline-velocity",
      expected: "component:health-pipeline-velocity",
    },
  ];

  cases.forEach(({ dimension, expected }) => {
    it(`maps ${dimension} → ${expected}`, () => {
      expect(dimensionToComponentLabel(dimension)).toBe(expected);
    });
  });

  it('always prefixes with "component:health-"', () => {
    const dimensions: HealthDimension[] = [
      "token-economics",
      "cost-health",
      "stage-effectiveness",
      "model-routing",
      "reliability",
      "learning-effectiveness",
      "pipeline-velocity",
    ];
    dimensions.forEach((d) => {
      expect(dimensionToComponentLabel(d)).toMatch(/^component:health-/);
    });
  });
});

// ── findingToLabels ───────────────────────────────────────────────

describe("findingToLabels", () => {
  it("returns exactly 4 labels", () => {
    const finding = makeFinding();
    expect(findingToLabels(finding)).toHaveLength(4);
  });

  it("includes priority label derived from severity", () => {
    const finding = makeFinding({ severity: "critical" });
    const labels = findingToLabels(finding);
    expect(labels).toContain("priority:critical");
  });

  it("includes size label derived from severity", () => {
    const finding = makeFinding({ severity: "critical" });
    const labels = findingToLabels(finding);
    expect(labels).toContain("size:M");
  });

  it("includes type label derived from severity", () => {
    const finding = makeFinding({ severity: "critical" });
    const labels = findingToLabels(finding);
    expect(labels).toContain("type:fix");
  });

  it("includes component label derived from dimension", () => {
    const finding = makeFinding({ dimension: "pipeline-velocity" });
    const labels = findingToLabels(finding);
    expect(labels).toContain("component:health-pipeline-velocity");
  });

  it("returns labels in order: priority, size, type, component", () => {
    const finding = makeFinding({
      severity: "high",
      dimension: "model-routing",
    });
    const labels = findingToLabels(finding);
    expect(labels[0]).toBe("priority:high");
    expect(labels[1]).toBe("size:S");
    expect(labels[2]).toBe("type:fix");
    expect(labels[3]).toBe("component:health-model-routing");
  });

  const severityCases: Array<{
    severity: Severity;
    dimension: HealthDimension;
    expectedLabels: string[];
  }> = [
    {
      severity: "critical",
      dimension: "reliability",
      expectedLabels: ["priority:critical", "size:M", "type:fix", "component:health-reliability"],
    },
    {
      severity: "high",
      dimension: "cost-health",
      expectedLabels: ["priority:high", "size:S", "type:fix", "component:health-cost-health"],
    },
    {
      severity: "medium",
      dimension: "token-economics",
      expectedLabels: [
        "priority:medium",
        "size:S",
        "type:chore",
        "component:health-token-economics",
      ],
    },
    {
      severity: "low",
      dimension: "learning-effectiveness",
      expectedLabels: [
        "priority:low",
        "size:XS",
        "type:chore",
        "component:health-learning-effectiveness",
      ],
    },
    {
      severity: "info",
      dimension: "stage-effectiveness",
      expectedLabels: [
        "priority:low",
        "size:XS",
        "type:chore",
        "component:health-stage-effectiveness",
      ],
    },
  ];

  severityCases.forEach(({ severity, dimension, expectedLabels }) => {
    it(`severity=${severity}, dimension=${dimension} → correct 4 labels`, () => {
      const finding = makeFinding({ severity, dimension });
      expect(findingToLabels(finding)).toEqual(expectedLabels);
    });
  });

  it("uses only severity and dimension fields from the finding", () => {
    const findingA = makeFinding({
      severity: "medium",
      dimension: "reliability",
      id: "finding-A",
      title: "Title A",
      description: "Description A",
    });
    const findingB = makeFinding({
      severity: "medium",
      dimension: "reliability",
      id: "finding-B",
      title: "Title B",
      description: "Description B",
    });
    expect(findingToLabels(findingA)).toEqual(findingToLabels(findingB));
  });
});
