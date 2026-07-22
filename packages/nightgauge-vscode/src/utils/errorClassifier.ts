/**
 * Error Classifier - Deterministic parsing and classification of build/test errors
 *
 * This module parses build and test output to extract structured error information
 * that can be used by the Ralph Loop to generate targeted fixes.
 *
 * The classification is DETERMINISTIC (regex patterns), not AI-driven.
 * This ensures predictable behavior and zero LLM token consumption.
 *
 * @see docs/RALPH_LOOP.md - Complete documentation
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 * @see Issue #83 - Ralph Wiggum Loop integration
 */

/**
 * Error type detected from output
 */
export type ErrorType = "build" | "test" | "lint" | "typecheck" | "runtime" | "unknown";

/**
 * Error severity determines handling strategy
 *
 * - fixable: Ralph Loop will attempt auto-fix
 * - architectural: Requires design changes, escalate to human
 * - configuration: Environment/dependency issue, escalate to human
 * - unknown: Cannot determine, escalate to human
 */
export type ErrorSeverity = "fixable" | "architectural" | "configuration" | "unknown";

/**
 * Structured error extracted from build/test output
 */
export interface ClassifiedError {
  /** Error type (build, test, lint, etc.) */
  type: ErrorType;

  /** Severity determines handling */
  severity: ErrorSeverity;

  /** File where error occurred (if detected) */
  file?: string;

  /** Line number (if detected) */
  line?: number;

  /** Column number (if detected) */
  column?: number;

  /** Error message (cleaned and normalized) */
  message: string;

  /** Error code (e.g., TS2345, ESLint rule name) */
  code?: string;

  /** Raw output that produced this error (for context) */
  rawOutput: string;

  /** Suggested fix category (for AI prompt guidance) */
  fixCategory?: "syntax" | "import" | "type" | "logic" | "test_assertion" | "other";
}

/**
 * TypeScript/JavaScript error patterns
 */
const TS_ERROR_PATTERNS = [
  // TypeScript compiler errors: src/file.ts(10,5): error TS2345: ...
  /^(?<file>[^\s(]+)\((?<line>\d+),(?<column>\d+)\):\s*error\s+(?<code>TS\d+):\s*(?<message>.+)$/m,
  // TypeScript compiler errors: src/file.ts:10:5 - error TS2345: ...
  /^(?<file>[^\s:]+):(?<line>\d+):(?<column>\d+)\s*-\s*error\s+(?<code>TS\d+):\s*(?<message>.+)$/m,
  // ESBuild/Vite errors: X [ERROR] Could not resolve "module"
  /^\s*X\s*\[ERROR\]\s*(?<message>.+)$/m,
];

/**
 * ESLint error patterns
 */
const ESLINT_ERROR_PATTERNS = [
  // ESLint: /path/file.ts:10:5: error 'rule-name': message
  /^(?<file>[^\s:]+):(?<line>\d+):(?<column>\d+):\s*error\s+'?(?<code>[^':]+)'?:\s*(?<message>.+)$/m,
  // ESLint (compact): file.ts(10,5): error message [rule-name]
  /^(?<file>[^\s(]+)\((?<line>\d+),(?<column>\d+)\):\s*error\s*(?<message>.+)\s*\[(?<code>[^\]]+)\]$/m,
];

/**
 * Jest/Vitest test failure patterns
 */
const TEST_ERROR_PATTERNS = [
  // Jest/Vitest: FAIL src/file.test.ts
  /^FAIL\s+(?<file>.+\.(?:test|spec)\.[jt]sx?)$/m,
  // Expect patterns: expect(received).toBe(expected)
  /expect\((?<received>.+)\)\.(?<matcher>toBe|toEqual|toContain|toMatch)\((?<expected>.+)\)/,
  // Test name pattern: ✕ should do something
  /^\s*[✕✗×]\s+(?<testName>.+)$/m,
  // Error with stack: Error: message
  /^\s+Error:\s*(?<message>.+)$/m,
  // At pattern: at Object.<anonymous> (file.ts:10:5)
  /at\s+.+\s+\((?<file>[^:]+):(?<line>\d+):(?<column>\d+)\)/,
];

/**
 * Python test (pytest) patterns
 */
const PYTEST_ERROR_PATTERNS = [
  // pytest: FAILED tests/test_file.py::test_name
  /^FAILED\s+(?<file>[^\s:]+)::(?<testName>\w+)/m,
  // AssertionError
  /AssertionError:\s*(?<message>.+)/,
  // File location: file.py:10: in test_name
  /(?<file>[^\s:]+):(?<line>\d+):\s+in\s+(?<context>\w+)/,
];

/**
 * Go test patterns
 */
