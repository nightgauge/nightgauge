/**
 * Cross-Model Skill Evaluation Harness — scenario + fixture loaders.
 *
 * Reads declarative scenario JSON files and mock fixtures from disk and
 * validates them against the Zod schemas. Parsing is delegated to an injectable
 * reader so the loaders are unit-testable without touching the filesystem.
 *
 * @see Issue #3814 - Build a cross-model skill evaluation harness
 */

import * as fs from "fs/promises";
import * as path from "path";
import { EvalScenarioSchema, PIPELINE_SKILLS, type EvalScenario } from "./schemas.js";
import type { MockFixture, MockFixtureMap } from "./modelRunner.js";

type PipelineSkill = (typeof PIPELINE_SKILLS)[number];

/** Default repo-relative roots for scenarios and fixtures. */
export const DEFAULT_SCENARIOS_DIR = "evals/scenarios";
export const DEFAULT_FIXTURES_DIR = "evals/fixtures";

/** Reader abstraction: list `*.json` files in a dir + read a file's text. */
export interface DirReader {
  listJson(dir: string): Promise<string[]>;
  readFile(filePath: string): Promise<string>;
}

export const defaultDirReader: DirReader = {
  async listJson(dir) {
    let entries: string[];
    try {
      entries = await fs.readdir(dir);
    } catch {
      return [];
    }
    return entries.filter((e) => e.endsWith(".json")).map((e) => path.join(dir, e));
  },
  readFile: (filePath) => fs.readFile(filePath, "utf-8"),
};

/** Parse + validate one scenario file's contents. Throws on schema failure. */
export function parseScenario(contents: string, sourcePath: string): EvalScenario {
  let raw: unknown;
  try {
    raw = JSON.parse(contents);
  } catch (err) {
    throw new Error(`invalid JSON in scenario ${sourcePath}: ${(err as Error).message}`, {
      cause: err,
    });
  }
  const result = EvalScenarioSchema.safeParse(raw);
  if (!result.success) {
    throw new Error(`invalid scenario ${sourcePath}: ${result.error.message}`);
  }
  return result.data;
}

/**
 * Load and validate all scenarios for the requested skills. When `skills` is
 * omitted, loads every pipeline skill. Scenario ids must be unique across the
 * whole load (the harness keys fixtures by id), so a duplicate id throws.
 */
export async function loadScenarios(
  options: {
    skills?: PipelineSkill[];
    scenariosDir?: string;
    reader?: DirReader;
  } = {}
): Promise<EvalScenario[]> {
  const skills = options.skills ?? [...PIPELINE_SKILLS];
  const baseDir = options.scenariosDir ?? DEFAULT_SCENARIOS_DIR;
  const reader = options.reader ?? defaultDirReader;

  const scenarios: EvalScenario[] = [];
  const seen = new Set<string>();

  for (const skill of skills) {
    const dir = path.join(baseDir, skill);
    const files = await reader.listJson(dir);
    for (const file of files.sort()) {
      const scenario = parseScenario(await reader.readFile(file), file);
      if (scenario.skill !== skill) {
        throw new Error(
          `scenario ${file} declares skill "${scenario.skill}" but lives under "${skill}/"`
        );
      }
      if (seen.has(scenario.id)) {
        throw new Error(`duplicate scenario id "${scenario.id}" (second occurrence in ${file})`);
      }
      seen.add(scenario.id);
      scenarios.push(scenario);
    }
  }

  return scenarios;
}

/**
 * Load mock fixtures for the requested skills into a `MockFixtureMap` keyed by
 * scenario id. Each fixture file is `<fixturesDir>/<skill>/<scenarioId>.json`
 * with a `{ haiku, sonnet, opus }` shape (any subset).
 */
export async function loadFixtures(
  options: {
    skills?: PipelineSkill[];
    fixturesDir?: string;
    reader?: DirReader;
  } = {}
): Promise<MockFixtureMap> {
  const skills = options.skills ?? [...PIPELINE_SKILLS];
  const baseDir = options.fixturesDir ?? DEFAULT_FIXTURES_DIR;
  const reader = options.reader ?? defaultDirReader;

  const map: MockFixtureMap = {};
  for (const skill of skills) {
    const dir = path.join(baseDir, skill);
    const files = await reader.listJson(dir);
    for (const file of files) {
      const scenarioId = path.basename(file, ".json");
      let raw: unknown;
      try {
        raw = JSON.parse(await reader.readFile(file));
      } catch (err) {
        throw new Error(`invalid JSON in fixture ${file}: ${(err as Error).message}`, {
          cause: err,
        });
      }
      map[scenarioId] = raw as Partial<Record<"haiku" | "sonnet" | "opus", MockFixture>>;
    }
  }
  return map;
}
