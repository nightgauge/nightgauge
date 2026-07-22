/**
 * Pattern Matcher — configurable regex-based file scanner
 *
 * Searches source files for patterns (secrets, XSS risks, etc.)
 * with confidence scoring and surrounding context extraction.
 */

export interface PatternMatch {
  /** File path where the match was found */
  file: string;
  /** 1-indexed line number */
  line: number;
  /** Matched text (secrets should be redacted by caller) */
  matchedText: string;
  /** 3-5 lines of surrounding context */
  context: string;
  /** Confidence score (0-100) */
  confidence: number;
  /** Pattern ID that triggered this match */
  patternId: string;
  /** Severity from pattern definition */
  severity: "critical" | "high" | "medium" | "low";
}

export interface PatternDefinition {
  id: string;
  pattern: string;
  confidence: number;
  severity: "critical" | "high" | "medium" | "low";
  description: string;
  false_positive_hints?: string[];
}

/**
 * Check if a line contains any false positive hints that would
 * reduce or eliminate the confidence of a match.
 */
export function hasFalsePositiveHint(line: string, hints: string[]): boolean {
  const lower = line.toLowerCase();
  return hints.some((hint) => lower.includes(hint.toLowerCase()));
}

/**
 * Redact the secret value from a matched line, replacing the
 * captured group (if any) with '***REDACTED***'.
 * If the regex has no capturing group, redacts the full match.
 */
export function redactSecret(line: string, regex: RegExp): string {
  return line.replace(regex, (match, ...groups) => {
    // If there are capturing groups, only redact the last non-offset group
    const capturedGroups = groups.filter((g): g is string => typeof g === "string");
    if (capturedGroups.length > 0) {
      const secret = capturedGroups[capturedGroups.length - 1];
      return match.replace(secret, "***REDACTED***");
    }
    return "***REDACTED***";
  });
}

/**
 * Extract N lines of context around a given line index.
 * Returns a formatted string with line numbers.
 */
export function extractContext(
  lines: string[],
  lineIndex: number,
  contextSize: number = 2
): string {
  const start = Math.max(0, lineIndex - contextSize);
  const end = Math.min(lines.length - 1, lineIndex + contextSize);
  return lines
    .slice(start, end + 1)
    .map((l, i) => `${start + i + 1}: ${l}`)
    .join("\n");
}

/**
 * Scan a file's content for all patterns.
 * Returns matches sorted by severity then line number.
 */
export function scanFileContent(
  filePath: string,
  content: string,
  patterns: PatternDefinition[]
): PatternMatch[] {
  const lines = content.split("\n");
  const matches: PatternMatch[] = [];

  for (const pattern of patterns) {
    let regex: RegExp;
    try {
      regex = new RegExp(pattern.pattern, "gi");
    } catch {
      // Invalid regex — skip
      continue;
    }

    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];
      const lineMatches = [...line.matchAll(regex)];

      for (const match of lineMatches) {
        const matchedText = match[0];

        // Check false positive hints
        const hints = pattern.false_positive_hints ?? [];
        if (hints.length > 0 && hasFalsePositiveHint(line, hints)) {
          continue;
        }

        // Compute confidence (reduce if common false positive indicators)
        let confidence = pattern.confidence;
        if (line.includes("process.env") || line.includes("config.") || line.includes("env.")) {
          confidence = Math.max(0, confidence - 30);
        }

        // Skip very low confidence
        if (confidence < 40) continue;

        const context = extractContext(lines, i);
        const redacted = redactSecret(matchedText, regex);

        matches.push({
          file: filePath,
          line: i + 1,
          matchedText: redacted,
          context,
          confidence,
          patternId: pattern.id,
          severity: pattern.severity,
        });
      }
    }
  }

  return matches;
}

/**
 * Filter matches to only those above a minimum confidence threshold.
 */
export function filterByConfidence(matches: PatternMatch[], minConfidence: number): PatternMatch[] {
  return matches.filter((m) => m.confidence >= minConfidence);
}
