/**
 * Integration test — deterministic AC reconciliation against a captured
 * platform-#801 snapshot.
 *
 * Fixture body and minimal file tree are committed under
 * `fixtures/platform-801-*`. The test runs the reconciler against a temp
 * workspace populated from the fixture tree — no live cross-repo or LLM
 * calls. Establishes a regression contract: the deterministic path must
 * classify at least 4 of the 6 platform-#801 ACs as satisfied without
 * consuming any tokens.
 */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdtemp, rm, mkdir, readFile, writeFile, cp } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { reconcileAcceptanceCriteria } from "../../../preflight/reconcile.js";

const FIXTURE_DIR = path.resolve(__dirname, "fixtures");
const FIXTURE_BODY = path.join(FIXTURE_DIR, "platform-801-body.md");
const FIXTURE_TREE = path.join(FIXTURE_DIR, "platform-801-tree");

async function copyFixtureTree(dest: string): Promise<void> {
  await cp(FIXTURE_TREE, dest, { recursive: true });
}

describe("AC reconciliation — platform-#801 fixture", () => {
  let workdir: string;
  let binDir: string;
  let realPath: string | undefined;

  beforeEach(async () => {
    workdir = await mkdtemp(path.join(os.tmpdir(), "preflight-801-"));

    // Make the docstring's "no live cross-repo calls" claim TRUE.
    //
    // The fixture body carries a branch-protection AC on purpose, which routes
    // to the branch-protection-rule-present rule — and that rule shells out to
    // `gh`. With a real `gh` on PATH those calls reach the network, so this
    // test's runtime was set by DNS/TLS/auth latency rather than by the code
    // under test: it passed in ~200ms locally and took 5023ms on a CI runner,
    // tripping vitest's 5000ms default. The same tree passed on a PR run and
    // failed on the merge-commit run.
    //
    // Pointing PATH at an empty directory makes the spawn fail with ENOENT
    // immediately, so the rule returns its "undetectable" classification
    // deterministically and this test measures reconciliation, not the network.
    // The rule's own gh-dependent behaviour is covered separately in
    // src/preflight/__tests__/ac-rules/branch-protection-rule-present.test.ts.
    binDir = await mkdtemp(path.join(os.tmpdir(), "preflight-801-nogh-"));
    realPath = process.env.PATH;
    process.env.PATH = binDir;
  });

  afterEach(async () => {
    if (realPath === undefined) delete process.env.PATH;
    else process.env.PATH = realPath;
    await rm(workdir, { recursive: true, force: true });
    await rm(binDir, { recursive: true, force: true });
  });

  it("classifies at least 4 of 6 ACs as satisfied with mostly-satisfied or all-satisfied aggregate", async () => {
    await copyFixtureTree(workdir);
    const body = await readFile(FIXTURE_BODY, "utf-8");

    const report = await reconcileAcceptanceCriteria({
      workdir,
      issueNumber: 801,
      issueBody: body,
      mainSha: "0000000000000000000000000000000000000000",
    });

    expect(report.acceptance_criteria.length).toBe(6);
    const satisfied = report.acceptance_criteria.filter(
      (c) => c.classification === "satisfied"
    ).length;
    expect(satisfied).toBeGreaterThanOrEqual(4);

    expect(["mostly-satisfied", "all-satisfied", "partial"]).toContain(report.aggregate_status);
    expect(["narrow-scope", "verify-and-close", "standard"]).toContain(
      report.suggested_route.approach
    );

    // Every reconciled AC must include a non-empty reason string.
    for (const ac of report.acceptance_criteria) {
      expect(ac.reason.length).toBeGreaterThan(0);
    }
  });

  it("returns no-acs-detected for a body with no checkboxes", async () => {
    await copyFixtureTree(workdir);
    const report = await reconcileAcceptanceCriteria({
      workdir,
      issueNumber: 801,
      issueBody: "Just a paragraph. No criteria.",
      mainSha: "abcdef",
    });
    expect(report.aggregate_status).toBe("no-acs-detected");
    expect(report.suggested_route.approach).toBe("standard");
  });

  it("aggregates to all-satisfied on a body whose ACs all map to existing files", async () => {
    await copyFixtureTree(workdir);
    // Add a file the AC asks for.
    const target = path.join(workdir, "docs", "README.md");
    await mkdir(path.dirname(target), { recursive: true });
    await writeFile(target, "# Readme\n", "utf-8");
    const body = "- [ ] File `docs/README.md` exists";

    const report = await reconcileAcceptanceCriteria({
      workdir,
      issueNumber: 999,
      issueBody: body,
      mainSha: "deadbeef",
    });
    expect(report.aggregate_status).toBe("all-satisfied");
    expect(report.suggested_route.approach).toBe("verify-and-close");
  });
});
