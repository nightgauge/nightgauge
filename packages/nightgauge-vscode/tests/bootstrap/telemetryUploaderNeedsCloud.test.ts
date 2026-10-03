/**
 * telemetryUploaderNeedsCloud.test.ts
 *
 * The telemetry uploader posts the local run history, health snapshots,
 * recommendation outcomes and lifecycle traces to the hosted service, with a
 * license key or, failing one, the signed-in session's token. A signed-in
 * session is no consent to upload: nothing is sent unless the user enabled the
 * cloud (`platform.enabled`), so the uploader is constructed only then. See
 * docs/TELEMETRY_PRIVACY.md.
 *
 * The construction sits inline in the VS Code-dependent bootstrap, which is
 * impractical to instantiate here (as tests/bootstrap/
 * duplicateRunRecordWritersRemoved.test.ts documents), so this pins the guard
 * in the source.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const servicesSource = readFileSync(
  path.resolve(__dirname, "../../src/bootstrap/services.ts"),
  "utf-8"
);

describe("the telemetry uploader needs the cloud enabled", () => {
  it("is constructed once, and only inside a platformEnabled guard", () => {
    const constructions = servicesSource.split("new TelemetryUploaderService(").length - 1;
    expect(constructions).toBe(1);

    const at = servicesSource.indexOf("new TelemetryUploaderService(");
    const guardStart = servicesSource.lastIndexOf("if (", at);
    const guard = servicesSource.slice(guardStart, servicesSource.indexOf("{", guardStart));
    expect(guard).toMatch(/\bplatformEnabled\b/);
    // Nothing between the guard and the construction closes it.
    expect(servicesSource.slice(guardStart, at)).not.toMatch(/\n {2}\}/);
  });
});
