import { describe, expect, it } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { DEFAULT_STAGES, type PipelineStage } from "../../src/orchestrator/PipelineOrchestrator.js";
import { buildStagePrompt } from "../../src/orchestrator/StageExecutor.js";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, "../../../..");

/**
 * Read a skill's full contract surface: its SKILL.md body plus every
 * progressive-disclosure fragment under the sibling `_includes/` directory.
 *
 * Since #3810 the 6 pipeline skills are split into a compact SKILL.md skeleton
 * that points at `_includes/*.md` fragments via Read-directives. The
 * stage-contract artifacts (context filenames, `project move-status`, status
 * keywords) therefore live in the fragments, not the top-level file. The
 * contract the pipeline actually executes is the composed body, so parity
 * checks must read that — not SKILL.md alone.
 */
async function readComposedSkill(skillRelPath: string): Promise<string> {
  const skillPath = path.join(repoRoot, skillRelPath);
  const parts = [await fs.readFile(skillPath, "utf-8")];

  const includesDir = path.join(path.dirname(skillPath), "_includes");
  let entries: string[] = [];
  try {
    entries = (await fs.readdir(includesDir)).filter((f) => f.endsWith(".md")).sort();
  } catch {
    // No _includes/ directory — composed body is just SKILL.md.
  }
  for (const entry of entries) {
    parts.push(await fs.readFile(path.join(includesDir, entry), "utf-8"));
  }

  return parts.join("\n");
}

const CORE_STAGES: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

const SKILL_PATH_BY_STAGE: Record<PipelineStage, string> = {
  "pipeline-start": "",
  "issue-pickup": "skills/nightgauge-issue-pickup/SKILL.md",
  "feature-planning": "skills/nightgauge-feature-planning/SKILL.md",
  "feature-dev": "skills/nightgauge-feature-dev/SKILL.md",
  "feature-validate": "skills/nightgauge-feature-validate/SKILL.md",
  "pr-create": "skills/nightgauge-pr-create/SKILL.md",
  "pr-merge": "skills/nightgauge-pr-merge/SKILL.md",
  "pipeline-finish": "",
};

// Skills-canonical contract (ADR-007, revised #3876): the slash command IS the
// bundled plugin skill — generated from the canonical SKILL.md by
// scripts/install-agent-skills.sh. There are no command-wrapper files.
const PLUGIN_SKILL_PATH_BY_STAGE: Record<PipelineStage, string> = {
  "pipeline-start": "",
  "issue-pickup": "claude-plugins/nightgauge/skills/issue-pickup/SKILL.md",
  "feature-planning": "claude-plugins/nightgauge/skills/feature-planning/SKILL.md",
  "feature-dev": "claude-plugins/nightgauge/skills/feature-dev/SKILL.md",
  "feature-validate": "claude-plugins/nightgauge/skills/feature-validate/SKILL.md",
  "pr-create": "claude-plugins/nightgauge/skills/pr-create/SKILL.md",
  "pr-merge": "claude-plugins/nightgauge/skills/pr-merge/SKILL.md",
  "pipeline-finish": "",
};

const REQUIRED_SKILL_CONTRACTS: Record<PipelineStage, string[]> = {
  "pipeline-start": [],
  "issue-pickup": [
    ".nightgauge/pipeline/issue-{N}.json",
    "project move-status",
    "running",
    "complete",
  ],
  "feature-planning": [
    "PLAN.md",
    ".nightgauge/pipeline/planning-{N}.json",
    "project move-status",
    "running",
    "complete",
  ],
  "feature-dev": [
    ".nightgauge/pipeline/planning-{N}.json",
    ".nightgauge/pipeline/dev-{N}.json",
    "project move-status",
    "running",
    "complete",
  ],
  "feature-validate": [
    ".nightgauge/pipeline/dev-{N}.json",
    ".nightgauge/pipeline/validate-{N}.json",
    "project move-status",
    "running",
    "complete",
  ],
  "pr-create": [
    ".nightgauge/pipeline/dev-{N}.json",
    ".nightgauge/pipeline/validate-{N}.json",
    "project move-status",
    "in-progress",
    "in-review",
  ],
  "pr-merge": [
    ".nightgauge/pipeline/pr-{N}.json",
    ".nightgauge/pipeline/issue-{N}.json",
    ".nightgauge/pipeline/planning-{N}.json",
    ".nightgauge/pipeline/dev-{N}.json",
    "project move-status",
    "running",
    "complete",
  ],
  "pipeline-finish": [],
};

describe("stage parity regression matrix", () => {
  it("keeps the orchestrator default stage order in sync with core parity stages", () => {
    expect(
      DEFAULT_STAGES,
      "DEFAULT_STAGES changed. Update Codex/Claude parity contracts and tests."
    ).toEqual(CORE_STAGES);
  });

  it("builds stage prompts that route every core stage to the matching skill contract", async () => {
    const issueNumber = 569;

    for (const stage of CORE_STAGES) {
      const prompt = await buildStagePrompt(stage, issueNumber);
      const expectedSkillPath = SKILL_PATH_BY_STAGE[stage];

      expect(
        prompt,
        `Stage '${stage}' prompt must include '${expectedSkillPath}' to avoid stage drift.`
      ).toContain(`Skill source: ${expectedSkillPath}`);
    }
  });

  // Adapter wrapper and run-stage.sh tests removed — shell scripts replaced
  // by Go binary (Epic #1543). Go binary CLI parity is validated by
  // internal/validation/runner_test.go.

  it("generates a bundled plugin skill (the /nightgauge:<stage> slash command) for every core stage", async () => {
    for (const stage of CORE_STAGES) {
      // Canonical source must exist, and the generated plugin skill that backs
      // the /nightgauge:<stage> slash command must exist alongside it.
      await fs.access(path.join(repoRoot, SKILL_PATH_BY_STAGE[stage]));
      const pluginSkill = await fs.readFile(
        path.join(repoRoot, PLUGIN_SKILL_PATH_BY_STAGE[stage]),
        "utf-8"
      );

      // install-agent-skills.sh rewrites the frontmatter name to the prefix-
      // stripped short name so Claude registers it as nightgauge:<stage>.
      expect(
        pluginSkill,
        `Plugin skill for '${stage}' must register the short name 'nightgauge:${stage}'.`
      ).toContain(`name: ${stage}\n`);

      // DMI is injected into the plugin copy so the skill IS the slash command
      // but the model never auto-runs the side-effecting stage.
      expect(
        pluginSkill,
        `Plugin skill for '${stage}' must set disable-model-invocation (user-triggered only).`
      ).toContain("disable-model-invocation: true");
    }
  });

  it("enforces Claude stage contract artifacts and status signaling in shared skills", async () => {
    for (const stage of CORE_STAGES) {
      // Read the composed contract surface (SKILL.md + `_includes/` fragments).
      // Post-#3810 the artifacts live in progressive-disclosure fragments, not
      // the SKILL.md skeleton — see readComposedSkill.
      const skillContent = await readComposedSkill(SKILL_PATH_BY_STAGE[stage]);
      const requiredContracts = REQUIRED_SKILL_CONTRACTS[stage];

      for (const requiredContract of requiredContracts) {
        expect(
          skillContent,
          `Skill contract drift for stage '${stage}': expected '${requiredContract}'.`
        ).toContain(requiredContract);
      }
    }
  });

  // run-stage.sh entrypoint test removed — shell scripts replaced by Go
  // binary (Epic #1543). Stage routing is validated by Go CLI tests.
});
