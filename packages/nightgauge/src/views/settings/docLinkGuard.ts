/**
 * docLinkGuard - keep settings documentation links inside the workspace's
 * `docs/` directory.
 *
 * The settings webview asks the host to open a documentation link such as
 * `docs/CONFIGURATION.md#pipeline`. The host opens only a file that resolves
 * (after symlinks) to a location under `<workspaceRoot>/docs/`.
 */

import * as fs from "node:fs";
import * as path from "node:path";

const MAX_LINK_LENGTH = 512;

/**
 * Syntactic check for a documentation link: a relative, forward-slash path
 * under `docs/`, optionally followed by a `#fragment`, with no `..` segment.
 */
export function isDocLinkPath(link: unknown): link is string {
  if (typeof link !== "string" || link.length === 0 || link.length > MAX_LINK_LENGTH) {
    return false;
  }
  const [rel] = link.split("#", 1);
  if (/[\\\0]/.test(rel) || rel.startsWith("/") || /^[A-Za-z]:/.test(rel)) return false;
  const segments = rel.split("/");
  if (segments[0] !== "docs" || segments.length < 2) return false;
  return segments.every((s) => s !== ".." && s !== "." && s !== "");
}

function isInside(child: string, parent: string): boolean {
  return child.startsWith(parent + path.sep);
}

/**
 * Resolve a documentation link against the workspace. Returns the absolute
 * file path and the fragment, or `undefined` when the link is malformed or
 * resolves outside `<workspaceRoot>/docs/`.
 */
export function resolveDocLink(
  workspaceRoot: string,
  link: unknown
): { file: string; fragment: string } | undefined {
  if (!workspaceRoot || !isDocLinkPath(link)) return undefined;
  const hashAt = link.indexOf("#");
  const rel = hashAt === -1 ? link : link.slice(0, hashAt);
  const fragment = hashAt === -1 ? "" : link.slice(hashAt + 1);

  const docsRoot = path.resolve(workspaceRoot, "docs");
  const file = path.resolve(workspaceRoot, rel);
  if (!isInside(file, docsRoot)) return undefined;

  // A symlink under docs/ must not lead outside it.
  if (fs.existsSync(file)) {
    try {
      if (!isInside(fs.realpathSync(file), fs.realpathSync(docsRoot))) return undefined;
    } catch {
      return undefined;
    }
  }
  return { file, fragment };
}
