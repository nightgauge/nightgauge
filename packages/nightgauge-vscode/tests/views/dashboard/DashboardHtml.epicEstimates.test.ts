/**
 * DashboardHtml.epicEstimates.test.ts
 *
 * Tests for Issue #1017: Epic estimation failure shows hardcoded message
 * instead of actual error. Verifies that failed epic cards render the
 * actual `entry.warning` message (HTML-escaped) rather than a generic
 * hardcoded string.
 *
 * @see Issue #1017 - Epic estimation failure shows hardcoded message
 */

import { describe, it, expect } from "vitest";
import { getEpicEstimatesHtml } from "../../../src/views/dashboard/DashboardHtml";
import type {
  DashboardAggregates,
  EpicDisplayEntry,
} from "../../../src/views/dashboard/DashboardState";

import { makeEmptyAggregates } from "./fixtures/aggregates";

function createEmptyAggregates(epicEstimates: EpicDisplayEntry[] = []): DashboardAggregates {
  return makeEmptyAggregates({ epicEstimates });
}

describe("Epic estimation error messages (Issue #1017)", () => {
  it("renders the actual warning message for failed epics", () => {
    const aggregates = createEmptyAggregates([
      {
        epic_number: 42,
        epic_title: "My Epic",
        estimate: null,
        warning: "Failed to fetch epic #42: gh CLI not found",
      },
    ]);

    const html = getEpicEstimatesHtml(aggregates);

    expect(html).toContain("Failed to fetch epic #42: gh CLI not found");
    expect(html).not.toContain(
      "Add sub-issue references (e.g., #123) to the epic body to enable progress tracking."
    );
  });

  it("renders no-sub-issue warning when that is the actual error", () => {
    const aggregates = createEmptyAggregates([
      {
        epic_number: 10,
        epic_title: "Another Epic",
        estimate: null,
        warning:
          'Epic #10 has no sub-issue references. Add issue references like #123, GH-456, or "closes #789" to the epic body.',
      },
    ]);

    const html = getEpicEstimatesHtml(aggregates);

    expect(html).toContain("Epic #10 has no sub-issue references.");
    expect(html).toContain("&quot;closes #789&quot;");
  });

  it("escapes HTML in warning messages to prevent XSS", () => {
    const aggregates = createEmptyAggregates([
      {
        epic_number: 99,
        epic_title: "XSS Epic",
        estimate: null,
        warning: '<script>alert("xss")</script>',
      },
    ]);

    const html = getEpicEstimatesHtml(aggregates);

    expect(html).toContain("&lt;script&gt;alert(&quot;xss&quot;)&lt;/script&gt;");
    expect(html).not.toContain('<script>alert("xss")</script>');
  });

  it("renders fallback message when warning is null", () => {
    const aggregates = createEmptyAggregates([
      {
        epic_number: 77,
        epic_title: "Null Warning Epic",
        estimate: null,
        warning: null,
      },
    ]);

    const html = getEpicEstimatesHtml(aggregates);

    expect(html).toContain("Unable to estimate this epic.");
  });
});
