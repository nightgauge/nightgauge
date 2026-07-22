/**
 * ChangeAnalyzer - Pure functions for detecting change type and complexity
 *
 * This module provides deterministic analysis of issue metadata to determine:
 * 1. Change type (docs, config, code)
 * 2. Complexity score (Fibonacci: 1/2/3/5/8)
 * 3. Suggested routing path (trivial, standard, extensive)
 *
 * All functions are pure (no side effects) to enable easy testing and predictable behavior.
 *
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 * @see Issue #216 - Complexity-Based Stage Routing
 */

/**
 * Change type categories for routing decisions
 */
export type ChangeType = "docs" | "config" | "code";

/**
 * Size labels from GitHub issue (matches GitHub Projects sizing)
 */
export type SizeLabel = "XS" | "S" | "M" | "L" | "XL" | null;

/**
 * Issue type labels
 */
export type TypeLabel =
  "feature" | "bug" | "docs" | "refactor" | "chore" | "test" | "verification" | "spike" | null;

/**
 * Task types for routing decisions
 *
 * Task type determines which pipeline stages are fundamentally needed,
 * independent of complexity.
 *
 * @see Issue #268 - Task-Type Routing
 */
export type TaskType =
  "feature" | "bugfix" | "verification" | "docs-only" | "refactor" | "chore" | "spike";

/**
 * Priority labels from GitHub issue
 */
export type PriorityLabel = "critical" | "high" | "medium" | "low" | null;

/**
 * Routing path options
 */
export type RoutingPath = "trivial" | "standard" | "extensive";

/**
 * Stages that can be skipped based on routing
 */
export type SkippableStage = "feature-planning" | "feature-validate";

/**
 * Result of change analysis
 */
export interface ChangeAnalysis {
  /** Detected change type */
  changeType: ChangeType;
  /** Detected task type for routing */
  taskType: TaskType;
  /** Size label from issue */
  sizeLabel: SizeLabel;
  /** Type label from issue */
  typeLabel: TypeLabel;
  /** Priority label from issue */
  priorityLabel: PriorityLabel;
  /** Computed complexity score (Fibonacci: 1/2/3/5/8) */
  complexityScore: number;
  /** Suggested routing path */
  suggestedRoute: RoutingPath;
  /** Stages to skip based on routing */
  skipStages: SkippableStage[];
  /** Human-readable rationale for routing decision */
  rationale: string;
  /** Estimated time in minutes based on route */
  estimatedTimeMinutes: number;
  /** True when issue is a foundation/scaffolding task (type:chore + scaffold title).
   * Foundation tasks skip feature-planning and feature-validate.
   * @see Issue #1318 */
  foundationTask: boolean;
  /** True when label-based risk classification forced the full pipeline +
   * extensive route regardless of complexity. @see Issue #4093 */
  riskHigh: boolean;
  /** Label slugs that triggered the high-risk classification (empty when
   * riskHigh is false). @see Issue #4093 */
  riskReasons: string[];
}

/**
 * Labels extracted from issue
 */
export interface IssueLabels {
  /** All label names (lowercase) */
  all: string[];
  /** Size label if present */
  size: SizeLabel;
  /** Type label if present */
  type: TypeLabel;
  /** Priority label if present */
  priority: PriorityLabel;
}

/**
 * Size label to complexity score mapping (Fibonacci)
 */
const SIZE_COMPLEXITY_MAP: Record<NonNullable<SizeLabel>, number> = {
  XS: 1,
  S: 2,
  M: 3,
  L: 5,
  XL: 8,
};

/**
 * Priority multipliers for complexity adjustment
 */
const PRIORITY_MULTIPLIER: Record<NonNullable<PriorityLabel>, number> = {
  critical: 1.5,
  high: 1.2,
  medium: 1.0,
  low: 0.8,
};

/**
 * Estimated time in minutes per routing path (fallback values)
 *
 * These are baseline estimates used when no work-time feedback data is available.
 * When feedback exists, actual_average from complexity-model.yaml takes precedence.
 *
 * @see Issue #310 - Work-time feedback loop
 */
const ROUTE_TIME_ESTIMATES: Record<RoutingPath, number> = {
  trivial: 6,
  standard: 30,
  extensive: 45,
};

