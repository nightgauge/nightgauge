/**
 * Tests for scripts/check-running-pipelines.sh — the dev-install guard (#1511).
 *
 * Installing the extension ends in a window reload, and a reload calls
 * `abortAll()`, killing every in-flight pipeline. `autonomous stop` does not do
 * that; it lets the running slots finish. The guard's job is to notice that
 * work is still in flight and let dev-install.sh ask before spending it.
 *
 * Every assertion here is about the two directions of being wrong:
 *   - a false alarm costs one confirmation prompt;
 *   - a false all-clear costs the operator every running pipeline,
 * so "unknown" must exit 0 (no alarm, and dev-install proceeds) while any
 * positive answer must exit 1.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync, chmodSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const SCRIPT = join(__dirname, "../../scripts/check-running-pipelines.sh");

let root: string;

/** Write a fake `nightgauge` whose `autonomous status --json` prints `payload`. */
function fakeBinary(payload: string, exitCode = 0): string {
  const bin = join(root, "nightgauge");
  writeFileSync(
    bin,
    `#!/usr/bin/env bash\ncat <<'PAYLOAD'\n${payload}\nPAYLOAD\nexit ${exitCode}\n`,
    "utf8"
  );
  chmodSync(bin, 0o755);
  return bin;
}

/** Run the guard. Returns its exit status and stdout. */
function runGuard(bin?: string): { status: number; stdout: string } {
  try {
    const stdout = execFileSync("bash", [SCRIPT, root], {
      encoding: "utf8",
      env: {
        ...process.env,
        ...(bin ? { NIGHTGAUGE_BIN: bin } : { NIGHTGAUGE_BIN: "/nonexistent" }),
      },
    });
    return { status: 0, stdout };
  } catch (error) {
    const e = error as { status?: number; stdout?: string };
    return { status: e.status ?? -1, stdout: e.stdout ?? "" };
  }
}

describe("check-running-pipelines.sh (#1511)", () => {
  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "ng-guard-"));
    mkdirSync(join(root, "bin"), { recursive: true });
  });
  afterEach(() => rmSync(root, { recursive: true, force: true }));

  it("exits 0 when no binary is available — unknown is not an alarm", () => {
    expect(runGuard().status).toBe(0);
  });

  it("exits 0 when the daemon did not answer, even with an empty run list", () => {
    // running_pipelines_known:false means "we could not ask". Reading the
    // empty list as "nothing is running" would be the false all-clear.
    const bin = fakeBinary(
      JSON.stringify({ running_pipelines_known: false, running_pipelines: [] })
    );
    expect(runGuard(bin).status).toBe(0);
  });

  it("exits 0 when the daemon answered and nothing is running", () => {
    const bin = fakeBinary(
      JSON.stringify({ running_pipelines_known: true, running_pipelines: [] })
    );
    expect(runGuard(bin).status).toBe(0);
  });

  it("exits 1 and names every running run when pipelines are in flight", () => {
    const bin = fakeBinary(
      JSON.stringify({
        running_pipelines_known: true,
        running_pipelines: [
          {
            repo: "acme/acme-flutter",
            issueNumber: 313,
            stage: "feature-dev",
            source: "autonomous",
            stale: false,
          },
          {
            repo: "acme/acme-platform",
            issueNumber: 1429,
            stage: "pr-create",
            source: "manual",
            stale: false,
          },
        ],
      })
    );

    const { status, stdout } = runGuard(bin);

    expect(status).toBe(1);
    // The identities are the point: a bare count cannot tell an operator
    // whether the work is theirs to wait for.
    expect(stdout).toContain("#313 acme-flutter");
    expect(stdout).toContain("#1429 acme-platform");
    // Both modes appear — a manual run dies in a reload identically, and the
    // scheduler has never heard of it.
    expect(stdout).toContain("[autonomous]");
    expect(stdout).toContain("[manual]");
    expect(stdout).toContain("feature-dev");
  });

  it("flags a stale run rather than dropping it", () => {
    const bin = fakeBinary(
      JSON.stringify({
        running_pipelines_known: true,
        running_pipelines: [{ repo: "acme/web", issueNumber: 7, source: "manual", stale: true }],
      })
    );

    const { status, stdout } = runGuard(bin);

    expect(status).toBe(1);
    expect(stdout).toContain("#7 web");
    expect(stdout).toContain("no progress recently");
  });

  it("exits 0 on unparseable output instead of failing the install", () => {
    const bin = fakeBinary("not json at all");
    expect(runGuard(bin).status).toBe(0);
  });

  it("exits 0 when the binary itself fails", () => {
    const bin = fakeBinary("", 1);
    expect(runGuard(bin).status).toBe(0);
  });
});
