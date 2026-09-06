/**
 * Comment-preserving YAML serialization for the nightgauge config tiers.
 *
 * `stringify()` from the `yaml` package renders a plain JS object, which
 * throws away every comment and blank line the file carried. That is fine for
 * a gitignored tier nobody reads, but `.nightgauge/config.yaml` is committed:
 * rewriting it from a plain object strips its header ("cloning must never
 * authorise work") and reflows the whole file, so any write — even one that
 * changes a single scalar — lands as a large diff in the working tree
 * (issue #1516).
 *
 * This module round-trips through the `yaml` Document API instead: the
 * existing text is parsed into a Document (which retains comments and
 * spacing), the desired leaves are applied with `setIn`/`deleteIn`, and the
 * Document is re-rendered. Untouched keys keep their original bytes, comments
 * included.
 *
 * No new dependency: `yaml` (eemeli) is already the extension's YAML library
 * and its Document API is comment-preserving by construction.
 */

import { parseDocument, stringify as stringifyYaml } from "yaml";

export interface YamlWriteOptions {
  indent?: number;
  lineWidth?: number;
  nullStr?: string;
}

type PlainObject = Record<string, unknown>;

interface Leaf {
  path: string[];
  value: unknown;
}

function isPlainObject(value: unknown): value is PlainObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Flatten an object into its leaf paths. Arrays and scalars are leaves; an
 * empty object is a leaf too (so `foo: {}` survives a round-trip).
 */
export function leafPaths(value: unknown, prefix: string[] = [], out: Leaf[] = []): Leaf[] {
  if (isPlainObject(value)) {
    const entries = Object.entries(value);
    if (entries.length === 0) {
      if (prefix.length > 0) out.push({ path: prefix, value: {} });
      return out;
    }
    for (const [key, child] of entries) {
      leafPaths(child, [...prefix, key], out);
    }
    return out;
  }
  if (prefix.length > 0) out.push({ path: prefix, value });
  return out;
}

/**
 * Serialize `config` as YAML, preserving the comments and layout of
 * `existingText` for every key that is not being changed.
 *
 * Falls back to a plain `stringify` when there is no existing text or the
 * existing text does not parse — a corrupt file is replaced, not merged into.
 */
export function serializePreservingComments(
  existingText: string | null | undefined,
  config: PlainObject,
  options: YamlWriteOptions = {}
): string {
  const opts = { indent: 2, lineWidth: 100, nullStr: "", ...options };

  if (!existingText || existingText.trim() === "") {
    return stringifyYaml(config, opts);
  }

  let doc;
  try {
    doc = parseDocument(existingText);
  } catch {
    return stringifyYaml(config, opts);
  }
  if (doc.errors.length > 0) {
    return stringifyYaml(config, opts);
  }

  const existingJs = doc.toJS({ maxAliasCount: -1 });
  if (!isPlainObject(existingJs)) {
    return stringifyYaml(config, opts);
  }

  const desired = leafPaths(config);
  const desiredKeys = new Set(desired.map((leaf) => JSON.stringify(leaf.path)));

  // Delete leaves the caller dropped. Longest paths first so a parent is only
  // considered once all of its children are gone.
  const stale = leafPaths(existingJs)
    .filter((leaf) => !desiredKeys.has(JSON.stringify(leaf.path)))
    .sort((a, b) => b.path.length - a.path.length);

  for (const leaf of stale) {
    doc.deleteIn(leaf.path);
    // Prune parents that the deletion emptied out.
    for (let i = leaf.path.length - 1; i > 0; i--) {
      const parentPath = leaf.path.slice(0, i);
      const parent = doc.getIn(parentPath, false) as { items?: unknown[] } | undefined;
      if (parent && Array.isArray(parent.items) && parent.items.length === 0) {
        doc.deleteIn(parentPath);
      } else {
        break;
      }
    }
  }

  for (const leaf of desired) {
    doc.setIn(leaf.path, leaf.value);
  }

  const rendered = doc.toString(opts);
  return rendered.endsWith("\n") ? rendered : `${rendered}\n`;
}