/**
 * Spike (research/investigation) task patterns in issue title/body
 *
 * These patterns indicate the task is exploratory research, proof-of-concept,
 * or investigation work that produces documentation rather than production code.
 *
 * @see Issue #168 - Research spike support
 */
const SPIKE_PATTERNS = [
  /\bspike\b/i,
  /\bresearch\b/i,
  /\binvestigat(e|ion)\b/i,
  /\bproof[- ]of[- ]concept\b/i,
  /\bfeasibility\b/i,
  /\bexplor(e|ation)\b/i,
  /\bprototyp(e|ing)\b/i,
  /\bevaluat(e|ion)\b/i,
];

/**
 * Docs-only patterns in issue content
 */
const DOCS_PATTERNS = [
  /^docs?[:\-\s]/i,
  /documentation/i,
  /readme/i,
  /changelog/i,
  /\.md\b/i,
  /update.*docs?/i,
  /fix.*typo/i,
];

/**
 * Config-only patterns in issue content
 */
const CONFIG_PATTERNS = [
  /config(uration)?[:\-\s]/i,
  /settings?[:\-\s]/i,
  /\.ya?ml\b/i,
  /\.json\b/i,
  /\.env\b/i,
  /\.eslint/i,
  /\.prettier/i,
  /tsconfig/i,
  /package\.json/i,
];

/**
 * Verification task patterns in issue title/body
 *
 * These patterns indicate the task is about verifying, confirming, or certifying
 * something rather than implementing new functionality.
 *
 * @see Issue #268 - Task-Type Routing
 */
const VERIFICATION_PATTERNS = [
  /\bverify\b/i,
  /\bconfirm\b/i,
  /\bcertify\b/i,
  /\bvalidate\b/i,
  /\baudit\b/i,
  /\bcheck\s+that\b/i,
  /\bensure\s+that\b/i,
  /confirm\s+(fix|implementation|change)\s+(for|in)\s+#?\d+/i,
  /verify\s+(fix|implementation|change)\s+(for|in)\s+#?\d+/i,
];

/**
 * Extract structured labels from raw label array
 *
 * @param labels - Raw label names from GitHub issue
 * @returns Structured label information
 */
export function extractLabels(labels: (string | { name?: string })[]): IssueLabels {
  // Normalize labels — may be strings or {name: string} objects from GitHub API
  const lowercaseLabels = labels
    .map((l) => (typeof l === "string" ? l : (l?.name ?? "")))
    .filter((l) => l.length > 0)
    .map((l) => l.toLowerCase().trim());

  // Extract size label
  let size: SizeLabel = null;
  for (const label of lowercaseLabels) {
    const sizeMatch = label.match(/^size[:-]?(xs|s|m|l|xl)$/i);
    if (sizeMatch) {
      size = sizeMatch[1].toUpperCase() as SizeLabel;
      break;
    }
  }

  // Extract type label
  let type: TypeLabel = null;
  const typePatterns: Array<{ pattern: RegExp; type: TypeLabel }> = [
    { pattern: /^type[:-]?feature$/i, type: "feature" },
    { pattern: /^(type[:-]?)?bug$/i, type: "bug" },
    { pattern: /^(type[:-]?)?(docs?|documentation)$/i, type: "docs" },
    { pattern: /^(type[:-]?)?refactor$/i, type: "refactor" },
    { pattern: /^(type[:-]?)?chore$/i, type: "chore" },
    { pattern: /^(type[:-]?)?test$/i, type: "test" },
    { pattern: /^(type[:-]?)?verification$/i, type: "verification" },
    { pattern: /^(type[:-]?)?spike$/i, type: "spike" },
    { pattern: /^enhancement$/i, type: "feature" },
  ];

  for (const label of lowercaseLabels) {
    for (const { pattern, type: labelType } of typePatterns) {
      if (pattern.test(label)) {
        type = labelType;
        break;
      }
    }
    if (type) break;
  }

  // Extract priority label
  let priority: PriorityLabel = null;
  for (const label of lowercaseLabels) {
    const priorityMatch = label.match(/^priority[:-]?(critical|high|medium|low)$/i);
    if (priorityMatch) {
      priority = priorityMatch[1].toLowerCase() as PriorityLabel;
      break;
    }
  }

  return {
    all: lowercaseLabels,
    size,
    type,
    priority,
  };
}

