import { readFileSync, existsSync } from "fs";
import { execFileSync } from "child_process";
import { load as yamlLoad } from "js-yaml";
import type { FeatureResult, FeatureRating } from "../types.js";

/** Feature definition for a single client. */
interface ClientPatterns {
  patterns: string[];
  test_patterns?: string[];
}

/** A single feature entry in features.yaml. */
export interface FeatureDefinition {
  name: string;
  vscode?: ClientPatterns;
  angular?: ClientPatterns;
  flutter?: ClientPatterns;
}

/** Top-level features.yaml structure. */
interface FeaturesYaml {
  features: FeatureDefinition[];
}

/**
 * Load feature definitions from a YAML file.
 * Returns an empty array if the file does not exist or cannot be parsed.
 */
export function loadFeatureDefinitions(yamlPath: string): FeatureDefinition[] {
  if (!existsSync(yamlPath)) {
    console.warn(`[feature-matcher] Features file not found: ${yamlPath}`);
    return [];
  }

  try {
    const content = readFileSync(yamlPath, "utf-8");
    const parsed = yamlLoad(content) as FeaturesYaml;
    return parsed?.features ?? [];
  } catch (err) {
    console.warn(`[feature-matcher] Failed to parse ${yamlPath}: ${(err as Error).message}`);
    return [];
  }
}

/**
 * Execute grep for a pattern in a directory.
 * Returns the number of matches found and up to 3 evidence snippets.
 */
function grepDirectory(pattern: string, directory: string): { count: number; evidence: string[] } {
  if (!existsSync(directory)) {
    return { count: 0, evidence: [] };
  }

  try {
    // Use execFileSync with args array to prevent shell injection
    const result = execFileSync(
      "grep",
      ["-r", "--include=*.ts", "--include=*.dart", "--include=*.js", "-l", pattern, directory],
      { encoding: "utf-8", timeout: 30000 }
    );
    const files = result
      .trim()
      .split("\n")
      .filter((f) => f.length > 0);
    const evidence = files.slice(0, 3).map((f) => f.replace(directory, ""));
    return { count: files.length, evidence };
  } catch (err) {
    // grep exits with code 1 when no matches found — not an error
    const exitCode = (err as NodeJS.ErrnoException & { status?: number }).status;
    if (exitCode !== 1) {
      console.warn(
        `[feature-matcher] grep failed for pattern "${pattern}" in ${directory}: exit ${exitCode}`
      );
    }
    return { count: 0, evidence: [] };
  }
}

/**
 * Rate a feature's implementation level for a single client.
 *
 * Rating logic:
 * - MISSING: No code patterns match
 * - STUB: 1 match in code, no test matches
 * - PARTIAL: ≥1 code match, no test match (or no test_patterns defined)
 * - FULL: ≥1 code match AND ≥1 test match
 */
function rateFeature(
  codeMatchCount: number,
  testMatchCount: number,
  hasTestPatterns: boolean
): FeatureRating {
  if (codeMatchCount === 0) return "MISSING";
  if (codeMatchCount === 1 && testMatchCount === 0) return "STUB";
  if (!hasTestPatterns || testMatchCount === 0) return "PARTIAL";
  return "FULL";
}

/**
 * Compute a confidence score (0–1) for a feature match.
 * Higher match counts and test coverage increase confidence.
 */
function computeConfidence(codeMatchCount: number, testMatchCount: number): number {
  if (codeMatchCount === 0) return 0;
  const codeScore = Math.min(codeMatchCount / 3, 1); // saturates at 3 files
  const testBonus = testMatchCount > 0 ? 0.3 : 0;
  return Math.min(codeScore * 0.7 + testBonus, 1);
}

/**
 * Check a single feature across a single client directory.
 */
export function checkFeatureForClient(
  feature: FeatureDefinition,
  clientName: string,
  clientDir: string
): FeatureResult {
  const clientDef =
    clientName === "vscode"
      ? feature.vscode
      : clientName === "angular"
        ? feature.angular
        : feature.flutter;

  if (!clientDef) {
    return {
      feature: feature.name,
      client: clientName,
      status: "MISSING",
      confidence: 0,
      evidence: [],
    };
  }

  const allCodeEvidence: string[] = [];
  let totalCodeMatches = 0;

  for (const pattern of clientDef.patterns) {
    const { count, evidence } = grepDirectory(pattern, clientDir);
    totalCodeMatches += count;
    allCodeEvidence.push(...evidence);
  }

  let totalTestMatches = 0;
  const testPatterns = clientDef.test_patterns ?? [];
  for (const pattern of testPatterns) {
    const { count } = grepDirectory(pattern, clientDir);
    totalTestMatches += count;
  }

  const status = rateFeature(totalCodeMatches, totalTestMatches, testPatterns.length > 0);
  const confidence = computeConfidence(totalCodeMatches, totalTestMatches);

  return {
    feature: feature.name,
    client: clientName,
    status,
    confidence,
    evidence: [...new Set(allCodeEvidence)].slice(0, 5),
  };
}

/**
 * Run feature matching for all features across all available clients.
 * Skips clients whose directories don't exist (graceful degradation).
 */
export async function matchAllFeatures(
  features: FeatureDefinition[],
  clientDirs: Record<string, string>
): Promise<FeatureResult[]> {
  const results: FeatureResult[] = [];

  for (const feature of features) {
    for (const [clientName, clientDir] of Object.entries(clientDirs)) {
      if (!existsSync(clientDir)) {
        console.warn(
          `[feature-matcher] Client directory not found: ${clientDir} — skipping ${clientName} for "${feature.name}"`
        );
        results.push({
          feature: feature.name,
          client: clientName,
          status: "MISSING",
          confidence: 0,
          evidence: [`Directory not found: ${clientDir}`],
        });
        continue;
      }

      const result = checkFeatureForClient(feature, clientName, clientDir);
      results.push(result);
    }
  }

  return results;
}

/**
 * Compute the parity score for a client:
 * (FULL_count + 0.5 * PARTIAL_count) / total_features
 */
export function computeParityScore(results: FeatureResult[]): number {
  if (results.length === 0) return 0;

  const fullCount = results.filter((r) => r.status === "FULL").length;
  const partialCount = results.filter((r) => r.status === "PARTIAL").length;

  return (fullCount + 0.5 * partialCount) / results.length;
}
