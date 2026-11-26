/**
 * adapterCompat.generated.ts — GENERATED. DO NOT EDIT.
 *
 * Source:    internal/adaptercompat/manifests/*.json
 * Generator: cmd/adaptercompat-codegen (`go run ./cmd/adaptercompat-codegen`)
 *
 * This is the SDK's view of what Nightgauge knows about each coding CLI's
 * supported versions: the oldest version anything in the tree was verified
 * against (`minVersion`), the newest version anyone tested
 * (`maxTested`, informational "tested up to" data: a newer version is
 * never refused or warned about), and whether falling below the floor is a warning or
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
  /** The https URL of the vendor's install script. Empty when the CLI installs from npm. */
  readonly installer: string;
  /** The pinned sha256 a downloaded installer must match before it runs. Empty without an installer. */
  readonly installerSha256: string;
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
    "claude-headless": {
      minVersion: "2.1.223",
      maxTested: "2.1.316",
      floorPolicy: "warn",
      installer: "",
      installerSha256: "",
    },
    "codex": {
      minVersion: "0.111.0",
      maxTested: "0.145.0",
      floorPolicy: "warn",
      installer: "",
      installerSha256: "",
    },
    "copilot": {
      minVersion: "",
      maxTested: "",
      floorPolicy: "warn",
      installer: "",
      installerSha256: "",
    },
    "gemini": {
      minVersion: "0.29.0",
      maxTested: "",
      floorPolicy: "warn",
      installer: "",
      installerSha256: "",
    },
    "grok": {
      minVersion: "1.0.0",
      maxTested: "1.0.4",
      floorPolicy: "warn",
      installer: "https://x.ai/cli/install.sh",
      installerSha256: "a7916e6d5cc606564dd589037043b4c6cbb58f3b54c3b9865af9e0aed2a5bc77",
    },
    "opencode": {
      minVersion: "2.2.7",
      maxTested: "2.2.7",
      floorPolicy: "fail_closed",
      installer: "https://opencode.ai/v2/install",
      installerSha256: "b60226508704fc97d7b9d12ae6d4a6f819dfe02b22c190a0425c14ef632b056b",
    },
  });