/**
 * Detect change type from issue metadata
 *
 * @param labels - Extracted label information
 * @param title - Issue title
 * @param body - Issue body (optional)
 * @returns Detected change type
 */
export function detectChangeType(labels: IssueLabels, title: string, body?: string): ChangeType {
  const content = `${title} ${body ?? ""}`;

  // Check type label first (most reliable)
  if (labels.type === "docs") {
    return "docs";
  }

  // Explicit feature/bug/refactor/spike labels indicate code changes
  // Spike explores code to produce docs — change type is code exploration
  if (
    labels.type === "feature" ||
    labels.type === "bug" ||
    labels.type === "refactor" ||
    labels.type === "spike"
  ) {
    return "code";
  }

  // Check for docs-only patterns
  const isDocsOnly = DOCS_PATTERNS.some((pattern) => pattern.test(content));
  const noCodeIndicators = !content.match(
    /\b(implement|fix|add|create|update|refactor)\s+(function|class|method|api|endpoint|component|service)/i
  );

  if (isDocsOnly && noCodeIndicators) {
    return "docs";
  }

  // Check for config-only patterns
  const isConfigOnly = CONFIG_PATTERNS.some((pattern) => pattern.test(content));
  const noFeatureIndicators = !content.match(
    /\b(feature|functionality|behavior|user can|should be able)/i
  );

  // At this point, labels.type is 'chore', 'test', or null
  // (feature, bug, refactor, docs already returned above)
  if (isConfigOnly && noFeatureIndicators && !isDocsOnly) {
    return "config";
  }

  // Default to code changes
  return "code";
}

/**
 * Detect task type from issue metadata
 *
 * Task type determines which pipeline stages are fundamentally needed,
 * independent of complexity. Labels take priority (explicit user intent),
 * with content analysis as fallback.
 *
 * @param labels - Extracted label information
 * @param title - Issue title
 * @param body - Issue body (optional)
 * @returns Detected task type
 *
 * @see Issue #268 - Task-Type Routing
 */
export function detectTaskType(labels: IssueLabels, title: string, body?: string): TaskType {
  const content = `${title} ${body ?? ""}`;

  // Check type label first (most reliable - explicit user intent)
  switch (labels.type) {
    case "verification":
      return "verification";
    case "docs":
      return "docs-only";
    case "bug":
      return "bugfix";
    case "refactor":
      return "refactor";
    case "chore":
      return "chore";
    case "feature":
      return "feature";
    case "spike":
      return "spike";
    case "test":
      // Test tasks are treated like chores (skip planning)
      return "chore";
  }

  // Content-based detection for verification tasks
  const isVerification = VERIFICATION_PATTERNS.some((pattern) => pattern.test(content));
  if (isVerification) {
    return "verification";
  }

  // Content-based detection for spike/research tasks (before docs to avoid
  // "Research X" being classified as docs-only)
  const isSpike = SPIKE_PATTERNS.some((pattern) => pattern.test(content));
  if (isSpike) {
    return "spike";
  }

  // Content-based detection for docs-only tasks
  const isDocsOnly = DOCS_PATTERNS.some((pattern) => pattern.test(content));
  const noCodeIndicators = !content.match(
    /\b(implement|fix|add|create|update|refactor)\s+(function|class|method|api|endpoint|component|service)/i
  );
  if (isDocsOnly && noCodeIndicators) {
    return "docs-only";
  }

  // Default to feature (safest - runs full pipeline)
  return "feature";
}

/**
 * Detect whether an issue is a foundation/scaffolding task.
 *
 * Foundation tasks are greenfield setup issues (type:chore + scaffold title)
 * that skip planning and use relaxed validation — no existing patterns exist yet.
 *
 * Logic mirrors AutoModelSelector.isFoundationTask() for cross-path consistency.
 *
 * @see Issue #1318 - Foundation task type routing
 */
