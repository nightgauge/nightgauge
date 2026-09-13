/**
 * adapterCompat.generated.ts — GENERATED. DO NOT EDIT.
 *
 * Source:    internal/adaptercompat/manifests/*.json
 * Generator: cmd/adaptercompat-codegen (`go run ./cmd/adaptercompat-codegen`)
 *
 * This is the SDK's view of what Nightgauge knows about each coding CLI's
 * supported versions: the oldest version anything in the tree was verified
 * against (`minVersion`), the newest version anyone tested
 * (`maxTested`), and whether falling below the floor is a warning or
 * disables the adapter (`floorPolicy`). Go's twin of this data is
 * internal/adaptercompat.Manifest, embedded from the same manifests.
 *
 * Hand-editing this file is caught by TestGeneratedTSCurrent — the way to
 * change a floor is to edit the manifest and regenerate, which updates every
 * consumer at once.
 */

/** One adapter's compatibility record, as the SDK needs it. */
export interface AdapterCompatEntry {
  /** Oldest version a captured fixture or documented behaviour backs. Empty means no known floor. */
  readonly minVersion: string;
  /** Newest version anyone verified Nightgauge against. Empty means none was. */
  readonly maxTested: string;
  /** "warn" reports a CLI below minVersion and keeps the adapter usable; "fail_closed" disables it. */
  readonly floorPolicy: "warn" | "fail_closed";
}

export type AdapterCompatKey =
  | "claude-headless"
  | "codex"
  | "copilot"
  | "gemini"
  | "grok"
  | "opencode";

export const ADAPTER_COMPAT: Readonly<Record<AdapterCompatKey, AdapterCompatEntry>> =
  Object.freeze({
    "claude-headless": { minVersion: "2.1.223", maxTested: "2.1.258", floorPolicy: "warn" },
    "codex": { minVersion: "0.111.0", maxTested: "0.145.0", floorPolicy: "warn" },
    "copilot": { minVersion: "", maxTested: "", floorPolicy: "warn" },
    "gemini": { minVersion: "0.29.0", maxTested: "", floorPolicy: "warn" },
    "grok": { minVersion: "1.0.0", maxTested: "1.0.4", floorPolicy: "warn" },
    "opencode": { minVersion: "1.18.30", maxTested: "1.18.30", floorPolicy: "fail_closed" },
  });
