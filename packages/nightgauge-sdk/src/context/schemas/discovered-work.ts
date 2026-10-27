import { z } from "zod";

/**
 * Schema for discovered-work records and the per-run discovered-work-{N}.json
 * capture file (issue #478, ADR 027).
 *
 * A record is an issue candidate a stage found but must not act on in its own
 * run. Stages append records through `nightgauge discovered-work record`; the
 * #480 materializer files them as issues and is the only deleter.
 *
 * The Go peer is `internal/cmd/discoveredwork` (Record / Capture). Both sides
 * pin their key sets against `internal/cmd/discoveredwork/testdata/record-v1.json`,
 * so a key added on one side only fails a test on both.
 *
 * Schema versions:
 * - 1.0: Initial schema (issue #478)
 *
 * @see docs/CONTEXT_ARCHITECTURE.md — "Discovered-Work Capture File" section
 * @see docs/decisions/027-discovered-work-substrate.md
 */

export const DiscoveredWorkStageSchema = z.enum([
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
]);
export type DiscoveredWorkStage = z.infer<typeof DiscoveredWorkStageSchema>;

/** A non-skill producer, e.g. `producer:post-merge-impact-set`. */
export const DiscoveredWorkProducerSchema = z
  .string()
  .regex(/^producer:[a-z0-9]+(-[a-z0-9]+)*$/, "must be producer:<kebab-name>");

// Closed enums shared with spike.ValidateSchema (ADR 027).
export const DiscoveredWorkTypeSchema = z.enum(["feature", "bug", "docs", "chore", "spike"]);
export const DiscoveredWorkPrioritySchema = z.enum(["critical", "high", "medium", "low"]);
export const DiscoveredWorkSizeSchema = z.enum(["XS", "S", "M", "L", "XL"]);

/** Repository-relative path hint; never opened by the primitive. */
const CandidateTargetFileSchema = z
  .string()
  .min(1)
  .max(256)
  .refine((p) => !p.includes("\0"), "must not contain NUL")
  .refine((p) => !p.startsWith("-"), "must not start with '-'")
  .refine(
    (p) => !p.startsWith("/") && !p.startsWith("\\") && !/^[A-Za-z]:/.test(p),
    "must be repository-relative, not absolute"
  )
  .refine((p) => !normalizedSegments(p).includes(".."), "must not contain a '..' segment");

/** Segments after the same lexical clean Go's filepath.Clean applies. */
// C0/C1 controls, DEL and bidi overrides; mirrors Go checkPrintable.
// eslint-disable-next-line no-control-regex
const CONTROL_OR_BIDI = /[\u0000-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/;
const noControl = (s: string) => !CONTROL_OR_BIDI.test(s);
const noControlMultiline = (s: string) => !CONTROL_OR_BIDI.test(s.replace(/[\n\t]/g, ""));

function normalizedSegments(p: string): string[] {
  const out: string[] = [];
  for (const seg of p.replace(/\\/g, "/").split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === ".." && out.length > 0 && out[out.length - 1] !== "..") {
      out.pop();
      continue;
    }
    out.push(seg);
  }
  return out;
}

export const DiscoveredWorkRecordSchema = z
  .object({
    id: z.string().regex(/^dw-[0-9a-f]{12}$/),
    summary: z
      .string()
      .min(1)
      .max(200)
      .refine((s) => s.trim() === s && s.trim() !== "", "must be trimmed and non-empty")
      .refine((s) => !/[\r\n]/.test(s), "must be a single line")
      .refine(noControl, "must not contain control or bidi characters"),
    evidence: z
      .array(
        z
          .string()
          .min(1)
          .max(2000)
          .refine(noControlMultiline, "must not contain control or bidi characters")
      )
      .min(1)
      .max(10),
    source_stage: z.union([DiscoveredWorkStageSchema, DiscoveredWorkProducerSchema]),
    issue_number: z.number().int().positive(),
    run_id: z.string().min(1).nullable(),
    suggested_type: DiscoveredWorkTypeSchema,
    suggested_priority: DiscoveredWorkPrioritySchema,
    suggested_size: DiscoveredWorkSizeSchema,
    candidate_target_files: z.array(CandidateTargetFileSchema).max(20),
    recorded_at: z.string().datetime(),
  })
  .strict();
export type DiscoveredWorkRecord = z.infer<typeof DiscoveredWorkRecordSchema>;

export const DiscoveredWorkContextSchema = z
  .object({
    schema_version: z.literal("1.0"),
    issue_number: z.number().int().positive(),
    records: z.array(DiscoveredWorkRecordSchema),
  })
  .strict()
  .superRefine((c, ctx) => {
    const seen = new Set<string>();
    c.records.forEach((r, i) => {
      if (r.issue_number !== c.issue_number) {
        ctx.addIssue({
          code: "custom",
          path: ["records", i, "issue_number"],
          message: "must match the envelope's issue_number",
        });
      }
      if (seen.has(r.id)) {
        ctx.addIssue({
          code: "custom",
          path: ["records", i, "id"],
          message: "duplicate id",
        });
      }
      seen.add(r.id);
    });
  });
export type DiscoveredWorkContext = z.infer<typeof DiscoveredWorkContextSchema>;
