/**
 * Phase Inference — Deterministic phase progress from observable tool activity
 *
 * Some skills do not reliably emit `<!-- phase:start ... -->` markers. The
 * `feature-dev` stage in particular is edit-heavy (Read/Edit/Write dominate its
 * tool calls; Bash is rare), so the model routinely skips the standalone
 * `printf` phase-marker commands scattered through its SKILL.md. The result is
 * that the pipeline tree shows no phase progress for Feature Development even
 * though planning and validation render fine (Issue #3760).
 *
 * Hardening the marker parser or delivery path cannot fix this — a marker that
 * is never emitted cannot be delivered. Instead, this module infers phase
 * progress deterministically from the tool calls the agent actually makes,
 * which the orchestrator already observes in the stream. The inferred markers
 * are fed through the exact same `onPhaseStart` channel that real markers use,
 * so they render identically.
 *
 * Design guarantees:
 * - **Monotonic**: the inferred phase cursor only ever advances, never regresses
 *   (a Read during the implementation phase will not snap back to an early
 *   context phase).
 * - **Real markers win**: when a skill *does* emit a genuine marker, call
 *   `observeRealMarker(index)` so inference syncs its cursor forward and never
 *   duplicates or contradicts it.
 * - **Opt-in per stage**: only stages with a rule table (currently `feature-dev`)
 *   produce inferred markers. Stages that already emit reliably are untouched —
 *   their inference is a no-op.
 *
 * @see Issue #3760 - Feature Development stage shows no phases in pipeline tree
 */

import { PHASE_REGISTRY, type ExecutionStage, type ParsedPhaseMarker } from "./phaseRegistry.js";

/**
 * A rule that maps an observed tool call to a target phase index within a stage.
 */
interface PhaseInferenceRule {
  /** Target 0-based phase index within the stage (must exist in PHASE_REGISTRY). */
  index: number;
  /** Predicate over an observed tool call. Returns true when this phase is reached. */
  match: (toolName: string, toolInput: unknown) => boolean;
}

/** Safely read a string field from an unknown tool-input object. */
function inputString(input: unknown, key: string): string {
  if (input && typeof input === "object") {
    const v = (input as Record<string, unknown>)[key];
    if (typeof v === "string") return v;
  }
  return "";
}

const EDIT_TOOLS = new Set(["Edit", "Write", "MultiEdit", "NotebookEdit"]);
const READ_TOOLS = new Set(["Read", "Grep", "Glob"]);

/** Paths that represent pipeline bookkeeping rather than feature source code. */
function isPipelineArtifactPath(path: string): boolean {
  return /(^|\/)\.nightgauge\//.test(path);
}

/** The dev-context handoff file written near the end of feature-dev. */
function isDevContextPath(path: string): boolean {
  return /(^|\/)dev-\d+\.json$/.test(path) || /\.nightgauge\/pipeline\/dev-/.test(path);
}

/** Bash commands that signal the testing / build-verification phase. */
function isTestOrBuildCommand(cmd: string): boolean {
  return /\b(vitest|jest|go\s+test|go\s+build|npm\s+(run\s+)?(-w\s+\S+\s+)?(test|build)|pytest|cargo\s+test)\b/.test(
    cmd
  );
}

/** Bash commands that signal the project-status sync phase. */
function isStatusSyncCommand(cmd: string): boolean {
  return /\b(move-status|gh\s+project)\b/.test(cmd);
}

/** The plan file written mid-way through feature-planning (`.nightgauge/plans/{N}-*.md`). */
function isPlanFilePath(path: string): boolean {
  return /(^|\/)\.nightgauge\/plans\/.+\.md$/.test(path);
}

/** The planning-context handoff file written near the end of feature-planning. */
function isPlanningContextPath(path: string): boolean {
  return /(^|\/)planning-\d+\.json$/.test(path) || /\.nightgauge\/pipeline\/planning-/.test(path);
}

/**
 * Per-stage inference rules. Only stages that do NOT reliably self-report phase
 * markers need entries here. Rules are evaluated against each tool call; when
 * several match, the highest index strictly greater than the current cursor
 * wins (monotonic advancement). Indices must match PHASE_REGISTRY ordering.
 */
