/**
 * Unit tests for global config merging functionality
 *
 * Tests the config merging logic with source annotations.
 * Uses pure functions from schema.ts that don't require VSCode mocking.
 *
 * @see Issue #434 - Add Global Config Layer
 */

import { describe, it, expect, beforeEach } from "vitest";
import {
  type ConfigSourceMap,
  type ConfigSource,
  trackSource,
  trackObjectSources,
  getSource,
  mergeWithDefaults,
  DEFAULT_CONFIG,
} from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";

describe("Global Config Merge", () => {
  describe("Source Tracking", () => {
    let sources: ConfigSourceMap;

    beforeEach(() => {
      sources = {};
    });

    describe("trackSource", () => {
      it("should track a single source", () => {
        trackSource(sources, "project.number", "project");

        expect(sources["project.number"]).toBe("project");
      });

      it("should overwrite existing source", () => {
        trackSource(sources, "pr.merge_strategy", "global");
        trackSource(sources, "pr.merge_strategy", "project");

        expect(sources["pr.merge_strategy"]).toBe("project");
      });

      it("should track multiple paths", () => {
        trackSource(sources, "project.number", "project");
        trackSource(sources, "pr.merge_strategy", "global");
        trackSource(sources, "branch.base", "default");

        expect(sources["project.number"]).toBe("project");
        expect(sources["pr.merge_strategy"]).toBe("global");
        expect(sources["branch.base"]).toBe("default");
      });
    });

    describe("trackObjectSources", () => {
      it("should track all keys in a flat object", () => {
        const obj = {
          number: 123,
          auto_dates: true,
        };

        trackObjectSources(sources, obj, "project", "global");

        expect(sources["project.number"]).toBe("global");
        expect(sources["project.auto_dates"]).toBe("global");
      });

      it("should track nested objects recursively", () => {
        const obj = {
          prefixes: {
            feature: "feat/",
            bugfix: "fix/",
          },
        };

        trackObjectSources(sources, obj, "branch", "global");

        expect(sources["branch.prefixes"]).toBe("global");
        expect(sources["branch.prefixes.feature"]).toBe("global");
        expect(sources["branch.prefixes.bugfix"]).toBe("global");
      });

      it("should skip undefined values", () => {
        const obj = {
          number: 123,
          owner: undefined,
        };

        trackObjectSources(sources, obj, "project", "project");

        expect(sources["project.number"]).toBe("project");
        expect(sources["project.owner"]).toBeUndefined();
      });

      it("should skip null values", () => {
        const obj = {
          number: 123,
          owner: null,
        };

        trackObjectSources(sources, obj as Record<string, unknown>, "project", "project");

        expect(sources["project.number"]).toBe("project");
        expect(sources["project.owner"]).toBeUndefined();
      });

      it("should track arrays as single values (not items)", () => {
        const obj = {
          reviewers: ["alice", "bob"],
        };

        trackObjectSources(sources, obj, "pr", "project");

        // Arrays are tracked as a single value, not recursively
        expect(sources["pr.reviewers"]).toBe("project");
        expect(sources["pr.reviewers.0"]).toBeUndefined();
      });

      it("should handle empty prefix (root level)", () => {
        const obj = {
          project: { number: 123 },
        };

        trackObjectSources(sources, obj, "", "default");

        expect(sources["project"]).toBe("default");
        expect(sources["project.number"]).toBe("default");
      });
    });

    describe("getSource", () => {
      it("should return tracked source", () => {
        sources["project.number"] = "project";

        expect(getSource(sources, "project.number")).toBe("project");
      });

      it('should return "default" for untracked paths', () => {
        expect(getSource(sources, "unknown.path")).toBe("default");
      });

      it("should return correct source for nested paths", () => {
        sources["branch.prefixes.feature"] = "global";

        expect(getSource(sources, "branch.prefixes.feature")).toBe("global");
      });
    });
  });

  describe("Config Merging", () => {
    describe("mergeWithDefaults", () => {
      it("should return defaults for empty config", () => {
        const result = mergeWithDefaults({});

        expect(result).toEqual(DEFAULT_CONFIG);
      });

      it("should preserve user values over defaults", () => {
        const config: IncrediConfig = {
          project: { number: 42 },
        };

        const result = mergeWithDefaults(config);

        expect(result.project?.number).toBe(42);
        expect(result.project?.auto_dates).toBe(true); // From defaults
      });

      it("should deep merge nested objects", () => {
        const config: IncrediConfig = {
          branch: {
            base: "develop",
            // Other values should come from defaults
          },
        };

        const result = mergeWithDefaults(config);

        expect(result.branch?.base).toBe("develop");
        expect(result.branch?.protected).toEqual(["main", "master"]);
        expect(result.branch?.suggestions).toBe(true);
      });

      it("should handle null input", () => {
        const result = mergeWithDefaults(null as unknown as IncrediConfig);

        expect(result).toEqual(DEFAULT_CONFIG);
      });

      it("should handle undefined input", () => {
        const result = mergeWithDefaults(undefined as unknown as IncrediConfig);

        expect(result).toEqual(DEFAULT_CONFIG);
      });
    });

    describe("Three-way merge simulation", () => {
      // Simulates the merge logic: defaults <- global <- project

      function deepMerge<T extends Record<string, unknown>>(target: T, source: Partial<T>): T {
        const result = { ...target } as Record<string, unknown>;

        for (const key in source) {
          const sourceValue = source[key];
          const targetValue = target[key];

          if (sourceValue === undefined) {
            continue;
          }

          if (
            typeof sourceValue === "object" &&
            sourceValue !== null &&
            !Array.isArray(sourceValue) &&
            typeof targetValue === "object" &&
            targetValue !== null &&
            !Array.isArray(targetValue)
          ) {
            result[key] = deepMerge(
              targetValue as Record<string, unknown>,
              sourceValue as Record<string, unknown>
            );
          } else {
            result[key] = sourceValue;
          }
        }

        return result as T;
      }

      it("should apply global config over defaults", () => {
        const defaultConfig: IncrediConfig = {
          pr: { merge_strategy: "squash", delete_branch: true },
        };

        const globalConfig: IncrediConfig = {
          pr: { merge_strategy: "rebase" },
        };

        const result = deepMerge(defaultConfig, globalConfig);

        expect(result.pr?.merge_strategy).toBe("rebase");
        expect(result.pr?.delete_branch).toBe(true); // From defaults
      });

      it("should apply project config over global config", () => {
        const globalConfig: IncrediConfig = {
          pr: { merge_strategy: "rebase", delete_branch: false },
        };

        const projectConfig: IncrediConfig = {
          pr: { merge_strategy: "squash" },
        };

        const result = deepMerge(globalConfig, projectConfig);

        expect(result.pr?.merge_strategy).toBe("squash"); // Project wins
        expect(result.pr?.delete_branch).toBe(false); // From global
      });

      it("should handle three-way merge correctly", () => {
        const defaultConfig: IncrediConfig = {
          pr: { merge_strategy: "squash", delete_branch: true, reviewers: [] },
          branch: { base: "main" },
        };

        const globalConfig: IncrediConfig = {
          pr: { merge_strategy: "rebase" },
          project: { number: 10 },
        };

        const projectConfig: IncrediConfig = {
          pr: { delete_branch: false },
          project: { number: 20 },
        };

        // Merge: defaults <- global <- project
        const merged1 = deepMerge(defaultConfig, globalConfig);
        const merged2 = deepMerge(merged1, projectConfig);

        expect(merged2.pr?.merge_strategy).toBe("rebase"); // From global (not overridden by project)
        expect(merged2.pr?.delete_branch).toBe(false); // From project
        expect(merged2.pr?.reviewers).toEqual([]); // From defaults
        expect(merged2.branch?.base).toBe("main"); // From defaults
        expect(merged2.project?.number).toBe(20); // From project (overrides global)
      });

      it("should replace arrays (not merge them)", () => {
        const globalConfig: IncrediConfig = {
          pr: { reviewers: ["alice", "bob"] },
        };

        const projectConfig: IncrediConfig = {
          pr: { reviewers: ["charlie"] },
        };

        const result = deepMerge(globalConfig, projectConfig);

        // Arrays are replaced, not merged
        expect(result.pr?.reviewers).toEqual(["charlie"]);
      });

      it("should not merge undefined values", () => {
        const globalConfig: IncrediConfig = {
          pr: { merge_strategy: "rebase" },
        };

        const projectConfig: IncrediConfig = {
          pr: { merge_strategy: undefined },
        };

        const result = deepMerge(globalConfig, projectConfig);

        // undefined doesn't override
        expect(result.pr?.merge_strategy).toBe("rebase");
      });
    });
  });

  describe("Source Annotation with Merge", () => {
    it("should track sources through a full merge scenario", () => {
      const sources: ConfigSourceMap = {};

      // 1. Track defaults
      const defaultConfig: IncrediConfig = {
        pr: { merge_strategy: "squash", delete_branch: true },
        branch: { base: "main" },
      };
      trackObjectSources(sources, defaultConfig as Record<string, unknown>, "", "default");

      // 2. Track global config (overwrites some defaults)
      const globalConfig: IncrediConfig = {
        pr: { merge_strategy: "rebase" },
      };
      trackObjectSources(sources, globalConfig as Record<string, unknown>, "", "global");

      // 3. Track project config (overwrites some global)
      const projectConfig: IncrediConfig = {
        pr: { delete_branch: false },
      };
      trackObjectSources(sources, projectConfig as Record<string, unknown>, "", "project");

      // Check sources
      expect(getSource(sources, "pr.merge_strategy")).toBe("global");
      expect(getSource(sources, "pr.delete_branch")).toBe("project");
      expect(getSource(sources, "branch.base")).toBe("default");
      expect(getSource(sources, "unknown.path")).toBe("default");
    });
  });
});
