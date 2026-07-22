/**
 * RoutingPatterns - Pure functions for epic decomposition and repository routing
 *
 * This module provides deterministic pattern matching for suggesting repository
 * assignments when creating epics in multi-repository workspaces. It follows
 * the hybrid deterministic/probabilistic architecture:
 *
 * 1. Deterministic: Regex-based keyword matching with confidence scoring
 * 2. Probabilistic: AI gap-filling for items that don't match patterns (external)
 *
 * All functions are pure (no side effects) to enable easy testing and predictable behavior.
 *
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 * @see Issue #325 - AI-Powered Epic Decomposition
 */

/**
 * Routing pattern definition from workspace configuration
 *
 * Patterns define keyword-to-repository mappings for routing epic sub-issues.
 */
export interface RoutingPattern {
  /** Unique identifier for the pattern */
  id: string;
  /** Keywords that trigger this pattern (case-insensitive) */
  keywords: string[];
  /** Repository to assign when pattern matches */
  preferred_repo: string;
  /** Optional: Minimum confidence threshold (0-1, default 0.5) */
  min_confidence?: number;
  /** Optional: Description for display in preview */
  description?: string;
}

/**
 * Result of pattern matching against content
 */
export interface PatternMatch {
  /** ID of the matched pattern */
  pattern_id: string;
  /** Suggested repository from the pattern */
  preferred_repo: string;
  /** Confidence score (0-1) based on keyword matches */
  confidence: number;
  /** Keywords from the pattern that matched */
  matched_keywords: string[];
}

/**
 * Suggested decomposition item for an epic
 */
export interface SuggestedDecomposition {
  /** Suggested title for child issue */
  title: string;
  /** Source text (acceptance criterion or sub-task) */
  source: string;
  /** Suggested repository assignment */
  suggested_repo: string;
  /** Confidence score (0-1) */
  confidence: number;
  /** Source of suggestion: 'pattern' or 'ai' */
  suggestion_source: "pattern" | "ai";
  /** Matched keywords (for pattern-based suggestions) */
  matched_keywords?: string[];
}

/**
 * Epic content structure for analysis
 */
export interface EpicContent {
  /** Epic title */
  title: string;
  /** Epic description/body */
  body: string;
  /** Acceptance criteria (parsed from body) */
  acceptance_criteria: string[];
}

/**
 * Routing configuration from workspace config
 */
export interface RoutingConfig {
  /** Array of routing patterns */
  patterns: RoutingPattern[];
  /** Default repository when no pattern matches */
  default_repository?: string;
  /** Enable AI fallback for unmatched items */
  ai_fallback?: boolean;
}

/**
 * Default minimum confidence threshold for pattern matches
 */
const DEFAULT_MIN_CONFIDENCE = 0.3;

/**
 * Tokenize content into lowercase words for matching
 *
 * @param content - Text content to tokenize
 * @returns Array of lowercase word tokens
 */
