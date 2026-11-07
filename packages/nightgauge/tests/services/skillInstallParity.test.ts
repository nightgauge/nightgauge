/**
 * The extension and scripts/install-agent-skills.sh install the same Codex and
 * Grok skills (#2762).
 *
 * The extension used to install from the Claude plugin mirror, which rewrites
 * `name:`, injects disable-model-invocation and rebases links, then renamed
 * the directories without touching SKILL.md: 43 installed skills kept DMI and
 * 7 links dangled. Both installers now read the canonical tree, so this test
 * runs both for real: the bundler into a temp dist, copySkillsTree from its
 * dist/skills, and install_skill_tree.py from the checkout's skills/. Their
 * outputs must be byte-identical, and the shared output must be one a
 * non-Claude agent can load.
 */

import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { spawnSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({ window: {}, workspace: {} }));

import { copySkillsTree, isSkillExcluded } from "../../src/services/bundledSkills";

const REPO_ROOT = path.resolve(__dirname, "..", "..", "..", "..");
const SKILLS = path.join(REPO_ROOT, "skills");
const BUNDLER = path.join(REPO_ROOT, "packages", "nightgauge", "scripts", "bundle-marketplace.sh");
const INSTALLER = path.join(REPO_ROOT, "scripts", "lib", "install_skill_tree.py");

/** Every file under root, relative path to content. */
function listTree(root: string): Map<string, Buffer> {
  const out = new Map<string, Buffer>();
  const walk = (dir: string): void => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        walk(p);
      } else {
        out.set(path.relative(root, p), fs.readFileSync(p));
      }
    }
  };
  walk(root);
  return out;
}

/** The first frontmatter block of a SKILL.md, or "" when it has none. */
function frontmatter(text: string): string {
  const match = /^---\r?\n([\s\S]*?)\r?\n---/.exec(text);
  return match ? match[1] : "";
}

