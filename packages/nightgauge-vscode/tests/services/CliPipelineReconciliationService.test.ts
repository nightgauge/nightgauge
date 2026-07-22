import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  CliPipelineReconciliationService,
  type ReconciledCliRun,
} from "../../src/services/CliPipelineReconciliationService";

const tempRoots: string[] = [];

async function fixture(
  repo: string,
  issueNumber: number,
  runId: string,
  pid = 123
): Promise<string> {
  const root = await mkdtemp(path.join(tmpdir(), "nightgauge-cli-run-"));
  tempRoots.push(root);
  const stateDir = path.join(root, ".nightgauge", "pipeline");
  await mkdir(stateDir, { recursive: true });
  await writeFile(
    path.join(stateDir, "current-run.json"),
    JSON.stringify({ issue_number: issueNumber, repo, pid })
  );
  await writeFile(
    path.join(stateDir, `runtime-${issueNumber}.json`),
    JSON.stringify({
      repo,
      issueNumber,
      runId,
      title: `Issue ${issueNumber}`,
      stage: "feature-dev",
    })
  );
  return root;
}

afterEach(async () => {
  await Promise.all(tempRoots.splice(0).map((root) => rm(root, { recursive: true, force: true })));
});

function callbacks() {
  const discovered: ReconciledCliRun[] = [];
  const updated: ReconciledCliRun[] = [];
  const settled: ReconciledCliRun[] = [];
  return {
    discovered,
    updated,
    settled,
    value: {
      onDiscovered: (run: ReconciledCliRun) => discovered.push(run),
      onUpdated: (run: ReconciledCliRun) => updated.push(run),
      onSettled: (run: ReconciledCliRun) => settled.push(run),
    },
  };
}

describe("CliPipelineReconciliationService", () => {
  it("discovers a same-root run on the first late-activation scan", async () => {
    const root = await fixture("nightgauge/nightgauge", 27, "run-27");
    const events = callbacks();
    const service = new CliPipelineReconciliationService(
      () => [{ path: root, repo: "nightgauge/nightgauge" }],
      events.value,
      { isProcessAlive: () => true }
    );

    await service.scan();

    expect(events.discovered).toHaveLength(1);
    expect(events.discovered[0].key).toBe("nightgauge/nightgauge:run-27:27");
    service.dispose();
  });

  it("scans registered secondary roots and deduplicates repeated paths", async () => {
    const primary = await fixture("acme/primary", 1, "primary-run");
    const secondary = await fixture("acme/secondary", 2, "secondary-run");
    const events = callbacks();
    const service = new CliPipelineReconciliationService(
      () => [
        { path: primary, repo: "acme/primary" },
        { path: secondary, repo: "acme/secondary" },
        { path: secondary, repo: "acme/secondary" },
      ],
      events.value,
      { isProcessAlive: () => true }
    );

    await service.scan();

    expect(events.discovered.map((run) => run.snapshot.repo).sort()).toEqual([
      "acme/primary",
      "acme/secondary",
    ]);
    service.dispose();
  });

  it("rejects stale processes, malformed identity, and cross-repository snapshots", async () => {
    const stale = await fixture("acme/stale", 3, "stale-run");
    const crossRepo = await fixture("acme/wrong", 4, "wrong-run");
    const events = callbacks();
    const service = new CliPipelineReconciliationService(
      () => [
        { path: stale, repo: "acme/stale" },
        { path: crossRepo, repo: "acme/expected" },
      ],
      events.value,
      { isProcessAlive: () => false }
    );

    await service.scan();

    expect(events.discovered).toHaveLength(0);
    service.dispose();
  });

  it("updates an existing run and settles it when its terminal sidecar disappears", async () => {
    const root = await fixture("acme/app", 5, "same-run");
    const events = callbacks();
    const service = new CliPipelineReconciliationService(
      () => [{ path: root, repo: "acme/app" }],
      events.value,
      { isProcessAlive: () => true }
    );
    await service.scan();
    await service.scan();
    await rm(path.join(root, ".nightgauge", "pipeline", "current-run.json"));
    await service.scan();

    expect(events.discovered).toHaveLength(1);
    expect(events.updated).toHaveLength(1);
    expect(events.settled).toHaveLength(1);
    service.dispose();
  });
});
