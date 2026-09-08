/**
 * Copy Nightgauge skills from the VSIX bundle (or a workspace checkout)
 * into an agent home directory (~/.codex/skills, ~/.grok/skills).
 *
 * Marketplace installs have no workspace `skills/` tree. The VSIX ships
 * the plugin skills under dist/claude-plugins/nightgauge/skills (short
 * names) and a pipeline subset under dist/skills (canonical names).
 */

import * as fs from "fs";
import * as path from "path";

const UNPREFIXED = new Set(["_shared", "smart-setup", "update-docs", "pr-preflight"]);

export function resolveBundledSkillsDir(extensionPath: string): string | null {
  const pluginSkills = path.join(extensionPath, "dist", "claude-plugins", "nightgauge", "skills");
  if (fs.existsSync(pluginSkills)) {
    return pluginSkills;
  }
  const distSkills = path.join(extensionPath, "dist", "skills");
  if (fs.existsSync(distSkills)) {
    return distSkills;
  }
  return null;
}

function destSkillName(dirName: string, restorePrefix: boolean): string {
  if (!restorePrefix) {
    return dirName;
  }
  if (
    UNPREFIXED.has(dirName) ||
    dirName.startsWith("nightgauge-") ||
    dirName.startsWith("incredibuilders-")
  ) {
    return dirName;
  }
  return `nightgauge-${dirName}`;
}

/**
 * Copy each SKILL.md directory (and `_shared/`) from sourceDir into destDir.
 * When the source is the plugin mirror (short names like `issue-create`),
 * restore the `nightgauge-` prefix so Codex/Grok invocations match the
 * canonical directory names used by `install-agent-skills.sh`.
 */
export async function copySkillsTree(sourceDir: string, destDir: string): Promise<number> {
  const restorePrefix =
    !fs.existsSync(path.join(sourceDir, "nightgauge-issue-create")) &&
    fs.existsSync(path.join(sourceDir, "issue-create"));

  await fs.promises.mkdir(destDir, { recursive: true });
  const entries = await fs.promises.readdir(sourceDir, { withFileTypes: true });
  let count = 0;
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    if (
      entry.name === "node_modules" ||
      entry.name === "dist" ||
      entry.name === "coverage" ||
      entry.name === "build"
    ) {
      continue;
    }
    const source = path.join(sourceDir, entry.name);
    if (entry.name !== "_shared" && !fs.existsSync(path.join(source, "SKILL.md"))) {
      continue;
    }
    const target = path.join(destDir, destSkillName(entry.name, restorePrefix));
    await fs.promises.rm(target, { recursive: true, force: true });
    await fs.promises.cp(source, target, { recursive: true });
    count += 1;
  }
  return count;
}
