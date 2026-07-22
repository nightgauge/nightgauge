import { existsSync, writeFileSync, mkdirSync } from "fs";
import { join, resolve, dirname } from "path";
import type { AuditDimension, AuditFinding, ParityMatrix } from "../types.js";
import {
  loadFeatureDefinitions,
  matchAllFeatures,
  computeParityScore,
} from "../matchers/feature-matcher.js";
import type { FeatureResult } from "../types.js";

export interface Dimension4Options {
  /** Path to features.yaml (default: .nightgauge/audit/features.yaml) */
  featuresYamlPath?: string;
  /** Root workspace directory (default: one level above cwd) */
  workspaceRoot?: string;
  /** Override client directories (useful for testing) */
  clientDirs?: Record<string, string>;
}

/** Client names and their source subdirectories within the repo. */
const CLIENT_SOURCE_DIRS: Record<string, string[]> = {
  vscode: ["packages/nightgauge-vscode/src"],
  angular: ["src", "src/app"],
  flutter: ["lib"],
};

/**
 * Resolve the source directory for a client repo.
 * Returns the first existing candidate directory.
 */
function resolveClientDir(repoPath: string, clientName: string): string | null {
  const candidates = CLIENT_SOURCE_DIRS[clientName] ?? ["src"];
  for (const subdir of candidates) {
    const full = join(repoPath, subdir);
    if (existsSync(full)) return full;
  }
  // Fall back to repo root if no src dir found
  if (existsSync(repoPath)) return repoPath;
  return null;
}

/**
 * Discover client directories from workspace.
 * Returns a map of client name → directory path.
 * Gracefully skips clients whose repos aren't present.
 */
function discoverClientDirs(
  workspaceRoot: string,
  overrides?: Record<string, string>
): Record<string, string> {
  const repoMap: Record<string, string> = {
    vscode: join(workspaceRoot, "nightgauge"),
    angular: join(workspaceRoot, "acme-dashboard"),
    flutter: join(workspaceRoot, "acme-mobile"),
  };

  const clientDirs: Record<string, string> = {};

  for (const [client, repoPath] of Object.entries(repoMap)) {
    const override = overrides?.[client];
    if (override) {
      clientDirs[client] = override;
      continue;
    }

    if (!existsSync(repoPath)) {
      console.warn(
        `[dimension-4] Repo not found for client "${client}": ${repoPath} — will mark all features as MISSING`
      );
      clientDirs[client] = repoPath; // Keep path so feature-matcher logs the skip
      continue;
    }

    const srcDir = resolveClientDir(repoPath, client);
    if (srcDir) {
      clientDirs[client] = srcDir;
    } else {
      clientDirs[client] = repoPath;
    }
  }

  return clientDirs;
}

/**
 * Group feature results by client name.
 */
function groupByClient(results: FeatureResult[]): Map<string, FeatureResult[]> {
  const map = new Map<string, FeatureResult[]>();
  for (const r of results) {
    const existing = map.get(r.client) ?? [];
    existing.push(r);
    map.set(r.client, existing);
  }
  return map;
}

/** Rating weights for markdown display. */
const RATING_EMOJI: Record<string, string> = {
  FULL: "✅",
  PARTIAL: "🟡",
  STUB: "🔶",
  MISSING: "❌",
};

/**
 * Generate a markdown matrix table from feature results.
 */
function generateMarkdownMatrix(
  featureNames: string[],
  clientNames: string[],
  resultsByClient: Map<string, FeatureResult[]>
): string {
  const header =
    `| Feature | ${clientNames.join(" | ")} |\n` +
    `|---------|${clientNames.map(() => "---------").join("|")}|`;

  const rows = featureNames.map((featureName) => {
    const cells = clientNames.map((client) => {
      const clientResults = resultsByClient.get(client) ?? [];
      const result = clientResults.find((r) => r.feature === featureName);
      const status = result?.status ?? "MISSING";
      const emoji = RATING_EMOJI[status] ?? "❓";
      return `${emoji} ${status}`;
    });
    return `| ${featureName} | ${cells.join(" | ")} |`;
  });

  return [header, ...rows].join("\n");
}

/**
 * Build the parity_matrix object from results.
 */
function buildParityMatrix(
  featureNames: string[],
  clientNames: string[],
  resultsByClient: Map<string, FeatureResult[]>
): ParityMatrix {
  const clients: ParityMatrix["clients"] = {};

  for (const client of clientNames) {
    const clientResults = resultsByClient.get(client) ?? [];
    const scores = featureNames.map((featureName) => {
      const result = clientResults.find((r) => r.feature === featureName);
      const status = result?.status ?? "MISSING";
      return status === "FULL" ? 1 : status === "PARTIAL" ? 0.5 : status === "STUB" ? 0.25 : 0;
    });

    const overallScore = computeParityScore(
      clientResults.length > 0
        ? clientResults
        : featureNames.map((f) => ({
            feature: f,
            client,
            status: "MISSING" as const,
            confidence: 0,
            evidence: [],
          }))
    );

    clients[client] = { scores, overall_score: overallScore };
  }

  return { features: featureNames, clients };
}

