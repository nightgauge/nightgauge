/**
 * Zod validation schemas for the GQL query language
 *
 * Provides runtime validation for:
 * - Field names (allowlist)
 * - Operators per field type
 * - Value formats (dates, numbers, etc.)
 *
 * @see standards/security.md - Input validation via Zod
 */

import { z } from "zod";
import type {
  FieldName,
  FieldDefinition,
  ComparisonOperator,
  SavedQuery,
  SavedQueriesFile,
  ASTNode,
} from "./types.js";

/**
 * Maximum query length in characters
 * Security: Prevents DoS through excessive parsing
 */
export const MAX_QUERY_LENGTH = 2000;

/**
 * Allowed field names (allowlist approach per security.md)
 */
export const ALLOWED_FIELDS: readonly FieldName[] = [
  "status",
  "priority",
  "size",
  "component",
  "assignee",
  "title",
  "number",
  "updated",
  "created",
  "labels",
  "type",
] as const;

/**
 * Field definitions with type and allowed operators
 */
export const FIELD_DEFINITIONS: Record<FieldName, FieldDefinition> = {
  status: {
    name: "status",
    type: "single_select",
    allowedOperators: [":", "!="],
    allowedValues: ["ready", "in-progress", "in-review", "done", "backlog"] as const,
  },
  priority: {
    name: "priority",
    type: "single_select",
    allowedOperators: [":", "!="],
    allowedValues: ["P0", "P1", "P2", "critical", "high", "medium", "low"] as const,
  },
  size: {
    name: "size",
    type: "single_select",
    allowedOperators: [":", "!=", ">", "<", ">=", "<="],
    allowedValues: ["XS", "S", "M", "L", "XL"] as const,
  },
  component: {
    name: "component",
    type: "label",
    allowedOperators: [":", "!="],
  },
  assignee: {
    name: "assignee",
    type: "text",
    allowedOperators: [":", "!="],
  },
  title: {
    name: "title",
    type: "text",
    allowedOperators: [":", "~"],
  },
  number: {
    name: "number",
    type: "number",
    allowedOperators: [":", ">", "<", ">=", "<="],
  },
  updated: {
    name: "updated",
    type: "date",
    allowedOperators: ["<", ">", "<=", ">="],
  },
  created: {
    name: "created",
    type: "date",
    allowedOperators: ["<", ">", "<=", ">="],
  },
  labels: {
    name: "labels",
    type: "array",
    allowedOperators: [":", "!="],
  },
  type: {
    name: "type",
    type: "label",
    allowedOperators: [":", "!="],
    allowedValues: ["feature", "bug", "docs", "refactor", "epic"] as const,
  },
} as const;

/**
 * Zod schema for field names
 */
export const FieldNameSchema = z.enum([
  "status",
  "priority",
  "size",
  "component",
  "assignee",
  "title",
  "number",
  "updated",
  "created",
  "labels",
  "type",
]);

/**
 * Zod schema for comparison operators
 */
export const ComparisonOperatorSchema = z.enum([":", "=", "!=", ">", "<", ">=", "<=", "~"]);

/**
 * Zod schema for boolean operators
 */
export const BooleanOperatorSchema = z.enum(["AND", "OR"]);

/**
 * Zod schema for relative date values (e.g., "7d", "30d")
 */
export const RelativeDateSchema = z.string().regex(/^\d+d$/i, {
  message: 'Relative date must be in format "Nd" (e.g., "7d", "30d")',
});

/**
 * Zod schema for ISO date values (e.g., "2026-01-15")
 */
export const ISODateSchema = z.string().regex(/^\d{4}-\d{2}-\d{2}$/, {
  message: "Date must be in format YYYY-MM-DD",
});

/**
 * Zod schema for date values (relative or ISO)
 */
export const DateValueSchema = z.union([RelativeDateSchema, ISODateSchema]);

/**
 * Zod schema for issue number values
 */
export const IssueNumberSchema = z.string().regex(/^\d+$/, {
  message: "Issue number must be a positive integer",
});

/**
 * Zod schema for comparison AST node
 */
