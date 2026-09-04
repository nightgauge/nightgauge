import { z } from "zod";

/**
 * KnowledgeTypeSchema — the entry types this codebase scaffolds and routes on.
 *
 * Values:
 * - prd: A product requirements document (PRD.md)
 * - decisions: The per-issue decision log (decisions.md)
 * - adr: A single formal architecture decision record
 * - architecture: A repo-wide architectural pattern or principle
 * - glossary: A domain term definition
 * - runbook: An operational procedure
 * - post-mortem: An incident write-up
 * - conversation: A recorded design conversation
 * - reference: External reference or link
 * - note: Freeform technical note
 *
 * This enum is advisory: it names the types the pipeline itself produces and
 * knows how to file. The frontmatter contract in KnowledgeEntrySchema accepts
 * ANY non-empty `type` string, matching the Go parser, because an OKF consumer
 * tolerates a type it does not understand rather than rejecting the entry.
 *
 * A plain enum, deliberately not flexEnum: that helper normalizes hyphens to
 * underscores, which would rewrite `post-mortem` into a value the Go binary
 * never writes and split the vocabulary across the two layers.
 */
export const KnowledgeTypeSchema = z.enum([
  "prd",
  "decisions",
  "adr",
  "architecture",
  "glossary",
  "runbook",
  "post-mortem",
  "conversation",
  "reference",
  "note",
]);

export type KnowledgeType = z.infer<typeof KnowledgeTypeSchema>;

/**
 * ActorSchema — the actor convention shared by `generated.by` and
 * `verified[].by`.
 *
 * `<producer>/<version>` for agents (e.g. `feature-dev/claude-sonnet-5`),
 * `human:<id>` for a person, `process:<id>` for a deterministic writer.
 * Mirrors actorRe in internal/knowledge/okf/okf.go — every actor the binary
 * writes is built from stage and model identifiers, never from model prose.
 */
export const ActorSchema = z
  .string()
  .regex(/^([a-z0-9._-]+\/[A-Za-z0-9._-]+|human:\S+|process:\S+)$/, {
    message: "actor must be <producer>/<version>, human:<id> or process:<id>",
  });

/**
 * ProvenanceSchema — who produced or confirmed an entry, and when.
 *
 * The OKF v0.2 `generated` object, and each element of `verified`.
 */
export const ProvenanceSchema = z
  .object({
    /** Actor that produced or confirmed the entry */
    by: ActorSchema,
    /** ISO 8601 datetime of the event */
    at: z.string().datetime().optional(),
  })
  .passthrough();

export type Provenance = z.infer<typeof ProvenanceSchema>;

/**
 * SourceSchema — material an entry was derived from.
 */
export const SourceSchema = z
  .object({
    /** https:// URL, bundle-absolute path, or repository-relative path */
    resource: z.string().min(1),
    /** Optional human-readable label */
    title: z.string().optional(),
  })
  .passthrough();

export type Source = z.infer<typeof SourceSchema>;

/**
 * KnowledgeStatusSchema — lifecycle status of a knowledge entry.
 *
 * `superseded` was deleted in favour of `deprecated` alongside
 * `superseded_by`; it is rejected rather than aliased.
 */
export const KnowledgeStatusSchema = z.enum(["draft", "stable", "deprecated"]);

export type KnowledgeStatus = z.infer<typeof KnowledgeStatusSchema>;

/** Status an entry has when its frontmatter omits the field. */
export const DEFAULT_KNOWLEDGE_STATUS: KnowledgeStatus = "stable";

/** Open Knowledge Format revision whose field vocabulary this contract implements. */
export const OKF_VERSION = "0.2";

/**
 * KnowledgeEntrySchema — the single knowledge frontmatter contract.
 *
 * Every non-reserved `.md` under the knowledge root carries this block, with
 * `type` required. The field set mirrors FrontmatterBlock in
 * internal/knowledge/okf/frontmatter.go exactly — the two layers had drifted
 * into disagreeing contracts, and this schema is the TypeScript half of the
 * one that replaced them.
 *
 * Unknown keys pass through: an OKF consumer tolerates what it does not
 * understand. `created`, `updated` and `superseded` are deleted — `generated`
 * carries the write timestamp and `verified` carries the review history.
 *
 * Schema version: 2.0
 *
 * @see docs/KNOWLEDGE_BASE.md for the contract and the actor convention
 */
export const KnowledgeEntrySchema = z
  .object({
    /** Entry kind. Required; unknown values are accepted for forward compatibility. */
    type: z.string().min(1),
    /** Human-readable title of the knowledge entry */
    title: z.string().min(1).optional(),
    /** One-line summary used in generated index pages */
    description: z.string().optional(),
    /** Optional topic tags for discovery */
    tags: z.array(z.string()).optional(),
    /** Related issue/PR references, e.g. ['#2090', '#2091'] */
    related: z.array(z.string()).optional(),
    /** Repository slugs this entry is scoped to; empty means workspace-wide */
    repos: z.array(z.string()).optional(),
    /** Lifecycle status; absent means DEFAULT_KNOWLEDGE_STATUS */
    status: KnowledgeStatusSchema.optional(),
    /** Reference to the entry that replaces this one (with status=deprecated) */
    superseded_by: z.string().optional(),
    /** Actor that produced this entry, and when */
    generated: ProvenanceSchema.optional(),
    /** Confirmation events on this entry, oldest first */
    verified: z.array(ProvenanceSchema).optional(),
    /** Material this entry was derived from */
    sources: z.array(SourceSchema).optional(),
    /** ISO 8601 datetime past which the entry is no longer current guidance */
    stale_after: z.string().datetime().optional(),
  })
  .passthrough();

