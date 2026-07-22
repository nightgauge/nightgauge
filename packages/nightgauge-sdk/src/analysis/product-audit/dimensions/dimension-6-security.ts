/**
 * Dimension 6: Security
 *
 * Detects hardcoded secrets, XSS risks, missing .gitignore patterns,
 * and missing auth header redaction across workspace repos.
 *
 * @see Issue #2366 — Implement Dimensions 5-8: Test coverage, security, dependencies, and CI integrity
 */

import * as fs from "fs";
import * as path from "path";
import * as yaml from "js-yaml";
import {
  scanFileContent,
  filterByConfidence,
  type PatternDefinition,
} from "../utils/pattern-matcher.js";

const MIN_CONFIDENCE = 60;

export interface SecurityFinding {
  id: string;
  severity: "critical" | "high" | "medium" | "low";
  category:
    | "HARDCODED_SECRET"
    | "XSS_RISK"
    | "UNVALIDATED_INPUT"
    | "UNENCRYPTED_DATA"
    | "SENSITIVE_DATA_LOG";
  confidence: number;
  repo: string | null;
  dimension: number;
  detail: string;
  auto_fixable: boolean;
  suggested_action: string;
  files: Array<{
    path: string;
    line: number | null;
    code_snippet: string | null;
  }>;
  metadata: {
    detected_at: string;
    detection_method: "pattern_match" | "static_analysis";
    manual_review_required: boolean;
  };
  // SecurityFinding fields
  security_category: SecurityFinding["category"];
  cve_reference: null;
  affected_code: string | null;
}

export interface SecurityConfig {
  secret_patterns: PatternDefinition[];
  xss_patterns: PatternDefinition[];
  gitignore_required_patterns: string[];
  auth_redaction_patterns: string[];
  file_exclusions: string[];
}

export interface Dimension6Result {
  findings: SecurityFinding[];
  repos_scanned: string[];
  repos_missing: string[];
  warnings: string[];
}

/**
 * Load security patterns from patterns.yaml.
 * Returns a default config if the file cannot be read.
 */
function loadSecurityConfig(configDir: string): SecurityConfig {
  const configPath = path.join(configDir, "patterns.yaml");
  try {
    const content = fs.readFileSync(configPath, "utf8");
    const raw = yaml.load(content) as SecurityConfig;
    return raw;
  } catch {
    // Fallback: minimal defaults
    return {
      secret_patterns: [
        {
          id: "stripe_live",
          pattern: "sk_live_[A-Za-z0-9]{20,}",
          confidence: 98,
          severity: "critical",
          description: "Stripe live secret key",
        },
        {
          id: "ib_live",
          pattern: "ib_live_[A-Za-z0-9]{20,}",
          confidence: 98,
          severity: "critical",
          description: "Nightgauge live API key",
        },
        {
          id: "aws_key",
          pattern: "AKIA[0-9A-Z]{16}",
          confidence: 95,
          severity: "critical",
          description: "AWS access key",
        },
      ],
      xss_patterns: [
        {
          id: "inner_html",
          pattern: "\\.innerHTML\\s*=",
          confidence: 80,
          severity: "high",
          description: "innerHTML assignment",
        },
        {
          id: "dangerous_html",
          pattern: "dangerouslySetInnerHTML",
          confidence: 75,
          severity: "medium",
          description: "React dangerouslySetInnerHTML",
        },
      ],
      gitignore_required_patterns: [".env", "*.pem", "*.key", "credentials*"],
      auth_redaction_patterns: ["redact", "sanitize"],
      file_exclusions: ["node_modules/", "dist/", ".git/"],
    };
  }
}

/**
 * Check if a file should be excluded from security scanning.
 */
function isExcluded(filePath: string, exclusions: string[]): boolean {
  const normalized = filePath.replace(/\\/g, "/");
  return exclusions.some((pattern) => {
    if (pattern.endsWith("/")) {
      return normalized.includes(pattern);
    }
    if (pattern.startsWith("*.")) {
      return normalized.endsWith(pattern.slice(1));
    }
    return normalized.includes(pattern);
  });
}

/**
 * Collect all source files in a repo for scanning.
 * Limited to TypeScript/JavaScript to avoid false positives.
 */
