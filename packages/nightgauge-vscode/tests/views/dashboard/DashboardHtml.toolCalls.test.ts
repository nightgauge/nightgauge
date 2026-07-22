/**
 * DashboardHtml.toolCalls.test.ts
 *
 * Tests for lazy tool call rendering optimization (Issue #716).
 * Verifies that tool call details are stored as data attributes
 * and not pre-rendered in the DOM.
 *
 * @see Issue #716 - Optimize tool call details rendering to reduce DOM overhead
 */

import { describe, it, expect } from "vitest";
import { getDashboardHtml, getToolCallsHtml } from "../../../src/views/dashboard/DashboardHtml";
import type {
  PipelineRunSummary,
  ToolCallEntry,
  DashboardAggregates,
  TimeSavingsConfig,
} from "../../../src/views/dashboard/DashboardState";

const mockWebview = { cspSource: "test-csp" } as any;

function createToolCall(overrides: Partial<ToolCallEntry> = {}): ToolCallEntry {
  return {
    tool: "Read",
    target: "src/index.ts",
    timestamp: new Date("2025-06-01T12:00:00Z"),
    ...overrides,
  };
}

function createRun(toolCalls: ToolCallEntry[]): PipelineRunSummary {
  return {
    issueNumber: 1,
    title: "Test run",
    branch: "feat/1-test",
    status: "running" as any,
    stages: [],
    toolCalls,
    startedAt: new Date("2025-06-01T12:00:00Z"),
    usage: {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
      durationMs: 0,
      stageCount: 0,
    },
  };
}

import { makeEmptyAggregates } from "./fixtures/aggregates";

const emptyAggregates: DashboardAggregates = makeEmptyAggregates();

const timeSavingsConfig: TimeSavingsConfig = {
  estimatedManualMinutes: {},
  hourlyRate: 50,
};

describe("Tool call lazy rendering (Issue #716)", () => {
  it("stores tool call args in data-args attribute instead of pre-rendering", () => {
    const toolCall = createToolCall({
      args: { path: "/src/index.ts", line: 42 },
    });
    const run = createRun([toolCall]);

    const html = getDashboardHtml(mockWebview, run, [], emptyAggregates, timeSavingsConfig);

    // Data attribute should be present with JSON-encoded args
    expect(html).toContain('data-args="');
    // The details div should be empty (no pre-rendered content)
    expect(html).toMatch(/id="details-0" style="display: none;"><\/div>/);
    // Should NOT contain pre-rendered args content
    expect(html).not.toMatch(/<div class="tool-call-args">.*<code>.*path.*<\/code><\/div>/s);
  });

  it("stores tool call result in data-result attribute", () => {
    const toolCall = createToolCall({
      result: "File contents here",
    });
    const run = createRun([toolCall]);

    const html = getDashboardHtml(mockWebview, run, [], emptyAggregates, timeSavingsConfig);

    expect(html).toContain('data-result="File contents here"');
    // Details div should be empty (lazy rendering), verified by empty closing tag
    expect(html).toMatch(/id="details-0" style="display: none;"><\/div>/);
  });

  it("stores tool call error in data-error attribute", () => {
    const toolCall = createToolCall({
      error: "File not found",
    });
    const run = createRun([toolCall]);

    const html = getDashboardHtml(mockWebview, run, [], emptyAggregates, timeSavingsConfig);

    expect(html).toContain('data-error="File not found"');
    // Details div should be empty (lazy rendering)
    expect(html).toMatch(/id="details-0" style="display: none;"><\/div>/);
  });

  it("stores tool call duration in data-duration attribute", () => {
    const toolCall = createToolCall({
      durationMs: 150,
    });
    const run = createRun([toolCall]);

    const html = getDashboardHtml(mockWebview, run, [], emptyAggregates, timeSavingsConfig);

    expect(html).toContain('data-duration="150"');
    expect(html).not.toContain('<div class="tool-call-duration">150ms</div>');
  });

  it("omits data attributes when fields are absent", () => {
    const toolCall = createToolCall(); // no args, result, error, durationMs
    const run = createRun([toolCall]);

    const html = getDashboardHtml(mockWebview, run, [], emptyAggregates, timeSavingsConfig);

    expect(html).toContain('data-tool="Read"');
    expect(html).not.toContain("data-args");
    expect(html).not.toContain("data-result");
    expect(html).not.toContain("data-error");
    expect(html).not.toContain("data-duration");
  });

  it("truncates long results at 500 chars in data-result attribute", () => {
    const longResult = "x".repeat(600);
    const toolCall = createToolCall({ result: longResult });
    const run = createRun([toolCall]);

    const html = getDashboardHtml(mockWebview, run, [], emptyAggregates, timeSavingsConfig);

    // Should contain truncated result with ellipsis indicator
    expect(html).toContain('data-result="' + "x".repeat(500) + "...");
  });

  it("includes toggleToolCall JS with lazy population logic", () => {
    const toolCall = createToolCall({ args: { foo: "bar" } });
    const run = createRun([toolCall]);

    const html = getDashboardHtml(mockWebview, run, [], emptyAggregates, timeSavingsConfig);

    // JS should check for data-populated flag
    expect(html).toContain("details.dataset.populated");
    // JS should read from item.dataset.args
    expect(html).toContain("item.dataset.args");
    // JS should use JSON.parse for pretty-printing args on expand
    expect(html).toContain("JSON.parse(item.dataset.args)");
  });

  it("escapes HTML entities in data attributes to prevent injection", () => {
    const toolCall = createToolCall({
      args: { query: '<script>alert("xss")</script>' },
      error: "Error: <b>bad</b>",
    });
    const run = createRun([toolCall]);

    const html = getDashboardHtml(mockWebview, run, [], emptyAggregates, timeSavingsConfig);

    // HTML entities should be escaped in data attributes
    expect(html).not.toContain("<script>alert");
    expect(html).toContain("&lt;script&gt;");
    expect(html).toContain("&lt;b&gt;bad&lt;/b&gt;");
  });
});