/** Relative Markdown link targets outside fenced code, without their anchors. */
function relativeLinks(text: string): string[] {
  const prose = text.replace(/```[\s\S]*?```/g, "").replace(/`[^`\n]*`/g, "");
  const links: string[] = [];
  for (const match of prose.matchAll(/\]\(([^)\s#]+)(?:#[^)]*)?\)/g)) {
    const target = match[1];
    if (!/^[a-z][a-z0-9+.-]*:/i.test(target) && !target.startsWith("/")) {
      links.push(target);
    }
  }
  return links;
}

function run(command: string, args: string[], env: NodeJS.ProcessEnv = process.env): void {
  const res = spawnSync(command, args, { encoding: "utf8", env });
  if (res.error) {
    // The arm could not run: say so rather than report a parity failure.
    throw new Error(`HARNESS ERROR: ${command} could not run: ${res.error.message}`);
  }
  expect(res.status, `${command} ${args.join(" ")}\n${res.stderr}${res.stdout}`).toBe(0);
}

describe("extension and script skill installs are identical (#2762)", () => {
  let tmp: string;
  let extensionTree: string;
  let scriptTree: string;
  let extensionFiles: Map<string, Buffer>;
  let scriptFiles: Map<string, Buffer>;

  beforeAll(async () => {
    tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ng-skill-parity-"));
    const dist = path.join(tmp, "dist");
    run("bash", [BUNDLER], { ...process.env, NIGHTGAUGE_BUNDLE_DIST: dist });

    extensionTree = path.join(tmp, "extension-home", "skills");
    const warnings: string[] = [];
    const result = await copySkillsTree(
      path.join(dist, "skills"),
      extensionTree,
      (m) => warnings.push(m),
      {
        prune: true,
      }
    );
    expect(warnings).toEqual([]);
    expect(result.skipped).toBe(0);

    scriptTree = path.join(tmp, "script-home", "skills");
    run("python3", [
      INSTALLER,
      "install",
      "--source",
      SKILLS,
      "--dest",
      scriptTree,
      "--checkout",
      REPO_ROOT,
      "--label",
      "Parity",
    ]);

    extensionFiles = listTree(extensionTree);
    scriptFiles = listTree(scriptTree);
  }, 120_000);

  afterAll(() => {
    fs.rmSync(tmp, { recursive: true, force: true });
  });

  it("installs the same files with the same bytes", () => {
    expect([...extensionFiles.keys()].sort()).toEqual([...scriptFiles.keys()].sort());
    for (const [rel, bytes] of scriptFiles) {
      expect(extensionFiles.get(rel)?.equals(bytes), rel).toBe(true);
    }
  });

  it("installs every canonical skill", () => {
    const canonical = fs
      .readdirSync(SKILLS)
      .filter((name) => fs.existsSync(path.join(SKILLS, name, "SKILL.md")));
    expect(canonical.length).toBeGreaterThan(40);
    for (const name of canonical) {
      expect(fs.existsSync(path.join(extensionTree, name, "SKILL.md")), name).toBe(true);
    }
    expect(fs.existsSync(path.join(extensionTree, "_shared"))).toBe(true);
  });

  it("leaves no skill CHANGELOG.md or test material in either install", () => {
    for (const tree of [extensionFiles, scriptFiles]) {
      for (const rel of tree.keys()) {
        const [skill, ...rest] = rel.split(path.sep);
        if (skill === "_shared") {
          continue;
        }
        const dirs = rest.slice(0, -1);
        const shipped =
          !isSkillExcluded(rest.join("/"), false) &&
          dirs.every((_, i) => !isSkillExcluded(dirs.slice(0, i + 1).join("/"), true));
        expect(shipped, rel).toBe(true);
      }
    }
    // .vscodeignore drops *.test.ts from the VSIX; shipping these failed staging (#2948).
    expect(fs.existsSync(path.join(extensionTree, "nightgauge-product-audit", "__tests__"))).toBe(
      false
    );
  });

  it("isSkillExcluded drops test material at any depth and only a top-level CHANGELOG.md", () => {
    expect(isSkillExcluded("CHANGELOG.md", false)).toBe(true);
    expect(isSkillExcluded("docs/CHANGELOG.md", false)).toBe(false);
    expect(isSkillExcluded("src/__tests__", true)).toBe(true);
    expect(isSkillExcluded("flows/tests", true)).toBe(true);
    expect(isSkillExcluded("src/b.test.js", false)).toBe(true);
    expect(isSkillExcluded("a.test.ts", false)).toBe(true);
    expect(isSkillExcluded("a.test.md", false)).toBe(false);
    expect(isSkillExcluded("src/foo.ts", false)).toBe(false);
  });

  it("gives every skill a name: matching its directory and no disable-model-invocation", () => {
    for (const name of fs.readdirSync(extensionTree)) {
      const skillFile = path.join(extensionTree, name, "SKILL.md");
      if (!fs.existsSync(skillFile)) {
        continue;
      }
      const meta = frontmatter(fs.readFileSync(skillFile, "utf8"));
      expect(/^name:\s*(.+?)\s*$/m.exec(meta)?.[1], skillFile).toBe(name);
      expect(meta, skillFile).not.toMatch(/^disable-model-invocation:/m);
    }
  });

  it("keeps every link that resolves inside the canonical tree resolving after install", () => {
    let checked = 0;
    for (const name of fs.readdirSync(extensionTree)) {
      const installed = path.join(extensionTree, name, "SKILL.md");
      if (!fs.existsSync(installed)) {
        continue;
      }
      for (const link of relativeLinks(fs.readFileSync(installed, "utf8"))) {
        const inSource = path.resolve(SKILLS, name, link);
        const withinSkills = !path.relative(SKILLS, inSource).startsWith("..");
        if (!withinSkills || !fs.existsSync(inSource)) {
          continue;
        }
        checked += 1;
        expect(fs.existsSync(path.resolve(extensionTree, name, link)), `${name}: ${link}`).toBe(
          true
        );
      }
    }
    expect(checked).toBeGreaterThan(0);
  });
});
