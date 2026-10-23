import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { EXECUTION_PROFILE_FIELDS } from "../../src/services/executionProfile";

/**
 * The execution profile is declared twice — Go's platform.ExecutionProfile,
 * which the binary resolves and the daemon sends, and the TypeScript
 * ExecutionProfile the extension copies onto its own registration and
 * heartbeat (#1567). toWireProfile copies only the TypeScript field list, so a
 * field added in Go alone would silently never leave the extension, and one
 * added here alone would make every profile fail to copy. This pins the two.
 */
function goProfileFields(): string[] {
  const goSource = readFileSync(
    join(__dirname, "..", "..", "..", "..", "internal", "platform", "execution_profile.go"),
    "utf8"
  );
  const start = goSource.indexOf("type ExecutionProfile struct {");
  expect(start, "type ExecutionProfile struct not found in execution_profile.go").toBeGreaterThan(
    -1
  );
  const body = goSource.slice(start, goSource.indexOf("\n}", start));
  return [...body.matchAll(/json:"([a-z_]+)[",]/g)].map((m) => m[1]);
}

describe("ExecutionProfile field set (#1567)", () => {
  it("matches Go's platform.ExecutionProfile json tags, in order", () => {
    const goFields = goProfileFields();
    expect(goFields.length).toBeGreaterThan(0);
    expect([...EXECUTION_PROFILE_FIELDS]).toEqual(goFields);
  });
});