const STAGE_RULES: Partial<Record<ExecutionStage, PhaseInferenceRule[]>> = {
  // feature-dev (18 phases). Maps the durable, observable waypoints of an
  // edit-heavy implementation run to their canonical phase indices.
  "feature-dev": [
    // Context loading (covers phases 0-7, which run in seconds at the start).
    { index: 1, match: (name) => READ_TOOLS.has(name) },
    // First edit to real source code → implementation. Excludes pipeline
    // bookkeeping and the dev-context handoff file so those don't mis-fire here.
    {
      index: 8,
      match: (name, input) => {
        if (!EDIT_TOOLS.has(name)) return false;
        const path = inputString(input, "file_path") || inputString(input, "notebook_path");
        return !!path && !isPipelineArtifactPath(path) && !isDevContextPath(path);
      },
    },
    // Running the test/build suite → testing.
    {
      index: 9,
      match: (name, input) =>
        name === "Bash" && isTestOrBuildCommand(inputString(input, "command")),
    },
    // Writing the downstream dev context handoff → write-dev-context.
    {
      index: 14,
      match: (name, input) =>
        EDIT_TOOLS.has(name) && isDevContextPath(inputString(input, "file_path")),
    },
    // Syncing project board status → sync-project-status.
    {
      index: 15,
      match: (name, input) => name === "Bash" && isStatusSyncCommand(inputString(input, "command")),
    },
  ],
  // feature-planning (14 phases). Read-dominated: the agent spends most of its
  // time reading docs/standards/source before writing the plan and the
  // planning-context handoff. When planning runs on an edit-heavy model (e.g.
  // sonnet rather than haiku) it skips the standalone printf markers, so infer
  // from the durable observable waypoints. Real markers still win (#3771).
  "feature-planning": [
    // Reading docs/standards/source → documentation-analysis, where planning
    // spends the bulk of its time. Covers the fast early phases 0-6.
    { index: 6, match: (name) => READ_TOOLS.has(name) },
    // Writing the plan file (.nightgauge/plans/{N}-*.md) → produce-plan.
    {
      index: 9,
      match: (name, input) =>
        EDIT_TOOLS.has(name) && isPlanFilePath(inputString(input, "file_path")),
    },
    // Writing the planning-context handoff → write-planning-context.
    {
      index: 10,
      match: (name, input) =>
        EDIT_TOOLS.has(name) && isPlanningContextPath(inputString(input, "file_path")),
    },
  ],
};

/**
 * A stateful phase inferrer for a single stage run.
 */
export interface PhaseInference {
  /** True when this stage has inference rules (i.e. inference is active). */
  readonly enabled: boolean;
  /**
   * Emit the stage's initial phase. Call once when the stage starts so the tree
   * shows a live phase immediately. Returns the marker to emit, or null.
   */
  start(): ParsedPhaseMarker | null;
  /**
   * Observe a tool call. Returns a marker to emit when it advances the phase
   * past the current cursor, otherwise null.
   */
  observeToolUse(toolName: string, toolInput: unknown): ParsedPhaseMarker | null;
  /**
   * Sync the cursor when the skill emitted a genuine marker, so inferred
   * markers never regress or duplicate a real one. No marker is returned —
   * the real marker is delivered through the normal path.
   */
  observeRealMarker(index: number): void;
}

/**
 * Build a parsed marker for a given stage/index from the canonical registry.
 * Returns null if the index is out of range for the stage.
 */
function markerFor(stage: ExecutionStage, index: number): ParsedPhaseMarker | null {
  const phases = PHASE_REGISTRY[stage];
  if (!phases || index < 0 || index >= phases.length) return null;
  return { name: phases[index].name, index, total: phases.length, stage };
}

/**
 * Create a phase inferrer for a stage. For stages without rules, all methods are
 * no-ops (`enabled === false`), so reliably-self-reporting stages are unaffected.
 *
 * @param stage - The pipeline stage name
 * @returns A stateful {@link PhaseInference}
 */
export function createPhaseInference(stage: string): PhaseInference {
  const rules = STAGE_RULES[stage as ExecutionStage];
  const execStage = stage as ExecutionStage;
  const enabled = !!rules && !!PHASE_REGISTRY[execStage];

  // Highest phase index emitted/observed so far. -1 means nothing yet.
  let cursor = -1;

  function advanceTo(index: number): ParsedPhaseMarker | null {
    if (index <= cursor) return null;
    const marker = markerFor(execStage, index);
    if (!marker) return null;
    cursor = index;
    return marker;
  }

  return {
    enabled,

    start(): ParsedPhaseMarker | null {
      if (!enabled) return null;
      return advanceTo(0);
    },

    observeToolUse(toolName: string, toolInput: unknown): ParsedPhaseMarker | null {
      if (!enabled || !rules) return null;
      let best = -1;
      for (const rule of rules) {
        if (rule.index > best && rule.index > cursor && rule.match(toolName, toolInput)) {
          best = rule.index;
        }
      }
      if (best === -1) return null;
      return advanceTo(best);
    },

    observeRealMarker(index: number): void {
      if (!enabled) return;
      if (index > cursor) cursor = index;
    },
  };
}