function collectSourceFiles(repoRoot: string, exclusions: string[]): string[] {
  const extensions = [".ts", ".tsx", ".js", ".jsx", ".mts", ".mjs"];
  const results: string[] = [];

  function walk(dir: string, depth: number): void {
    if (depth > 8) return; // Avoid infinite recursion
    let entries: string[];
    try {
      entries = fs.readdirSync(dir);
    } catch {
      return;
    }
    for (const entry of entries) {
      const full = path.join(dir, entry);
      const rel = path.relative(repoRoot, full);
      if (isExcluded(rel + (entry.includes(".") ? "" : "/"), exclusions)) {
        continue;
      }
      let stat: fs.Stats;
      try {
        stat = fs.statSync(full);
      } catch {
        continue;
      }
      if (stat.isDirectory()) {
        walk(full, depth + 1);
      } else if (extensions.includes(path.extname(entry).toLowerCase())) {
        results.push(full);
      }
    }
  }

  walk(repoRoot, 0);
  return results;
}

/**
 * Check .gitignore covers all required secret file patterns.
 */
function checkGitignore(repoRoot: string, requiredPatterns: string[]): Array<{ missing: string }> {
  const gitignorePath = path.join(repoRoot, ".gitignore");
  if (!fs.existsSync(gitignorePath)) {
    return requiredPatterns.map((p) => ({ missing: p }));
  }

  let content: string;
  try {
    content = fs.readFileSync(gitignorePath, "utf8");
  } catch {
    return requiredPatterns.map((p) => ({ missing: p }));
  }

  const lines = content.split("\n").map((l) => l.trim());
  const missing: Array<{ missing: string }> = [];

  for (const required of requiredPatterns) {
    const covered = lines.some((l) => l === required || l.includes(required.replaceAll("*", "")));
    if (!covered) missing.push({ missing: required });
  }

  return missing;
}

/**
 * Check whether auth middleware/interceptors include redaction logic.
 * Returns false if no redaction found in auth-adjacent files.
 */
function checkAuthRedaction(repoRoot: string, redactionPatterns: string[]): boolean {
  const authDirs = ["middleware", "interceptors", "auth", "services/auth"];
  const extensions = [".ts", ".js"];

  for (const dir of authDirs) {
    const dirPath = path.join(repoRoot, "src", dir);
    if (!fs.existsSync(dirPath)) continue;
    let entries: string[];
    try {
      entries = fs.readdirSync(dirPath);
    } catch {
      continue;
    }
    for (const entry of entries) {
      if (!extensions.includes(path.extname(entry))) continue;
      try {
        const content = fs.readFileSync(path.join(dirPath, entry), "utf8").toLowerCase();
        if (redactionPatterns.some((p) => content.includes(p.toLowerCase()))) {
          return true;
        }
      } catch {
        // Unreadable — skip
      }
    }
  }

  return false;
}

let globalSeq = 0;
function nextId(slug: string): string {
  globalSeq++;
  return `security-${String(globalSeq).padStart(3, "0")}-${slug.slice(0, 25)}`;
}

/**
 * Run Dimension 6: Security analysis.
 */
