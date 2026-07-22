/**
 * Unified audit finding schema for all product audit dimensions.
 * Schema version: 1.0
 */

export interface AuditFinding {
  severity: "critical" | "high" | "medium" | "low";
  category: string;
  repo?: string;
  file?: string;
  line?: number;
  detail: string;
  auto_fixable?: boolean;
}

export interface ParityMatrix {
  features: string[];
  clients: Record<
    string,
    {
      scores: number[];
      overall_score: number;
    }
  >;
}

export interface AuditDimension {
  schema_version: "1.0";
  dimension: "documentation_accuracy" | "feature_parity";
  timestamp: string;
  score: number;
  summary: string;
  findings: AuditFinding[];
  parity_matrix?: ParityMatrix;
}

/** Feature parity rating for a single feature × client combination */
export type FeatureRating = "FULL" | "PARTIAL" | "STUB" | "MISSING";

export interface FeatureResult {
  feature: string;
  client: string;
  status: FeatureRating;
  confidence: number;
  evidence: string[];
}

/** Parsed endpoint from documentation */
export interface DocEndpoint {
  method: string;
  path: string;
  status: string;
  notes: string;
  file: string;
  line: number;
}

/** Version info extracted from a package.json */
export interface PackageVersion {
  name: string;
  version: string;
  repo: string;
  file: string;
}

/** Result of README command validation */
export interface ReadmeCommandResult {
  command: string;
  valid: boolean;
  reason?: string;
  file: string;
  line: number;
}
