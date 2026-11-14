/**
 * #3163 AC 0/1: the extension splices the same `partial_delivery` (and
 * `operator_steer`) block into planning-{N}.json that the Go scheduler does.
 *
 * Both suites read internal/orchestrator/testdata/partial-delivery-splice.golden.json:
 * Go asserts its writer produces `remainder` and its splice turns
 * `planning_in` into `planning_out`; this test asserts ContextAssembler turns
 * the same inputs into the same `planning_out`.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import type { Logger } from "../../../src/utils/logger";
import type { RepositoryContextLoader } from "../../../src/services/RepositoryContextLoader";
import { ContextAssembler } from "../../../src/orchestrator/context/ContextAssembler";

vi.mock("../../../src/utils/skillRunner", () => ({
  findSkillFile: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
}));

const GOLDEN = path.resolve(
  __dirname,
  "../../../../../internal/orchestrator/testdata/partial-delivery-splice.golden.json"
);

interface Golden {
  issue_number: number;
  remainder: unknown;
  feedback: unknown;
  planning_in: Record<string, unknown>;
  planning_out: Record<string, unknown>;
}

function makeLogger(): Logger {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as unknown as Logger;
}

let dir: string;

function assemblerIn(stateDir: string): ContextAssembler {
  const loader = {
    getContextFile: (type: string, n: number) => path.join(stateDir, `${type}-${n}.json`),
  } as unknown as RepositoryContextLoader;
  return new ContextAssembler(makeLogger(), () => stateDir, loader);
}

function write(name: string, value: unknown): void {
  fs.writeFileSync(path.join(dir, name), JSON.stringify(value, null, 2));
}

function readPlanning(n: number): Record<string, unknown> {
  return JSON.parse(fs.readFileSync(path.join(dir, `planning-${n}.json`), "utf-8"));
}

/** planning_in plus the fields the schema requires, which the fixture omits. */
function planningInput(g: Golden): Record<string, unknown> {
  return {
    ...g.planning_in,
    plan_file: "plans/1051.md",
    created_at: "2026-11-14T00:00:00Z",
  };
}

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "partial-splice-"));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

describe("ContextAssembler — partial_delivery splice parity with Go (#3163)", () => {
  const golden = JSON.parse(fs.readFileSync(GOLDEN, "utf-8")) as Golden;
  const n = golden.issue_number;

  it("produces the Go-spliced planning_out from the same inputs", async () => {
    write(`partial-delivery-${n}.json`, golden.remainder);
    write(`feedback-${n}.json`, golden.feedback);
    write(`planning-${n}.json`, planningInput(golden));

    await assemblerIn(dir).validateStageContextOutput("feature-planning", n);

    const got = readPlanning(n);
    expect(got.partial_delivery).toEqual(golden.planning_out.partial_delivery);
    expect(got.operator_steer).toEqual(golden.planning_out.operator_steer);
    // Every field Go's output carries, the extension's carries identically.
    for (const [key, value] of Object.entries(golden.planning_out)) {
      expect(got[key], key).toEqual(value);
    }
  });

  it("leaves planning untouched — no null — when no remainder or steer exists", async () => {
    write(`planning-${n}.json`, planningInput(golden));

    await assemblerIn(dir).validateStageContextOutput("feature-planning", n);

    const got = readPlanning(n);
    expect("partial_delivery" in got).toBe(false);
    expect("operator_steer" in got).toBe(false);
  });

  it("ignores an invalid remainder file instead of failing the stage", async () => {
    fs.writeFileSync(path.join(dir, `partial-delivery-${n}.json`), "{not json");
    write(`planning-${n}.json`, planningInput(golden));

    const result = await assemblerIn(dir).validateStageContextOutput("feature-planning", n);

    expect(result.error).toBeNull();
    expect("partial_delivery" in readPlanning(n)).toBe(false);
  });
});
