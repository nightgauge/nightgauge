// Command terminalkind-codegen renders the consumers of the canonical
// terminal-kind rule table (internal/terminalkind/table.json):
//
//	packages/nightgauge-sdk/src/analysis/health/terminalKindTable.generated.ts
//	internal/terminalkind/testdata/stress-golden.json
//
// Run it with `make generate-terminal-kind-table`. The same rendering is
// asserted byte for byte by TestGeneratedTypeScriptIsInSync and
// TestStressGoldenIsInSync, by .husky/pre-commit and by scripts/ci-local.sh, so
// a consumer cannot be edited on its own — which is the whole point of #306.
//
// `--check` renders without writing and exits non-zero on drift.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

func main() {
	root := flag.String("root", ".", "Repository root")
	check := flag.Bool("check", false, "Verify the generated files are in sync; do not write")
	flag.Parse()

	tb := terminalkind.Load()

	ts, err := terminalkind.RenderTypeScript(tb)
	if err != nil {
		fail("rendering TypeScript: %v", err)
	}
	golden, err := terminalkind.RenderStressGolden(tb)
	if err != nil {
		fail("rendering stress golden: %v", err)
	}

	outputs := []struct {
		path string
		data []byte
	}{
		{filepath.Join(*root, terminalkind.GeneratedTSPath), ts},
		{filepath.Join(*root, terminalkind.StressGoldenPath), golden},
	}

	drift := false
	for _, o := range outputs {
		if *check {
			existing, err := os.ReadFile(o.path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "missing generated file %s: %v\n", o.path, err)
				drift = true
				continue
			}
			if string(existing) != string(o.data) {
				fmt.Fprintf(os.Stderr, "out of sync: %s\n", o.path)
				drift = true
			}
			continue
		}
		if err := os.WriteFile(o.path, o.data, 0o644); err != nil {
			fail("writing %s: %v", o.path, err)
		}
		fmt.Printf("wrote %s\n", o.path)
	}

	if drift {
		fmt.Fprintln(os.Stderr,
			"Run `make generate-terminal-kind-table` and commit the result. "+
				"Terminal-kind classification is defined once, in "+
				"internal/terminalkind/table.json; the generated files are how the "+
				"TypeScript consumers see it.")
		os.Exit(1)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
