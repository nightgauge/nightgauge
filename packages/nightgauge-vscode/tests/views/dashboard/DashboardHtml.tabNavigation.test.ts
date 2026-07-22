/**
 * DashboardHtml.tabNavigation.test.ts
 *
 * Tests for tabbed dashboard navigation (Issue #1539).
 * Verifies tab bar rendering, tab panel structure, active tab state,
 * section-to-tab assignments, and ARIA attributes.
 */

import { describe, it, expect } from "vitest";
import { getDashboardHtml } from "../../../src/views/dashboard/DashboardHtml";
import type {
  DashboardAggregates,
  PipelineRunSummary,
  TimeSavingsConfig,
} from "../../../src/views/dashboard/DashboardState";

const mockWebview = { cspSource: "test-csp" } as any;

import { makeEmptyAggregates } from "./fixtures/aggregates";

const emptyAggregates: DashboardAggregates = makeEmptyAggregates();

const timeSavingsConfig: TimeSavingsConfig = {
  estimatedManualMinutes: {},
  hourlyRate: 50,
};

function createRun(): PipelineRunSummary {
  return {
    issueNumber: 42,
    title: "Test run",
    branch: "feat/42-test",
    status: "running" as any,
    stages: [],
    toolCalls: [],
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

function renderWithTab(activeTab?: string): string {
  return getDashboardHtml(
    mockWebview,
    null,
    [],
    emptyAggregates,
    timeSavingsConfig,
    "all",
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    activeTab
  );
}

describe("Tab navigation (Issue #1539)", () => {
  describe("Tab bar rendering", () => {
    it('renders a tab bar with role="tablist"', () => {
      const html = renderWithTab();
      expect(html).toContain('role="tablist"');
      expect(html).toContain('class="tab-bar"');
    });

    it("renders exactly 13 tab buttons", () => {
      const html = renderWithTab();
      const tabMatches = html.match(/role="tab"/g);
      expect(tabMatches).toHaveLength(13);
    });

    it("renders tabs with correct data-tab values", () => {
      const html = renderWithTab();
      expect(html).toContain('data-tab="overview"');
      expect(html).toContain('data-tab="pipeline"');
      expect(html).toContain('data-tab="analytics"');
      expect(html).toContain('data-tab="history"');
      expect(html).toContain('data-tab="epics"');
      expect(html).toContain('data-tab="audit"');
      expect(html).toContain('data-tab="discovery"');
      expect(html).toContain('data-tab="runs"');
    });

    it("renders tab labels", () => {
      const html = renderWithTab();
      expect(html).toContain(">Overview</button>");
      expect(html).toContain(">Pipeline</button>");
      expect(html).toContain(">Analytics</button>");
      expect(html).toContain(">History</button>");
      expect(html).toContain(">Epics</button>");
      expect(html).toContain(">Audit Trail</button>");
      expect(html).toContain(">Discovery</button>");
      expect(html).toContain(">Runs</button>");
    });
  });

  describe("Active tab state", () => {
    it("defaults to overview tab as active", () => {
      const html = renderWithTab();
      expect(html).toContain('class="tab-btn active"');
      expect(html).toContain('id="tab-overview" data-tab="overview" tabindex="0"');
      expect(html).toMatch(/class="tab-btn active"[^>]*aria-selected="true"[^>]*id="tab-overview"/);
    });

    it("marks pipeline tab as active when activeTab=pipeline", () => {
      const html = renderWithTab("pipeline");
      // Pipeline tab should be active
      expect(html).toMatch(/class="tab-btn active"[^>]*aria-selected="true"[^>]*id="tab-pipeline"/);
      // Overview tab should not be active
      expect(html).toMatch(/class="tab-btn"[^>]*aria-selected="false"[^>]*id="tab-overview"/);
    });

    it("marks analytics tab as active when activeTab=analytics", () => {
      const html = renderWithTab("analytics");
      expect(html).toMatch(
        /class="tab-btn active"[^>]*aria-selected="true"[^>]*id="tab-analytics"/
      );
    });

    it("marks history tab as active when activeTab=history", () => {
      const html = renderWithTab("history");
      expect(html).toMatch(/class="tab-btn active"[^>]*aria-selected="true"[^>]*id="tab-history"/);
    });

    it("marks epics tab as active when activeTab=epics", () => {
      const html = renderWithTab("epics");
      expect(html).toMatch(/class="tab-btn active"[^>]*aria-selected="true"[^>]*id="tab-epics"/);
      expect(html).toContain('class="tab-panel active" id="tab-panel-epics"');
    });
  });

  describe("Tab panels", () => {
    it('renders 13 tab panels with role="tabpanel"', () => {
      const html = renderWithTab();
      const panelMatches = html.match(/role="tabpanel"/g);
      expect(panelMatches).toHaveLength(13);
    });

    it("renders panels with correct IDs", () => {
      const html = renderWithTab();
      expect(html).toContain('id="tab-panel-overview"');
      expect(html).toContain('id="tab-panel-pipeline"');
      expect(html).toContain('id="tab-panel-analytics"');
      expect(html).toContain('id="tab-panel-history"');
      expect(html).toContain('id="tab-panel-epics"');
      expect(html).toContain('id="tab-panel-discovery"');
      expect(html).toContain('id="tab-panel-runs"');
    });

    it("renders panels with aria-labelledby linking to tab buttons", () => {
      const html = renderWithTab();
      expect(html).toContain('aria-labelledby="tab-overview"');
      expect(html).toContain('aria-labelledby="tab-pipeline"');
      expect(html).toContain('aria-labelledby="tab-analytics"');
      expect(html).toContain('aria-labelledby="tab-history"');
      expect(html).toContain('aria-labelledby="tab-epics"');
    });

    it("only the active tab panel has active class by default", () => {
      const html = renderWithTab();
      expect(html).toContain('class="tab-panel active" id="tab-panel-overview"');
      expect(html).toMatch(/class="tab-panel" id="tab-panel-pipeline"/);
      expect(html).toMatch(/class="tab-panel" id="tab-panel-analytics"/);
      expect(html).toMatch(/class="tab-panel" id="tab-panel-history"/);
    });

    it("pipeline panel is active when activeTab=pipeline", () => {
      const html = renderWithTab("pipeline");
      expect(html).toContain('class="tab-panel active" id="tab-panel-pipeline"');
      expect(html).toMatch(/class="tab-panel" id="tab-panel-overview"/);
    });
  });

  describe("Section-to-tab assignment", () => {
    it("places section-summary-cards inside the overview panel", () => {
      const html = renderWithTab();
      const overviewStart = html.indexOf('id="tab-panel-overview"');
      const overviewEnd = html.indexOf('id="tab-panel-pipeline"');
      const summaryPos = html.indexOf('id="section-summary-cards"');
      expect(summaryPos).toBeGreaterThan(overviewStart);
      expect(summaryPos).toBeLessThan(overviewEnd);
    });

    it("places section-tool-calls inside the pipeline panel", () => {
      const html = renderWithTab();
      const pipelineStart = html.indexOf('id="tab-panel-pipeline"');
      const pipelineEnd = html.indexOf('id="tab-panel-analytics"');
      const toolCallsPos = html.indexOf('id="section-tool-calls"');
      expect(toolCallsPos).toBeGreaterThan(pipelineStart);
      expect(toolCallsPos).toBeLessThan(pipelineEnd);
    });

    it("places section-analytics inside the analytics panel", () => {
      const html = renderWithTab();
      const analyticsStart = html.indexOf('id="tab-panel-analytics"');
      const analyticsEnd = html.indexOf('id="tab-panel-history"');
      const sectionPos = html.indexOf('id="section-analytics"');
      expect(sectionPos).toBeGreaterThan(analyticsStart);
      expect(sectionPos).toBeLessThan(analyticsEnd);
    });

    it("places section-pipeline-progress inside the pipeline panel when run exists", () => {
      const html = getDashboardHtml(
        mockWebview,
        createRun(),
        [],
        emptyAggregates,
        timeSavingsConfig,
        "all",
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        "overview"
      );
      const pipelineStart = html.indexOf('id="tab-panel-pipeline"');
      const pipelineEnd = html.indexOf('id="tab-panel-analytics"');
      const progressPos = html.indexOf('id="section-pipeline-progress"');
      expect(progressPos).toBeGreaterThan(pipelineStart);
      expect(progressPos).toBeLessThan(pipelineEnd);
    });
  });

  describe("Tab CSS", () => {
    it("includes tab CSS styles", () => {
      const html = renderWithTab();
      expect(html).toContain(".tab-bar");
      expect(html).toContain(".tab-btn");
      expect(html).toContain(".tab-panel");
      expect(html).toContain(".tab-panel.active");
    });

    it("tab bar uses sticky positioning", () => {
      const html = renderWithTab();
      expect(html).toContain("position: sticky");
    });
  });

  describe("Tab script", () => {
    it("includes activateTab function in script", () => {
      const html = renderWithTab();
      expect(html).toContain("function activateTab");
    });

    it("includes keyboard navigation handlers", () => {
      const html = renderWithTab();
      expect(html).toContain("ArrowRight");
      expect(html).toContain("ArrowLeft");
      expect(html).toContain("'Home'");
      expect(html).toContain("'End'");
    });

    it("sends selectTab message to extension host", () => {
      const html = renderWithTab();
      expect(html).toContain("type: 'selectTab'");
    });
  });
});
