/**
 * Doctor panel screenshots in the light, dark and high-contrast themes, and
 * the keyboard-only fix (#2099's manual verification).
 *
 *   npm run -w nightgauge-vscode demo:doctor-screenshots -- [--out <dir>]
 *     [--themes light,dark,high-contrast] [--code <path>]
 *
 * Build first (`npm run -w nightgauge-vscode build`). For each theme it opens
 * a demo session (`scripts/demo-session.ts`) in that built-in theme: a profile
 * of its own, the demo daemon, and the reference scenario's seed state with no
 * playback, so nothing moves while you capture. The demo daemon answers the
 * `doctor.*` methods with fictional findings (a warning, two housekeeping
 * items and an info) and applies a fix in memory, so nothing on this machine
 * changes. Then, in each window:
 *
 *   1. Cmd/Ctrl+Shift+P, "Nightgauge: Run Doctor", and wait for the cards.
 *   2. Press Enter here. On macOS, click the VS Code window: screencapture's
 *      window mode saves doctor-<theme>.png (the terminal needs the Screen
 *      Recording permission). Elsewhere, save your own screenshot under the
 *      name printed.
 *   3. In the first theme only, without the mouse: Tab to the Housekeeping
 *      group and open it, Tab to the leaked worktree's (NGD017) Fix button and
 *      press Enter. When its card shows the verified outcome, press Enter here
 *      for doctor-<theme>-keyboard-fix.png.
 *   4. Close the window; the next theme opens.
 *
 * Attach the PNGs to the pull request or to #2099. The default output
 * directory is `<os temp dir>/nightgauge-doctor-screenshots`.
 */

import { spawn, spawnSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { createInterface, type Interface } from "node:readline/promises";
import { pathToFileURL } from "node:url";
import {
  DEMO_THEMES,
  DEMO_WORKSPACE,
  PACKAGE_ROOT,
  REFERENCE_SCENARIO,
  defaultOptions,
  isDemoTheme,
  planDemoSession,
  prepareDemoSession,
  resolveCode,
  sessionEnv,
  type DemoTheme,
} from "./demo-session";

export interface ScreenshotOptions {
  /** Where the PNGs and the session's profile and workspace go. */
  out: string;
  themes: DemoTheme[];
  /** VS Code executable; the cached test-electron build when unset. */
  code?: string;
}

export const DEFAULT_THEMES: readonly DemoTheme[] = ["light", "dark", "high-contrast"];

/** Parse the command line; throws naming the bad flag. */
export function parseScreenshotArgs(argv: readonly string[]): ScreenshotOptions {
  const options: ScreenshotOptions = {
    out: path.join(os.tmpdir(), "nightgauge-doctor-screenshots"),
    themes: [...DEFAULT_THEMES],
  };
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    const value = (): string => {
      const next = argv[++i];
      if (next === undefined || next.startsWith("--")) throw new Error(`${flag} needs a value`);
      return next;
    };
    switch (flag) {
      case "--out":
        options.out = path.resolve(value());
        break;
      case "--code":
        options.code = path.resolve(value());
        break;
      case "--themes": {
        const themes = value()
          .split(",")
          .map((t) => t.trim())
          .filter((t) => t !== "");
        if (themes.length === 0 || !themes.every(isDemoTheme)) {
          throw new Error(
            `--themes takes a comma-separated list of ${Object.keys(DEMO_THEMES).join(", ")}`
          );
        }
        options.themes = themes;
        break;
      }
      default:
        throw new Error(`unknown option ${flag}`);
    }
  }
  return options;
}

export interface Shot {
  theme: DemoTheme;
  file: string;
  /** Set on the first theme only: the keyboard-only fix's capture. */
  keyboardFix?: string;
}

/** One capture per theme, plus the keyboard-only fix in the first. */
export function planShots(options: ScreenshotOptions): Shot[] {
  return options.themes.map((theme, i) => ({
    theme,
    file: path.join(options.out, `doctor-${theme}.png`),
    ...(i === 0 ? { keyboardFix: path.join(options.out, `doctor-${theme}-keyboard-fix.png`) } : {}),
  }));
}

/** The scenario without its steps: the seed state, and nothing plays. */
export function stillScenario(raw: string): string {
  const scenario = JSON.parse(raw) as Record<string, unknown>;
  return `${JSON.stringify({ ...scenario, steps: [] }, null, 2)}\n`;
}

/** Save one capture; true when the file exists afterwards. */
async function capture(file: string, rl: Interface): Promise<boolean> {
  fs.rmSync(file, { force: true });
  if (process.platform === "darwin") {
    console.log(`  Click the VS Code window to save ${file} (Esc cancels).`);
    spawnSync("screencapture", ["-i", "-w", "-o", "-x", file], { stdio: "inherit" });
  } else {
    await rl.question(`  Save a screenshot of the VS Code window as ${file}, then press Enter. `);
  }
  if (fs.existsSync(file)) return true;
  console.log(
    "  Not captured: cancelled, or (macOS) the terminal lacks System Settings → Privacy &" +
      " Security → Screen Recording."
  );
  return false;
}

async function main(): Promise<void> {
  const options = parseScreenshotArgs(process.argv.slice(2));
  if (!fs.existsSync(path.join(PACKAGE_ROOT, "dist", "extension.cjs"))) {
    throw new Error(
      "dist/extension.cjs is missing: run `npm run -w nightgauge-vscode build` first."
    );
  }
  const home = path.join(options.out, "session");
  fs.mkdirSync(home, { recursive: true });
  const scenario = path.join(home, "still-scenario.json");
  fs.writeFileSync(scenario, stillScenario(fs.readFileSync(REFERENCE_SCENARIO, "utf8")));
  const code = await resolveCode(options);

  const rl = createInterface({ input: process.stdin, output: process.stdout });
  const saved: string[] = [];
  try {
    for (const shot of planShots(options)) {
      const plan = planDemoSession({ ...defaultOptions(), home, scenario, theme: shot.theme });
      prepareDemoSession(plan, DEMO_WORKSPACE);
      console.log(`\n== ${DEMO_THEMES[shot.theme]}`);
      const child = spawn(code, plan.args, { env: sessionEnv(process.env, plan), stdio: "ignore" });
      const closed = new Promise<void>((resolve) => {
        child.on("error", (err) => {
          console.error(`ERROR: could not start VS Code at ${code}: ${err.message}`);
          resolve();
        });
        child.on("exit", () => resolve());
      });
      await rl.question(
        '  In the window: Cmd/Ctrl+Shift+P, "Nightgauge: Run Doctor". Press Enter when the cards show. '
      );
      if (await capture(shot.file, rl)) saved.push(shot.file);
      if (shot.keyboardFix) {
        await rl.question(
          "  Keyboard-only fix, no mouse: Tab to the Housekeeping group and open it, Tab to the" +
            " leaked worktree's (NGD017) Fix and press Enter. Press Enter here when its card shows" +
            " the verified outcome. "
        );
        if (await capture(shot.keyboardFix, rl)) saved.push(shot.keyboardFix);
      }
      console.log(`  Close the window (VS Code pid ${child.pid}) to go on.`);
      await closed;
    }
  } finally {
    rl.close();
  }
  console.log(`\nSaved ${saved.length} screenshot(s):`);
  for (const file of saved) console.log(`  ${file}`);
  console.log("Attach them to the pull request or to #2099.");
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  main().catch((err: unknown) => {
    console.error(`ERROR: ${err instanceof Error ? err.message : String(err)}`);
    process.exit(1);
  });
}
