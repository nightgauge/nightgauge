#!/usr/bin/env tsx
/**
 * Validate Phase Markers — Lint skill phase markers against PHASE_REGISTRY
 *
 * Walks every `skills/**\/SKILL.md` file and extracts every
 * `<!-- phase:start name="X" index=N total=T stage="S" -->` marker. Validates:
 *
 *   1. Pipeline-stage markers (stage in PHASE_REGISTRY) match the registry
 *      exactly: every (name, index) tuple emitted by the skill must appear in
 *      the registry, every registry entry must be emitted by at least one
 *      marker, and every marker's `total=` must equal the registry length.
 *
 *   2. Non-pipeline-stage markers (stage not in PHASE_REGISTRY) are only
 *      permitted in skill files that carry the explicit opt-out annotation
 *      `<!-- phase-registry: standalone-skill -->`. Within those files the
 *      lint still requires `total=` to be consistent across all markers for
 *      the same stage.
 *
 *   3. Within a single SKILL.md, every phase name is emitted at most once
 *      (the same index/total may legitimately appear multiple times in
 *      explanatory blocks; the first occurrence wins).
 *
 * Exit codes:
 *   0 — clean
 *   1 — drift found; details printed to stderr
 *
 * Usage:
 *   npx tsx scripts/validate-phase-markers.ts
 */
import * as fs from "fs";
import * as path from "path";
import {
  PHASE_REGISTRY,
  type ExecutionStage,
} from "../packages/nightgauge-sdk/src/events/phaseRegistry.js";

const REPO_ROOT = path.resolve(__dirname, "..");
const SKILLS_DIR = path.join(REPO_ROOT, "skills");
const STANDALONE_ANNOTATION = "<!-- phase-registry: standalone-skill -->";

// Match phase markers with integer indices (decimal indices like 1.5 are
// conditional sub-phases and intentionally outside the registry contract).
const MARKER_RE =
  /phase:start name="([a-z][a-z0-9-]*)" index=(\d+) total=(\d+) stage="([a-z][a-z0-9-]*)"/g;

interface SkillMarker {
  name: string;
  index: number;
  total: number;
  stage: string;
  file: string;
  line: number;
}

const PIPELINE_STAGES = new Set<string>(Object.keys(PHASE_REGISTRY));

function findSkillFiles(): string[] {
  const out: string[] = [];
  if (!fs.existsSync(SKILLS_DIR)) return out;
  for (const entry of fs.readdirSync(SKILLS_DIR, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const skillPath = path.join(SKILLS_DIR, entry.name, "SKILL.md");
    if (fs.existsSync(skillPath)) out.push(skillPath);
  }
  return out.sort();
}

function extractMarkers(file: string, content: string): SkillMarker[] {
  const markers: SkillMarker[] = [];
  const lines = content.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    MARKER_RE.lastIndex = 0;
    let m: RegExpExecArray | null;
    while ((m = MARKER_RE.exec(line)) !== null) {
      const [, name, indexStr, totalStr, stage] = m;
      // Skip the literal placeholder template in skill bodies.
      if (name === "{phase-name}") continue;
      markers.push({
        name,
        index: parseInt(indexStr, 10),
        total: parseInt(totalStr, 10),
        stage,
        file: path.relative(REPO_ROOT, file),
        line: i + 1,
      });
    }
  }
  return markers;
}

function fmtFinding(file: string, line: number, msg: string): string {
  return `  ${file}:${line} — ${msg}`;
}

