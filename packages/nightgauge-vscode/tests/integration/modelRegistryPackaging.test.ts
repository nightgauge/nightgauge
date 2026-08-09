/**
 * Regression guard: model-registry.json must be present in dist/ after build.
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
 * The build:assets step in package.json copies the JSON — this test guards
 * against it being removed. Mirrors failureTaxonomyPackaging.test.ts, the
 * precedent for build:assets-copied data files.
 */
import { describe, it, expect } from "vitest";
import { existsSync } from "node:fs";
import { resolve } from "node:path";

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
});
