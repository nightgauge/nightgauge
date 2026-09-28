/**
 * Local-path, executable and code/credential URL settings must be `machine`
 * (or `machine-overridable`) scope, so a committed or malicious
 * `.vscode/settings.json` cannot redirect which binary the extension spawns
 * or where it fetches code from (ADR-024 § 14).
 *
 * @see Issue #2044
 */

import { describe, it, expect } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const manifest = JSON.parse(fs.readFileSync(path.resolve(here, "../../package.json"), "utf-8"));

type Setting = { scope?: string };

function allSettings(): Record<string, Setting> {
  const cfg = manifest.contributes.configuration;
  const sections = Array.isArray(cfg) ? cfg : [cfg];
  return Object.assign({}, ...sections.map((s: { properties?: object }) => s.properties ?? {}));
}

const MACHINE_SCOPES = new Set(["machine", "machine-overridable"]);
const LOCAL_KEY = /(Path|binary|Binary|Dir|Url)$/;

describe("setting scopes (#2044)", () => {
  const settings = allSettings();

  it("finds the manifest's settings", () => {
    expect(Object.keys(settings).length).toBeGreaterThan(0);
  });

  it.each(Object.keys(settings).filter((k) => LOCAL_KEY.test(k)))(
    "%s is machine or machine-overridable scope",
    (key) => {
      expect(MACHINE_SCOPES.has(settings[key].scope ?? "window")).toBe(true);
    }
  );

  it("pins the executable path to machine scope", () => {
    expect(settings["nightgauge.backend.binaryPath"]?.scope).toBe("machine");
  });
});
