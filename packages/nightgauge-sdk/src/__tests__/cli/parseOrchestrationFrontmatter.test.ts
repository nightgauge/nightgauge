/**
 * Tests for parseOrchestrationFrontmatter (#3913).
 *
 * Proves a stage SKILL.md's `orchestration:` frontmatter compiles into a
 * single-phase, schema-valid WorkflowSpec (agents from `units`, an optional
 * judge, the provider `ceiling` keyword), and that a skill with no frontmatter /
 * no orchestration block / no usable units resolves to `null` (the single-agent
 * path). Run-identity, `prefer_native_offload`, and the budget cap fold in from
 * the caller context.
 */

import { describe, it, expect } from "vitest";
import {
  parseOrchestrationFrontmatter,
  validateWorkflowSpec,
  WORKFLOW_SCHEMA_VERSION,
  CLAUDE_CEILING,
  FANOUT_CEILING,
  type OrchestrationFrontmatterContext,
} from "../../cli/workflow/index.js";

const CTX: OrchestrationFrontmatterContext = {
  runId: "wf-42-feature-dev",
  issueNumber: 42,
  stage: "feature-dev",
};

const FANOUT_SKILL = `---
name: demo
orchestration:
  mode: fanout
  phase: quality-review
  ceiling: fanout
  units:
    - id: code-quality
      role: reviewer
      promptRef: _includes/review.md
    - id: security
      role: reviewer
      promptRef: _includes/review.md
  judge:
    mode: merge
    quorum: 1
    promptRef: _includes/review.md
---

# Body
prose floor.
`;

const NO_BLOCK_SKILL = `---
name: demo
allowed-tools: Read Write
---

# Body
`;

describe("parseOrchestrationFrontmatter", () => {
  it("compiles a fanout block into a single-phase, schema-valid WorkflowSpec", () => {
    const spec = parseOrchestrationFrontmatter(FANOUT_SKILL, CTX);
    expect(spec).not.toBeNull();
    expect(spec!.schemaVersion).toBe(WORKFLOW_SCHEMA_VERSION);
    expect(spec!.runId).toBe("wf-42-feature-dev");
    expect(spec!.issueNumber).toBe(42);
    expect(spec!.stage).toBe("feature-dev");
    expect(spec!.phases).toHaveLength(1);
    expect(spec!.phases[0].name).toBe("quality-review");
    expect(spec!.phases[0].agents.map((a) => a.agentId)).toEqual(["code-quality", "security"]);
    expect(spec!.phases[0].agents[0].role).toBe("reviewer");
    expect(spec!.phases[0].judges).toHaveLength(1);
    expect(spec!.phases[0].judges![0].quorum).toBe(1);
    // The compiled spec is valid against its own ceiling.
    expect(validateWorkflowSpec(spec!)).toEqual([]);
  });

  it("maps the `fanout` ceiling keyword to FANOUT_CEILING", () => {
    const spec = parseOrchestrationFrontmatter(FANOUT_SKILL, CTX);
    expect(spec!.ceiling).toEqual(FANOUT_CEILING);
  });

  it("maps the `claude` ceiling keyword to CLAUDE_CEILING", () => {
    const skill = FANOUT_SKILL.replace("ceiling: fanout", "ceiling: claude");
    const spec = parseOrchestrationFrontmatter(skill, CTX);
    expect(spec!.ceiling).toEqual(CLAUDE_CEILING);
  });

  it("falls back to the conservative FANOUT_CEILING for an unknown ceiling keyword", () => {
    const skill = FANOUT_SKILL.replace("ceiling: fanout", "ceiling: bogus");
    const spec = parseOrchestrationFrontmatter(skill, CTX);
    expect(spec!.ceiling).toEqual(FANOUT_CEILING);
  });

  it("folds prefer_native_offload + budget cap from the caller context onto the spec", () => {
    const spec = parseOrchestrationFrontmatter(FANOUT_SKILL, {
      ...CTX,
      preferNativeOffload: true,
      budgetUsd: 12.5,
    });
    expect(spec!.preferNativeOffload).toBe(true);
    expect(spec!.budgetUsd).toBe(12.5);
  });

  it("leaves budgetUsd off the spec when the cap is 0 (uncapped)", () => {
    const spec = parseOrchestrationFrontmatter(FANOUT_SKILL, { ...CTX, budgetUsd: 0 });
    expect(spec!.budgetUsd).toBeUndefined();
  });

  it("returns null for a skill with no frontmatter at all", () => {
    expect(parseOrchestrationFrontmatter("# Just a body\n", CTX)).toBeNull();
  });

  it("returns null for a skill whose frontmatter has no orchestration block", () => {
    expect(parseOrchestrationFrontmatter(NO_BLOCK_SKILL, CTX)).toBeNull();
  });

  it("returns null for an orchestration block with zero usable units", () => {
    const skill = `---
orchestration:
  mode: fanout
  phase: x
  units: []
---
body`;
    expect(parseOrchestrationFrontmatter(skill, CTX)).toBeNull();
  });

  it("returns null for malformed frontmatter rather than throwing", () => {
    const skill = `---
orchestration: : : :
  units: [
---
body`;
    expect(() => parseOrchestrationFrontmatter(skill, CTX)).not.toThrow();
    expect(parseOrchestrationFrontmatter(skill, CTX)).toBeNull();
  });

  it("skips units missing an id but keeps the valid ones", () => {
    const skill = `---
orchestration:
  phase: p
  ceiling: fanout
  units:
    - role: reviewer
    - id: kept
      promptRef: ref.md
---
body`;
    const spec = parseOrchestrationFrontmatter(skill, CTX);
    expect(spec!.phases[0].agents.map((a) => a.agentId)).toEqual(["kept"]);
  });

  it("defaults the phase name to the stage when `phase:` is absent", () => {
    const skill = `---
orchestration:
  ceiling: fanout
  units:
    - id: only
---
body`;
    const spec = parseOrchestrationFrontmatter(skill, { ...CTX, stage: "feature-validate" });
    expect(spec!.phases[0].name).toBe("feature-validate");
  });
});
