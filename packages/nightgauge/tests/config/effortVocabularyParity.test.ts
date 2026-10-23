/**
 * Effort vocabulary parity — the config schema must accept exactly what the
 * resolver accepts (#394).
 *
 * `ClaudeEffortSchema` used to re-list the ladder as a literal `z.enum` and had
 * fallen a level behind: it stopped at `xhigh` while the resolver (and the SDK's
 * `EFFORT_LEVELS`) already carried `max`. A config that set
 * `model_routing.default_effort: max` — a value the resolver reads and honours —
 * failed schema validation. These tests pin the two surfaces together for every
 * level, and pin that a non-member is rejected by both.
 *
 * @see Issue #394 - ClaudeEffortSchema is a third, drifted copy of the effort vocabulary
 */

import { describe, it, expect, afterEach } from "vitest";
import { EFFORT_LEVELS } from "@nightgauge/sdk";
import { ClaudeEffortSchema } from "../../src/config/schema";
import { getModelDefaultEffort, type ClaudeEffort } from "../../src/utils/resolvers/stageResolver";

const ENV_KEY = "NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT";
// A path that cannot hold a config, so the resolver answers from env alone.
const NO_CONFIG_ROOT = "/nonexistent-nightgauge-root-394";

/** What the resolver accepts, observed through its real env entry point. */
function resolverAccepts(value: string): boolean {
  const previous = process.env[ENV_KEY];
  process.env[ENV_KEY] = value;
  try {
    return getModelDefaultEffort(NO_CONFIG_ROOT) === value;
  } finally {
    if (previous === undefined) {
      delete process.env[ENV_KEY];
    } else {
      process.env[ENV_KEY] = previous;
    }
  }
}

afterEach(() => {
  delete process.env[ENV_KEY];
});

describe("ClaudeEffortSchema vocabulary", () => {
  it.each([...EFFORT_LEVELS])("accepts %s — the level the resolver accepts", (level) => {
    expect(ClaudeEffortSchema.safeParse(level).success).toBe(true);
    expect(resolverAccepts(level)).toBe(true);
  });

  it("enumerates exactly EFFORT_LEVELS, in order", () => {
    expect(ClaudeEffortSchema.options).toEqual([...EFFORT_LEVELS]);
  });

  it("rejects a non-member — and so does the resolver", () => {
    expect(ClaudeEffortSchema.safeParse("ultra").success).toBe(false);
    expect(resolverAccepts("ultra")).toBe(false);
  });

  it("infers the resolver's ClaudeEffort union (compile-time, mutually assignable)", () => {
    type Inferred = (typeof ClaudeEffortSchema)["_output"];
    const fromResolver: Inferred = "max" as ClaudeEffort;
    const fromSchema: ClaudeEffort = "max" as Inferred;
    expect(fromResolver).toBe("max");
    expect(fromSchema).toBe("max");
  });
});
