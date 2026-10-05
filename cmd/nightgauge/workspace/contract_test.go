package workspacecmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/contractrollout"
	"github.com/spf13/cobra"
)

const contractManifest = "name: demo\nfiles:\n  - path: a.sh\n"

// TestLoadContractTargets: --target replaces the manifest's targets, a
// workspace manifest supplies them when neither names any, and a target's
// checkout resolves against the workspace root.
func TestLoadContractTargets(t *testing.T) {
	newWorkspace(t, `workspace:
  name: demo
repositories:
  - name: app
    path: ./app
    role: primary
    project_number: 1
`, "app")
	root, _ := os.Getwd()
	if err := os.MkdirAll(filepath.Join(root, "app", ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", ".nightgauge", "config.yaml"),
		[]byte("owner: acme\ndefaultRepo: app-repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "demo.yaml")
	if err := os.WriteFile(manifest, []byte(contractManifest), 0o644); err != nil {
		t.Fatal(err)
	}

	c, resolve, err := loadContract(manifest, contractFlags{})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Targets) != 1 || c.Targets[0].Repo != "acme/app-repo" {
		t.Fatalf("workspace targets = %+v", c.Targets)
	}
	got, err := resolve(c.Targets[0])
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(filepath.Join(root, "app")); !sameDir(got, want) {
		t.Errorf("checkout = %s, want %s", got, want)
	}

	c, resolve, err = loadContract(manifest, contractFlags{targets: []string{"acme/other=app"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Targets) != 1 || c.Targets[0].Repo != "acme/other" {
		t.Fatalf("--target targets = %+v", c.Targets)
	}
	if _, err := resolve(c.Targets[0]); err != nil {
		t.Errorf("a relative --target path did not resolve against the workspace root: %v", err)
	}
	if _, err := resolve(contractrollout.Target{Repo: "acme/unknown"}); err == nil {
		t.Error("a target with no path and no workspace entry resolved")
	}
	if _, _, err := loadContract(manifest, contractFlags{targets: []string{"not-a-repo"}}); err == nil {
		t.Error("an invalid --target was accepted")
	}
}

func sameDir(a, b string) bool {
	ea, _ := filepath.EvalSymlinks(a)
	eb, _ := filepath.EvalSymlinks(b)
	return ea == eb
}

// TestPrintContractRowsFailsOnAttention: the table is printed either way,
// and the command fails when a target needs attention.
func TestPrintContractRowsFailsOnAttention(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	ok := []contractrollout.TargetStatus{{Repo: "o/a", Status: contractrollout.StatusCompliant}}
	if err := printContractRows(cmd, ok, false); err != nil {
		t.Fatal(err)
	}
	bad := append(ok, contractrollout.TargetStatus{Repo: "o/b", Status: contractrollout.StatusGateFailed})
	if err := printContractRows(cmd, bad, false); err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "| o/b | gate-failed |") {
		t.Errorf("table:\n%s", out.String())
	}
}
