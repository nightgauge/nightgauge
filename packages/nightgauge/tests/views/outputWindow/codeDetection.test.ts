/**
 * codeDetection.test.ts - Tests for code block detection accuracy
 *
 * Uses real code samples from pipeline output to verify that isCode()
 * and detectContentType() correctly classify all code content.
 *
 * The code sample below is a real excerpt from a pipeline run (#789)
 * that was NOT correctly detected as code, causing fragmented rendering
 * in the Output Window.
 */

import { describe, it, expect } from "vitest";
import { detectContentType } from "../../../src/views/outputWindow/contentFormatter";

// Real code sample from pipeline run #789 that failed detection.
// This is the exact content the user reported as breaking out of code blocks.
const REAL_CODE_SAMPLE = `if (recent.length < 3 || older.length < 3) {
  return { improving: true, percentChange: 0, hasEnoughData: false };
}

const avgCost = (runs: PipelineRunSummary[]): number => {
  if (runs.length === 0) return 0;
  return runs.reduce((sum, r) => sum + r.usage.costUsd, 0) / runs.length;
};

const recentAvg = avgCost(recent);
const olderAvg = avgCost(older);

if (olderAvg === 0) {
  return { improving: true, percentChange: 0, hasEnoughData: false };
}

const percentChange = ((recentAvg - olderAvg) / olderAvg) * 100;

// Lower cost = improving (negative percent change is good)
return {
  improving: percentChange < 0,
  percentChange: Math.round(percentChange * 10) / 10,
  hasEnoughData: true,
};
  }


  /**



Get token usage trend analysis


@param recentCount - Number of recent runs to compare (default: 5)

@param olderCount - Number of older runs to compare against (default: 5)

@returns Object with trend direction and percent change
   */
  getTokenTrend(
recentCount: number = 5,
olderCount: number = 5
  ): {
direction: 'up' | 'down' | 'stable';
percentChange: number;
hasEnoughData: boolean;
  } {
const recent = this.history.slice(0, recentCount);
const older = this.history.slice(recentCount, recentCount + olderCount);


if (recent.length < 3 || older.length < 3) {
  return { direction: 'stable', percentChange: 0, hasEnoughData: false };
}

const avgTokens = (runs: PipelineRunSummary[]): number => {
  if (runs.length === 0) return 0;
  return (
    runs.reduce(
      (sum, r) => sum + r.usage.inputTokens + r.usage.outputTokens,
      0
    ) / runs.length
  );
};

const recentAvg = avgTokens(recent);
const olderAvg = avgTokens(older);

if (olderAvg === 0) {
  return { direction: 'stable', percentChange: 0, hasEnoughData: false };
}

const percentChange = ((recentAvg - olderAvg) / olderAvg) * 100;
const roundedChange = Math.round(percentChange * 10) / 10;

let direction: 'up' | 'down' | 'stable' = 'stable';
if (Math.abs(percentChange) >= 5) {
  direction = percentChange > 0 ? 'up' : 'down';
}

return {
  direction,
  percentChange: roundedChange,
  hasEnoughData: true,
};
  }`;

