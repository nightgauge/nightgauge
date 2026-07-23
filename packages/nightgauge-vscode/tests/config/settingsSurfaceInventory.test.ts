import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import packageJson from "../../package.json";
import inventory from "../../src/config/settings-surface-inventory.json";
import { DEFAULT_CONFIG, IncrediConfigSchema } from "../../src/config/schema";
import { getSettingsHtml } from "../../src/views/settings/SettingsHtml";
import { SETTINGS_SECTIONS } from "../../src/views/settings/types";

type InventoryRule = (typeof inventory.vscode_namespaces)[number];

const packageRoot = resolve(import.meta.dirname, "../..");

function matches(setting: string, rule: InventoryRule): boolean {
  return rule.setting.endsWith("*")
    ? setting.startsWith(rule.setting.slice(0, -1))
    : setting === rule.setting;
}

function schemaHasPath(path: string): boolean {
  const segments = path.replace(/\.\*$/, "").split(".");
  let schema: unknown = IncrediConfigSchema;

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
    for (const rule of inventory.vscode_namespaces) {
      expect(existsSync(resolve(packageRoot, rule.runtime_consumer))).toBe(true);
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
      expect(schemaHasPath(entry.schema_path), entry.section).toBe(true);
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
