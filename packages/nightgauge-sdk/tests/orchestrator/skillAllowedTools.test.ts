/**
 * The SDK reads a SKILL.md's tool lists exactly as the Go side does: every
 * case of internal/skillrender/testdata/allowed_tools_expected.json, which
 * the Go suite (TestAllowedToolsFixture) reads too (#2358).
 */
import { readFileSync } from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  NO_ALLOWED_TOOLS,
  NO_HEADLESS_TOOLS,
  filterHeadlessTools,
  skillAllowedTools,
  skillFrontmatterTools,
  splitAllowedTools,
} from "../../src/orchestrator/skillAllowedTools.js";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../..");

interface AllowedToolsCase {
  name: string;
  skill: string;
  declared?: string[];
  headless?: string[];
  /** A skill whose allowed-tools field is there but lists no tool: refused. */
  refused?: boolean;
  /** A skill that declares tools but none a headless run can use (#2390). */
  headless_refused?: boolean;
  /** Checked only where a case has them, as the Go suite does. */
  mcp?: string[];
  programmatic?: string[];
}

const FIXTURE = JSON.parse(
  readFileSync(
    path.join(REPO_ROOT, "internal/skillrender/testdata/allowed_tools_expected.json"),
    "utf-8"
  )
) as { cases: AllowedToolsCase[] };

describe("skillAllowedTools — the Go side's grammar (#2358)", () => {
  it("reads a fixture with cases", () => {
    expect(FIXTURE.cases.length).toBeGreaterThan(0);
  });

  it.each(FIXTURE.cases.map((c) => [c.name, c] as const))("%s", (_name, c) => {
    if (c.refused) {
      expect(() => skillAllowedTools(c.skill)).toThrow(NO_ALLOWED_TOOLS);
      return;
    }
    const declared = skillAllowedTools(c.skill);
    expect(declared).toEqual(c.declared);
    if (c.headless_refused) {
      expect(() => filterHeadlessTools(declared, "/s/SKILL.md")).toThrow(
        `/s/SKILL.md: ${NO_HEADLESS_TOOLS}`
      );
    } else {
      expect(filterHeadlessTools(declared, "/s/SKILL.md")).toEqual(c.headless);
    }
    if (c.mcp !== undefined) {
      expect(skillFrontmatterTools(c.skill, "mcp-tools")).toEqual(c.mcp);
    }
    if (c.programmatic !== undefined) {
      expect(skillFrontmatterTools(c.skill, "programmatic-tools")).toEqual(c.programmatic);
    }
  });

  it("splits a bare list the way it splits the frontmatter's", () => {
    expect(splitAllowedTools("Read, Bash(gh *),mcp__github__*")).toEqual([
      "Read",
      "Bash(gh *)",
      "mcp__github__*",
    ]);
    expect(splitAllowedTools("")).toEqual([]);
  });

  it("refuses only allowed-tools: an empty mcp-tools or programmatic-tools lists none", () => {
    const skill = "---\nname: s\nallowed-tools:\nmcp-tools:\nprogrammatic-tools: []\n---\n";
    expect(() => skillFrontmatterTools(skill, "allowed-tools")).toThrow(NO_ALLOWED_TOOLS);
    expect(skillFrontmatterTools(skill, "mcp-tools")).toEqual([]);
    expect(skillFrontmatterTools(skill, "programmatic-tools")).toEqual([]);
  });
});
