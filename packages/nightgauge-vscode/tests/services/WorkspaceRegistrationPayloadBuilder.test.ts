import { describe, it, expect } from "vitest";
import { WorkspaceRegistrationPayloadBuilder } from "../../src/services/WorkspaceRegistrationPayloadBuilder";
import type { WorkspaceConfig } from "../../src/types/WorkspaceConfig";

function makeConfig(name: string): WorkspaceConfig {
  return {
    workspace: { name },
    repositories: [{ name: "repo-a", path: "./repo-a" }],
  };
}

describe("WorkspaceRegistrationPayloadBuilder", () => {
  describe("build()", () => {
    it("returns undefined when config is null (single-repo mode)", () => {
      expect(WorkspaceRegistrationPayloadBuilder.build(null)).toBeUndefined();
    });

    it("returns undefined when workspace.name is empty string", () => {
      expect(WorkspaceRegistrationPayloadBuilder.build(makeConfig(""))).toBeUndefined();
    });

    it("returns undefined when workspace name sanitizes to empty slug", () => {
      // All special chars → empty after sanitization
      expect(WorkspaceRegistrationPayloadBuilder.build(makeConfig("---"))).toBeUndefined();
      expect(WorkspaceRegistrationPayloadBuilder.build(makeConfig("!@#$%"))).toBeUndefined();
    });

    it("returns correct slug and display_name for a simple name", () => {
      const result = WorkspaceRegistrationPayloadBuilder.build(makeConfig("My Workspace"));
      expect(result).toEqual({ slug: "my-workspace", display_name: "My Workspace" });
    });

    it("preserves display_name verbatim (no lowercasing)", () => {
      const result = WorkspaceRegistrationPayloadBuilder.build(makeConfig("Acme Platform"));
      expect(result?.display_name).toBe("Acme Platform");
    });

    it("returns correct metadata for multi-word workspace name", () => {
      const result = WorkspaceRegistrationPayloadBuilder.build(makeConfig("Acme Platform Dev"));
      expect(result).toEqual({
        slug: "acme-platform-dev",
        display_name: "Acme Platform Dev",
      });
    });
  });

  describe("toSlug()", () => {
    it("lowercases the name", () => {
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("MyWorkspace")).toBe("myworkspace");
    });

    it("replaces spaces with dashes", () => {
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("my workspace")).toBe("my-workspace");
    });

    it("replaces multiple consecutive special chars with a single dash", () => {
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("my  workspace")).toBe("my-workspace");
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("my---workspace")).toBe("my-workspace");
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("my / workspace")).toBe("my-workspace");
    });

    it("strips leading and trailing dashes", () => {
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("-my-workspace-")).toBe("my-workspace");
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("  spaces  ")).toBe("spaces");
    });

    it("truncates to exactly 50 chars", () => {
      const name = "a".repeat(60);
      const slug = WorkspaceRegistrationPayloadBuilder.toSlug(name);
      expect(slug.length).toBe(50);
    });

    it("truncates a 51-char name to 50", () => {
      const name = "a".repeat(51);
      expect(WorkspaceRegistrationPayloadBuilder.toSlug(name).length).toBe(50);
    });

    it("returns empty string for all-special-char input", () => {
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("---")).toBe("");
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("!@#$")).toBe("");
    });

    it("preserves digits in slug", () => {
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("workspace-v2")).toBe("workspace-v2");
    });

    it("handles unicode by stripping non-ascii chars", () => {
      expect(WorkspaceRegistrationPayloadBuilder.toSlug("café workspace")).toBe("caf-workspace");
    });

    it("produces slug matching platform validator pattern ^[a-z0-9-]{1,50}$", () => {
      const slugs = ["my-workspace", "acme-platform-dev", "workspace-v2", "a", "a".repeat(50)];
      const pattern = /^[a-z0-9-]{1,50}$/;
      for (const s of slugs) {
        expect(pattern.test(s), `slug "${s}" should match pattern`).toBe(true);
      }
    });

    it("never produces slug starting or ending with a dash", () => {
      const inputs = ["- my workspace -", " hello ", "--test--", "  a  "];
      for (const input of inputs) {
        const slug = WorkspaceRegistrationPayloadBuilder.toSlug(input);
        if (slug) {
          expect(slug.startsWith("-"), `slug "${slug}" should not start with -`).toBe(false);
          expect(slug.endsWith("-"), `slug "${slug}" should not end with -`).toBe(false);
        }
      }
    });
  });
});
