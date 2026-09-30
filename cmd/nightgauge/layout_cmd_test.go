package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func runLayoutCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := layoutCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// layoutFieldsFixture is the shared parity fixture: every path field of the
// layout JSON as a template over {common} and {gitdir}, which the SDK's
// cloneLayout tests read too (ADR-024 § 7, "One path source").
type layoutFieldsFixture struct {
	SchemaVersion   int               `json:"schema_version"`
	Fields          map[string]string `json:"fields"`
	CheckoutEntries map[string]string `json:"checkout_entries"`
}

func readLayoutFieldsFixture(t *testing.T) layoutFieldsFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "layout", "testdata", "layout-fields.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx layoutFieldsFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	return fx
}

// TestLayoutCmdJSON: `nightgauge layout` prints the resolved layout: the
// per-clone paths are the same from the main checkout and a linked worktree,
// and each checkout has its own per-checkout root (ADR-024 § 7). Every path
// field matches the shared parity fixture the SDK tests read.
func TestLayoutCmdJSON(t *testing.T) {
	root := layouttest.Repo(t)
	gittest.Run(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, root, "worktree", "add", "-q", wt)
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(real, ".git")
	fx := readLayoutFieldsFixture(t)
	if fx.SchemaVersion != layoutSchemaVersion {
		t.Errorf("fixture schema_version = %d, want %d", fx.SchemaVersion, layoutSchemaVersion)
	}

	for from, gitDir := range map[string]string{root: common, wt: filepath.Join(common, "worktrees", "wt")} {
		out, err := runLayoutCmd(t, "", "--workdir", from)
		if err != nil {
			t.Fatalf("layout --workdir %s: %v", from, err)
		}
		var rep map[string]any
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out)
		}
		if rep["root"] != from || rep["schema_version"] != float64(layoutSchemaVersion) {
			t.Errorf("layout from %s: root %v, schema_version %v", from, rep["root"], rep["schema_version"])
		}
		for field, tmpl := range fx.Fields {
			want := filepath.FromSlash(strings.NewReplacer("{common}", filepath.ToSlash(common), "{gitdir}", filepath.ToSlash(gitDir)).Replace(tmpl))
			if rep[field] != want {
				t.Errorf("layout from %s: %s = %v, want %s", from, field, rep[field], want)
			}
		}
	}

	// The per-checkout entry names are the fixture's, so the SDK's
	// CHECKOUT_ENTRIES (pinned to the same fixture) agree with Go's.
	var names []string
	for _, e := range layout.CheckoutEntries {
		names = append(names, e.Name)
	}
	names = append(names, layout.CheckoutServeLock)
	for key, name := range fx.CheckoutEntries {
		if !slices.Contains(names, name) {
			t.Errorf("fixture checkout entry %s = %q is not a Go per-checkout entry %v", key, name, names)
		}
	}
	if len(fx.CheckoutEntries) != len(names) {
		t.Errorf("fixture has %d checkout entries, Go has %d (%v)", len(fx.CheckoutEntries), len(names), names)
	}
}

// TestLayoutCmdCheckoutClass: `layout path|write|append checkout` address the
// per-checkout root of the checkout --workdir names, confined to it.
func TestLayoutCmdCheckoutClass(t *testing.T) {
	root := layouttest.Repo(t)
	gittest.Run(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, root, "worktree", "add", "-q", wt)
	for _, from := range []string{root, wt} {
		out, err := runLayoutCmd(t, `{"state":"running"}`, "write", "checkout", "run-state.json", "--workdir", from)
		if err != nil {
			t.Fatal(err)
		}
		written := strings.TrimSpace(out)
		if want := layouttest.CheckoutPath(t, from, "run-state.json"); written != want {
			t.Errorf("write from %s printed %q, want %q", from, written, want)
		}
		if out, err := runLayoutCmd(t, "", "path", "checkout", "run-state.json", "--workdir", from); err != nil || strings.TrimSpace(out) != written {
			t.Errorf("path from %s = %q, %v; want %q", from, out, err, written)
		}
		if _, err := runLayoutCmd(t, "a\n", "append", "checkout", "health/trends.jsonl", "--workdir", from); err != nil {
			t.Fatal(err)
		}
		if _, err := runLayoutCmd(t, "x", "write", "checkout", "../escape", "--workdir", from); err == nil {
			t.Errorf("write checkout ../escape from %s succeeded", from)
		}
		if out := gittest.Run(t, from, "status", "--porcelain", "--ignored"); out != "" {
			t.Errorf("git status in %s after checkout writes = %q, want clean", from, out)
		}
	}
	if layouttest.CheckoutDir(t, root) == layouttest.CheckoutDir(t, wt) {
		t.Error("the main checkout and the linked worktree share a checkout dir")
	}
}

func TestLayoutCmdWriteAndPath(t *testing.T) {
	root := layouttest.Repo(t)
	out, err := runLayoutCmd(t, `{"n":1}`, "write", "pipeline", "issue-7.json", "--workdir", root)
	if err != nil {
		t.Fatal(err)
	}
	written := strings.TrimSpace(out)
	if want := filepath.Join(layouttest.PipelineDir(t, root), "issue-7.json"); written != want {
		t.Fatalf("write printed %q, want %q", written, want)
	}
	if got, _ := os.ReadFile(written); string(got) != `{"n":1}` {
		t.Fatalf("written content = %q", got)
	}
	out, err = runLayoutCmd(t, "", "path", "pipeline", "issue-7.json", "--workdir", root)
	if err != nil || strings.TrimSpace(out) != written {
		t.Fatalf("path = %q, %v; want %q", out, err, written)
	}
	if _, err := runLayoutCmd(t, "x\n", "append", "retros", "7.md", "--workdir", root); err != nil {
		t.Fatal(err)
	}
	if _, err := runLayoutCmd(t, "x", "write", "plans", "../escape.md", "--workdir", root); err == nil {
		t.Error("write ../escape.md succeeded")
	}
	if out := gittest.Run(t, root, "status", "--porcelain", "--ignored"); out != "" {
		t.Errorf("git status after layout writes = %q, want clean", out)
	}
}

func TestLayoutCmdOutsideGit(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	dir := t.TempDir()
	_, err := runLayoutCmd(t, "", "--workdir", dir)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("layout outside git: err = %v, want \"not a git repository\"", err)
	}
}