describe("Historical tool call on-demand loading (Issue #1032)", () => {
  it("should show load button when historicalIssueNumber is provided and toolCalls are empty", () => {
    const html = getToolCallsHtml([], 42);

    expect(html).toContain("Load Tool Calls");
    expect(html).toContain('data-action="load-tool-calls"');
    expect(html).toContain('data-issue="42"');
    expect(html).toContain("tool-calls-load-container");
  });

  it("should show normal empty state when no historicalIssueNumber", () => {
    const html = getToolCallsHtml([]);

    expect(html).toContain("No tool calls recorded yet");
    expect(html).not.toContain("Load Tool Calls");
  });

  it("should render tool calls normally when data is present regardless of historicalIssueNumber", () => {
    const toolCalls = [createToolCall()];
    const html = getToolCallsHtml(toolCalls, 42);

    expect(html).toContain('data-tool="Read"');
    expect(html).not.toContain("Load Tool Calls");
  });

  it("should include loadRunToolCalls function in full dashboard HTML", () => {
    const run = createRun([]);
    const html = getDashboardHtml(mockWebview, run, [run], emptyAggregates, timeSavingsConfig);

    expect(html).toContain("load-tool-calls");
    expect(html).toContain("type: 'loadRunDetails'");
  });

  it("should auto-load tool calls for most recent historical run (no current run) in full dashboard (Issue #1842)", () => {
    const historyRun = createRun([]);
    // When currentRun is null, displayRun comes from history[0] — auto-load instead of manual button
    const html = getDashboardHtml(
      mockWebview,
      null,
      [historyRun],
      emptyAggregates,
      timeSavingsConfig
    );

    // Auto-load marker instead of manual button
    expect(html).toContain(`data-auto-load-issue="${historyRun.issueNumber}"`);
    expect(html).not.toContain("Load Tool Calls");
  });

  it("should include tool-calls in VALID_SECTIONS for incremental updates", () => {
    const run = createRun([]);
    const html = getDashboardHtml(mockWebview, run, [], emptyAggregates, timeSavingsConfig);

    expect(html).toContain("'tool-calls'");
    expect(html).toContain('id="section-tool-calls"');
  });
});
