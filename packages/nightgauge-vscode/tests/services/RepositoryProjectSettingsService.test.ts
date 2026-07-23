import { describe, expect, it } from "vitest";
import {
  normalizeProjectAssignments,
  withSingleDefault,
} from "../../src/services/RepositoryProjectSettingsService";

describe("RepositoryProjectSettingsService helpers", () => {
  it("converts legacy project.number into one default assignment", () => {
    expect(normalizeProjectAssignments({ project: { number: 8 } }, "team")).toEqual([
      { name: "Default", number: 8, default: true, source: "team" },
    ]);
  });

  it("preserves multiple projects and their configured default", () => {
    expect(
      normalizeProjectAssignments(
        {
          projects: [
            { name: "Engineering", number: 8 },
            { name: "Community", number: 10, default: true },
          ],
        },
        "local"
      )
    ).toEqual([
      { name: "Engineering", number: 8, default: false, source: "local" },
      { name: "Community", number: 10, default: true, source: "local" },
    ]);
  });

  it("uses the first project as the effective default when none is marked", () => {
    const assignments = normalizeProjectAssignments(
      {
        projects: [
          { name: "One", number: 1 },
          { name: "Two", number: 2 },
        ],
      },
      "team"
    );
    expect(assignments.map((entry) => entry.default)).toEqual([true, false]);
  });

  it("sets exactly one default without changing repository-local provenance", () => {
    const assignments = normalizeProjectAssignments(
      {
        projects: [
          { name: "One", number: 1, default: true },
          { name: "Two", number: 2 },
        ],
      },
      "local"
    );
    expect(withSingleDefault(assignments, 2)).toEqual([
      { name: "One", number: 1, default: false, source: "local" },
      { name: "Two", number: 2, default: true, source: "local" },
    ]);
  });
});
