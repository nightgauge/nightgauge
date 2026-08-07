package codegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// TestStressGoldenIsInSync is the behaviour drift check. It is the reason a
// clause cannot be deleted or two rules swapped without a visible diff: the
// golden holds an answer for every input derived from the table.
func TestStressGoldenIsInSync(t *testing.T) {
	want, err := RenderStressGolden(terminalkind.Load())
	if err != nil {
		t.Fatalf("render golden: %v", err)
	}
	got, err := os.ReadFile(filepath.Join("..", "testdata", "stress-golden.json"))
	if err != nil {
		t.Fatalf("read stress-golden.json: %v", err)
	}
	if string(got) != string(want) {
		var a, b terminalkind.StressGolden
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal(want, &b)
		t.Errorf("testdata/stress-golden.json is out of sync with table.json "+
			"(committed %d cases, table produces %d).\n"+
			"Run `make generate-terminal-kind-table` and review the diff: every line that changed "+
			"is an input whose routing changed.", len(a.Cases), len(b.Cases))
	}
}

// TestGeneratedTypeScriptIsInSync is the cross-language drift check. Together
// with .husky/pre-commit and scripts/ci-local.sh it makes "add a rule to one
// consumer" impossible: the SDK's copy of the table is generated, and a hand
// edit fails here.
func TestGeneratedTypeScriptIsInSync(t *testing.T) {
	want, err := RenderTypeScript(terminalkind.Load())
	if err != nil {
		t.Fatalf("render TypeScript: %v", err)
	}
	path := filepath.Join("..", "..", "..", GeneratedTSPath)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", GeneratedTSPath, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s is out of sync with internal/terminalkind/table.json.\n"+
			"Run `make generate-terminal-kind-table` and commit the result. Terminal-kind "+
			"classification is defined ONCE; the generated module is only how TypeScript sees it, "+
			"and hand-editing it is the exact drift #306 removed.", GeneratedTSPath)
	}
}
