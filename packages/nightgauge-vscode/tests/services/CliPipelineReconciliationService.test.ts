import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  CliPipelineReconciliationService,
  type ReconciledCliRun,
} from "../../src/services/CliPipelineReconciliationService";

const tempRoots: string[] = [];

/**
 * Builds a CLI-run fixture on disk.
 *
 * `scheme` chooses which on-disk layout the fixture uses:
 *  - "identity" (the default) is what the Go binary writes from ADR-017 (#370)
 *    onwards: the sidecar carries `run_id` and the snapshot is named
 *    `runtime-{issue}-{runId}.json`.
 *  - "legacy" is a sidecar with NO `run_id` beside a `runtime-{issue}.json`
 *    snapshot — the shape a binary older than ADR-017 leaves behind. It is
 *    kept because the composition falls back on a missing key, and a fallback
 *    with no test is a fallback nobody notices breaking.
 */
async function fixture(
  repo: string,
  issueNumber: number,
  runId: string,
  pid = 123,
  scheme: "identity" | "legacy" = "identity"
): Promise<string> {
  const root = await mkdtemp(path.join(tmpdir(), "nightgauge-cli-run-"));
  tempRoots.push(root);
  const stateDir = path.join(root, ".nightgauge", "pipeline");
  await mkdir(stateDir, { recursive: true });
  await writeFile(
    path.join(stateDir, "current-run.json"),
    JSON.stringify(
      scheme === "identity"
        ? { issue_number: issueNumber, repo, pid, run_id: runId }
        : { issue_number: issueNumber, repo, pid }
    )
  );
  await writeFile(
    path.join(
      stateDir,
      scheme === "identity" ? `runtime-${issueNumber}-${runId}.json` : `runtime-${issueNumber}.json`
    ),
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

/** A canonical lowercase UUIDv7, the run-identity format ADR-017 fixes. */
function runIdFor(n: number): string {
  const tail = n.toString(16).padStart(12, "0");
  return `01966b4c-0000-7000-a000-${tail}`;
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
    const root = await fixture("nightgauge/nightgauge", 27, runIdFor(27));
    const events = callbacks();
    const service = new CliPipelineReconciliationService(
      () => [{ path: root, repo: "nightgauge/nightgauge" }],
      events.value,
      { isProcessAlive: () => true }
    );

    await service.scan();

    expect(events.discovered).toHaveLength(1);
    expect(events.discovered[0].key).toBe(`nightgauge/nightgauge:${runIdFor(27)}:27`);
    service.dispose();
  });

  it("scans registered secondary roots and deduplicates repeated paths", async () => {
    const primary = await fixture("acme/primary", 1, runIdFor(1));
    const secondary = await fixture("acme/secondary", 2, runIdFor(2));
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
    const stale = await fixture("acme/stale", 3, runIdFor(3));
    const crossRepo = await fixture("acme/wrong", 4, runIdFor(4));
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
    const root = await fixture("acme/app", 5, runIdFor(5));
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

  // ADR-017 (#370) forward-pull. The Go binary writes
  // `runtime-{issue}-{runId}.json` and puts `run_id` on the sidecar; composing
  // the old `runtime-{issue}.json` here would ENOENT on every 1s poll forever,
  // and because the whole read sits inside a bare `catch { return null }`,
  // onDiscovered would never fire and — since `active` never gains an entry —
  // neither would onSettled. No log, no error, no failing test: the entire
  // CLI-observability feature dies with zero diagnostics (F25).
  it("composes the run-identity-keyed snapshot name from the sidecar's run_id", async () => {
    const runId = runIdFor(370);
    const root = await fixture("acme/app", 370, runId);
    const events = callbacks();
    const service = new CliPipelineReconciliationService(
      () => [{ path: root, repo: "acme/app" }],
      events.value,
      { isProcessAlive: () => true }
    );

    await service.scan();

    expect(events.discovered).toHaveLength(1);
    expect(events.discovered[0].snapshot.runId).toBe(runId);
    expect(events.discovered[0].key).toBe(`acme/app:${runId}:370`);
    service.dispose();
  });

  // A sidecar with no run_id was written by a binary older than ADR-017; the
  // legacy composition is the only thing that can read it.
  it("falls back to the legacy snapshot name when the sidecar carries no run_id", async () => {
    const runId = runIdFor(371);
    const root = await fixture("acme/app", 371, runId, 123, "legacy");
    const events = callbacks();
    const service = new CliPipelineReconciliationService(
      () => [{ path: root, repo: "acme/app" }],
      events.value,
      { isProcessAlive: () => true }
    );

    await service.scan();

    expect(events.discovered).toHaveLength(1);
    expect(events.discovered[0].snapshot.runId).toBe(runId);
    service.dispose();
  });

  // The ADR-017 MIXED-VERSION WINDOW, made visible. A `serve` daemon between
  // step 0b and step 1 writes `run_id` into the sidecar but still names the
  // snapshot `runtime-{issue}.json`. Under a new extension bundle the composed
  // new-scheme read ENOENTs inside a bare `catch { return null }`, so this
  // service goes completely dark — no tree slot, no PipelineStateService, no
  // onDiscovered, no onSettled — with no diagnostic anywhere.
  it("reports the mixed-version window instead of going silently dark", async () => {
    const runId = runIdFor(372);
    const root = await mkdtemp(path.join(tmpdir(), "nightgauge-cli-run-"));
    tempRoots.push(root);
    const stateDir = path.join(root, ".nightgauge", "pipeline");
    await mkdir(stateDir, { recursive: true });
    // Sidecar from the NEW binary…
    await writeFile(
      path.join(stateDir, "current-run.json"),
      JSON.stringify({ issue_number: 372, repo: "acme/app", pid: 123, run_id: runId })
    );
    // …snapshot from the OLD one.
    await writeFile(
      path.join(stateDir, "runtime-372.json"),
      JSON.stringify({ repo: "acme/app", issueNumber: 372, runId, stage: "feature-dev" })
    );

    const events = callbacks();
    const seen: Array<{ root: string; issueNumber: number; runId: string }> = [];
    const service = new CliPipelineReconciliationService(
      () => [{ path: root, repo: "acme/app" }],
      events.value,
      { isProcessAlive: () => true, onLegacySnapshotName: (info) => seen.push(info) }
    );

    await service.scan();

    expect(events.discovered).toHaveLength(0);
    expect(seen).toEqual([{ root, issueNumber: 372, runId }]);
    service.dispose();
  });

  // …and it must NOT fire on the healthy path, or it becomes noise the operator
  // learns to ignore.
  it("does not report the mixed-version window when the new-scheme snapshot is present", async () => {
    const runId = runIdFor(373);
    const root = await fixture("acme/app", 373, runId);
    const events = callbacks();
    const seen: unknown[] = [];
    const service = new CliPipelineReconciliationService(
      () => [{ path: root, repo: "acme/app" }],
      events.value,
      { isProcessAlive: () => true, onLegacySnapshotName: (info) => seen.push(info) }
    );

    await service.scan();

    expect(events.discovered).toHaveLength(1);
    expect(seen).toEqual([]);
    service.dispose();
  });
});