const GO_TEST_PATTERNS = [
  // Go: --- FAIL: TestName (0.00s)
  /^---\s*FAIL:\s*(?<testName>\w+)\s*\(/m,
  // Go: file.go:10:5: error message
  /^(?<file>[^\s:]+\.go):(?<line>\d+):(?<column>\d+):\s*(?<message>.+)$/m,
];

/**
 * Configuration/environment error patterns (should NOT auto-fix)
 */
const CONFIG_ERROR_PATTERNS = [
  /Module not found|Cannot find module|npm ERR!/i,
  /ENOENT|EACCES|EPERM|ENOMEM/,
  /Permission denied/i,
  /Out of memory/i,
  /Segmentation fault/i,
  /npm ERR! code ERESOLVE/,
  /ModuleNotFoundError/,
  /pip._vendor/,
  /go: module .+ not found/,
];

/**
 * Architectural error patterns (should NOT auto-fix)
 */
const ARCHITECTURAL_ERROR_PATTERNS = [
  /circular dependency/i,
  /dependency cycle/i,
  /maximum call stack/i,
  /stack overflow/i,
  /infinite loop detected/i,
  /type .+ is not assignable to type .+ \(.*complex\)/i,
];

/**
 * Classify a single error from output
 *
 * @param rawOutput - Raw build/test output
 * @param errorType - Hint about the error type
 * @returns Classified error or null if no error found
 */
export function classifyError(
  rawOutput: string,
  errorType: ErrorType = "unknown"
): ClassifiedError | null {
  if (!rawOutput.trim()) {
    return null;
  }

  // Check for configuration errors first (escalate to human)
  for (const pattern of CONFIG_ERROR_PATTERNS) {
    if (pattern.test(rawOutput)) {
      return {
        type: errorType === "unknown" ? "runtime" : errorType,
        severity: "configuration",
        message: extractFirstErrorMessage(rawOutput),
        rawOutput: truncateOutput(rawOutput),
      };
    }
  }

  // Check for architectural errors (escalate to human)
  for (const pattern of ARCHITECTURAL_ERROR_PATTERNS) {
    if (pattern.test(rawOutput)) {
      return {
        type: errorType,
        severity: "architectural",
        message: extractFirstErrorMessage(rawOutput),
        rawOutput: truncateOutput(rawOutput),
      };
    }
  }

  // Try to extract structured error info based on type
  let error: ClassifiedError | null;

  switch (errorType) {
    case "build":
    case "typecheck":
      error = extractTypeScriptError(rawOutput);
      break;
    case "lint":
      error = extractESLintError(rawOutput);
      break;
    case "test":
      error = extractTestError(rawOutput);
      break;
    default:
      // Try all patterns
      error =
        extractTypeScriptError(rawOutput) ??
        extractESLintError(rawOutput) ??
        extractTestError(rawOutput);
  }

  // If we found an error, mark it as fixable
  if (error) {
    error.severity = "fixable";
    error.fixCategory = categorizeForFix(error);
    return error;
  }

  // Generic error - could still be fixable
  if (hasErrorIndicators(rawOutput)) {
    return {
      type: errorType === "unknown" ? detectErrorType(rawOutput) : errorType,
      severity: "fixable",
      message: extractFirstErrorMessage(rawOutput),
      rawOutput: truncateOutput(rawOutput),
      fixCategory: "other",
    };
  }

  return null;
}

/**
 * Classify multiple errors from output
 *
 * @param rawOutput - Raw build/test output
 * @param errorType - Hint about the error type
 * @param maxErrors - Maximum errors to return (default: 5)
 * @returns Array of classified errors
 */
export function classifyErrors(
  rawOutput: string,
  errorType: ErrorType = "unknown",
  maxErrors: number = 5
): ClassifiedError[] {
  const errors: ClassifiedError[] = [];
  const lines = rawOutput.split("\n");
  let currentChunk = "";

  for (const line of lines) {
    // Check if this line starts a new error
    if (isErrorStartLine(line) && currentChunk) {
      const error = classifyError(currentChunk, errorType);
      if (error && errors.length < maxErrors) {
        errors.push(error);
      }
      currentChunk = line;
    } else {
      currentChunk += "\n" + line;
    }
  }

  // Process last chunk
  if (currentChunk) {
    const error = classifyError(currentChunk, errorType);
    if (error && errors.length < maxErrors) {
      errors.push(error);
    }
  }

  // If no structured errors found, try single classification
  if (errors.length === 0) {
    const error = classifyError(rawOutput, errorType);
    if (error) {
      errors.push(error);
    }
  }

  return errors;
}

/**
 * Extract TypeScript/JavaScript error from output
 */
function extractTypeScriptError(rawOutput: string): ClassifiedError | null {
  for (const pattern of TS_ERROR_PATTERNS) {
    const match = pattern.exec(rawOutput);
    if (match?.groups) {
      return {
        type: "build",
        severity: "fixable",
        file: match.groups.file,
        line: match.groups.line ? parseInt(match.groups.line, 10) : undefined,
        column: match.groups.column ? parseInt(match.groups.column, 10) : undefined,
        message: match.groups.message ?? "TypeScript error",
        code: match.groups.code,
        rawOutput: truncateOutput(rawOutput),
      };
    }
  }
  return null;
}

/**
 * Extract ESLint error from output
 */
function extractESLintError(rawOutput: string): ClassifiedError | null {
  for (const pattern of ESLINT_ERROR_PATTERNS) {
    const match = pattern.exec(rawOutput);
    if (match?.groups) {
      return {
        type: "lint",
        severity: "fixable",
        file: match.groups.file,
        line: match.groups.line ? parseInt(match.groups.line, 10) : undefined,
        column: match.groups.column ? parseInt(match.groups.column, 10) : undefined,
        message: match.groups.message ?? "ESLint error",
        code: match.groups.code,
        rawOutput: truncateOutput(rawOutput),
      };
    }
  }
  return null;
}

/**
 * Extract test failure from output
 */
function extractTestError(rawOutput: string): ClassifiedError | null {
  // Try Jest/Vitest patterns
  for (const pattern of TEST_ERROR_PATTERNS) {
    const match = pattern.exec(rawOutput);
    if (match?.groups) {
      return {
        type: "test",
        severity: "fixable",
        file: match.groups.file,
        line: match.groups.line ? parseInt(match.groups.line, 10) : undefined,
        column: match.groups.column ? parseInt(match.groups.column, 10) : undefined,
        message: match.groups.message ?? match.groups.testName ?? "Test assertion failed",
        rawOutput: truncateOutput(rawOutput),
      };
    }
  }

  // Try pytest patterns
  for (const pattern of PYTEST_ERROR_PATTERNS) {
    const match = pattern.exec(rawOutput);
    if (match?.groups) {
      return {
        type: "test",
        severity: "fixable",
        file: match.groups.file,
        line: match.groups.line ? parseInt(match.groups.line, 10) : undefined,
        message: match.groups.message ?? match.groups.testName ?? "Test failed",
        rawOutput: truncateOutput(rawOutput),
      };
    }
  }

  // Try Go test patterns
  for (const pattern of GO_TEST_PATTERNS) {
    const match = pattern.exec(rawOutput);
    if (match?.groups) {
      return {
        type: "test",
        severity: "fixable",
        file: match.groups.file,
        line: match.groups.line ? parseInt(match.groups.line, 10) : undefined,
        column: match.groups.column ? parseInt(match.groups.column, 10) : undefined,
        message: match.groups.message ?? match.groups.testName ?? "Test failed",
        rawOutput: truncateOutput(rawOutput),
      };
    }
  }

  return null;
}

/**
 * Detect error type from generic output
 */
function detectErrorType(rawOutput: string): ErrorType {
  const lower = rawOutput.toLowerCase();

  if (/typescript|tsc|type error|ts\d{4}/i.test(lower) || /compile|build.*failed/i.test(lower)) {
    return "build";
  }

  if (/test.*fail|fail.*test|jest|vitest|pytest|go test/i.test(lower)) {
    return "test";
  }

  if (/eslint|lint|prettier/i.test(lower)) {
    return "lint";
  }

  if (/typecheck|type-check|type check/i.test(lower)) {
    return "typecheck";
  }

  return "unknown";
}

/**
 * Check if output contains error indicators
 */
function hasErrorIndicators(rawOutput: string): boolean {
  return (
    /error|fail|exception|fatal/i.test(rawOutput) && !/0 error|0 fail|no error/i.test(rawOutput)
  );
}

/**
 * Check if a line starts a new error
 */
function isErrorStartLine(line: string): boolean {
  return (
    /^(FAIL|ERROR|✕|✗|×|---\s*FAIL:)/i.test(line.trim()) ||
    /^\s*\d+\)\s/.test(line) || // Numbered test failure
    /^[^\s:]+\.[jt]sx?:\d+/.test(line) // File:line pattern
  );
}

