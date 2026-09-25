package main

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
)

// GeneratedTSPath is the SDK module rendered from the adapter compat
// manifests, relative to the repository root. Adapters read their version
// floor from it instead of holding their own literal (#1621).
const GeneratedTSPath = "packages/nightgauge-sdk/src/cli/adapters/adapterCompat.generated.ts"

const tsHeader = `/**
 * adapterCompat.generated.ts — GENERATED. DO NOT EDIT.
 *
 * Source:    internal/adaptercompat/manifests/*.json
 * Generator: cmd/adaptercompat-codegen (` + "`go run ./cmd/adaptercompat-codegen`" + `)
 *
 * This is the SDK's view of what Nightgauge knows about each coding CLI's
 * supported versions: the oldest version anything in the tree was verified
 * against (` + "`minVersion`" + `), the newest version anyone tested
 * (` + "`maxTested`" + `, informational "tested up to" data: a newer version is
 * never refused or warned about), and whether falling below the floor is a warning or
 * disables the adapter (` + "`floorPolicy`" + `). Go's twin of this data is
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

`

const tsFooter = `;
`

// renderTypeScript emits the SDK's generated ADAPTER_COMPAT module. The
// payload is built from the PARSED manifests rather than copied from their
// source bytes, so the output depends only on their content: reformatting a
// manifest (key order, whitespace) cannot produce a spurious drift failure,
// while a changed min_version, max_tested or floor_policy does.
func renderTypeScript(manifests []adaptercompat.Manifest) ([]byte, error) {
	sorted := make([]adaptercompat.Manifest, len(manifests))
	copy(sorted, manifests)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Adapter < sorted[j].Adapter })

	var b bytes.Buffer
	b.WriteString(tsHeader)

	b.WriteString("export type AdapterCompatKey =\n")
	for i, m := range sorted {
		terminator := "\n"
		if i == len(sorted)-1 {
			terminator = ";\n\n"
		}
		fmt.Fprintf(&b, "  | %q%s", m.Adapter, terminator)
	}

	b.WriteString("export const ADAPTER_COMPAT: Readonly<Record<AdapterCompatKey, AdapterCompatEntry>> =\n")
	b.WriteString("  Object.freeze({\n")
	for _, m := range sorted {
		fmt.Fprintf(&b, "    %q: { minVersion: %q, maxTested: %q, floorPolicy: %q },\n",
			m.Adapter, m.MinVersion, m.MaxTested, m.FloorPolicy)
	}
	b.WriteString("  })")
	b.WriteString(tsFooter)

	return b.Bytes(), nil
}