export type KnowledgeEntry = z.infer<typeof KnowledgeEntrySchema>;

/**
 * KnowledgeIndexSchema — validates the auto-generated README index structure.
 *
 * The index groups knowledge entries by category (epics, features) and
 * provides a directory listing for the knowledge base. The categories field
 * uses z.record for forward compatibility with future categories beyond
 * epics/ and features/ (e.g., glossary/, architecture/).
 *
 * Schema version: 1.0
 *
 * @see docs/KNOWLEDGE_BASE.md for knowledge directory structure
 */
export const KnowledgeIndexSchema = z
  .object({
    /** Total number of knowledge entries in the index */
    total_entries: z.number().int().min(0),
    /** ISO 8601 datetime when the index was last generated */
    generated_at: z.string().datetime(),
    /** Entries grouped by category directory */
    categories: z.record(
      z.string(),
      z.array(
        z
          .object({
            /** Issue number for this knowledge entry */
            issue_number: z.number().int().positive(),
            /** Slug portion of the directory name */
            slug: z.string().min(1),
            /** Full directory path relative to workspace root */
            path: z.string().min(1),
            /** Files present in this knowledge directory */
            files: z.array(z.string()),
            /** Optional entry metadata (present when index includes full metadata) */
            entry: KnowledgeEntrySchema.optional(),
          })
          .passthrough()
      )
    ),
  })
  .passthrough();

export type KnowledgeIndex = z.infer<typeof KnowledgeIndexSchema>;

/**
 * RepoTopicTypeSchema — enum of repo-topic knowledge category types.
 *
 * These correspond to flat-file categories under .nightgauge/knowledge/
 * that hold repo-wide reference material (not per-issue entries).
 *
 * - architecture: Cross-issue architectural principles and patterns
 * - glossary: One-file-per-term domain vocabulary definitions
 * - runbook: Operational procedures for recurring tasks
 * - post-mortem: Incident write-ups and retrospective analyses
 */
export const RepoTopicTypeSchema = z.enum(["architecture", "glossary", "runbook", "post-mortem"]);

export type RepoTopicType = z.infer<typeof RepoTopicTypeSchema>;

/**
 * Trust tiers rank an entry by who has confirmed it.
 *
 * The tier is DERIVED from the `verified` log, never declared — an entry
 * cannot assert its own trust. Mirrors the constants and the derivation in
 * internal/knowledge/okf/lifecycle.go.
 */
export const TRUST_HUMAN_REVIEWED = "human-reviewed";
export const TRUST_MACHINE_CONFIRMED = "machine-confirmed";
export const TRUST_UNVERIFIED = "unverified";

export type TrustTier =
  typeof TRUST_HUMAN_REVIEWED | typeof TRUST_MACHINE_CONFIRMED | typeof TRUST_UNVERIFIED;

/**
 * Derive an entry's trust tier from its verification log. The highest tier
 * present wins: one human event outranks any number of machine ones.
 *
 * Actor matching is on the `human:` PREFIX, not a substring. An actor like
 * `feature-dev/human-review-model` is a stage, not a person, and a substring
 * match would silently promote it to the top tier — which would make the
 * "unverified backlog" a maintainer reviews quietly wrong.
 */
export function trustTierOf(entry: Pick<KnowledgeEntry, "verified"> | null | undefined): TrustTier {
  const verified = entry?.verified;
  if (!verified || verified.length === 0) return TRUST_UNVERIFIED;
  for (const event of verified) {
    if (typeof event?.by === "string" && event.by.startsWith("human:") && event.by.length > 6) {
      return TRUST_HUMAN_REVIEWED;
    }
  }
  return TRUST_MACHINE_CONFIRMED;
}

/**
 * Read the trust tier straight out of a markdown file's frontmatter.
 *
 * Deliberately tolerant: a file with no block, an unparseable block, or a
 * block the schema rejects reads as `unverified` rather than throwing. It
 * exists in the base and nothing has confirmed it, which is exactly what that
 * tier means.
 */
export function trustTierOfContent(content: string): TrustTier {
  if (!content.startsWith("---\n") && !content.startsWith("---\r\n")) {
    return TRUST_UNVERIFIED;
  }
  const endIndex = content.indexOf("\n---", 3);
  if (endIndex === -1) return TRUST_UNVERIFIED;

  try {
    // Only the `verified` list matters here, and it is a list of {by, at}
    // mappings — a targeted scan avoids pulling a YAML parser into the
    // extension's synchronous tree-rendering path.
    const block = content.slice(4, endIndex);
    const actors = block.match(/^\s*-\s*by:\s*(\S+)/gm) ?? [];
    if (actors.length === 0) return TRUST_UNVERIFIED;
    for (const line of actors) {
      const by = line.replace(/^\s*-\s*by:\s*/, "").replace(/^["']|["']$/g, "");
      if (by.startsWith("human:") && by.length > 6) return TRUST_HUMAN_REVIEWED;
    }
    return TRUST_MACHINE_CONFIRMED;
  } catch {
    return TRUST_UNVERIFIED;
  }
}