export async function runDimension6(
  repos: Array<{ name: string; root: string }>,
  configDir?: string
): Promise<Dimension6Result> {
  globalSeq = 0;
  const resolvedConfigDir = configDir ?? path.resolve(__dirname, "..", "config");

  const config = loadSecurityConfig(resolvedConfigDir);
  const findings: SecurityFinding[] = [];
  const reposScanned: string[] = [];
  const reposMissing: string[] = [];
  const warnings: string[] = [];

  for (const repo of repos) {
    if (!fs.existsSync(repo.root)) {
      reposMissing.push(repo.name);
      warnings.push(`Repo not found: ${repo.name} — skipping security scan`);
      continue;
    }

    reposScanned.push(repo.name);

    // --- Secret scanning ---
    const sourceFiles = collectSourceFiles(repo.root, config.file_exclusions);
    for (const filePath of sourceFiles) {
      let content: string;
      try {
        content = fs.readFileSync(filePath, "utf8");
      } catch {
        continue;
      }

      const relPath = path.relative(repo.root, filePath);

      // Scan for secrets
      const secretMatches = filterByConfidence(
        scanFileContent(relPath, content, config.secret_patterns),
        MIN_CONFIDENCE
      );

      for (const m of secretMatches) {
        findings.push({
          id: nextId("hardcoded-secret"),
          severity: m.severity,
          category: "HARDCODED_SECRET",
          confidence: m.confidence,
          repo: repo.name as SecurityFinding["repo"],
          dimension: 6,
          detail: `Possible hardcoded secret in ${relPath}:${m.line} — pattern: ${m.patternId}`,
          auto_fixable: false,
          suggested_action:
            "Move secret to environment variable or secrets manager. Remove from source control history.",
          files: [
            {
              path: relPath,
              line: m.line,
              code_snippet: m.context.slice(0, 500),
            },
          ],
          metadata: {
            detected_at: new Date().toISOString(),
            detection_method: "pattern_match",
            manual_review_required: true,
          },
          security_category: "HARDCODED_SECRET",
          cve_reference: null,
          affected_code: m.matchedText,
        });
      }

      // Scan for XSS risks
      const xssMatches = filterByConfidence(
        scanFileContent(relPath, content, config.xss_patterns),
        MIN_CONFIDENCE
      );

      for (const m of xssMatches) {
        findings.push({
          id: nextId("xss-risk"),
          severity: m.severity,
          category: "XSS_RISK",
          confidence: m.confidence,
          repo: repo.name as SecurityFinding["repo"],
          dimension: 6,
          detail: `XSS risk: ${m.patternId} in ${relPath}:${m.line}`,
          auto_fixable: false,
          suggested_action:
            "Use safe DOM APIs (textContent, createElement) or sanitize HTML with DOMPurify before inserting.",
          files: [
            {
              path: relPath,
              line: m.line,
              code_snippet: m.context.slice(0, 500),
            },
          ],
          metadata: {
            detected_at: new Date().toISOString(),
            detection_method: "pattern_match",
            manual_review_required: true,
          },
          security_category: "XSS_RISK",
          cve_reference: null,
          affected_code: m.matchedText,
        });
      }
    }

    // --- .gitignore coverage check ---
    const missingGitignore = checkGitignore(repo.root, config.gitignore_required_patterns);
    for (const { missing } of missingGitignore) {
      findings.push({
        id: nextId("gitignore-missing"),
        severity: "medium",
        category: "HARDCODED_SECRET",
        confidence: 85,
        repo: repo.name as SecurityFinding["repo"],
        dimension: 6,
        detail: `${repo.name}/.gitignore does not cover pattern: ${missing}`,
        auto_fixable: false,
        suggested_action: `Add '${missing}' to .gitignore to prevent accidental secret commits.`,
        files: [{ path: ".gitignore", line: null, code_snippet: null }],
        metadata: {
          detected_at: new Date().toISOString(),
          detection_method: "static_analysis",
          manual_review_required: false,
        },
        security_category: "HARDCODED_SECRET",
        cve_reference: null,
        affected_code: null,
      });
    }

    // --- Auth redaction check ---
    const hasRedaction = checkAuthRedaction(repo.root, config.auth_redaction_patterns);
    if (!hasRedaction) {
      // Only warn for repos that have auth-related code
      const hasAuthDir = ["src/middleware", "src/interceptors", "src/auth"].some((d) =>
        fs.existsSync(path.join(repo.root, d))
      );
      if (hasAuthDir) {
        findings.push({
          id: nextId("auth-no-redact"),
          severity: "medium",
          category: "SENSITIVE_DATA_LOG",
          confidence: 65,
          repo: repo.name as SecurityFinding["repo"],
          dimension: 6,
          detail: `No auth header redaction found in ${repo.name} auth middleware/interceptors`,
          auto_fixable: false,
          suggested_action:
            "Ensure Authorization headers are redacted in logs and error responses. Add redact() calls in auth middleware.",
          files: [{ path: "src/middleware", line: null, code_snippet: null }],
          metadata: {
            detected_at: new Date().toISOString(),
            detection_method: "static_analysis",
            manual_review_required: true,
          },
          security_category: "SENSITIVE_DATA_LOG",
          cve_reference: null,
          affected_code: null,
        });
      }
    }
  }

  return {
    findings,
    repos_scanned: reposScanned,
    repos_missing: reposMissing,
    warnings,
  };
}
