/**
 * #196 — skill-relative read directives must be rewritten to absolute host
 * paths at prompt-build time. ADR-010 assumed CWD is the nightgauge repo
 * root; cross-repo pipeline runs spawn in the target repo's worktree (no
 * skills/ directory), and agents fell back to `find / -maxdepth 6` scans
 * and stale ~/.codex/skills copies.
 */

import { describe, it, expect } from "vitest";
import { rewriteSkillRelativePaths } from "../../src/utils/skillRunner";

const SKILL_DIR = "/bundle/dist/skills/nightgauge-pr-merge";

describe("rewriteSkillRelativePaths (#196)", () => {
  it("rewrites the skill's own read directives to the resolved absolute dir", () => {
    const content =
      "> **Read `skills/nightgauge-pr-merge/_includes/merge.md` now and follow its instructions.**";
    const out = rewriteSkillRelativePaths(content, "pr-merge", SKILL_DIR);
    expect(out).toContain("/bundle/dist/skills/nightgauge-pr-merge/_includes/merge.md");
    expect(out).not.toContain("`skills/nightgauge-pr-merge/");
  });

  it("rewrites the prefix-stripped plugin-cache variant", () => {
    const out = rewriteSkillRelativePaths(
      "Read skills/pr-merge/_includes/reviews.md now.",
      "pr-merge",
      SKILL_DIR
    );
    expect(out).toContain("/bundle/dist/skills/nightgauge-pr-merge/_includes/reviews.md");
  });

  it("rewrites skills/_shared/ to the sibling shared directory", () => {
    const out = rewriteSkillRelativePaths(
      "See skills/_shared/GOTCHAS.md for cross-cutting gotchas.",
      "pr-merge",
      SKILL_DIR
    );
    expect(out).toContain("/bundle/dist/skills/_shared/GOTCHAS.md");
  });

  it("leaves cross-skill references untouched", () => {
    const content = "Compare with skills/nightgauge-pipeline-audit/SKILL.md if needed.";
    const out = rewriteSkillRelativePaths(content, "pr-merge", SKILL_DIR);
    expect(out).toContain("skills/nightgauge-pipeline-audit/SKILL.md");
    expect(out).not.toContain("/bundle/dist/skills/nightgauge-pr-merge/SKILL.md");
  });
});
