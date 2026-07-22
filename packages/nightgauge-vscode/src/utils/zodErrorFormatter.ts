/**
 * Zod Error Formatter - Convert ZodError objects to human-readable corrective
 * instructions for agent prompts during context schema repair.
 *
 * Compatible with Zod v4 issue types ($ZodIssue).
 *
 * @see Issue #2552 - Pipeline context schema self-correction
 */

import type { ZodError } from "zod";

/**
 * Zod v4 issue shape — uses `code` discriminant with per-code properties.
 * We use a minimal type alias here to avoid tight coupling to Zod's internal
 * `$ZodIssue` prefixed types while matching the actual runtime structure.
 */
interface ZodIssueBase {
  readonly code?: string;
  readonly input?: unknown;
  readonly path: PropertyKey[];
  readonly message: string;
  [key: string]: unknown;
}

/**
 * Structured representation of a single Zod validation error.
 */
export interface FormattedZodError {
  /** Dot-separated path to the invalid field (e.g., "files_changed.created") */
  path: string;
  /** Severity level — errors block repair, warnings are informational */
  severity: "error" | "warning";
  /** Human-readable description of what went wrong */
  message: string;
  /** Suggested corrective action (when determinable) */
  suggestion?: string;
}

/**
 * Format a ZodError into structured error objects for programmatic use.
 *
 * @param zodError - The ZodError from schema.safeParse()
 * @returns Array of structured error objects
 */
export function formatZodErrors(zodError: ZodError): FormattedZodError[] {
  return zodError.issues.map((issue) => {
    const iss = issue as unknown as ZodIssueBase;
    const pathStr = iss.path.length > 0 ? iss.path.map(String).join(".") : "(root)";
    return {
      path: pathStr,
      severity: "error" as const,
      message: buildIssueMessage(iss),
      suggestion: buildIssueSuggestion(iss),
    };
  });
}

/**
 * Format a ZodError into a human-readable prompt string for agent repair.
 *
 * The output is designed to be appended to a SKILL.md prompt so the agent
 * can correct its JSON output on retry.
 *
 * @param zodError - The ZodError from schema.safeParse()
 * @returns Multi-line string with corrective instructions
 */
export function formatZodErrorsForPrompt(zodError: ZodError): string {
  if (zodError.issues.length === 0) {
    return "No schema violations detected.";
  }

  const lines = [
    "Your previous output had schema validation errors in the JSON context file.",
    "Please fix the following fields in your JSON output:\n",
  ];

  for (const issue of zodError.issues) {
    const iss = issue as unknown as ZodIssueBase;
    const pathStr = iss.path.length > 0 ? iss.path.map(String).join(".") : "(root)";
    const message = buildIssueMessage(iss);
    const suggestion = buildIssueSuggestion(iss);

    let line = `- ${pathStr}: ${message}`;
    if (suggestion) {
      line += ` → ${suggestion}`;
    }
    lines.push(line);
  }

  lines.push(
    "",
    "Ensure your JSON output strictly matches the expected schema.",
    "Do NOT change any other behavior — only fix the JSON field names, types, and values listed above."
  );

  return lines.join("\n");
}

/**
 * Build a human-readable message for a single Zod issue.
 *
 * Uses runtime property checks to support Zod v4's issue union types
 * without coupling to internal $Zod prefixed types.
 */
function buildIssueMessage(issue: ZodIssueBase): string {
  const code = issue.code;

  if (code === "invalid_type") {
    // Zod v4: { code: "invalid_type", expected: string, input?: unknown }
    const expected = issue.expected as string;
    const inputType = issue.input === undefined ? "undefined" : typeof issue.input;
    return `Expected ${expected}, received ${inputType}`;
  }

  if (code === "unrecognized_keys" && "keys" in issue) {
    return `Unrecognized field(s): ${(issue.keys as string[]).join(", ")}`;
  }

  if (code === "invalid_value" && "values" in issue) {
    // Zod v4: replaces Zod v3's invalid_enum_value and invalid_literal
    const values = issue.values as unknown[];
    const inputStr = issue.input !== undefined ? JSON.stringify(issue.input) : "undefined";
    return `Invalid value ${inputStr}; expected one of: ${values.map(String).join(", ")}`;
  }

  if (code === "too_small" && "minimum" in issue) {
    const origin = (issue.origin as string) ?? "value";
    return `Value too small (minimum: ${String(issue.minimum)}, type: ${origin})`;
  }

  if (code === "too_big" && "maximum" in issue) {
    const origin = (issue.origin as string) ?? "value";
    return `Value too large (maximum: ${String(issue.maximum)}, type: ${origin})`;
  }

  // Fallback: use Zod's built-in message
  return issue.message;
}

/**
 * Build a corrective suggestion for a Zod issue (when determinable).
 */
function buildIssueSuggestion(issue: ZodIssueBase): string | undefined {
  const code = issue.code;

  if (code === "invalid_type") {
    const expected = issue.expected as string;
    if (issue.input === undefined) {
      return `Add this required field with a ${expected} value`;
    }
    return `Change the value to type ${expected}`;
  }

  if (code === "unrecognized_keys") {
    return "Remove these fields or rename to match the expected schema";
  }

  if (code === "invalid_value" && "values" in issue) {
    const values = issue.values as unknown[];
    return `Use one of: ${values.map((v) => `"${String(v)}"`).join(", ")}`;
  }

  return undefined;
}