export function tokenizeContent(content: string): string[] {
  // Remove markdown formatting, punctuation, and split by whitespace
  return content
    .toLowerCase()
    .replace(/[#*`_[\](){}]/g, " ")
    .replace(/[^\w\s-]/g, " ")
    .split(/\s+/)
    .filter((token) => token.length > 1);
}

/**
 * Calculate confidence score for a pattern match
 *
 * Uses the formula: matched_keywords / total_pattern_keywords
 * Applies minimum threshold filtering.
 *
 * @param matchedKeywords - Keywords from pattern that matched content
 * @param pattern - The routing pattern
 * @returns Confidence score (0-1) or 0 if below threshold
 */
export function calculateConfidence(matchedKeywords: string[], pattern: RoutingPattern): number {
  if (pattern.keywords.length === 0) {
    return 0;
  }

  const confidence = matchedKeywords.length / pattern.keywords.length;
  const threshold = pattern.min_confidence ?? DEFAULT_MIN_CONFIDENCE;

  return confidence >= threshold ? confidence : 0;
}

/**
 * Match content against a single routing pattern
 *
 * @param content - Text content to match
 * @param pattern - Routing pattern to match against
 * @returns PatternMatch if pattern matches, null otherwise
 */
export function matchPattern(content: string, pattern: RoutingPattern): PatternMatch | null {
  const contentTokens = tokenizeContent(content);
  const matchedKeywords: string[] = [];

  for (const keyword of pattern.keywords) {
    const keywordLower = keyword.toLowerCase();
    // Check for exact token match or substring match for compound terms
    const isMatch = contentTokens.some(
      (token) => token === keywordLower || token.includes(keywordLower)
    );
    if (isMatch) {
      matchedKeywords.push(keyword);
    }
  }

  const confidence = calculateConfidence(matchedKeywords, pattern);

  if (confidence > 0) {
    return {
      pattern_id: pattern.id,
      preferred_repo: pattern.preferred_repo,
      confidence,
      matched_keywords: matchedKeywords,
    };
  }

  return null;
}

/**
 * Match content against multiple routing patterns
 *
 * Returns all matching patterns sorted by confidence (highest first).
 *
 * @param content - Text content to match
 * @param patterns - Array of routing patterns
 * @returns Array of PatternMatch results sorted by confidence
 */
export function matchRoutingPatterns(content: string, patterns: RoutingPattern[]): PatternMatch[] {
  const matches: PatternMatch[] = [];

  for (const pattern of patterns) {
    const match = matchPattern(content, pattern);
    if (match) {
      matches.push(match);
    }
  }

  // Sort by confidence (highest first)
  return matches.sort((a, b) => b.confidence - a.confidence);
}

/**
 * Get the best matching pattern for content
 *
 * @param content - Text content to match
 * @param patterns - Array of routing patterns
 * @returns Best PatternMatch or null if no match
 */
export function getBestMatch(content: string, patterns: RoutingPattern[]): PatternMatch | null {
  const matches = matchRoutingPatterns(content, patterns);
  return matches.length > 0 ? matches[0] : null;
}

/**
 * Parse acceptance criteria from epic body
 *
 * Looks for markdown checkbox items under "Acceptance Criteria" or "Sub-Issues" sections.
 *
 * @param body - Epic body text
 * @returns Array of acceptance criteria strings
 */
export function parseAcceptanceCriteria(body: string): string[] {
  const criteria: string[] = [];

  // Match markdown checkboxes: - [ ] or - [x]
  const checkboxPattern = /^[\s]*[-*]\s*\[[ x]\]\s*(.+)$/gim;
  let match: RegExpExecArray | null;

  while ((match = checkboxPattern.exec(body)) !== null) {
    const criterion = match[1].trim();
    if (criterion.length > 0) {
      criteria.push(criterion);
    }
  }

  return criteria;
}

/**
 * Parse sub-issues section from epic body
 *
 * Looks for numbered or bulleted lists that describe sub-tasks.
 *
 * @param body - Epic body text
 * @returns Array of sub-issue descriptions
 */
export function parseSubIssues(body: string): string[] {
  const subIssues: string[] = [];

  // Match numbered items: 1. description
  const numberedPattern = /^\s*\d+\.\s+(.+)$/gim;
  let match: RegExpExecArray | null;

  while ((match = numberedPattern.exec(body)) !== null) {
    const item = match[1].trim();
    // Skip if it looks like a step in "Steps to Reproduce" (starts with action verb)
    if (!item.match(/^(go to|navigate|click|select|enter|type|open|close|run)/i)) {
      subIssues.push(item);
    }
  }

  return subIssues;
}

/**
 * Extract keywords from epic content
 *
 * Identifies domain-specific terms that can be used for routing.
 *
 * @param content - Epic content (title + body)
 * @returns Array of extracted keywords
 */
export function extractKeywords(content: string): string[] {
  // Domain-specific keyword patterns
  const keywordPatterns = [
    // Frontend patterns
    /\b(ui|ux|frontend|front-end|component|react|vue|angular|css|styling|button|form|page|modal|dialog|layout|responsive)\b/gi,
    // Backend patterns
    /\b(api|endpoint|service|controller|database|db|auth|authentication|backend|back-end|server|rest|graphql)\b/gi,
    // Shared/common patterns
    /\b(types?|interfaces?|utils?|common|shared|library|lib|package|module)\b/gi,
    // Testing patterns
    /\b(tests?|testing|unit|integration|e2e|coverage|spec)\b/gi,
    // Infrastructure patterns
    /\b(ci|cd|deploy|infrastructure|docker|kubernetes|aws|cloud)\b/gi,
    // Documentation patterns
    /\b(docs?|documentation|readme|guide|tutorial)\b/gi,
  ];

  const keywords = new Set<string>();

  for (const pattern of keywordPatterns) {
    let match: RegExpExecArray | null;
    while ((match = pattern.exec(content)) !== null) {
      keywords.add(match[1].toLowerCase());
    }
  }

  return Array.from(keywords);
}

/**
 * Generate suggested decomposition for an epic
 *
 * Analyzes epic content and suggests repository assignments for each
 * acceptance criterion or sub-task based on routing patterns.
 *
 * @param epic - Epic content to analyze
 * @param config - Routing configuration
 * @returns Array of suggested decomposition items
 */
export function generateDecomposition(
  epic: EpicContent,
  config: RoutingConfig
): SuggestedDecomposition[] {
  const suggestions: SuggestedDecomposition[] = [];

  // Combine acceptance criteria and any parsed sub-issues
  const items = [...epic.acceptance_criteria, ...parseSubIssues(epic.body)].filter(
    (item, index, self) => self.indexOf(item) === index
  ); // Dedupe

  for (const item of items) {
    // Try to match against patterns
    const match = getBestMatch(item, config.patterns);

    if (match) {
      suggestions.push({
        title: generateSubIssueTitle(item),
        source: item,
        suggested_repo: match.preferred_repo,
        confidence: match.confidence,
        suggestion_source: "pattern",
        matched_keywords: match.matched_keywords,
      });
    } else if (config.default_repository) {
      // Use default repository with low confidence
      suggestions.push({
        title: generateSubIssueTitle(item),
        source: item,
        suggested_repo: config.default_repository,
        confidence: 0.1,
        suggestion_source: "pattern",
        matched_keywords: [],
      });
    }
    // Items without matches and no default will need AI fallback (handled externally)
  }

  return suggestions;
}

/**
 * Generate a concise title from a longer description
 *
 * Truncates to ~70 characters while preserving meaning.
 *
 * @param description - Full description text
 * @returns Concise title suitable for an issue
 */
export function generateSubIssueTitle(description: string): string {
  // Remove markdown formatting
  let title = description.replace(/[#*`_[\]()]/g, "").trim();

  // Capitalize first letter
  title = title.charAt(0).toUpperCase() + title.slice(1);

  // Truncate if too long
  if (title.length > 70) {
    title = title.substring(0, 67) + "...";
  }

  return title;
}

/**
 * Get unmatched items that need AI fallback
 *
 * Returns acceptance criteria that didn't match any routing pattern.
 *
 * @param epic - Epic content
 * @param suggestions - Generated suggestions from pattern matching
 * @returns Array of items that need AI routing
 */
export function getUnmatchedItems(
  epic: EpicContent,
  suggestions: SuggestedDecomposition[]
): string[] {
  const allItems = [...epic.acceptance_criteria, ...parseSubIssues(epic.body)].filter(
    (item, index, self) => self.indexOf(item) === index
  );

  const matchedSources = new Set(suggestions.map((s) => s.source));

  return allItems.filter((item) => !matchedSources.has(item));
}

/**
 * Format confidence as a human-readable percentage
 *
 * @param confidence - Confidence score (0-1)
 * @returns Formatted string (e.g., "78%")
 */
export function formatConfidence(confidence: number): string {
  return `${Math.round(confidence * 100)}%`;
}

/**
 * Get confidence level description
 *
 * @param confidence - Confidence score (0-1)
 * @returns Human-readable confidence level
 */
export function getConfidenceLevel(confidence: number): "high" | "medium" | "low" {
  if (confidence >= 0.7) return "high";
  if (confidence >= 0.4) return "medium";
  return "low";
}

/**
 * Group suggestions by repository
 *
 * @param suggestions - Array of suggestions
 * @returns Map of repository to suggestions
 */
export function groupByRepository(
  suggestions: SuggestedDecomposition[]
): Map<string, SuggestedDecomposition[]> {
  const groups = new Map<string, SuggestedDecomposition[]>();

  for (const suggestion of suggestions) {
    const existing = groups.get(suggestion.suggested_repo) ?? [];
    existing.push(suggestion);
    groups.set(suggestion.suggested_repo, existing);
  }

  return groups;
}

/**
 * Validate routing patterns
 *
 * Checks that patterns have required fields and valid values.
 *
 * @param patterns - Array of patterns to validate
 * @returns Object with valid flag and any error messages
 */
export function validatePatterns(patterns: RoutingPattern[]): {
  valid: boolean;
  errors: string[];
} {
  const errors: string[] = [];

  const seenIds = new Set<string>();

  for (let i = 0; i < patterns.length; i++) {
    const pattern = patterns[i];

    // Check required fields
    if (!pattern.id || pattern.id.trim() === "") {
      errors.push(`Pattern at index ${i}: missing or empty 'id'`);
    } else if (seenIds.has(pattern.id)) {
      errors.push(`Pattern at index ${i}: duplicate id '${pattern.id}'`);
    } else {
      seenIds.add(pattern.id);
    }

    if (!Array.isArray(pattern.keywords) || pattern.keywords.length === 0) {
      errors.push(`Pattern '${pattern.id}': 'keywords' must be a non-empty array`);
    }

    if (!pattern.preferred_repo || pattern.preferred_repo.trim() === "") {
      errors.push(`Pattern '${pattern.id}': missing or empty 'preferred_repo'`);
    }

    // Check min_confidence if provided
    if (
      pattern.min_confidence !== undefined &&
      (pattern.min_confidence < 0 || pattern.min_confidence > 1)
    ) {
      errors.push(`Pattern '${pattern.id}': 'min_confidence' must be between 0 and 1`);
    }
  }

  return {
    valid: errors.length === 0,
    errors,
  };
}
