/**
 * Pre-planning ac-reconcile regeneration for the extension's own pipeline loop
 * (#3228). The Go scheduler's twin is internal/orchestrator/ac_reconcile_regen.go.
 *
 * The orchestrator, not the planning model, writes the scope every later stage
 * judges: `ac-reconcile-{N}.json` is rebuilt from the issue's current, vouched
 * body with the two binary verbs the feature-planning skill used to run itself —
 * `forge issue view --vouched` (#2130) and `preflight ac-reconcile
 * --notify-empty` (#2906). An earlier run's report is never a fallback.
 */

import { execFile } from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { promisify } from "util";

const execFileAsync = promisify(execFile);

export interface AcReconcileRegenParams {
  /** Resolved nightgauge binary, or null when none was found. */
  binary: string | null;
  issueNumber: number;
  /** The run's working tree: the reconcile evaluates criteria against it. */
  cwd: string;
  /** owner/name, or "" to let the binary resolve it from cwd. */
  repoSlug: string;
  /** Where ac-reconcile-{N}.json is written. */
  outPath: string;
}

/**
 * Rebuild the report. Returns the error that fails feature-planning, or null
 * once the report is written.
 */
export async function regenerateAcReconcileReport(
  p: AcReconcileRegenParams
): Promise<Error | null> {
  const fail = (why: string) => new Error(`ac-reconcile regeneration failed: ${why}`);
  if (!p.binary) return fail("nightgauge binary not found");
  const repoArgs = p.repoSlug.includes("/") ? ["--repo", p.repoSlug] : [];
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "nightgauge-ac-body-"));
  try {
    const { stdout } = await execFileAsync(
      p.binary,
      ["forge", "issue", "view", String(p.issueNumber), ...repoArgs, "--json", "--vouched"],
      { encoding: "utf-8", cwd: p.cwd, timeout: 60_000 }
    );
    const parsed = JSON.parse(stdout) as { body?: unknown };
    const bodyFile = path.join(tmpDir, "body.md");
    fs.writeFileSync(bodyFile, typeof parsed.body === "string" ? parsed.body : "", "utf-8");
    await execFileAsync(
      p.binary,
      [
        "preflight",
        "ac-reconcile",
        String(p.issueNumber),
        "--workdir",
        p.cwd,
        "--body-file",
        bodyFile,
        "--out",
        p.outPath,
        "--notify-empty",
        ...repoArgs,
      ],
      { encoding: "utf-8", cwd: p.cwd, timeout: 60_000 }
    );
    return null;
  } catch (err) {
    return fail(err instanceof Error ? err.message : String(err));
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
}