/**
 * Extract the first error message from output
 */
function extractFirstErrorMessage(rawOutput: string): string {
  // Try to find a line with "error:" or "Error:"
  const errorMatch = /(?:error|Error):\s*(.+)/m.exec(rawOutput);
  if (errorMatch) {
    return errorMatch[1].trim();
  }

  // Try to find assertion failures
  const assertMatch = /(?:AssertionError|expect|assert).*:\s*(.+)/m.exec(rawOutput);
  if (assertMatch) {
    return assertMatch[1].trim();
  }

  // Fall back to first non-empty line that looks like an error
  const lines = rawOutput.split("\n");
  for (const line of lines) {
    const trimmed = line.trim();
    if (trimmed && /error|fail|exception/i.test(trimmed)) {
      return trimmed.slice(0, 200);
    }
  }

  return "Unknown error";
}

/**
 * Truncate output to reasonable size for context
 */
function truncateOutput(rawOutput: string, maxLength: number = 500): string {
  if (rawOutput.length <= maxLength) {
    return rawOutput;
  }
  return rawOutput.slice(0, maxLength) + "... [truncated]";
}

/**
 * Categorize error for fix guidance
 */
function categorizeForFix(error: ClassifiedError): ClassifiedError["fixCategory"] {
  const msg = error.message.toLowerCase();
  const code = error.code?.toLowerCase() ?? "";

  // Syntax errors
  if (/syntax|unexpected token|parsing|semicolon/i.test(msg)) {
    return "syntax";
  }

  // Import/module errors
  if (
    /cannot find|not found|module|import|export|require/i.test(msg) ||
    code.includes("ts2307") ||
    code.includes("ts2305")
  ) {
    return "import";
  }

  // Type errors
  if (
    /type|assignable|argument.*is not|property.*does not exist/i.test(msg) ||
    code.startsWith("ts2")
  ) {
    return "type";
  }

  // Test assertion errors
  if (error.type === "test" || /expect|assert|toBe|toEqual/i.test(msg)) {
    return "test_assertion";
  }

  // Logic errors
  if (/undefined|null|NaN|Infinity|reference/i.test(msg)) {
    return "logic";
  }

  return "other";
}

