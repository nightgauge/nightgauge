// Fixture for ts-exec-interpolated, checked by `semgrep --test .semgrep/rules`
// (scripts/semgrep-check.sh). ESLint and tsc ignore this directory.
import * as child_process from "child_process";
import { exec, execFile, execSync } from "child_process";
import { promisify } from "util";

const execAsync = promisify(exec);
const execFileAsync = promisify(execFile);

export async function unsafe(x: string): Promise<void> {
  // ruleid: ts-exec-interpolated
  exec(`gh ${x}`);
  // ruleid: ts-exec-interpolated
  execSync("git log " + x);
  const cmd = `gh issue view ${x}`;
  // ruleid: ts-exec-interpolated
  await execAsync(cmd);
  // ruleid: ts-exec-interpolated
  child_process.exec(`git show ${x}`);
}

export async function safe(x: string): Promise<void> {
  // ok: ts-exec-interpolated
  exec("git status");
  // ok: ts-exec-interpolated
  await execFileAsync("gh", ["issue", "view", x]);
  // ok: ts-exec-interpolated
  /^(\d+)$/.exec(x);
}
