/**
 * Unit tests for ProjectBoardService.mapToReadyIssue()
 *
 * Validates null-safe handling of labels field from GraphQL responses.
 *
 * @see Issue #633 - Project Board Summary crashes on undefined labels.nodes
 */

import { describe, it, expect } from "vitest";
import { ProjectBoardService } from "../ProjectBoardService";

/**
 * Minimal GraphQL issue content for testing mapToReadyIssue().
 * Mirrors GraphQLIssueContent but allows null/undefined labels.
 */
function makeContent(overrides: Record<string, unknown> = {}) {
  return {
    number: 100,
    title: "Test issue",
    url: "https://github.com/test/repo/issues/100",
    labels: { nodes: [{ name: "priority:high" }, { name: "size:S" }] },
    ...overrides,
  };
}

describe("ProjectBoardService.mapToReadyIssue", () => {
  // Access private method via bracket notation for unit testing
  const service = new ProjectBoardService("/tmp/test-workspace");
  const mapToReadyIssue = (content: unknown) =>
    (service as unknown as Record<string, CallableFunction>)["mapToReadyIssue"](content);

  it("maps labels correctly when present", () => {
    const result = mapToReadyIssue(makeContent());

    expect(result.labels).toEqual(["priority:high", "size:S"]);
    expect(result.priority).toBe("P1");
    expect(result.size).toBe("S");
  });

  it("defaults to empty labels when labels is undefined", () => {
    const result = mapToReadyIssue(makeContent({ labels: undefined }));

    expect(result.labels).toEqual([]);
    expect(result.priority).toBeNull();
    expect(result.size).toBeNull();
  });

  it("defaults to empty labels when labels is null", () => {
    const result = mapToReadyIssue(makeContent({ labels: null }));

    expect(result.labels).toEqual([]);
    expect(result.priority).toBeNull();
    expect(result.size).toBeNull();
  });

  it("defaults to empty labels when labels.nodes is undefined", () => {
    const result = mapToReadyIssue(makeContent({ labels: { nodes: undefined } }));

    expect(result.labels).toEqual([]);
  });

  it("defaults to empty labels when labels.nodes is null", () => {
    const result = mapToReadyIssue(makeContent({ labels: { nodes: null } }));

    expect(result.labels).toEqual([]);
  });

  it("handles empty labels.nodes array", () => {
    const result = mapToReadyIssue(makeContent({ labels: { nodes: [] } }));

    expect(result.labels).toEqual([]);
    expect(result.priority).toBeNull();
    expect(result.size).toBeNull();
  });

  it("preserves other fields regardless of labels state", () => {
    const result = mapToReadyIssue(makeContent({ labels: null }));

    expect(result.number).toBe(100);
    expect(result.title).toBe("Test issue");
    expect(result.url).toBe("https://github.com/test/repo/issues/100");
  });
});
