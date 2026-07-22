/**
 * Tests for the realistic-task corpus loader (Issue #4170).
 *
 * Unit tests use an in-memory reader; the corpus integration test loads the real
 * evals/tasks/ files from disk and asserts breadth (all job classes) and that
 * every referenced fixture scaffold script actually exists.
 */

import { describe, it, expect } from "vitest";
import { existsSync } from "node:fs";
import { resolve } from "node:path";
import { parseEvalTask, loadEvalTasks, DEFAULT_TASKS_DIR } from "../../src/eval/taskLoader.js";
import type { DirReader } from "../../src/eval/loader.js";
import { JOB_CLASSES, type EvalTask } from "../../src/eval/modelEvalSchemas.js";

const REPO_ROOT = resolve(__dirname, "../../../..");
const CORPUS_DIR = resolve(REPO_ROOT, DEFAULT_TASKS_DIR);

const VALID: EvalTask = {
  id: "sample-task",
  title: "Sample",
  job_class: "bugfix",
  target_stages: ["feature-dev"],
  difficulty: "easy",
  instruction: "Fix it.",
  fixture: { kind: "scaffold-script", ref: "evals/fixtures/sample-task/setup.sh" },
  checks: [{ name: "test", command: "npm test", expect_exit_code: 0 }],
  rubric: { criteria: [{ dimension: "correctness", weight: 1, guidance: "Fixed?" }] },
};

function memReader(files: Record<string, string>): DirReader {
  return {
    listJson: async (_dir) => Object.keys(files),
    readFile: async (p) => files[p],
  };
}

describe("parseEvalTask", () => {
  it("parses a valid task", () => {
    expect(parseEvalTask(JSON.stringify(VALID), "x.json").id).toBe("sample-task");
  });

  it("throws a sourced error on invalid JSON", () => {
    expect(() => parseEvalTask("{not json", "broken.json")).toThrow(/broken\.json/);
  });

  it("throws a sourced error on schema violation", () => {
    const bad = { ...VALID, job_class: "not-a-class" };
    expect(() => parseEvalTask(JSON.stringify(bad), "bad.json")).toThrow(/bad\.json/);
  });
});

describe("loadEvalTasks", () => {
  it("loads and sorts tasks by id", async () => {
    const b = { ...VALID, id: "b-task" };
    const a = { ...VALID, id: "a-task" };
    const tasks = await loadEvalTasks(
      "d",
      memReader({ "b.json": JSON.stringify(b), "a.json": JSON.stringify(a) })
    );
    expect(tasks.map((t) => t.id)).toEqual(["a-task", "b-task"]);
  });

  it("throws on a duplicate task id", async () => {
    await expect(
      loadEvalTasks(
        "d",
        memReader({ "1.json": JSON.stringify(VALID), "2.json": JSON.stringify(VALID) })
      )
    ).rejects.toThrow(/duplicate task id "sample-task"/);
  });
});

describe("the shipped corpus (evals/tasks/)", () => {
  it("loads and validates every task file", async () => {
    const tasks = await loadEvalTasks(CORPUS_DIR);
    expect(tasks.length).toBeGreaterThanOrEqual(7);
  });

  it("covers every job class at least once", async () => {
    const tasks = await loadEvalTasks(CORPUS_DIR);
    const covered = new Set(tasks.map((t) => t.job_class));
    for (const jc of JOB_CLASSES) {
      expect(covered.has(jc), `job class "${jc}" is not represented in the corpus`).toBe(true);
    }
  });

  it("includes hard tasks (frontier models only separate on hard work)", async () => {
    const tasks = await loadEvalTasks(CORPUS_DIR);
    expect(tasks.some((t) => t.difficulty === "hard")).toBe(true);
  });

  it("references a fixture scaffold script that exists on disk", async () => {
    const tasks = await loadEvalTasks(CORPUS_DIR);
    for (const t of tasks) {
      const fixturePath = resolve(REPO_ROOT, t.fixture.ref);
      expect(existsSync(fixturePath), `${t.id}: missing fixture ${t.fixture.ref}`).toBe(true);
    }
  });

  it("every task declares difficulty and at least one target stage", async () => {
    const tasks = await loadEvalTasks(CORPUS_DIR);
    for (const t of tasks) {
      expect(t.difficulty).toBeTruthy();
      expect(t.target_stages.length).toBeGreaterThan(0);
    }
  });
});
