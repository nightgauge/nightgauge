import * as path from "node:path";
import * as fs from "node:fs/promises";

/**
 * Checks whether `.nightgauge/config.yaml` exists at `nightgaugeRoot`.
 * This is the canonical signal that `/nightgauge:repo-init` has run.
 *
 * Lives here rather than in `commands/quickstart` because services need it
 * too — `ProjectBoardService` gates its incomplete-config warning on it
 * (#901) — and a second copy of the predicate is exactly the dual-path drift
 * `docs/FAILURE_TAXONOMY.md` warns about.
 */
export async function isRepoInitialized(nightgaugeRoot: string): Promise<boolean> {
  const configPath = path.join(nightgaugeRoot, ".nightgauge", "config.yaml");
  try {
    const stat = await fs.stat(configPath);
    return stat.isFile();
  } catch {
    return false;
  }
}
