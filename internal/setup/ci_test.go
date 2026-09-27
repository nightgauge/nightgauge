package setup

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

const gateFixturePackageJSON = `{
  "engines": {"node": ">=22"},
  "scripts": {"build": "tsc -p tsconfig.json", "test": "vitest run", "typecheck": "tsc -p tsconfig.json --noEmit", "dev": "tsx src/cli.ts"},
  "devDependencies": {"typescript": "^5", "vitest": "^3"}
}`

func renderCI(t *testing.T, pkg string, opts ScaffoldToolingOptions) (*ScaffoldToolingResult, string) {
	t.Helper()
	dir := t.TempDir()
	if pkg != "" {
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts.Workdir = dir
	opts.Select = []string{TemplateKeyCI}
	res, err := RunScaffoldTooling(context.Background(), opts)
	if err != nil {
		t.Fatalf("RunScaffoldTooling: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return res, string(body)
}

// TestCIGolden pins the full emitted workflow for the issue #2206 fixture:
// scripts {build, test: "vitest run", typecheck} on a self-hosted runner.
func TestCIGolden(t *testing.T) {
	res, got := renderCI(t, gateFixturePackageJSON, ScaffoldToolingOptions{RunsOn: "self-hosted"})
	golden := filepath.Join("testdata", "ci_gate_scripts.golden.yml")
	if *updateGolden {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("ci.yml differs from %s (run with -update to rewrite)\n--- got ---\n%s", golden, got)
	}
	if !reflect.DeepEqual(res.CISteps, []string{"typecheck", "test", "build"}) {
		t.Errorf("CISteps = %v, want [typecheck test build]", res.CISteps)
	}
	if res.RunsOn != "self-hosted" {
		t.Errorf("RunsOn = %q", res.RunsOn)
	}
	for _, bad := range []string{"-- --run", "--if-present", "npm run lint", "npm run dev"} {
		if strings.Contains(got, bad) {
			t.Errorf("ci.yml unexpectedly contains %q", bad)
		}
	}
	assertSHAPinned(t, got)
}

var usesRe = regexp.MustCompile(`(?m)^\s*(?:-\s*)?uses:\s*(\S+)`)
var shaPinnedRe = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)

func assertSHAPinned(t *testing.T, yml string) {
	t.Helper()
	uses := usesRe.FindAllStringSubmatch(yml, -1)
	if len(uses) == 0 {
		t.Fatal("no uses: lines found")
	}
	for _, m := range uses {
		if !shaPinnedRe.MatchString(m[1]) {
			t.Errorf("uses: %s is not pinned to a 40-char commit SHA", m[1])
		}
	}
}

func TestCIAllGateScriptsAndDefaultRunner(t *testing.T) {
	pkg := `{"scripts":{"lint":"eslint .","build":"tsc","test":"jest","typecheck":"tsc --noEmit"}}`
	res, got := renderCI(t, pkg, ScaffoldToolingOptions{})
	if !reflect.DeepEqual(res.CISteps, []string{"typecheck", "lint", "test", "build"}) {
		t.Errorf("CISteps = %v", res.CISteps)
	}
	order := []string{"npm run typecheck", "npm run lint", "npm run test", "npm run build"}
	last := -1
	for _, s := range order {
		i := strings.Index(got, "run: "+s+"\n")
		if i < 0 || i < last {
			t.Errorf("step %q missing or out of order in:\n%s", s, got)
		}
		last = i
	}
	if !strings.Contains(got, "runs-on: ubuntu-latest") {
		t.Errorf("default runner missing:\n%s", got)
	}
	assertSHAPinned(t, got)
}

func TestCINoScriptsWarns(t *testing.T) {
	res, got := renderCI(t, "", ScaffoldToolingOptions{})
	if len(res.CISteps) != 0 || res.CISteps == nil {
		t.Errorf("CISteps = %#v, want empty non-nil", res.CISteps)
	}
	if strings.Contains(got, "npm run") {
		t.Errorf("unexpected npm run step:\n%s", got)
	}
	if !containsWarning(res.Warnings, "none of the gate scripts") {
		t.Errorf("missing no-scripts warning: %v", res.Warnings)
	}
}

func TestCIRejectsMultilineRunsOn(t *testing.T) {
	_, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{Workdir: t.TempDir(), Select: []string{TemplateKeyCI}, RunsOn: "x\n  evil: 1"})
	if err == nil {
		t.Fatal("expected error for multi-line runs-on")
	}
}

func TestCIPolicyProbeErrorIsWarningOnly(t *testing.T) {
	probe := func(context.Context, string) (*ActionsPolicy, error) { return nil, errors.New("HTTP 403") }
	res, _ := renderCI(t, gateFixturePackageJSON, ScaffoldToolingOptions{PolicyProbe: probe})
	if res.Outcomes[0].Outcome != OutcomeCreated {
		t.Errorf("outcome = %+v", res.Outcomes[0])
	}
	if !containsWarning(res.Warnings, "could not read the repository's Actions policy") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestPolicyWarnings(t *testing.T) {
	cases := []struct {
		name string
		p    ActionsPolicy
		want string // substring; "" means no warnings
	}{
		{"all + sha pinning", ActionsPolicy{Enabled: true, AllowedActions: "all", SHAPinningRequired: true}, ""},
		{"disabled", ActionsPolicy{Enabled: false}, "disabled"},
		{"local only", ActionsPolicy{Enabled: true, AllowedActions: "local_only"}, "local_only"},
		{"selected github owned", ActionsPolicy{Enabled: true, AllowedActions: "selected", SelectedKnown: true, GithubOwnedAllowed: true}, ""},
		{"selected pattern", ActionsPolicy{Enabled: true, AllowedActions: "selected", SelectedKnown: true, PatternsAllowed: []string{"actions/*"}}, ""},
		{"selected rejects", ActionsPolicy{Enabled: true, AllowedActions: "selected", SelectedKnown: true, PatternsAllowed: []string{"acme/*"}}, "does not allow actions/checkout"},
		{"selected unknown", ActionsPolicy{Enabled: true, AllowedActions: "selected"}, "could not be read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := PolicyWarnings(&tc.p)
			if tc.want == "" {
				if len(w) != 0 {
					t.Errorf("unexpected warnings: %v", w)
				}
				return
			}
			if !containsWarning(w, tc.want) {
				t.Errorf("warnings %v missing %q", w, tc.want)
			}
		})
	}
}

func containsWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}
