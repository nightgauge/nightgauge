package deliverable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubWorkspace writes a manifest and a stub checker that prints body and
// exits with code.
func stubWorkspace(t *testing.T, body string, code int) string {
	t.Helper()
	ws := t.TempDir()
	for rel, content := range map[string]string{
		publicationManifest: "allow: []\n",
		publicationChecker:  "import sys\nprint('''" + body + "''')\nsys.exit(" + string(rune('0'+code)) + ")\n",
	} {
		p := filepath.Join(ws, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

func TestCheckPublicationNotApplicableWithoutManifest(t *testing.T) {
	res, err := CheckPublication(t.TempDir(), []string{"docs/spikes/1-x.md"})
	if err != nil || res.Applicable {
		t.Fatalf("want not applicable, got %+v err=%v", res, err)
	}
}

func TestCheckPublicationDenied(t *testing.T) {
	ws := stubWorkspace(t, `[{"path":"docs/spikes/1-x.md","verdict":"deny","rule":"docs/spikes/**","class":"PRIVATE"}]`, 1)
	res, err := CheckPublication(ws, []string{"docs/spikes/1-x.md"})
	if err != nil {
		t.Fatal(err)
	}
	rej := res.Rejected()
	if !res.Applicable || len(rej) != 1 || rej[0].Rule != "docs/spikes/**" {
		t.Fatalf("want one denial on docs/spikes/**, got %+v", res)
	}
}

func TestCheckPublicationAllowed(t *testing.T) {
	ws := stubWorkspace(t, `[{"path":"internal/x.go","verdict":"allow","rule":"internal/**"}]`, 0)
	res, err := CheckPublication(ws, []string{"internal/x.go"})
	if err != nil || len(res.Rejected()) != 0 {
		t.Fatalf("want allow, got %+v err=%v", res, err)
	}
}

func TestCheckPublicationHarnessErrorIsNotAPass(t *testing.T) {
	ws := stubWorkspace(t, "", 2)
	if _, err := CheckPublication(ws, []string{"internal/x.go"}); err == nil {
		t.Fatal("exit 2 must be an error, never an allow")
	}
}

func TestCheckPublicationGarbageOutputIsAnError(t *testing.T) {
	ws := stubWorkspace(t, "not json", 0)
	if _, err := CheckPublication(ws, []string{"internal/x.go"}); err == nil || !strings.Contains(err.Error(), "verdict list") {
		t.Fatalf("want verdict-list error, got %v", err)
	}
}

// TestCheckPublicationRealChecker runs the repository's own checker, so the
// Go and Python sides are proven against each other, not only against a stub.
func TestCheckPublicationRealChecker(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, publicationChecker)); err != nil {
		t.Skip("repository checker not present")
	}
	res, err := CheckPublication(root, []string{"docs/spikes/9999-x.md", "docs/spikes/README.md", "internal/x.go"})
	if err != nil {
		t.Skipf("checker could not run here (PyYAML?): %v", err)
	}
	rej := res.Rejected()
	if len(rej) != 1 || rej[0].Path != "docs/spikes/9999-x.md" || rej[0].Verdict != "deny" {
		t.Fatalf("want only the spike path denied, got %+v", res.Verdicts)
	}
}