/**
 * Compute a 0–100 score based on the average parity score across all clients.
 */
function computeOverallScore(parityMatrix: ParityMatrix): number {
  const clientScores = Object.values(parityMatrix.clients).map((c) => c.overall_score);
  if (clientScores.length === 0) return 0;
  const avg = clientScores.reduce((a, b) => a + b, 0) / clientScores.length;
  return Math.round(avg * 100);
}

/**
 * Generate findings for features with low coverage.
 */
function generateParityFindings(
  featureNames: string[],
  clientNames: string[],
  resultsByClient: Map<string, FeatureResult[]>
): AuditFinding[] {
  const findings: AuditFinding[] = [];

  for (const featureName of featureNames) {
    for (const client of clientNames) {
      const clientResults = resultsByClient.get(client) ?? [];
      const result = clientResults.find((r) => r.feature === featureName);
      const status = result?.status ?? "MISSING";

      if (status === "MISSING") {
        findings.push({
          severity: "high",
          category: "FEATURE_MISSING",
          repo: `nightgauge-${client === "vscode" ? "vscode" : client}`,
          detail: `Feature "${featureName}" is not implemented in the ${client} client.`,
          auto_fixable: false,
        });
      } else if (status === "STUB") {
        findings.push({
          severity: "medium",
          category: "FEATURE_STUB",
          repo: `nightgauge-${client === "vscode" ? "vscode" : client}`,
          detail: `Feature "${featureName}" is stubbed in the ${client} client — code skeleton exists but may not be functional or tested.`,
          auto_fixable: false,
        });
      } else if (status === "PARTIAL") {
        findings.push({
          severity: "low",
          category: "FEATURE_PARTIAL",
          repo: `nightgauge-${client === "vscode" ? "vscode" : client}`,
          detail: `Feature "${featureName}" is partially implemented in the ${client} client — code found but tests are missing.`,
          auto_fixable: false,
        });
      }
    }
  }

  return findings;
}

/**
 * Run Dimension 4: Feature Parity Matrix.
 *
 * Analyzes:
 * - Feature implementation status per client (VSCode, Angular, Flutter)
 * - Rating: FULL / PARTIAL / STUB / MISSING
 * - Parity scores per client
 * - Markdown matrix table
 */
export async function runDimension4(options: Dimension4Options = {}): Promise<AuditDimension> {
  const cwd = process.cwd();
  const workspaceRoot = options.workspaceRoot ?? resolve(cwd, "..");

  const featuresYamlPath =
    options.featuresYamlPath ?? join(cwd, ".nightgauge", "audit", "features.yaml");

  // Phase 1: Load feature definitions
  const features = loadFeatureDefinitions(featuresYamlPath);
  if (features.length === 0) {
    return {
      schema_version: "1.0",
      dimension: "feature_parity",
      timestamp: new Date().toISOString(),
      score: 0,
      summary: `No features defined in ${featuresYamlPath}. Create or populate the features.yaml to enable parity analysis.`,
      findings: [],
    };
  }

  // Phase 2: Discover client directories
  const clientDirs = discoverClientDirs(workspaceRoot, options.clientDirs);

  console.log(
    `[dimension-4] Checking ${features.length} features across clients: ${Object.keys(clientDirs).join(", ")}`
  );

  // Phase 3: Run feature matching for all features × clients
  const allResults = await matchAllFeatures(features, clientDirs);

  // Phase 4: Build matrix
  const featureNames = features.map((f) => f.name);
  const clientNames = Object.keys(clientDirs);
  const resultsByClient = groupByClient(allResults);

  const markdownMatrix = generateMarkdownMatrix(featureNames, clientNames, resultsByClient);
  const parityMatrix = buildParityMatrix(featureNames, clientNames, resultsByClient);

  // Phase 5: Generate findings and score
  const findings = generateParityFindings(featureNames, clientNames, resultsByClient);
  const score = computeOverallScore(parityMatrix);

  const clientSummaries = clientNames
    .map((c) => {
      const overall = parityMatrix.clients[c]?.overall_score ?? 0;
      return `${c}: ${Math.round(overall * 100)}%`;
    })
    .join(", ");

  const summary = `Feature parity analysis of ${features.length} features across ${clientNames.length} clients. Score: ${score}/100. Client scores — ${clientSummaries}.\n\n${markdownMatrix}`;

  return {
    schema_version: "1.0",
    dimension: "feature_parity",
    timestamp: new Date().toISOString(),
    score,
    summary,
    findings,
    parity_matrix: parityMatrix,
  };
}

/**
 * Write dimension output to disk as JSON.
 */
export function writeDimensionOutput(result: AuditDimension, outputPath: string): void {
  mkdirSync(dirname(outputPath), { recursive: true });
  writeFileSync(outputPath, JSON.stringify(result, null, 2) + "\n", "utf-8");
  console.log(`[dimension-4] Output written: ${outputPath}`);
}