describe("Code detection accuracy", () => {
  it("should detect the full code sample as code", () => {
    const result = detectContentType(REAL_CODE_SAMPLE);
    expect(result).toBe("code");
  });

  it("should detect pure TypeScript function body as code", () => {
    const sample = `const avgCost = (runs: PipelineRunSummary[]): number => {
  if (runs.length === 0) return 0;
  return runs.reduce((sum, r) => sum + r.usage.costUsd, 0) / runs.length;
};`;
    expect(detectContentType(sample)).toBe("code");
  });

  it("should detect code with JSDoc comments as code", () => {
    const sample = `  /**
   * Get token usage trend analysis
   *
   * @param recentCount - Number of recent runs to compare (default: 5)
   * @param olderCount - Number of older runs to compare against (default: 5)
   * @returns Object with trend direction and percent change
   */
  getTokenTrend(
    recentCount: number = 5,
    olderCount: number = 5
  ): {
    direction: 'up' | 'down' | 'stable';
    percentChange: number;
    hasEnoughData: boolean;
  } {`;
    expect(detectContentType(sample)).toBe("code");
  });

  it("should detect code with return statements and object literals as code", () => {
    const sample = `return {
  improving: percentChange < 0,
  percentChange: Math.round(percentChange * 10) / 10,
  hasEnoughData: true,
};
  }`;
    expect(detectContentType(sample)).toBe("code");
  });

  it("should detect code with JSDoc that has stripped formatting as code", () => {
    // This is how JSDoc appears when newlines are normalized but
    // formatting asterisks were stripped by the streaming process
    const sample = `  }


  /**



Get token usage trend analysis


@param recentCount - Number of recent runs to compare (default: 5)

@param olderCount - Number of older runs to compare against (default: 5)

@returns Object with trend direction and percent change
   */
  getTokenTrend(
recentCount: number = 5,
olderCount: number = 5
  ): {`;
    expect(detectContentType(sample)).toBe("code");
  });

  it("should detect simple if/return blocks as code", () => {
    const sample = `if (recent.length < 3 || older.length < 3) {
  return { direction: 'stable', percentChange: 0, hasEnoughData: false };
}`;
    expect(detectContentType(sample)).toBe("code");
  });

  it("should detect variable declarations with function calls as code", () => {
    const sample = `const recentAvg = avgCost(recent);
const olderAvg = avgCost(older);

if (olderAvg === 0) {
  return { improving: true, percentChange: 0, hasEnoughData: false };
}

const percentChange = ((recentAvg - olderAvg) / olderAvg) * 100;`;
    expect(detectContentType(sample)).toBe("code");
  });

  it("should detect the second chunk (JSDoc + method) that breaks out", () => {
    // This is the exact chunk that fails: after the first code block ends,
    // the remaining content starts with closing brace, blanks, and a JSDoc
    // with stripped formatting (no * prefix, blank lines between @param).
    const breakoutChunk = `  }


  /**



Get token usage trend analysis


@param recentCount - Number of recent runs to compare (default: 5)

@param olderCount - Number of older runs to compare against (default: 5)

@returns Object with trend direction and percent change
   */
  getTokenTrend(
recentCount: number = 5,
olderCount: number = 5
  ): {
direction: 'up' | 'down' | 'stable';
percentChange: number;
hasEnoughData: boolean;
  } {
const recent = this.history.slice(0, recentCount);
const older = this.history.slice(recentCount, recentCount + olderCount);


if (recent.length < 3 || older.length < 3) {
  return { direction: 'stable', percentChange: 0, hasEnoughData: false };
}

const avgTokens = (runs: PipelineRunSummary[]): number => {
  if (runs.length === 0) return 0;
  return (
    runs.reduce(
      (sum, r) => sum + r.usage.inputTokens + r.usage.outputTokens,
      0
    ) / runs.length
  );
};

const recentAvg = avgTokens(recent);
const olderAvg = avgTokens(older);

if (olderAvg === 0) {
  return { direction: 'stable', percentChange: 0, hasEnoughData: false };
}

const percentChange = ((recentAvg - olderAvg) / olderAvg) * 100;
const roundedChange = Math.round(percentChange * 10) / 10;

let direction: 'up' | 'down' | 'stable' = 'stable';
if (Math.abs(percentChange) >= 5) {
  direction = percentChange > 0 ? 'up' : 'down';
}

return {
  direction,
  percentChange: roundedChange,
  hasEnoughData: true,
};
  }`;
    expect(detectContentType(breakoutChunk)).toBe("code");
  });

  it("should detect JUST the JSDoc + method signature chunk as code", () => {
    // Minimal JSDoc + method signature that might arrive as its own chunk
    const jsdocChunk = `  /**



Get token usage trend analysis


@param recentCount - Number of recent runs to compare (default: 5)

@param olderCount - Number of older runs to compare against (default: 5)

@returns Object with trend direction and percent change
   */
  getTokenTrend(
recentCount: number = 5,
olderCount: number = 5
  ): {
direction: 'up' | 'down' | 'stable';
percentChange: number;
hasEnoughData: boolean;
  } {`;
    expect(detectContentType(jsdocChunk)).toBe("code");
  });

  it("should NOT detect plain prose as code", () => {
    const sample = `This is a description of the function.
It takes two parameters and returns an object.
The first parameter controls how many recent items to look at.
The second parameter controls how many older items to compare against.`;
    expect(detectContentType(sample)).toBe("text");
  });

  it("should detect TypeScript type annotations in return types as code", () => {
    const sample = `  ): {
    direction: 'up' | 'down' | 'stable';
    percentChange: number;
    hasEnoughData: boolean;
  } {
    const recent = this.history.slice(0, recentCount);
    const older = this.history.slice(recentCount, recentCount + olderCount);`;
    expect(detectContentType(sample)).toBe("code");
  });
});
