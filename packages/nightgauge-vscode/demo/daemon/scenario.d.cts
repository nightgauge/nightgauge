/**
 * Types for the part of `scenario.cjs` TypeScript modules import directly
 * (`demo/workspace-dates.ts`). Tests load the rest through `createRequire`
 * with their own casts.
 */

/**
 * Copy `value`, shifting ISO-8601 UTC timestamps by `deltaMs` and calendar
 * dates (`YYYY-MM-DD`) by `deltaMs` rounded to whole days, and replacing
 * `{{now}}` with `nowIso`.
 */
export declare function rebaseValue<T>(value: T, deltaMs: number, nowIso: string): T;
