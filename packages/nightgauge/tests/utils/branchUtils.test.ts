import { describe, it, expect } from "vitest";
import {
  categorizeBranch,
  parseRemoteBranch,
  isValidBranchName,
  getSortedBranches,
  filterTargetBranches,
  getBranchLabel,
  getBranchDescription,
  type BranchInfo,
  type BranchCategory,
} from "../../src/utils/branchUtils";

describe("branchUtils", () => {
  describe("categorizeBranch", () => {
    it("should categorize main/master as default", () => {
      expect(categorizeBranch("main")).toBe("default");
      expect(categorizeBranch("master")).toBe("default");
    });

    it("should categorize develop/development as develop", () => {
      expect(categorizeBranch("develop")).toBe("develop");
      expect(categorizeBranch("development")).toBe("develop");
    });

    it("should categorize release branches", () => {
      expect(categorizeBranch("release/v1.0")).toBe("release");
      expect(categorizeBranch("release/2024.01")).toBe("release");
    });

    it("should categorize epic branches", () => {
      expect(categorizeBranch("epic/user-auth")).toBe("epic");
      expect(categorizeBranch("epic/123-new-feature")).toBe("epic");
    });

    it("should categorize feature branches", () => {
      expect(categorizeBranch("feat/42-add-login")).toBe("feature");
      expect(categorizeBranch("feature/new-feature")).toBe("feature");
    });

    it("should categorize unknown patterns as other", () => {
      expect(categorizeBranch("fix/42-bug")).toBe("other");
      expect(categorizeBranch("hotfix/urgent")).toBe("other");
      expect(categorizeBranch("random-branch")).toBe("other");
    });
  });

  describe("parseRemoteBranch", () => {
    it("should remove origin/ prefix", () => {
      expect(parseRemoteBranch("origin/main")).toBe("main");
      expect(parseRemoteBranch("origin/develop")).toBe("develop");
      expect(parseRemoteBranch("origin/release/v1.0")).toBe("release/v1.0");
    });

    it("should handle branches without origin/ prefix", () => {
      expect(parseRemoteBranch("main")).toBe("main");
      expect(parseRemoteBranch("develop")).toBe("develop");
    });
  });

  describe("isValidBranchName", () => {
    it("should accept valid branch names", () => {
      expect(isValidBranchName("main")).toBe(true);
      expect(isValidBranchName("feat/42-add-login")).toBe(true);
      expect(isValidBranchName("release/v1.0.0")).toBe(true);
      expect(isValidBranchName("epic/user-authentication")).toBe(true);
    });

    it("should reject empty or null names", () => {
      expect(isValidBranchName("")).toBe(false);
      expect(isValidBranchName(null as unknown as string)).toBe(false);
      expect(isValidBranchName(undefined as unknown as string)).toBe(false);
    });

    it("should reject names with consecutive dots", () => {
      expect(isValidBranchName("branch..name")).toBe(false);
      expect(isValidBranchName("test...branch")).toBe(false);
    });

    it("should reject names starting with dot or slash", () => {
      expect(isValidBranchName(".hidden")).toBe(false);
      expect(isValidBranchName("/leading-slash")).toBe(false);
    });

    it("should reject names ending with slash or dot", () => {
      expect(isValidBranchName("branch/")).toBe(false);
      expect(isValidBranchName("branch.")).toBe(false);
    });

    it("should reject names with @{", () => {
      expect(isValidBranchName("branch@{upstream}")).toBe(false);
    });

    it("should reject names with backslash", () => {
      expect(isValidBranchName("branch\\name")).toBe(false);
    });

    it("should reject names with control characters", () => {
      expect(isValidBranchName("branch\x00name")).toBe(false);
      expect(isValidBranchName("branch\x1fname")).toBe(false);
    });

    it("should reject names with special characters", () => {
      expect(isValidBranchName("branch name")).toBe(false);
      expect(isValidBranchName("branch~name")).toBe(false);
      expect(isValidBranchName("branch^name")).toBe(false);
      expect(isValidBranchName("branch:name")).toBe(false);
      expect(isValidBranchName("branch?name")).toBe(false);
      expect(isValidBranchName("branch*name")).toBe(false);
      expect(isValidBranchName("branch[name")).toBe(false);
    });

    it("should reject names ending with .lock", () => {
      expect(isValidBranchName("branch.lock")).toBe(false);
      expect(isValidBranchName("test.lock")).toBe(false);
    });
  });

  describe("getSortedBranches", () => {
    it("should return empty array for empty input", () => {
      expect(getSortedBranches([])).toEqual([]);
    });

    it("should categorize and sort branches", () => {
      const branches = ["feat/test", "develop", "main", "release/v1.0"];
      const result = getSortedBranches(branches);

      expect(result[0].name).toBe("main");
      expect(result[0].category).toBe("default");
      expect(result[1].name).toBe("develop");
      expect(result[1].category).toBe("develop");
      expect(result[2].name).toBe("release/v1.0");
      expect(result[2].category).toBe("release");
    });

    it("should include config suggestions at the top", () => {
      const branches = ["develop", "release/v1.0"];
      const suggestions = ["main", "custom-target"];
      const result = getSortedBranches(branches, suggestions);

      // Unique branches should include both
      const names = result.map((b) => b.name);
      expect(names).toContain("main");
      expect(names).toContain("custom-target");
    });

    it("should mark default branches correctly", () => {
      const branches = ["main", "develop"];
      const result = getSortedBranches(branches);

      const mainBranch = result.find((b) => b.name === "main");
      const developBranch = result.find((b) => b.name === "develop");

      expect(mainBranch?.isDefault).toBe(true);
      expect(developBranch?.isDefault).toBe(false);
    });

    it("should mark remote branches correctly", () => {
      const branches = ["main", "develop"];
      const suggestions = ["custom-target"];
      const result = getSortedBranches(branches, suggestions);

      const mainBranch = result.find((b) => b.name === "main");
      const customBranch = result.find((b) => b.name === "custom-target");

      expect(mainBranch?.isRemote).toBe(true);
      expect(customBranch?.isRemote).toBe(false);
    });
  });

  describe("filterTargetBranches", () => {
    const createBranches = (): BranchInfo[] => [
      { name: "main", isRemote: true, isDefault: true, category: "default" },
      {
        name: "develop",
        isRemote: true,
        isDefault: false,
        category: "develop",
      },
      {
        name: "release/v1.0",
        isRemote: true,
        isDefault: false,
        category: "release",
      },
      {
        name: "release/v2.0",
        isRemote: true,
        isDefault: false,
        category: "release",
      },
      {
        name: "feat/test",
        isRemote: true,
        isDefault: false,
        category: "feature",
      },
      { name: "random", isRemote: true, isDefault: false, category: "other" },
    ];

    it("should filter out feature and other branches by default", () => {
      const branches = createBranches();
      const result = filterTargetBranches(branches);

      const names = result.map((b) => b.name);
      expect(names).not.toContain("feat/test");
      expect(names).not.toContain("random");
      expect(names).toContain("main");
      expect(names).toContain("develop");
    });

    it("should include feature branches when option is set", () => {
      const branches = createBranches();
      const result = filterTargetBranches(branches, {
        includeFeatureBranches: true,
      });

      const names = result.map((b) => b.name);
      expect(names).toContain("feat/test");
    });

    it("should limit release branches", () => {
      const branches: BranchInfo[] = [
        { name: "main", isRemote: true, isDefault: true, category: "default" },
        {
          name: "release/v1.0",
          isRemote: true,
          isDefault: false,
          category: "release",
        },
        {
          name: "release/v2.0",
          isRemote: true,
          isDefault: false,
          category: "release",
        },
        {
          name: "release/v3.0",
          isRemote: true,
          isDefault: false,
          category: "release",
        },
        {
          name: "release/v4.0",
          isRemote: true,
          isDefault: false,
          category: "release",
        },
        {
          name: "release/v5.0",
          isRemote: true,
          isDefault: false,
          category: "release",
        },
        {
          name: "release/v6.0",
          isRemote: true,
          isDefault: false,
          category: "release",
        },
      ];
      const result = filterTargetBranches(branches, { maxReleaseBranches: 3 });

      const releaseCount = result.filter((b) => b.category === "release").length;
      expect(releaseCount).toBe(3);
    });
  });

  describe("getBranchLabel", () => {
    it("should add (default) suffix for default branches", () => {
      const branch: BranchInfo = {
        name: "main",
        isRemote: true,
        isDefault: true,
        category: "default",
      };
      expect(getBranchLabel(branch)).toBe("main (default)");
    });

    it("should add (development) suffix for develop branches", () => {
      const branch: BranchInfo = {
        name: "develop",
        isRemote: true,
        isDefault: false,
        category: "develop",
      };
      expect(getBranchLabel(branch)).toBe("develop (development)");
    });

    it("should add (epic) suffix for epic branches", () => {
      const branch: BranchInfo = {
        name: "epic/auth",
        isRemote: true,
        isDefault: false,
        category: "epic",
      };
      expect(getBranchLabel(branch)).toBe("epic/auth (epic)");
    });

    it("should return plain name for release branches", () => {
      const branch: BranchInfo = {
        name: "release/v1.0",
        isRemote: true,
        isDefault: false,
        category: "release",
      };
      expect(getBranchLabel(branch)).toBe("release/v1.0");
    });

    it("should add check mark for current branch", () => {
      const branch: BranchInfo = {
        name: "main",
        isRemote: true,
        isDefault: true,
        category: "default",
      };
      expect(getBranchLabel(branch, "main")).toBe("$(check) main (default)");
    });
  });

  describe("getBranchDescription", () => {
    it("should return correct description for default category", () => {
      const branch: BranchInfo = {
        name: "main",
        isRemote: true,
        isDefault: true,
        category: "default",
      };
      expect(getBranchDescription(branch)).toContain("Production");
    });

    it("should return correct description for develop category", () => {
      const branch: BranchInfo = {
        name: "develop",
        isRemote: true,
        isDefault: false,
        category: "develop",
      };
      expect(getBranchDescription(branch)).toContain("Development");
    });

    it("should return correct description for release category", () => {
      const branch: BranchInfo = {
        name: "release/v1.0",
        isRemote: true,
        isDefault: false,
        category: "release",
      };
      expect(getBranchDescription(branch)).toContain("Release");
    });

    it("should return correct description for epic category", () => {
      const branch: BranchInfo = {
        name: "epic/auth",
        isRemote: true,
        isDefault: false,
        category: "epic",
      };
      expect(getBranchDescription(branch)).toContain("Epic");
    });

    it("should return generic description for other categories", () => {
      const branch: BranchInfo = {
        name: "random",
        isRemote: true,
        isDefault: false,
        category: "other",
      };
      expect(getBranchDescription(branch)).toContain("Remote branch");
    });
  });
});