export const ComparisonNodeSchema = z.object({
  type: z.literal("comparison"),
  field: FieldNameSchema,
  operator: ComparisonOperatorSchema,
  value: z.string().min(1),
});

/**
 * Recursive AST node schema
 * Uses z.lazy for recursive types
 */
export const ASTNodeSchema: z.ZodType<ASTNode> = z.lazy(() =>
  z.union([
    ComparisonNodeSchema,
    z.object({
      type: z.literal("binary"),
      operator: BooleanOperatorSchema,
      left: ASTNodeSchema,
      right: ASTNodeSchema,
    }),
    z.object({
      type: z.literal("unary"),
      operator: z.literal("NOT"),
      operand: ASTNodeSchema,
    }),
  ])
);

/**
 * Zod schema for saved query
 */
export const SavedQuerySchema = z.object({
  name: z.string().min(1).max(100),
  query: z.string().min(1).max(MAX_QUERY_LENGTH),
  description: z.string().max(500).optional(),
  createdAt: z.string().datetime().optional(),
  lastUsedAt: z.string().datetime().optional(),
}) satisfies z.ZodType<SavedQuery>;

/**
 * Zod schema for saved queries file
 */
export const SavedQueriesFileSchema = z.object({
  version: z.literal("1.0"),
  queries: z.array(SavedQuerySchema),
}) satisfies z.ZodType<SavedQueriesFile>;

/**
 * Check if a field name is valid
 */
export function isValidField(field: string): field is FieldName {
  return ALLOWED_FIELDS.includes(field as FieldName);
}

/**
 * Get field definition by name
 * Returns undefined for unknown fields
 */
export function getFieldDefinition(field: string): FieldDefinition | undefined {
  if (!isValidField(field)) {
    return undefined;
  }
  return FIELD_DEFINITIONS[field];
}

/**
 * Check if an operator is valid for a field
 */
export function isValidOperatorForField(field: FieldName, operator: ComparisonOperator): boolean {
  const definition = FIELD_DEFINITIONS[field];
  return definition.allowedOperators.includes(operator);
}

/**
 * Get allowed operators for a field
 */
export function getAllowedOperators(field: FieldName): ComparisonOperator[] {
  const definition = FIELD_DEFINITIONS[field];
  return [...definition.allowedOperators];
}

/**
 * Get allowed values for a field (if restricted)
 */
export function getAllowedValues(field: FieldName): readonly string[] | undefined {
  const definition = FIELD_DEFINITIONS[field];
  return definition.allowedValues;
}

/**
 * Size ordering for comparison operators
 */
export const SIZE_ORDER: Record<string, number> = {
  XS: 1,
  S: 2,
  M: 3,
  L: 4,
  XL: 5,
};

/**
 * Check if a value is valid for a field
 * Only applicable for fields with restricted allowedValues
 */
export function isValidValueForField(field: FieldName, value: string): boolean {
  const definition = FIELD_DEFINITIONS[field];

  // If no restricted values, any value is allowed
  if (!definition.allowedValues) {
    return true;
  }

  // Case-insensitive comparison for single_select fields
  const normalizedValue = value.toLowerCase();
  return definition.allowedValues.some((allowed) => allowed.toLowerCase() === normalizedValue);
}

/**
 * Parse a relative date string to a Date object
 * @param relativeDate - Format: "Nd" (e.g., "7d" for 7 days ago)
 * @returns Date object representing the threshold date
 */
export function parseRelativeDate(relativeDate: string): Date {
  const match = relativeDate.match(/^(\d+)d$/i);
  if (!match) {
    throw new Error(`Invalid relative date format: ${relativeDate}`);
  }

  const days = parseInt(match[1], 10);
  const date = new Date();
  date.setDate(date.getDate() - days);
  return date;
}

/**
 * Parse a date value (relative or ISO) to a Date object
 */
export function parseDateValue(value: string): Date {
  // Check for relative date format (e.g., "7d")
  if (/^\d+d$/i.test(value)) {
    return parseRelativeDate(value);
  }

  // Try parsing as ISO date
  const date = new Date(value);
  if (isNaN(date.getTime())) {
    throw new Error(`Invalid date value: ${value}`);
  }

  return date;
}
