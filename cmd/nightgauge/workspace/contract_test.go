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

	// --target names a registered repository, case-insensitively.
	if c, _, err = loadContract(manifest, contractFlags{targets: []string{"ACME/app-repo"}}); err != nil || len(c.Targets) != 1 {
		t.Fatalf("--target of a registered repository: %+v, %v", c, err)
	}
	// A repository the workspace does not register is refused, from a flag
	// or from the manifest.
	if _, _, err := loadContract(manifest, contractFlags{targets: []string{"acme/other"}}); err == nil || !strings.Contains(err.Error(), "not a repository of this workspace") {
		t.Errorf("an unregistered --target was accepted: %v", err)
	}
	withTarget := filepath.Join(t.TempDir(), "t.yaml")
	if err := os.WriteFile(withTarget, []byte(contractManifest+"targets:\n  - repo: evil/elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadContract(withTarget, contractFlags{}); err == nil {
		t.Error("an unregistered manifest target was accepted")
	}
	if _, err := resolve(contractrollout.Target{Repo: "acme/unknown"}); err == nil {
		t.Error("an unregistered target resolved to a checkout")
	}
	// A manifest cannot choose a checkout path or a gate command.
	for _, extra := range []string{"    path: /tmp\n", "    gate: [sh, -c, id]\n"} {
		p := filepath.Join(t.TempDir(), "x.yaml")
		if err := os.WriteFile(p, []byte(contractManifest+"targets:\n  - repo: acme/app-repo\n"+extra), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadContract(p, contractFlags{}); err == nil {
			t.Errorf("a manifest target with %q was accepted", extra)
		}
	}
}

// TestLoadContractNeedsAWorkspace: with no workspace manifest there is no
// registered repository, so nothing can be targeted.
func TestLoadContractNeedsAWorkspace(t *testing.T) {
	dir := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	manifest := filepath.Join(dir, "demo.yaml")
	if err := os.WriteFile(manifest, []byte(contractManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadContract(manifest, contractFlags{targets: []string{"acme/app"}}); err == nil {
		t.Fatal("a rollout outside a workspace was accepted")
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
