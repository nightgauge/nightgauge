/**
 * Temp workspace helpers for integration tests
 *
 * Provides setup/teardown utilities for real filesystem I/O tests.
 * Each test gets an isolated temp directory that is cleaned up after.
 */

import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";

export interface TestWorkspace {
  dir: string;
  pipelineDir: string;
  plansDir: string;
  cleanup: () => Promise<void>;
}

/**
 * Create a temporary workspace directory for a test.
 * Returns cleanup function that removes all created files.
 */
export async function createTestWorkspace(): Promise<TestWorkspace> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "sdk-integration-"));
  const pipelineDir = path.join(dir, ".nightgauge", "pipeline");
  const plansDir = path.join(dir, ".nightgauge", "plans");

  await fs.mkdir(pipelineDir, { recursive: true });
  await fs.mkdir(plansDir, { recursive: true });

  return {
    dir,
    pipelineDir,
    plansDir,
    cleanup: async () => {
      await fs.rm(dir, { recursive: true, force: true });
    },
  };
}

/**
 * Write a JSON fixture to a workspace pipeline directory.
 */
export async function writeFixture(
  workspace: TestWorkspace,
  filename: string,
  data: unknown
): Promise<string> {
  const filePath = path.join(workspace.pipelineDir, filename);
  await fs.writeFile(filePath, JSON.stringify(data, null, 2), "utf-8");
  return filePath;
}

/**
 * Read a JSON file from a workspace pipeline directory.
 */
export async function readFixture(workspace: TestWorkspace, filename: string): Promise<unknown> {
  const filePath = path.join(workspace.pipelineDir, filename);
  const content = await fs.readFile(filePath, "utf-8");
  return JSON.parse(content);
}