function main(): number {
  const files = findSkillFiles();
  if (files.length === 0) {
    console.error("ERROR: no SKILL.md files found under skills/");
    return 1;
  }

  // Per-file: dedupe markers by (stage, name) to first occurrence.
  // Per-stage (pipeline): collect unique markers from all skill files.
  // Per-stage (standalone): aggregate markers, scoped to files that emit them.
  const findings: string[] = [];
  const pipelineMarkersByStage = new Map<string, Map<string, SkillMarker>>();
  const standaloneMarkersByStage = new Map<string, SkillMarker[]>();

  for (const file of files) {
    const content = fs.readFileSync(file, "utf8");
    const isStandalone = content.includes(STANDALONE_ANNOTATION);
    const markers = extractMarkers(file, content);
    if (markers.length === 0) continue;

    // Within-file dedupe by (stage, name).
    const seen = new Set<string>();
    const fileMarkers: SkillMarker[] = [];
    for (const m of markers) {
      const key = `${m.stage}::${m.name}`;
      if (seen.has(key)) continue;
      seen.add(key);
      fileMarkers.push(m);
    }

    for (const m of fileMarkers) {
      if (PIPELINE_STAGES.has(m.stage)) {
        // Pipeline-stage marker — must match registry.
        if (!pipelineMarkersByStage.has(m.stage)) {
          pipelineMarkersByStage.set(m.stage, new Map());
        }
        const stageMap = pipelineMarkersByStage.get(m.stage)!;
        const existing = stageMap.get(m.name);
        if (existing) {
          findings.push(
            fmtFinding(
              m.file,
              m.line,
              `duplicate emit of phase "${m.name}" in stage "${m.stage}" — also emitted at ${existing.file}:${existing.line}. ` +
                `Pipeline phases must be emitted by exactly one skill file.`
            )
          );
          continue;
        }
        stageMap.set(m.name, m);
      } else {
        // Non-pipeline-stage marker — opt-out annotation required.
        if (!isStandalone) {
          findings.push(
            fmtFinding(
              m.file,
              m.line,
              `phase marker uses non-pipeline stage "${m.stage}" but the skill is missing the ` +
                `\`${STANDALONE_ANNOTATION}\` annotation. ` +
                `Add the annotation near the top of the file to opt out of registry validation, or ` +
                `register the stage in packages/nightgauge-sdk/src/events/phaseRegistry.ts.`
            )
          );
          continue;
        }
        if (!standaloneMarkersByStage.has(m.stage)) {
          standaloneMarkersByStage.set(m.stage, []);
        }
        standaloneMarkersByStage.get(m.stage)!.push(m);
      }
    }
  }

  // Validate pipeline-stage markers against the registry.
  for (const stageKey of Object.keys(PHASE_REGISTRY)) {
    const stage = stageKey as ExecutionStage;
    const registryPhases = PHASE_REGISTRY[stage];
    const skillMap = pipelineMarkersByStage.get(stage) ?? new Map<string, SkillMarker>();
    const registryNames = new Set(registryPhases.map((p) => p.name));
    const skillNames = new Set(skillMap.keys());

    // Every registry entry must have at least one matching emit.
    for (const phase of registryPhases) {
      if (!skillNames.has(phase.name)) {
        findings.push(
          `  PHASE_REGISTRY.${stage}["${phase.name}"] (index ${phase.index}) has no matching ` +
            `\`phase:start\` marker in any skill file. Add the marker to the corresponding ` +
            `skill, or remove the registry entry.`
        );
      }
    }

    // Every emitted marker must be in the registry, with matching index/total.
    for (const [name, m] of skillMap) {
      if (!registryNames.has(name)) {
        findings.push(
          fmtFinding(
            m.file,
            m.line,
            `phase "${name}" (stage "${stage}") is emitted by the skill but not present in ` +
              `PHASE_REGISTRY. Add it to packages/nightgauge-sdk/src/events/phaseRegistry.ts ` +
              `or remove the marker.`
          )
        );
        continue;
      }
      const registryPhase = registryPhases.find((p) => p.name === name)!;
      if (registryPhase.index !== m.index) {
        findings.push(
          fmtFinding(
            m.file,
            m.line,
            `phase "${name}" (stage "${stage}") has index=${m.index} in skill but index=${registryPhase.index} in PHASE_REGISTRY.`
          )
        );
      }
      if (m.total !== registryPhases.length) {
        findings.push(
          fmtFinding(
            m.file,
            m.line,
            `phase "${name}" (stage "${stage}") has total=${m.total} in skill but PHASE_REGISTRY ` +
              `has ${registryPhases.length} phases. Update every \`total=\` value in the skill to ` +
              `match the registry.`
          )
        );
      }
    }
  }

  // Validate standalone-stage marker consistency.
  for (const [stage, markers] of standaloneMarkersByStage) {
    const totals = new Set(markers.map((m) => m.total));
    if (totals.size > 1) {
      const reported = markers.map((m) => `${m.file}:${m.line} (total=${m.total})`).join(", ");
      findings.push(
        `  Standalone stage "${stage}" has inconsistent \`total=\` values across markers: ${reported}. ` +
          `All markers for the same stage must declare the same total.`
      );
    }
  }

  if (findings.length === 0) {
    const pipelineEmitted = Array.from(pipelineMarkersByStage.values()).reduce(
      (sum, m) => sum + m.size,
      0
    );
    const standaloneStages = standaloneMarkersByStage.size;
    console.log(
      `Validated ${files.length} SKILL.md files: ${pipelineEmitted} pipeline-stage phases match registry, ` +
        `${standaloneStages} standalone stage(s) opted out.`
    );
    return 0;
  }

  console.error("✗ Phase marker drift detected:");
  for (const f of findings) console.error(f);
  console.error("");
  console.error(
    `${findings.length} finding(s). Update PHASE_REGISTRY in ` +
      `packages/nightgauge-sdk/src/events/phaseRegistry.ts and the relevant ` +
      `SKILL.md files until both sides agree.`
  );
  return 1;
}

process.exit(main());
