import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdtemp, rm, writeFile, mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import workflowJobNamedRule from "../../ac-rules/workflow-job-named.js";

describe("workflow-job-named rule", () => {
  let workdir: string;

  beforeEach(async () => {
    workdir = await mkdtemp(path.join(os.tmpdir(), "ac-wfjob-"));
  });

  afterEach(async () => {
    await rm(workdir, { recursive: true, force: true });
  });

  describe("applies()", () => {
    it("matches workflow + job in backticks", () => {
      const r = workflowJobNamedRule.applies("Workflow .github/workflows/ci.yml has job `lint`");
      expect(r).toEqual({ workflow: "ci.yml", job: "lint" });
    });

    it("returns null when no workflow path is referenced", () => {
      expect(workflowJobNamedRule.applies("the job `lint` runs")).toBeNull();
    });

    it("returns null when no job name is referenced", () => {
      expect(workflowJobNamedRule.applies(".github/workflows/ci.yml exists")).toBeNull();
    });
  });

  describe("evaluate()", () => {
    async function writeWorkflow(filename: string, content: string): Promise<void> {
      const dir = path.join(workdir, ".github", "workflows");
      await mkdir(dir, { recursive: true });
      await writeFile(path.join(dir, filename), content, "utf-8");
    }

    it("classifies satisfied when job exists in workflow", async () => {
      await writeWorkflow(
        "ci.yml",
        `name: CI
on: [push]
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - run: echo lint
  test:
    runs-on: ubuntu-latest
`
      );
      const r = await workflowJobNamedRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { workflow: "ci.yml", job: "lint" },
      });
      expect(r.classification).toBe("satisfied");
      expect(r.evidence).toContain(".github/workflows/ci.yml");
    });

    it("classifies unsatisfied when job is missing", async () => {
      await writeWorkflow(
        "ci.yml",
        `name: CI
on: [push]
jobs:
  test:
    runs-on: ubuntu-latest
`
      );
      const r = await workflowJobNamedRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { workflow: "ci.yml", job: "lint" },
      });
      expect(r.classification).toBe("unsatisfied");
    });

    it("classifies unsatisfied when workflow file is missing", async () => {
      const r = await workflowJobNamedRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { workflow: "missing.yml", job: "lint" },
      });
      expect(r.classification).toBe("unsatisfied");
    });
  });
});