export function detectFoundationTask(labels: IssueLabels, title: string): boolean {
  const titleLower = title.toLowerCase();
  const isChore = labels.type === "chore";

  const foundationKeywords = [
    "scaffold",
    "foundation",
    "setup",
    "bootstrap",
    "initialize",
    "configure",
    "init monorepo",
    "init workspace",
  ];
  const hasFoundationTitle = foundationKeywords.some((k) => titleLower.includes(k));

  // type:chore + foundation keyword is the primary signal
  if (isChore && hasFoundationTitle) return true;

  // Strong foundation phrases trigger detection even without chore label
  const strongFoundation = [
    "initialize monorepo",
    "initialize npm",
    "setup typescript",
    "setup vitest",
    "setup eslint",
    "scaffold project",
    "bootstrap project",
    "configure ci",
    "configure github actions",
  ];
  return strongFoundation.some((k) => titleLower.includes(k));
}

/**
 * Calculate complexity score based on labels and change type
 *
 * Uses Fibonacci sequence: 1, 2, 3, 5, 8
 *
 * @param labels - Extracted label information
 * @param changeType - Detected change type
 * @returns Complexity score (1-8)
 */
export function calculateComplexityScore(labels: IssueLabels, changeType: ChangeType): number {
  // Base score from size label
  let baseScore = labels.size ? SIZE_COMPLEXITY_MAP[labels.size] : 3; // Default to M (3)

  // Reduce complexity for non-code changes
  if (changeType === "docs") {
    baseScore = Math.min(baseScore, 2);
  } else if (changeType === "config") {
    baseScore = Math.min(baseScore, 2);
  }

  // Apply priority multiplier
  const multiplier = labels.priority ? PRIORITY_MULTIPLIER[labels.priority] : 1.0;
  const adjustedScore = baseScore * multiplier;

  // Round to nearest Fibonacci number
  const fibonacciSequence = [1, 2, 3, 5, 8];
  let closestFib = fibonacciSequence[0];
  let minDiff = Math.abs(adjustedScore - closestFib);

  for (const fib of fibonacciSequence) {
    const diff = Math.abs(adjustedScore - fib);
    if (diff < minDiff) {
      minDiff = diff;
      closestFib = fib;
    }
  }

  return closestFib;
}

/**
 * Label keywords (case-insensitive substring match) that mark an issue as high
 * blast-radius. MUST stay byte-identical to riskKeywords in
 * internal/intelligence/routing/risk.go.
 *
 * @see Issue #4093 - Risk dimension forces the extensive route
 */
const RISK_KEYWORDS = [
  "security",
  "auth",
  "billing",
  "payment",
  "migration",
  "public-api",
  "breaking",
  "credential",
] as const;

/**
 * Labels that force a high-risk classification regardless of keyword matching.
 * MUST mirror riskEscapeHatchLabels in risk.go.
 */
const RISK_ESCAPE_HATCH = new Set(["risk:high", "risk-high"]);

/**
 * Classify an issue as high-risk from its labels.
 *
 * Pure mirror of isHighRisk() in internal/intelligence/routing/risk.go —
 * label-based only (no diff/blast-radius signal is plumbed into this path).
 * complexity_score tracks size, not blast radius; a small change to
 * security/auth/billing/a migration/a public API must still run the full
 * pipeline, so high-risk forces it (see determineSkipStages / determineRoutingPath).
 *
 * @param labels - Extracted label information (labels.all is lowercased)
 * @returns high flag plus de-duplicated matched label slugs
 *
 * @see Issue #4093 - Risk dimension forces the extensive route
 */
export function isHighRisk(labels: IssueLabels): { high: boolean; reasons: string[] } {
  const reasons: string[] = [];
  const seen = new Set<string>();
  const add = (slug: string): void => {
    if (seen.has(slug)) {
      return;
    }
    seen.add(slug);
    reasons.push(slug);
  };

  for (const raw of labels.all) {
    const slug = raw.trim().toLowerCase();
    if (RISK_ESCAPE_HATCH.has(slug)) {
      add(slug);
      continue;
    }
    if (RISK_KEYWORDS.some((kw) => slug.includes(kw))) {
      add(slug);
    }
  }

  return { high: reasons.length > 0, reasons };
}

/**
 * Determine which stages to skip based on analysis
 *
 * This function now considers both complexity-based routing and task-type routing.
 * Task type determines which stages are fundamentally needed, while complexity
 * affects stages within those constraints.
 *
 * @param changeType - Detected change type
 * @param complexityScore - Computed complexity score
 * @param sizeLabel - Size label from issue
 * @param taskType - Detected task type (optional for backward compatibility)
 * @returns Array of stages to skip
 *
 * @see Issue #268 - Task-Type Routing
 */
