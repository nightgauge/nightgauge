import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import packageJson from "../../package.json";
import inventory from "../../src/config/settings-surface-inventory.json";
import { DEFAULT_CONFIG, NightgaugeConfigSchema } from "../../src/config/schema";
import { getSettingsHtml } from "../../src/views/settings/SettingsHtml";
import { SETTINGS_SECTIONS } from "../../src/views/settings/types";

type InventoryRule = (typeof inventory.vscode_namespaces)[number];

const packageRoot = resolve(import.meta.dirname, "../..");

function matches(setting: string, rule: InventoryRule): boolean {
  return rule.setting.endsWith("*")
    ? setting.startsWith(rule.setting.slice(0, -1))
    : setting === rule.setting;
}

/**
 * True when `source` actually READS one of the namespace's settings through the
 * VS Code settings API.
 *
 * A file-exists check is not enough: it passes for any path that resolves, which
 * is how `nightgauge.orchestration.*` and `nightgauge.agentTeams.*` shipped as
 * inert surfaces behind a green test (#968).
 *
 * A bare substring check is not enough either, and that is the subtler trap. The
 * namespace string also appears in memento keys ("nightgauge.outputWindow.state"),
 * in the manifest that DECLARES the settings, and in the Zod schema — none of
 * which is a consumer. So the check requires a real `getConfiguration()` call
 * whose section prefixes the setting, plus the remaining leaf as a string
 * literal. That pairing is what distinguishes reading a setting from merely
 * naming one.
 */
function readsSetting(source: string, setting: string): boolean {
  const sections = [...source.matchAll(/getConfiguration\(\s*["'`]([^"'`]+)["'`]\s*\)/g)].map(
    (m) => m[1]
  );

  // `getConfiguration(CONST)` — resolve simple module-level string constants.
  for (const [, name] of source.matchAll(/getConfiguration\(\s*([A-Z_][A-Z0-9_]*)\s*\)/g)) {
    const decl = source.match(new RegExp(`${name}\\s*=\\s*["'\`]([^"'\`]+)["'\`]`));
    if (decl) sections.push(decl[1]);
  }

  return sections.some((section) => {
    if (!setting.startsWith(`${section}.`)) return false;
    const leaf = setting.slice(section.length + 1);
    return new RegExp(`["'\`]${leaf.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}["'\`]`).test(source);
  });
}

function schemaHasPath(path: string): boolean {
  const segments = path.replace(/\.\*$/, "").split(".");
  let schema: unknown = NightgaugeConfigSchema;

  for (const segment of segments) {
    while (
      schema &&
      typeof schema === "object" &&
      "unwrap" in schema &&
      typeof (schema as { unwrap: () => unknown }).unwrap === "function"
    ) {
      schema = (schema as { unwrap: () => unknown }).unwrap();
    }
    if (
      schema &&
      typeof schema === "object" &&
      "element" in schema &&
      (schema as { element?: unknown }).element
    ) {
      schema = (schema as { element: unknown }).element;
      if (segment === "*") continue;
    }
    if (
      schema &&
      typeof schema === "object" &&
      (schema as { _def?: { type?: string } })._def?.type === "record"
    ) {
      return true;
    }
    const candidate = schema as {
      shape?: Record<string, unknown> | (() => Record<string, unknown>);
      def?: { shape?: Record<string, unknown> | (() => Record<string, unknown>) };
    };
    const rawShape = candidate.shape ?? candidate.def?.shape;
    const shape = typeof rawShape === "function" ? rawShape() : rawShape;
    if (!shape || !(segment in shape)) return false;
    schema = shape[segment];
  }
  return true;
}

describe("settings surface inventory", () => {
  it("classifies every generated VS Code setting exactly once", () => {
    const settings = Object.keys(packageJson.contributes.configuration.properties);
    const missing = settings.filter(
      (setting) => inventory.vscode_namespaces.filter((rule) => matches(setting, rule)).length !== 1
    );

    expect(missing, "unclassified or ambiguously classified VS Code settings").toEqual([]);
  });

  it("maps schema-backed VS Code namespaces to real schema paths and consumers", () => {
    const settings = Object.keys(packageJson.contributes.configuration.properties);

    for (const rule of inventory.vscode_namespaces) {
      const consumer = resolve(packageRoot, rule.runtime_consumer);
      expect(existsSync(consumer), `${rule.setting}: ${rule.runtime_consumer}`).toBe(true);

      // The consumer must actually read at least one setting in the namespace.
      const source = readFileSync(consumer, "utf8");
      const owned = settings.filter((setting) => matches(setting, rule));
      expect(
        owned.some((setting) => readsSetting(source, setting)),
        `${rule.setting} is declared to be consumed by ${rule.runtime_consumer}, which never reads it`
      ).toBe(true);
      if ("schema_path" in rule && rule.schema_path) {
        expect(schemaHasPath(rule.schema_path), rule.setting).toBe(true);
      } else {
        expect("classification" in rule, rule.setting).toBe(true);
      }
    }
  });

  it("maps every custom Settings section to the canonical schema", () => {
    expect(inventory.custom_sections.map(({ section }) => section).sort()).toEqual(
      SETTINGS_SECTIONS.map(({ id }) => id).sort()
    );
    for (const entry of inventory.custom_sections) {
      // A section may be backed by the canonical config schema OR declare a
      // different backing store, the same escape hatch vscode_namespaces uses.
      // `workspace_repos` edits .vscode/nightgauge-workspace.yaml, which is a
      // different file with a different writer, so demanding a schema path
      // would force a fictional one.
      if ("schema_path" in entry && entry.schema_path) {
        expect(schemaHasPath(entry.schema_path), entry.section).toBe(true);
      } else {
        expect("classification" in entry, entry.section).toBe(true);
      }
    }
  });

  it("maps every rendered custom Settings control to a canonical schema path", () => {
    const webview = { cspSource: "test" } as Parameters<typeof getSettingsHtml>[0];
    const html = getSettingsHtml(webview, DEFAULT_CONFIG);
    const controls = [...html.matchAll(/\sdata-path="([^"]+)"/g)].map((match) => match[1]);
    const unsupported = [...new Set(controls)].filter((path) => !schemaHasPath(path));

    expect(controls.length).toBeGreaterThan(40);
    expect(unsupported, "visible custom controls without schema mappings").toEqual([]);
  });

  it("requires explicit omission classes and pins sensitive boundaries", () => {
    const omissionClasses = new Set(
      inventory.intentional_omissions.map(({ classification }) => classification)
    );
    expect(omissionClasses).toEqual(new Set(["non-gui", "secret", "derived", "deprecated"]));

    const secureBoundary = inventory.critical_boundaries.find(
      ({ id }) => id === "secure-credentials"
    );
    expect(secureBoundary?.storage).toEqual(["secret-storage"]);
    expect(secureBoundary?.schema_paths).toContain("platform.license_key");
    expect(
      secureBoundary?.schema_paths.filter((path) => !schemaHasPath(path)),
      "credential paths missing from schema"
    ).toEqual([]);

    const projectBoundary = inventory.critical_boundaries.find(
      ({ id }) => id === "repository-project-routing"
    );
    expect(projectBoundary?.storage).toEqual(["team", "local"]);
    expect(readFileSync(resolve(packageRoot, projectBoundary!.runtime_consumer), "utf8")).toContain(
      "projects-for-repo"
    );
    for (const boundary of inventory.critical_boundaries) {
      for (const testFile of boundary.boundary_tests) {
        expect(existsSync(resolve(packageRoot, testFile)), `${boundary.id}: ${testFile}`).toBe(
          true
        );
      }
    }
  });
});
