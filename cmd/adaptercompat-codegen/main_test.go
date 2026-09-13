package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
)

// TestGeneratedTSCurrent is the drift guard. It renders the generated module
// in-memory from the embedded manifests and compares it byte-for-byte against
// the committed file, so a hand edit to either side is caught before merge.
func TestGeneratedTSCurrent(t *testing.T) {
	manifests, err := adaptercompat.Load()
	if err != nil {
		t.Fatalf("load adapter compat manifests: %v", err)
	}

	want, err := renderTypeScript(manifests)
	if err != nil {
		t.Fatalf("render TypeScript: %v", err)
	}

	path := filepath.Join("..", "..", GeneratedTSPath)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", GeneratedTSPath, err)
	}

	if string(got) != string(want) {
		t.Errorf("%s is out of sync with internal/adaptercompat/manifests/*.json.\n"+
			"Run `go run ./cmd/adaptercompat-codegen` and commit the result. Adapter version "+
			"floors are defined ONCE, in the manifests; the generated module is only how "+
			"TypeScript sees them, and hand-editing it is exactly the drift this test exists "+
			"to catch.", GeneratedTSPath)
	}
}

// TestRenderTypeScriptSortsKeys pins the determinism the drift guard relies
// on: the rendered keys are sorted by adapter name regardless of the order
// Load returns them in, so re-running the generator never produces a diff
// that is pure noise.
func TestRenderTypeScriptSortsKeys(t *testing.T) {
	manifests := []adaptercompat.Manifest{
		{Adapter: "opencode", FloorPolicy: adaptercompat.FloorFailClosed},
		{Adapter: "codex", FloorPolicy: adaptercompat.FloorWarn},
	}

	got, err := renderTypeScript(manifests)
	if err != nil {
		t.Fatalf("render TypeScript: %v", err)
	}

	again, err := renderTypeScript([]adaptercompat.Manifest{manifests[1], manifests[0]})
	if err != nil {
		t.Fatalf("render TypeScript (reordered input): %v", err)
	}

	if string(got) != string(again) {
		t.Fatalf("rendering is not order-independent:\nfirst:  %s\nsecond: %s", got, again)
	}

	wantOrder := []string{`"codex"`, `"opencode"`}
	text := string(got)
	lastIdx := -1
	for _, key := range wantOrder {
		idx := indexAfter(text, key, lastIdx)
		if idx < 0 {
			t.Fatalf("expected %s to appear after position %d in:\n%s", key, lastIdx, text)
		}
		lastIdx = idx
	}
}

func indexAfter(s, substr string, after int) int {
	for i := after + 1; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