export function determineSkipStages(
  changeType: ChangeType,
  complexityScore: number,
  sizeLabel: SizeLabel,
  taskType?: TaskType,
  foundationTask?: boolean,
  highRisk?: boolean
): SkippableStage[] {
  // RISK_FLOOR (#4093): high-risk forces the full pipeline — skip nothing, so
  // feature-planning and feature-validate (and the verification gates that hang
  // off them) always run on high blast-radius changes.
  if (highRisk) {
    return [];
  }

  const skipStages: SkippableStage[] = [];

  // Default to 'feature' task type for backward compatibility
  const effectiveTaskType = taskType ?? "feature";

  const isTrivialComplexity = complexityScore <= 2;
  const isNonCode = changeType === "docs" || changeType === "config";

  // Complexity-based skip for ALL task types (Issue #1593):
  // Any issue with complexity ≤ 2 skips planning and validate, regardless of
  // task type. A complexity-2 refactor (delete dead code) doesn't need a
  // PLAN.md or a validation suite.
  if (isTrivialComplexity) {
    if (!skipStages.includes("feature-planning")) {
      skipStages.push("feature-planning");
    }
    if (!skipStages.includes("feature-validate")) {
      skipStages.push("feature-validate");
    }
  }

  // Task-type-based skip for feature-planning (chore tasks skip planning)
  if (effectiveTaskType === "chore" && !skipStages.includes("feature-planning")) {
    skipStages.push("feature-planning");
  }

  // Foundation task routing: skip both planning AND validate (#1318)
  // Mirrors SKILL.md behavior: foundation tasks force trivial path
  if (foundationTask && !skipStages.includes("feature-planning")) {
    skipStages.push("feature-planning");
  }
  if (foundationTask && !skipStages.includes("feature-validate")) {
    skipStages.push("feature-validate");
  }

  // Skip validation for non-code changes (no code to validate)
  if (isNonCode && !skipStages.includes("feature-validate")) {
    skipStages.push("feature-validate");
  }

  // Skip validation for docs-only task type
  if (effectiveTaskType === "docs-only" && !skipStages.includes("feature-validate")) {
    skipStages.push("feature-validate");
  }

  return skipStages;
}

/**
 * Determine routing path based on analysis
 *
 * @param changeType - Detected change type
 * @param complexityScore - Computed complexity score
 * @param labels - Extracted label information
 * @returns Routing path
 */
export function determineRoutingPath(
  changeType: ChangeType,
  complexityScore: number,
  labels: IssueLabels,
  highRisk?: boolean
): RoutingPath {
  // RISK_FLOOR (#4093): high-risk floors the route at "extensive", overriding
  // any docs/trivial/standard downgrade.
  if (highRisk) {
    return "extensive";
  }

  const isTrivialSize = labels.size === "XS" || labels.size === "S";
  const isNonCode = changeType === "docs" || changeType === "config";

  // Trivial path: docs-only, XS/S size, or complexity 1-2
  if ((isNonCode && isTrivialSize) || complexityScore <= 2) {
    return "trivial";
  }

  // Extensive path: L/XL size, critical priority, or complexity 5+
  const isLargeSize = labels.size === "L" || labels.size === "XL";
  const isCritical = labels.priority === "critical";

  if (isLargeSize || isCritical || complexityScore >= 5) {
    return "extensive";
  }

  // Standard path: everything else
  return "standard";
}

/**
 * Generate human-readable rationale for routing decision
 *
 * @param route - Selected routing path
 * @param changeType - Detected change type
 * @param complexityScore - Computed complexity score
 * @param labels - Extracted label information
 * @param taskType - Detected task type (optional for backward compatibility)
 * @returns Human-readable rationale
 *
 * @see Issue #268 - Task-Type Routing
 */
export function generateRationale(
  route: RoutingPath,
  changeType: ChangeType,
  complexityScore: number,
  labels: IssueLabels,
  taskType?: TaskType
): string {
  const parts: string[] = [];

  if (labels.size) {
    parts.push(`${labels.size} size`);
  }

  parts.push(`${changeType} change`);
  parts.push(`complexity ${complexityScore}`);

  if (labels.priority) {
    parts.push(`${labels.priority} priority`);
  }

  // Include task type if not default 'feature'
  if (taskType && taskType !== "feature") {
    parts.push(`${taskType} task`);
  }

  const factorsStr = parts.join(", ");

  switch (route) {
    case "trivial":
      return `Trivial path selected: ${factorsStr}. Skipping planning and validation.`;
    case "extensive":
      return `Extensive path selected: ${factorsStr}. Using extended documentation scope.`;
    case "standard":
    default:
      return `Standard path selected: ${factorsStr}. Full pipeline execution.`;
  }
}

