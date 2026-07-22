/**
 * Unit tests for local config merge functionality
 *
 * Tests the local config merging logic with source annotations.
 * Uses pure functions from schema.ts that don't require VSCode mocking.
 *
 * @see Issue #435 - Add local config override
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

describe("Local Config Merge", () => {
  describe("Source Tracking with Local", () => {
    let sources: ConfigSourceMap;

    beforeEach(() => {
      sources = {};
    });

    describe("trackSource with local", () => {
      it("should track local source", () => {
        trackSource(sources, "human_in_the_loop.auto_accept_stages", "local");

        expect(sources["human_in_the_loop.auto_accept_stages"]).toBe("local");
      });

      it("should allow local to overwrite project source", () => {
        trackSource(sources, "pr.delete_branch", "project");
        trackSource(sources, "pr.delete_branch", "local");

        expect(sources["pr.delete_branch"]).toBe("local");
      });

      it("should track local alongside other sources", () => {
        trackSource(sources, "project.number", "project");
        trackSource(sources, "pr.merge_strategy", "global");
        trackSource(sources, "branch.base", "default");
        trackSource(sources, "pr.delete_branch", "local");

        expect(sources["project.number"]).toBe("project");
        expect(sources["pr.merge_strategy"]).toBe("global");
        expect(sources["branch.base"]).toBe("default");
        expect(sources["pr.delete_branch"]).toBe("local");
      });
    });

    describe("trackObjectSources with local", () => {
      it("should track all keys from local config", () => {
        const obj = {
          delete_branch: true,
          draft_by_default: true,
        };

        trackObjectSources(sources, obj, "pr", "local");

        expect(sources["pr.delete_branch"]).toBe("local");
        expect(sources["pr.draft_by_default"]).toBe("local");
      });

      it("should track nested local config", () => {
        const obj = {
          auto_accept_stages: true,
          trusted_stages: ["feature-dev", "pr-create"],
        };

        trackObjectSources(sources, obj, "human_in_the_loop", "local");

        expect(sources["human_in_the_loop.auto_accept_stages"]).toBe("local");
        expect(sources["human_in_the_loop.trusted_stages"]).toBe("local");
      });
    });

    describe("getSource with local", () => {
      it("should return local for locally set values", () => {
        sources["pr.delete_branch"] = "local";

        expect(getSource(sources, "pr.delete_branch")).toBe("local");
      });

      it("should distinguish local from other sources", () => {
        sources["project.number"] = "project";
        sources["pr.merge_strategy"] = "local";
        sources["branch.base"] = "global";

        expect(getSource(sources, "project.number")).toBe("project");
        expect(getSource(sources, "pr.merge_strategy")).toBe("local");
        expect(getSource(sources, "branch.base")).toBe("global");
      });
    });
  });

  describe("Four-way Merge Simulation", () => {
    // Simulates the merge logic: defaults <- global <- project <- local

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

    it("should apply local config over project config", () => {
      const projectConfig: IncrediConfig = {
        pr: { delete_branch: false },
      };

      const localConfig: IncrediConfig = {
        pr: { delete_branch: true },
      };

      const result = deepMerge(projectConfig, localConfig);

      expect(result.pr?.delete_branch).toBe(true); // Local wins
    });

    it("should handle four-way merge correctly", () => {
      const defaultConfig: IncrediConfig = {
        pr: { merge_strategy: "squash", delete_branch: true, reviewers: [] },
        branch: { base: "main" },
        human_in_the_loop: { auto_accept_stages: false },
      };

      const globalConfig: IncrediConfig = {
        pr: { merge_strategy: "rebase" },
        project: { number: 10 },
      };

      const projectConfig: IncrediConfig = {
        pr: { delete_branch: false },
        project: { number: 20 },
      };

      const localConfig: IncrediConfig = {
        human_in_the_loop: { auto_accept_stages: true },
        pr: { draft_by_default: true },
      };

      // Merge: defaults <- global <- project <- local
      const merged1 = deepMerge(defaultConfig, globalConfig);
      const merged2 = deepMerge(merged1, projectConfig);
      const merged3 = deepMerge(merged2, localConfig);

      expect(merged3.pr?.merge_strategy).toBe("rebase"); // From global
      expect(merged3.pr?.delete_branch).toBe(false); // From project
      expect(merged3.pr?.draft_by_default).toBe(true); // From local
      expect(merged3.pr?.reviewers).toEqual([]); // From defaults
      expect(merged3.branch?.base).toBe("main"); // From defaults
      expect(merged3.project?.number).toBe(20); // From project
      expect(merged3.human_in_the_loop?.auto_accept_stages).toBe(true); // From local
    });

    it("should replace arrays in local config (not merge them)", () => {
      const projectConfig: IncrediConfig = {
        human_in_the_loop: {
          trusted_stages: ["feature-dev", "pr-create"],
        },
      };

      const localConfig: IncrediConfig = {
        human_in_the_loop: {
          trusted_stages: ["issue-pickup"],
        },
      };

      const result = deepMerge(projectConfig, localConfig);

      // Arrays are replaced, not merged
      expect(result.human_in_the_loop?.trusted_stages).toEqual(["issue-pickup"]);
    });

    it("should not merge undefined values from local", () => {
      const projectConfig: IncrediConfig = {
        pr: { delete_branch: true },
      };

      const localConfig: IncrediConfig = {
        pr: { delete_branch: undefined },
      };

      const result = deepMerge(projectConfig, localConfig);

      // undefined doesn't override
      expect(result.pr?.delete_branch).toBe(true);
    });
  });

  describe("Source Annotation with Four-way Merge", () => {
    it("should track sources through a full four-way merge scenario", () => {
      const sources: ConfigSourceMap = {};

      // 1. Track defaults
      const defaultConfig: IncrediConfig = {
        pr: { merge_strategy: "squash", delete_branch: true },
        branch: { base: "main" },
      };
      trackObjectSources(sources, defaultConfig as Record<string, unknown>, "", "default");

      // 2. Track global config
      const globalConfig: IncrediConfig = {
        pr: { merge_strategy: "rebase" },
      };
      trackObjectSources(sources, globalConfig as Record<string, unknown>, "", "global");

      // 3. Track project config
      const projectConfig: IncrediConfig = {
        pr: { delete_branch: false },
        project: { number: 10 },
      };
      trackObjectSources(sources, projectConfig as Record<string, unknown>, "", "project");

      // 4. Track local config (overwrites some project)
      const localConfig: IncrediConfig = {
        pr: { draft_by_default: true },
        human_in_the_loop: { auto_accept_stages: true },
      };
      trackObjectSources(sources, localConfig as Record<string, unknown>, "", "local");

      // Check sources
      expect(getSource(sources, "pr.merge_strategy")).toBe("global");
      expect(getSource(sources, "pr.delete_branch")).toBe("project");
      expect(getSource(sources, "pr.draft_by_default")).toBe("local");
      expect(getSource(sources, "project.number")).toBe("project");
      expect(getSource(sources, "branch.base")).toBe("default");
      expect(getSource(sources, "human_in_the_loop.auto_accept_stages")).toBe("local");
      expect(getSource(sources, "unknown.path")).toBe("default");
    });

    it("should show local overriding project for same field", () => {
      const sources: ConfigSourceMap = {};

      // Project sets delete_branch
      trackSource(sources, "pr.delete_branch", "project");
      expect(getSource(sources, "pr.delete_branch")).toBe("project");

      // Local overrides it
      trackSource(sources, "pr.delete_branch", "local");
      expect(getSource(sources, "pr.delete_branch")).toBe("local");
    });
  });

  describe("Local Config Use Cases", () => {
    it("should allow local auto_accept_stages without affecting team config", () => {
      const sources: ConfigSourceMap = {};

      // Team disables auto-accept in project config
      const projectConfig: IncrediConfig = {
        human_in_the_loop: { auto_accept_stages: false },
      };
      trackObjectSources(sources, projectConfig as Record<string, unknown>, "", "project");

      // Developer enables it locally for faster iteration
      const localConfig: IncrediConfig = {
        human_in_the_loop: { auto_accept_stages: true },
      };
      trackObjectSources(sources, localConfig as Record<string, unknown>, "", "local");

      expect(getSource(sources, "human_in_the_loop.auto_accept_stages")).toBe("local");
    });

    it("should allow local skip_checks for development without affecting CI", () => {
      const sources: ConfigSourceMap = {};

      // Project config requires lint
      const projectConfig: IncrediConfig = {
        pipeline: { skip: { lint: false } },
      };
      trackObjectSources(sources, projectConfig as Record<string, unknown>, "", "project");

      // Developer skips lint locally for quick iteration
      const localConfig: IncrediConfig = {
        pipeline: { skip: { lint: true } },
      };
      trackObjectSources(sources, localConfig as Record<string, unknown>, "", "local");

      expect(getSource(sources, "pipeline.skip.lint")).toBe("local");
    });
  });
});
