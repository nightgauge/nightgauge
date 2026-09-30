/**
 * Temporary git repositories for tests that exercise per-clone paths
 * (ADR-024 § 7): per-clone data resolves through the git common dir, so a
 * fixture root must be a repository.
 */
import { execFileSync } from "child_process";
import { resolveCloneLayout, type CloneLayout } from "../../context/cloneLayout.js";

/** `git init`s `dir` (inherited GIT_* location variables cleared) and resolves its layout. */
export function initCloneRepo(dir: string): CloneLayout {
  const env = { ...process.env };
  for (const k of ["GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE"]) delete env[k];
  execFileSync("git", ["init", "-q"], { cwd: dir, env, stdio: "ignore" });
  return resolveCloneLayout(dir);
}
