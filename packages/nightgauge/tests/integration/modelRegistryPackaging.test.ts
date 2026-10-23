/**
 * Regression guard: model-registry.json must be present in dist/ after build —
 * and it must be the CURRENT registry, parseable by the CURRENT schema (#436).
 *
 * The SDK's registry loader resolves its data file as:
 *   readFileSync(resolve(__dirname, 'model-registry.json'))
 * When esbuild bundles the extension, __dirname becomes the extension's dist/
 * directory. If the JSON is absent there, `loadRegistry()` throws at module
 * load and every registry consumer in the extension goes down with it.
 *
 * That blast radius grew with #336: the `--effort` emission gate and the effort
 * LEVEL check both read `supported_efforts` from this file, so it is now on the
 * dispatch path rather than only in eval tooling.
 *
 * Existence alone stopped being enough with #578 (found by #436's review):
 * `ModelDescriptorSchema` is `.strict()` and `RegistryFileSchema.parse` runs at
 * module load, so a STALE dist copy — built by build:assets before a schema
 * change — is a load-time crash in the extension, not a red test. Two guards
 * close that:
 *
 *  1. the dist copy must deep-equal the SDK source (a stale build:assets
 *     output fails here first, whichever direction the skew runs);
 *  2. the dist copy must parse under the exact strict schema the loader uses
 *     (the crash-at-import class, demonstrated as a test failure instead).
 *
 * The build:assets step in package.json copies the JSON — this test guards
 * against it being removed or left stale. Mirrors
 * failureTaxonomyPackaging.test.ts, the precedent for build:assets-copied data
 * files.
 */
import { describe, it, expect } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { RegistryFileSchema } from "@nightgauge/sdk";

const DIST_JSON = resolve(__dirname, "../../dist/model-registry.json");
const SDK_SOURCE_JSON = resolve(__dirname, "../../../nightgauge-sdk/src/eval/model-registry.json");

describe("model-registry.json packaging", () => {
  it("SDK source registry exists (prerequisite for build:assets)", () => {
    expect(existsSync(SDK_SOURCE_JSON), `SDK source registry not found at ${SDK_SOURCE_JSON}`).toBe(
      true
    );
  });

  it("dist/model-registry.json is present after build", () => {
    expect(
      existsSync(DIST_JSON),
      `dist/model-registry.json not found — run "npm run build" first. ` +
        `The build:assets step in package.json must copy it from the SDK source.`
    ).toBe(true);
  });

  it("dist copy deep-equals the SDK source — a stale build:assets output is a red test (#436)", () => {
    const dist: unknown = JSON.parse(readFileSync(DIST_JSON, "utf-8"));
    const source: unknown = JSON.parse(readFileSync(SDK_SOURCE_JSON, "utf-8"));
    // Deep equality on parsed JSON (not bytes) so formatting can never fail
    // this, only data drift can.
    expect(
      dist,
      `dist/model-registry.json has drifted from the SDK source — rerun "npm run build" ` +
        `(build:assets recopies it). A stale copy plus the strict registry schema is a ` +
        `load-time crash in the extension, which is exactly why this is a test instead.`
    ).toEqual(source);
  });

  it("dist copy parses under the packaged strict schema — the load-time-crash class as a test (#436)", () => {
    // RegistryFileSchema is the EXACT schema `loadRegistry()` runs at module
    // import (strict descriptors, required effort_levels). If this throws,
    // the bundled extension would have thrown at activation instead.
    const dist: unknown = JSON.parse(readFileSync(DIST_JSON, "utf-8"));
    expect(() => RegistryFileSchema.parse(dist)).not.toThrow();
  });
});
