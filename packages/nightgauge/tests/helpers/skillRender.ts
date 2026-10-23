/**
 * Test stub for `nightgauge skill render` — the ONE composer (#78, ADR 016 §4).
 *
 * Since #79 the extension no longer reads or composes SKILL.md itself: it
 * passes `--skills-root` to the binary and consumes the JSON envelope. Tests
 * that drive a stage dispatch therefore need the render call answered, and a
 * shared stub keeps 9 test files from each inventing a different envelope
 * shape (which is how a mock drifts away from the contract it stands in for).
 *
 * Composition behavior itself is NOT tested here — it is tested against the
 * real implementation in internal/skillrender/render_test.go. What these tests
 * cover is the migration contract: the right arguments go out, and the
 * envelope that comes back is what reaches the prompt and the tool allowlist.
 */

/** Stage extracted from a `skill render` argv, or "" when absent. */
export function stageFromRenderArgs(args: readonly string[]): string {
  const i = args.indexOf("--stage");
  return i >= 0 ? (args[i + 1] ?? "") : "";
}

/** Model extracted from a `skill render` argv, or undefined when absent. */
export function modelFromRenderArgs(args: readonly string[]): string | undefined {
  const i = args.indexOf("--model");
  return i >= 0 ? args[i + 1] : undefined;
}

/** Every `--skills-root` value, in the order the host supplied them. */
export function rootsFromRenderArgs(args: readonly string[]): string[] {
  const roots: string[] = [];
  for (let i = 0; i < args.length; i++) {
    if (args[i] === "--skills-root" && args[i + 1]) roots.push(args[i + 1]);
  }
  return roots;
}

/** True when this argv is a `nightgauge skill render` invocation. */
export function isSkillRenderCall(args: readonly string[] | undefined): boolean {
  return !!args && args[0] === "skill" && args[1] === "render";
}

export interface SkillRenderStubOptions {
  content?: string;
  allowedTools?: string[];
  mcpTools?: string[];
  programmaticTools?: string[];
  resolvedKeys?: string[];
  /** Root the fake SKILL.md sits under. Defaults to the mocked workspace. */
  skillsRoot?: string;
}

/**
 * Build the stdout of `skill render --json --include-content` for an argv.
 *
 * The skill path is derived from the argv's stage so a test dispatching two
 * stages gets two distinct skill directories — the value the runner exports as
 * NIGHTGAUGE_SKILL_DIR and prints in the prompt's "Skill directory" line.
 */
export function skillRenderStdout(
  args: readonly string[],
  options: SkillRenderStubOptions = {}
): string {
  const stage = stageFromRenderArgs(args) || "feature-dev";
  const root = options.skillsRoot ?? rootsFromRenderArgs(args)[0] ?? "/test/workspace/skills";
  const skillPath = `${root}/nightgauge-${stage}/SKILL.md`;

  return JSON.stringify({
    v: 1,
    stage,
    skill_path: skillPath,
    skill_name: `nightgauge-${stage}`,
    content: options.content ?? `# ${stage}\n\nStub skill body.\n`,
    allowed_tools: options.allowedTools ?? [
      "Read",
      "Write",
      "Edit",
      "Glob",
      "Grep",
      "Bash",
      "Task",
    ],
    ...(options.mcpTools ? { mcp_tools: options.mcpTools } : {}),
    ...(options.programmaticTools ? { programmatic_tools: options.programmaticTools } : {}),
    resolved_keys: options.resolvedKeys ?? [],
    fragments: [],
    injection_site: "none",
    warnings: [],
  });
}
