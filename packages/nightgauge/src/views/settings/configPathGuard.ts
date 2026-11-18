/**
 * configPathGuard - decide whether a dotted settings path names a field of the
 * Nightgauge config schema.
 *
 * The settings webview addresses every field by a dotted path
 * (`pipeline.max_concurrent`, `pipeline.stage_adapters.feature-dev`). The host
 * only writes paths that the Zod config schema (config/schema.ts, the source of
 * truth for config fields) defines, so a message cannot create keys outside the
 * schema. Record schemas accept any key segment and continue into their value
 * schema; every other segment must be a declared object key.
 */

import type { z } from "zod";
import { NightgaugeConfigSchema } from "../../config/schema";

/** A path segment: a config key, never a prototype member or an empty string. */
const SEGMENT = /^[A-Za-z0-9_-]+$/;
const FORBIDDEN_SEGMENTS = new Set(["__proto__", "prototype", "constructor"]);

/** Maximum nesting this walk follows; the schema is far shallower. */
const MAX_DEPTH = 12;

interface ZodDefLike {
  type: string;
  shape?: Record<string, z.ZodType>;
  valueType?: z.ZodType;
  innerType?: z.ZodType;
  in?: z.ZodType;
  getter?: () => z.ZodType;
  options?: z.ZodType[];
  left?: z.ZodType;
  right?: z.ZodType;
}

function defOf(schema: z.ZodType): ZodDefLike {
  return (schema as unknown as { def: ZodDefLike }).def;
}

/** Schemas the path walk may continue into from `schema`, after unwrapping. */
function unwrap(schema: z.ZodType, depth = 0): z.ZodType[] {
  if (depth > MAX_DEPTH) return [];
  const def = defOf(schema);
  switch (def.type) {
    case "optional":
    case "nullable":
    case "default":
    case "prefault":
    case "readonly":
    case "catch":
    case "nonoptional":
      return def.innerType ? unwrap(def.innerType, depth + 1) : [];
    case "pipe":
      return def.in ? unwrap(def.in, depth + 1) : [];
    case "lazy":
      return def.getter ? unwrap(def.getter(), depth + 1) : [];
    case "union":
      return (def.options ?? []).flatMap((o) => unwrap(o, depth + 1));
    case "intersection":
      return [def.left, def.right].flatMap((s) => (s ? unwrap(s, depth + 1) : []));
    default:
      return [schema];
  }
}

function walk(schema: z.ZodType, segments: string[], depth: number): boolean {
  if (segments.length === 0) return true;
  if (depth > MAX_DEPTH) return false;
  const [head, ...rest] = segments;
  return unwrap(schema).some((candidate) => {
    const def = defOf(candidate);
    if (def.type === "object" && def.shape) {
      const child = Object.prototype.hasOwnProperty.call(def.shape, head)
        ? def.shape[head]
        : undefined;
      return child !== undefined && walk(child, rest, depth + 1);
    }
    if (def.type === "record" && def.valueType) {
      return walk(def.valueType, rest, depth + 1);
    }
    return false;
  });
}

/**
 * True when `path` is a dotted path to a field (or a record entry) that the
 * config schema defines. Malformed paths (empty segments, prototype member
 * names, characters outside `[A-Za-z0-9_-]`) are never schema paths.
 */
export function isConfigSchemaPath(
  path: unknown,
  schema: z.ZodType = NightgaugeConfigSchema
): path is string {
  if (typeof path !== "string" || path.length === 0 || path.length > 256) return false;
  const segments = path.split(".");
  if (segments.some((s) => !SEGMENT.test(s) || FORBIDDEN_SEGMENTS.has(s))) return false;
  return walk(schema, segments, 0);
}