/**
 * Maximum allowed length for issue title
 */
const MAX_TITLE_LENGTH = 1000;

/**
 * Validation error for routing input
 *
 * @see Issue #418 - Add routing input validation
 */
export interface RoutingValidationError {
  /** Error field */
  field: "labels" | "title" | "body";
  /** Error message */
  message: string;
}

/**
 * Validation result for routing input
 *
 * @see Issue #418 - Add routing input validation
 */
export interface RoutingValidationResult {
  /** Whether validation passed */
  valid: boolean;
  /** Validation errors (empty if valid) */
  errors: RoutingValidationError[];
}

/**
 * Validate input for routing analysis
 *
 * Uses allowlist-based validation per standards/security.md:
 * - Labels must be strings
 * - Title must be non-empty and under MAX_TITLE_LENGTH
 * - Body is optional but must be a string if provided
 *
 * For recoverable cases, returns validation with warnings but allows
 * processing to continue with defensive defaults.
 *
 * @param labels - Raw label names from GitHub issue
 * @param title - Issue title
 * @param body - Issue body (optional)
 * @returns Validation result
 *
 * @see Issue #418 - Add routing input validation
 * @see standards/security.md - Input validation before processing
 */
export function validateRoutingInput(
  labels: unknown,
  title: unknown,
  body?: unknown
): RoutingValidationResult {
  const errors: RoutingValidationError[] = [];

  // Validate labels (allow empty array as fallback)
  if (labels === null || labels === undefined) {
    // Recoverable: empty labels -> feature task type (defensive default)
    // Not an error, just noted in analysis
  } else if (!Array.isArray(labels)) {
    errors.push({
      field: "labels",
      message: `Expected labels to be an array, got ${typeof labels}`,
    });
  } else if (!labels.every((l) => typeof l === "string")) {
    errors.push({
      field: "labels",
      message: "All labels must be strings",
    });
  }

  // Validate title (required, non-empty)
  if (title === null || title === undefined) {
    errors.push({
      field: "title",
      message: "Title is required",
    });
  } else if (typeof title !== "string") {
    errors.push({
      field: "title",
      message: `Expected title to be a string, got ${typeof title}`,
    });
  } else if (title.trim().length === 0) {
    errors.push({
      field: "title",
      message: "Title cannot be empty",
    });
  } else if (title.length > MAX_TITLE_LENGTH) {
    errors.push({
      field: "title",
      message: `Title exceeds maximum length of ${MAX_TITLE_LENGTH} characters`,
    });
  }

  // Validate body (optional, must be string if provided)
  if (body !== null && body !== undefined && typeof body !== "string") {
    errors.push({
      field: "body",
      message: `Expected body to be a string, got ${typeof body}`,
    });
  }

  return {
    valid: errors.length === 0,
    errors,
  };
}

/**
 * Analyze issue metadata to determine change type and routing
 *
 * This is the main entry point for change analysis.
 *
 * Note: This function performs basic type coercion for defensive programming.
 * For strict validation, use validateRoutingInput() first.
 *
 * @param labels - Raw label names from GitHub issue
 * @param title - Issue title
 * @param body - Issue body (optional)
 * @returns Complete change analysis result
 *
 * @see Issue #418 - Add routing input validation
 */
