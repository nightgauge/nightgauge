/**
 * The Doctor panel screenshot helper (#2099): which windows it opens, in
 * which themes, and what it plays there. The capture itself is run by hand
 * (see the script's header).
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { createRequire } from "node:module";
import { describe, expect, it } from "vitest";
import { REFERENCE_SCENARIO } from "../../scripts/demo-session";
import {
  DEFAULT_THEMES,
  parseScreenshotArgs,
  planShots,
  stillScenario,
} from "../../scripts/doctor-theme-screenshots";

const load = createRequire(__filename);
const { loadScenario } = load("../../demo/daemon/scenario.cjs") as {
  loadScenario(raw: unknown): { steps: unknown[] };
};

describe("doctor theme screenshots", () => {
  it("covers light, dark and high contrast by default, under the temp directory", () => {
    const options = parseScreenshotArgs([]);
    expect(options.themes).toEqual(["light", "dark", "high-contrast"]);
    expect(DEFAULT_THEMES).toEqual(options.themes);
    expect(options.out).toBe(path.join(os.tmpdir(), "nightgauge-doctor-screenshots"));
  });

  it("takes a theme list and refuses an unknown theme or flag", () => {
    expect(parseScreenshotArgs(["--themes", "dark, high-contrast-light"]).themes).toEqual([
      "dark",
      "high-contrast-light",
    ]);
    expect(() => parseScreenshotArgs(["--themes", "dark,sepia"])).toThrow(/--themes takes/);
    expect(() => parseScreenshotArgs(["--themes", ","])).toThrow(/--themes takes/);
    expect(() => parseScreenshotArgs(["--out"])).toThrow(/--out needs a value/);
    expect(() => parseScreenshotArgs(["--theme", "dark"])).toThrow(/unknown option --theme/);
  });

  it("plans one capture per theme and the keyboard-only fix in the first", () => {
    const shots = planShots(parseScreenshotArgs(["--out", "/shots"]));
    expect(shots.map((s) => path.basename(s.file))).toEqual([
      "doctor-light.png",
      "doctor-dark.png",
      "doctor-high-contrast.png",
    ]);
    expect(shots[0].keyboardFix).toBe(path.resolve("/shots/doctor-light-keyboard-fix.png"));
    expect(shots.slice(1).every((s) => s.keyboardFix === undefined)).toBe(true);
  });

  it("plays the reference seed with no steps, which the demo daemon accepts", () => {
    const reference = JSON.parse(fs.readFileSync(REFERENCE_SCENARIO, "utf8"));
    expect(reference.steps.length).toBeGreaterThan(0);
    const still = JSON.parse(stillScenario(fs.readFileSync(REFERENCE_SCENARIO, "utf8")));
    expect(still.steps).toEqual([]);
    expect(still.seed).toEqual(reference.seed);
    expect(still.state).toEqual(reference.state);
    expect(loadScenario(still).steps).toEqual([]);
  });
});