/**
 * Format classified error for logging/display
 *
 * @param error - The classified error to format
 * @returns Human-readable error description
 */
export function formatClassifiedError(error: ClassifiedError): string {
  const lines: string[] = [];

  // Header with type and severity
  lines.push(`[${error.type.toUpperCase()}] ${error.severity}`);

  // Location if available
  if (error.file) {
    let location = error.file;
    if (error.line !== undefined) {
      location += `:${error.line}`;
      if (error.column !== undefined) {
        location += `:${error.column}`;
      }
    }
    lines.push(`  Location: ${location}`);
  }

  // Error code if available
  if (error.code) {
    lines.push(`  Code: ${error.code}`);
  }

  // Message
  lines.push(`  Message: ${error.message}`);

  // Fix category if available
  if (error.fixCategory) {
    lines.push(`  Fix category: ${error.fixCategory}`);
  }

  return lines.join("\n");
}

/**
 * Create a prompt context for AI fix generation
 *
 * @param error - The classified error to fix
 * @returns Structured context for AI prompt
 */
export function createFixPromptContext(error: ClassifiedError): string {
  const lines: string[] = [];

  lines.push("## Error to Fix");
  lines.push("");

  if (error.file) {
    lines.push(`**File**: ${error.file}`);
    if (error.line !== undefined) {
      lines.push(`**Line**: ${error.line}`);
    }
  }

  if (error.code) {
    lines.push(`**Error Code**: ${error.code}`);
  }

  lines.push(`**Type**: ${error.type}`);
  lines.push(`**Message**: ${error.message}`);
  lines.push("");

  if (error.fixCategory) {
    lines.push("## Fix Guidance");
    lines.push("");

    switch (error.fixCategory) {
      case "syntax":
        lines.push("- Check for missing semicolons, brackets, or parentheses");
        lines.push("- Verify proper string/template literal syntax");
        break;
      case "import":
        lines.push("- Check if the import path is correct");
        lines.push("- Verify the export exists in the source module");
        lines.push("- Check for typos in module names");
        break;
      case "type":
        lines.push("- Check type annotations match actual values");
        lines.push("- Verify interface/type definitions");
        lines.push("- Consider adding type assertions if appropriate");
        break;
      case "test_assertion":
        lines.push("- Check expected vs actual values");
        lines.push("- Verify the test logic matches the implementation");
        lines.push("- Consider if the implementation or test needs to change");
        break;
      case "logic":
        lines.push("- Check for undefined/null values");
        lines.push("- Verify variable initialization");
        lines.push("- Check conditional logic");
        break;
      default:
        lines.push("- Analyze the error message carefully");
        lines.push("- Make the minimal change needed to fix the error");
    }
  }

  lines.push("");
  lines.push("## Raw Output");
  lines.push("```");
  lines.push(error.rawOutput);
  lines.push("```");

  return lines.join("\n");
}
