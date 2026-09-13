// Command adaptercompat-codegen renders the canonical adapter compatibility
// manifests (internal/adaptercompat/manifests/*.json) into their generated
// consumer:
//
//	packages/nightgauge-sdk/src/cli/adapters/adapterCompat.generated.ts
//
// Run it with `go run ./cmd/adaptercompat-codegen`. The same rendering is
// asserted byte for byte by TestGeneratedTSCurrent, so the generated file
// cannot be hand-edited without the drift check catching it (#1621) — the
// version floor an adapter warns or fails closed on is defined once, in the
// manifest, and every consumer sees the same value.
//
// `-check` renders without writing and exits non-zero on drift.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
)

func main() {
	root := flag.String("root", ".", "Repository root")
	check := flag.Bool("check", false, "Verify the generated file is in sync; do not write")
	flag.Parse()

	manifests, err := adaptercompat.Load()
	if err != nil {
		fail("loading adapter compat manifests: %v", err)
	}

	ts, err := renderTypeScript(manifests)
	if err != nil {
		fail("rendering TypeScript: %v", err)
	}

	path := filepath.Join(*root, GeneratedTSPath)

	if *check {
		existing, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "missing generated file %s: %v\n", path, err)
			os.Exit(1)
		}
		if string(existing) != string(ts) {
			fmt.Fprintf(os.Stderr, "out of sync: %s\n", path)
			fmt.Fprintln(os.Stderr,
				"Run `go run ./cmd/adaptercompat-codegen` and commit the result. Adapter "+
					"compatibility floors are defined once, in "+
					"internal/adaptercompat/manifests/*.json; the generated file is only how "+
					"TypeScript sees them.")
			os.Exit(1)
		}
		return
	}

	if err := os.WriteFile(path, ts, 0o644); err != nil {
		fail("writing %s: %v", path, err)
	}
	fmt.Printf("wrote %s\n", path)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
