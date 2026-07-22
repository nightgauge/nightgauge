/**
 * Regression guard: failure-taxonomy.yaml must be present in dist/ after build.
 *
 * FailurePatternDetector resolves its taxonomy path as:
 *   resolve(__dirname, 'failure-taxonomy.yaml')
 * When esbuild bundles the extension, __dirname becomes the extension's dist/
 * directory. If the yaml is absent there, every self-check logs an ENOENT error
 * and reports 0 detected failure patterns.
 *
 * The build:assets step in package.json copies the yaml — this test guards
 * against it being removed. See issue #1268.
 */
import { describe, it, expect } from "vitest";
import { existsSync } from "node:fs";
import { resolve } from "node:path";

const DIST_YAML = resolve(__dirname, "../../dist/failure-taxonomy.yaml");
const SDK_SOURCE_YAML = resolve(
  __dirname,
  "../../../nightgauge-sdk/src/analysis/failure-taxonomy.yaml"
);

describe("failure-taxonomy.yaml packaging", () => {
  it("SDK source yaml exists (prerequisite for build:assets)", () => {
    expect(existsSync(SDK_SOURCE_YAML), `SDK source yaml not found at ${SDK_SOURCE_YAML}`).toBe(
      true
    );
  });

  it("dist/failure-taxonomy.yaml is present after build", () => {
    expect(
      existsSync(DIST_YAML),
      `dist/failure-taxonomy.yaml not found — run "npm run build" first. ` +
        `The build:assets step in package.json must copy it from the SDK source.`
    ).toBe(true);
  });
});
