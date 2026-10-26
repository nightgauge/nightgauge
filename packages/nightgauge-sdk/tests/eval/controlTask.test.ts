/**
 * Tests for pinned control task integrity (Issue #2117).
 */

import { describe, it, expect } from "vitest";
import {
  controlContentHash,
  verifyRegisteredHash,
  verifyControlIssue,
} from "../../src/eval/controlTask.js";
import type { EvalTask } from "../../src/eval/modelEvalSchemas.js";

const SHA = "a".repeat(40);
const BODY = "## Summary\nAdd monthlySummary.\n";

function control(overrides: Partial<EvalTask> = {}): EvalTask {
  const title = "Add monthlySummary";
  return {
    id: "eval-control",
    title,
    job_class: "backend-logic",
    target_stages: ["feature-dev"],
    difficulty: "medium",
    instruction: BODY,
    fixture: { kind: "base-commit", ref: SHA, repo: "acme/fixture" },
    checks: [],
    rubric: { criteria: [{ dimension: "correctness", weight: 1, guidance: "?" }] },
    control: true,
    source_issue: {
      url: "https://github.com/acme/fixture/issues/1",
      repo: "acme/fixture",
      number: 1,
      base_sha: SHA,
      content_sha256: controlContentHash({ title, body: BODY, base_sha: SHA }),
    },
    pass_criteria: { tests_must_pass: ["tests/a.test.ts"], files_must_change: ["src/a.ts"] },
    ...overrides,
  };
}

describe("controlContentHash", () => {
  const base = { title: "T", body: "line1\nline2", base_sha: SHA };

  it("is a 64-hex sha256", () => {
    expect(controlContentHash(base)).toMatch(/^[0-9a-f]{64}$/);
  });

  it("is stable under CRLF vs LF line endings", () => {
    expect(controlContentHash({ ...base, body: "line1\r\nline2" })).toBe(controlContentHash(base));
  });

  it("changes on a body, title or base SHA edit", () => {
    const h = controlContentHash(base);
    expect(controlContentHash({ ...base, body: "line1\nline2!" })).not.toBe(h);
    expect(controlContentHash({ ...base, title: "T2" })).not.toBe(h);
    expect(controlContentHash({ ...base, base_sha: "b".repeat(40) })).not.toBe(h);
  });

  it("does not collide when text moves across the title/body boundary", () => {
    expect(controlContentHash({ title: "ab", body: "c", base_sha: SHA })).not.toBe(
      controlContentHash({ title: "a", body: "bc", base_sha: SHA })
    );
  });
});

describe("verifyRegisteredHash", () => {
  it("passes for an unedited snapshot", () => {
    expect(() => verifyRegisteredHash(control())).not.toThrow();
  });

  it("throws when the instruction was edited without re-registering", () => {
    expect(() => verifyRegisteredHash(control({ instruction: BODY + "more" }))).toThrow(
      /drifted from its registered hash/
    );
  });

  it("throws for a task without source_issue", () => {
    expect(() => verifyRegisteredHash(control({ source_issue: undefined }))).toThrow(
      /not a control task/
    );
  });
});

describe("verifyControlIssue", () => {
  it("returns the hash when the live issue matches", async () => {
    const task = control();
    const urls: string[] = [];
    const hash = await verifyControlIssue(task, async (url) => {
      urls.push(url);
      return { title: task.title, body: BODY.replace(/\n/g, "\r\n") };
    });
    expect(hash).toBe(task.source_issue?.content_sha256);
    expect(urls).toEqual(["https://github.com/acme/fixture/issues/1"]);
  });

  it("throws naming both hashes when the live issue body was edited", async () => {
    const task = control();
    await expect(
      verifyControlIssue(task, async () => ({ title: task.title, body: BODY + "edited" }))
    ).rejects.toThrow(/drifted: registered hash [0-9a-f]{64}, live hash [0-9a-f]{64}/);
  });
});
