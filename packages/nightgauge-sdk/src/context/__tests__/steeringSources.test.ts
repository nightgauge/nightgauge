/**
 * Tests for the provider-neutral steering readers (issue 1675).
 *
 * The extractSummary cases are shared with the Go twin
 * (`internal/execution/codexprovision/steering_test.go`): both read the same
 * inputs and byte-exact goldens, so the two dispatch paths cannot drift.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";
import {
  CODEX_MANAGED_BEGIN,
  CODEX_MANAGED_END,
  extractSummary,
  readProjectDescription,
  stripLeadingAgentsImport,
} from "../steeringSources.js";

const fixtureDir = path.resolve(
  __dirname,
  "../../../../../internal/execution/codexprovision/testdata/extract-summary"
);

describe("extractSummary", () => {
  it("keeps the section body when an H1 is immediately followed by an H2", () => {
    const got = extractSummary("# Project\n## Overview\nIt does X.\n## Build\nrun make\n");
    for (const want of ["# Project", "## Overview", "It does X.", "## Build", "run make"]) {
      expect(got).toContain(want);
    }
    expect(got).not.toBe("# Project");
  });

  it("drops headings with no body", () => {
    const got = extractSummary("# T\n\n## Empty\n\n## Real\nbody\n## Tail\n");
    expect(got).not.toContain("Empty");
    expect(got).not.toContain("Tail");
    expect(got).toContain("## Real\nbody");
  });

  const cases = JSON.parse(fs.readFileSync(path.join(fixtureDir, "cases.json"), "utf-8")) as Array<{
    name: string;
    maxLines: number;
  }>;

  it("has shared fixtures", () => {
    expect(cases.length).toBeGreaterThan(0);
  });

  for (const c of cases) {
    it(`matches the Go golden for ${c.name}`, () => {
      const input = fs.readFileSync(path.join(fixtureDir, `${c.name}.input`), "utf-8");
      const golden = fs.readFileSync(path.join(fixtureDir, `${c.name}.golden`), "utf-8");
      expect(extractSummary(input, c.maxLines)).toBe(golden);
    });
  }
});

describe("readProjectDescription", () => {
  let dir: string;
  beforeEach(() => {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), "steering-src-"));
  });
  afterEach(() => {
    fs.rmSync(dir, { recursive: true, force: true });
  });

  it("prefers the AGENTS.md contract over a CLAUDE.md adapter", () => {
    fs.writeFileSync(path.join(dir, "AGENTS.md"), "# Contract\n## Scope\nThe real rules.\n");
    fs.writeFileSync(
      path.join(dir, "CLAUDE.md"),
      "@AGENTS.md\n\n# Claude Code adapter\nClaude-only notes.\n"
    );
    const got = readProjectDescription(dir) ?? "";
    expect(got).toContain("The real rules.");
    expect(got).not.toContain("Claude-only");
  });

  it("falls back to CLAUDE.md, skipping a leading @AGENTS.md import", () => {
    fs.writeFileSync(
      path.join(dir, "AGENTS.md"),
      `${CODEX_MANAGED_BEGIN}\ngen\n${CODEX_MANAGED_END}\n`
    );
    fs.writeFileSync(
      path.join(dir, "CLAUDE.md"),
      "\n@AGENTS.md\n\n# Legacy\nOld-model description.\n"
    );
    const got = readProjectDescription(dir) ?? "";
    expect(got).not.toContain("@AGENTS.md");
    expect(got).toContain("Old-model description.");
  });

  it("treats an import-only CLAUDE.md as no description", () => {
    fs.writeFileSync(path.join(dir, "CLAUDE.md"), "@AGENTS.md\n");
    expect(readProjectDescription(dir)).toBeNull();
  });

  it("stripLeadingAgentsImport leaves a CLAUDE.md without the import untouched", () => {
    const body = "# Old model\nrules\n";
    expect(stripLeadingAgentsImport(body)).toBe(body);
  });
});
