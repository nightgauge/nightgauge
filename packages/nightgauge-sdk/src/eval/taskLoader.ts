/**
 * Model-eval realistic-task corpus loader (Issue #4170).
 *
 * Reads `EvalTask` JSON files from disk and validates them against the S1
 * contracts (`modelEvalSchemas.ts`). Mirrors the skill-eval `loader.ts` pattern
 * (injectable `DirReader` so loading is unit-testable without a filesystem), and
 * reuses its reader abstraction — two lanes, one reader.
 *
 * @see docs/decisions/011-model-eval-system.md
 * @see evals/tasks/README.md - the task/fixture format
 */

import { EvalTaskSchema, type EvalTask } from "./modelEvalSchemas.js";
import { defaultDirReader, type DirReader } from "./loader.js";

/** Default repo-relative root for the realistic-task corpus. */
export const DEFAULT_TASKS_DIR = "evals/tasks";

/** Parse + validate one task file's contents. Throws a sourced error on failure. */
export function parseEvalTask(contents: string, sourcePath: string): EvalTask {
  let raw: unknown;
  try {
    raw = JSON.parse(contents);
  } catch (err) {
    throw new Error(`invalid JSON in task ${sourcePath}: ${(err as Error).message}`, {
      cause: err,
    });
  }
  const result = EvalTaskSchema.safeParse(raw);
  if (!result.success) {
    throw new Error(`invalid eval task ${sourcePath}: ${result.error.message}`);
  }
  return result.data;
}

/**
 * Load and validate every task JSON under `dir`. Task ids must be unique across
 * the corpus (the runner keys results by id), so a duplicate id throws. Results
 * are returned sorted by id for deterministic ordering.
 */
export async function loadEvalTasks(
  dir: string = DEFAULT_TASKS_DIR,
  reader: DirReader = defaultDirReader
): Promise<EvalTask[]> {
  const files = await reader.listJson(dir);
  const tasks: EvalTask[] = [];
  const seen = new Map<string, string>();
  for (const file of files) {
    const task = parseEvalTask(await reader.readFile(file), file);
    const prior = seen.get(task.id);
    if (prior) {
      throw new Error(`duplicate task id "${task.id}" in ${file} (already defined in ${prior})`);
    }
    seen.set(task.id, file);
    tasks.push(task);
  }
  return tasks.sort((a, b) => a.id.localeCompare(b.id));
}