export function analyzeChange(labels: string[], title: string, body?: string): ChangeAnalysis {
  // Extract structured labels
  const extractedLabels = extractLabels(labels);

  // Detect change type
  const changeType = detectChangeType(extractedLabels, title, body);

  // Detect task type for routing
  const taskType = detectTaskType(extractedLabels, title, body);

  // Detect foundation task for routing (#1318)
  const foundationTask = detectFoundationTask(extractedLabels, title);

  // Calculate complexity score
  const complexityScore = calculateComplexityScore(extractedLabels, changeType);

  // RISK_FLOOR (#4093): label-based high-risk forces extensive + full pipeline.
  const { high: riskHigh, reasons: riskReasons } = isHighRisk(extractedLabels);

  // Determine routing path
  const suggestedRoute = determineRoutingPath(
    changeType,
    complexityScore,
    extractedLabels,
    riskHigh
  );

  // Determine stages to skip (now includes task type, foundation task, and risk)
  const skipStages = determineSkipStages(
    changeType,
    complexityScore,
    extractedLabels.size,
    taskType,
    foundationTask,
    riskHigh
  );

  // Generate rationale (prefixed with a risk note to mirror buildRationale in derive.go)
  const rationale =
    (riskHigh ? "High-risk — forced extensive route + full pipeline. " : "") +
    generateRationale(suggestedRoute, changeType, complexityScore, extractedLabels, taskType);

  // Get time estimate
  const estimatedTimeMinutes = ROUTE_TIME_ESTIMATES[suggestedRoute];

  return {
    changeType,
    taskType,
    sizeLabel: extractedLabels.size,
    typeLabel: extractedLabels.type,
    priorityLabel: extractedLabels.priority,
    complexityScore,
    suggestedRoute,
    skipStages,
    rationale,
    estimatedTimeMinutes,
    foundationTask,
    riskHigh,
    riskReasons,
  };
}

/**
 * Get time estimate with work-time feedback
 *
 * Uses actual_average from feedback if available, falling back to
 * route-based estimates if no feedback exists for this size.
 *
 * @param sizeLabel - Size label from issue (XS, S, M, L, XL)
 * @param suggestedRoute - Routing path (fallback if no feedback)
 * @param feedbackPath - Path to complexity-model.yaml (optional)
 * @returns Estimated time in minutes
 *
 * @see Issue #310 - Work-time feedback loop
 */
export async function getTimeEstimateWithFeedback(
  sizeLabel: SizeLabel,
  suggestedRoute: RoutingPath,
  feedbackPath?: string
): Promise<number> {
  // If no feedbackPath provided, use fallback
  if (!feedbackPath) {
    return ROUTE_TIME_ESTIMATES[suggestedRoute];
  }

  try {
    const { readWorkTimeFeedback } = await import("./workTimeFeedback");
    const feedback = await readWorkTimeFeedback(feedbackPath);

    // If feedback exists and has data for this size, use actual average
    if (feedback?.enabled && sizeLabel && feedback.size_averages[sizeLabel]) {
      return feedback.size_averages[sizeLabel]!.actual_average;
    }
  } catch (error) {
    // Feedback read failed - use fallback
    console.warn("Failed to read work-time feedback, using fallback estimate");
  }

  // No feedback or size not found - use route-based estimate
  return ROUTE_TIME_ESTIMATES[suggestedRoute];
}

/**
 * Validate a size estimate against calibration history.
 *
 * Reads the calibration table from .nightgauge/pipeline/calibration.json
 * and checks if the estimated cost for the given size is an outlier under the
 * active performance mode (issue #3216). Falls back to the same-size `elevated`
 * bucket when the active mode bucket is empty. Returns null when calibration
 * data is unavailable.
 *
 * @param workspaceRoot - Workspace root directory
 * @param sizeLabel - Size label to validate (XS/S/M/L/XL)
 * @param estimatedCost - Estimated cost in USD (optional)
 * @returns Validation result, or null if no calibration data
 *
 * @see Issue #1589 - Calibrate complexity estimator using pipeline outcome history
 * @see Issue #3216 - Calibration bucketing by (size, mode)
 */
export async function validateSizeEstimate(
  workspaceRoot: string,
  sizeLabel: NonNullable<SizeLabel>,
  estimatedCost?: number
): Promise<import("@nightgauge/sdk").EstimateValidation | null> {
  try {
    const { CalibrationService } = await import("@nightgauge/sdk");
    const { getPerformanceMode } = await import("./resolvers/monitoringResolver");
    const calPath = CalibrationService.getDefaultPath(workspaceRoot);
    const table = await CalibrationService.load(calPath);
    if (!table) return null;

    const mode = getPerformanceMode(workspaceRoot);
    return CalibrationService.validateEstimate(table, mode, sizeLabel, estimatedCost);
  } catch {
    return null;
  }
}
