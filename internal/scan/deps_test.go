package scan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeRunner is the test double for CommandRunner. It returns canned outputs
// keyed by command name + first arg (e.g. "npm audit", "go list", "cargo
// outdated"). Tools listed in absent are reported as not-on-PATH.
type fakeRunner struct {
	absent  map[string]bool
	outputs map[string][]byte
	exits   map[string]int
	calls   []string
}

func (r *fakeRunner) LookPath(name string) error {
	if r.absent[name] {
		return errors.New("not on PATH")
	}
	return nil
}

func (r *fakeRunner) Run(_ context.Context, _, name string, args ...string) ([]byte, int, error) {
	key := name
	if len(args) > 0 {
		key = name + " " + args[0]
	}
	r.calls = append(r.calls, key)
	out, ok := r.outputs[key]
	if !ok {
		return []byte{}, 0, nil
	}
	return out, r.exits[key], nil
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		absent:  map[string]bool{},
		outputs: map[string][]byte{},
		exits:   map[string]int{},
	}
}

// writeFiles creates the named empty files inside dir so detection sees them.
func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		path := filepath.Join(dir, n)
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func TestRunDepScan_AllFourEcosystemsCounted(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "package.json", "requirements.txt", "go.mod", "Cargo.toml")

	runner := newFakeRunner()
	runner.outputs["npm audit"] = []byte(`{"vulnerabilities":{"foo":{"severity":"high"},"bar":{"severity":"critical"},"baz":{"severity":"moderate"}}}`)
	runner.outputs["npm outdated"] = []byte(`{"a":{},"b":{},"c":{}}`)
	runner.exits["npm outdated"] = 1 // npm outdated exits 1 when packages are outdated
	runner.outputs["pip-audit --format"] = []byte(`{"dependencies":[{"name":"foo","vulns":[{"id":"GHSA-1"}]},{"name":"bar","vulns":[{"id":"GHSA-2"},{"id":"GHSA-3"}]}]}`)
	runner.outputs["pip list"] = []byte(`[{"name":"foo"},{"name":"bar"}]`)
	runner.outputs["govulncheck -json"] = []byte(`{"finding":{"id":"GO-1"}}` + "\n" + `{"finding":{"id":"GO-2"}}` + "\n" + `{"progress":{}}` + "\n")
	runner.outputs["go list"] = []byte(`{"Path":"foo","Update":{"Version":"1.2.3"}}` + "\n" + `{"Path":"bar"}` + "\n" + `{"Path":"baz","Update":{"Version":"2.0.0"}}` + "\n")
	runner.outputs["cargo audit"] = []byte(`{"vulnerabilities":{"list":[{"advisory":{"severity":"low"}}]}}`)
	runner.outputs["cargo outdated"] = []byte(`{"dependencies":[{"name":"foo"},{"name":"bar"},{"name":"baz"},{"name":"qux"}]}`)

	res, err := RunDepScan(context.Background(), Options{
		Workdir:      dir,
		IncludeVulns: true,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("RunDepScan: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}

	node := res.Ecosystems["nodejs"]
	if !node.Detected || !node.Available {
		t.Errorf("nodejs detected=%v available=%v", node.Detected, node.Available)
	}
	if node.Vulnerabilities == nil ||
		node.Vulnerabilities.Critical != 1 || node.Vulnerabilities.High != 1 || node.Vulnerabilities.Moderate != 1 {
		t.Errorf("nodejs vulns = %#v", node.Vulnerabilities)
	}
	if node.Outdated != 3 {
		t.Errorf("nodejs outdated = %d, want 3", node.Outdated)
	}

	py := res.Ecosystems["python"]
	if py.Vulnerabilities == nil || py.Vulnerabilities.Moderate != 3 {
		t.Errorf("python vulns = %#v", py.Vulnerabilities)
	}
	if py.Outdated != 2 {
		t.Errorf("python outdated = %d, want 2", py.Outdated)
	}

	g := res.Ecosystems["go"]
	if g.Vulnerabilities == nil || g.Vulnerabilities.High != 2 {
		t.Errorf("go vulns = %#v", g.Vulnerabilities)
	}
	if g.Outdated != 2 {
		t.Errorf("go outdated = %d, want 2", g.Outdated)
	}

	r := res.Ecosystems["rust"]
	if r.Vulnerabilities == nil || r.Vulnerabilities.Low != 1 {
		t.Errorf("rust vulns = %#v", r.Vulnerabilities)
	}
	if r.Outdated != 4 {
		t.Errorf("rust outdated = %d, want 4", r.Outdated)
	}

	want := Totals{Critical: 1, High: 3, Moderate: 4, Low: 1, Outdated: 11}
	if !reflect.DeepEqual(res.Totals, want) {
		t.Errorf("totals = %#v, want %#v", res.Totals, want)
	}
}

func TestRunDepScan_ToolNotOnPath_RecordsAvailableFalse(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "package.json")

	runner := newFakeRunner()
	runner.absent["npm"] = true

	res, err := RunDepScan(context.Background(), Options{
		Workdir:      dir,
		IncludeVulns: true,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("RunDepScan: %v", err)
	}
	node := res.Ecosystems["nodejs"]
	if !node.Detected {
		t.Errorf("nodejs detected = false, want true (package.json present)")
	}
	if node.Available {
		t.Errorf("nodejs available = true, want false (npm absent)")
	}
	if len(node.Errors) == 0 || !strings.Contains(node.Errors[0], "not on PATH") {
		t.Errorf("expected 'not on PATH' error, got %v", node.Errors)
	}
	if node.Vulnerabilities != nil {
		t.Errorf("nodejs vulnerabilities = %#v, want nil when tool absent", node.Vulnerabilities)
	}
}

func TestRunDepScan_MalformedJSON_RecordsErrorButDoesNotFail(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "package.json")

	runner := newFakeRunner()
	runner.outputs["npm audit"] = []byte(`{not json`)
	runner.outputs["npm outdated"] = []byte(`{}`)

	res, err := RunDepScan(context.Background(), Options{
		Workdir:      dir,
		IncludeVulns: true,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("RunDepScan returned error for malformed audit JSON: %v", err)
	}
	node := res.Ecosystems["nodejs"]
	if len(node.Errors) == 0 {
		t.Errorf("expected parse error in errors[], got %v", node.Errors)
	}
	hasParseErr := false
	for _, e := range node.Errors {
		if strings.Contains(e, "parse npm audit") {
			hasParseErr = true
			break
		}
	}
	if !hasParseErr {
		t.Errorf("expected 'parse npm audit' error, got %v", node.Errors)
	}
}

func TestRunDepScan_EcosystemsNarrows(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "package.json", "go.mod")

	runner := newFakeRunner()
	runner.outputs["npm audit"] = []byte(`{"vulnerabilities":{}}`)
	runner.outputs["npm outdated"] = []byte(`{}`)

	res, err := RunDepScan(context.Background(), Options{
		Workdir:      dir,
		Ecosystems:   []string{"nodejs"},
		IncludeVulns: true,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("RunDepScan: %v", err)
	}
	if !res.Ecosystems["nodejs"].Detected {
		t.Errorf("nodejs should have been scanned")
	}
	if res.Ecosystems["go"].Detected {
		t.Errorf("go should NOT be scanned when --ecosystems=nodejs even though go.mod exists")
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "go ") || strings.HasPrefix(call, "govulncheck") {
			t.Errorf("unexpected call to go tooling when ecosystems=[nodejs]: %s", call)
		}
	}
}

func TestRunDepScan_IncludeVulnsFalse_SkipsAuditButRunsOutdated(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "package.json")

	runner := newFakeRunner()
	runner.outputs["npm outdated"] = []byte(`{"a":{}}`)

	res, err := RunDepScan(context.Background(), Options{
		Workdir:      dir,
		IncludeVulns: false,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("RunDepScan: %v", err)
	}
	for _, call := range runner.calls {
		if call == "npm audit" {
			t.Errorf("npm audit should not run when IncludeVulns=false")
		}
	}
	if res.Ecosystems["nodejs"].Outdated != 1 {
		t.Errorf("outdated = %d, want 1", res.Ecosystems["nodejs"].Outdated)
	}
	if res.Ecosystems["nodejs"].Vulnerabilities != nil {
		t.Errorf("vulnerabilities should be nil when IncludeVulns=false")
	}
}

func TestRunDepScan_NoEcosystemFiles_AllUndetected(t *testing.T) {
	dir := t.TempDir()
	runner := newFakeRunner()

	res, err := RunDepScan(context.Background(), Options{
		Workdir:      dir,
		IncludeVulns: true,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("RunDepScan: %v", err)
	}
	for name, e := range res.Ecosystems {
		if e.Detected {
			t.Errorf("ecosystem %s detected=true on empty workdir", name)
		}
	}
	if (res.Totals != Totals{}) {
		t.Errorf("totals = %#v, want zero", res.Totals)
	}
}

func TestRunDepScan_UnknownEcosystem_ReturnsError(t *testing.T) {
	_, err := RunDepScan(context.Background(), Options{
		Workdir:    t.TempDir(),
		Ecosystems: []string{"haskell"},
		Runner:     newFakeRunner(),
	})
	if err == nil {
		t.Fatal("expected error for unknown ecosystem")
	}
	if !strings.Contains(err.Error(), "unknown ecosystem") {
		t.Errorf("error = %q, want containing 'unknown ecosystem'", err.Error())
	}
}

func TestRunDepScan_JSONRoundTrip_StableFieldOrder(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "package.json")

	runner := newFakeRunner()
	runner.outputs["npm audit"] = []byte(`{"vulnerabilities":{"x":{"severity":"high"}}}`)
	runner.outputs["npm outdated"] = []byte(`{"a":{}}`)

	res, err := RunDepScan(context.Background(), Options{
		Workdir:      dir,
		IncludeVulns: true,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("RunDepScan: %v", err)
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundtrip DepScanResult
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if roundtrip.V != 1 {
		t.Errorf("roundtrip V = %d, want 1", roundtrip.V)
	}
	if roundtrip.Ecosystems["nodejs"].Vulnerabilities == nil ||
		roundtrip.Ecosystems["nodejs"].Vulnerabilities.High != 1 {
		t.Errorf("roundtrip nodejs vulns = %#v", roundtrip.Ecosystems["nodejs"].Vulnerabilities)
	}

	// Schema lock — every supported ecosystem must appear in the output even
	// when undetected. Skills depend on this for jq-free path stability.
	for _, want := range []string{"nodejs", "python", "go", "rust"} {
		if _, ok := roundtrip.Ecosystems[want]; !ok {
			t.Errorf("missing ecosystem %q in output (schema requires all four keys)", want)
		}
	}
}

func TestParseNpmAudit_LegacyV6Schema(t *testing.T) {
	// npm v6 emits metadata.vulnerabilities counts directly.
	in := []byte(`{"metadata":{"vulnerabilities":{"critical":2,"high":1,"moderate":3,"low":4,"info":5}}}`)
	vc, err := parseNpmAudit(in)
	if err != nil {
		t.Fatalf("parseNpmAudit: %v", err)
	}
	want := &VulnCount{Critical: 2, High: 1, Moderate: 3, Low: 9} // info folds into Low
	if !reflect.DeepEqual(vc, want) {
		t.Errorf("parseNpmAudit = %#v, want %#v", vc, want)
	}
}

func TestParseNpmAudit_EmptyOutput(t *testing.T) {
	vc, err := parseNpmAudit([]byte(""))
	if err != nil {
		t.Fatalf("parseNpmAudit empty: %v", err)
	}
	if (*vc != VulnCount{}) {
		t.Errorf("empty audit = %#v, want zero", *vc)
	}
}

func TestAddSeverity_UnknownFoldsToLow(t *testing.T) {
	vc := &VulnCount{}
	addSeverity(vc, "totally-unknown")
	if vc.Low != 1 {
		t.Errorf("unknown severity should fold to Low, got %#v", vc)
	}
}

func TestRunDepScan_NpmAuditNonZeroExitStillCounted(t *testing.T) {
	// `npm audit` exits non-zero when vulns are found. The runner must
	// still capture stdout and parse it — the verb treats exit code as a
	// hint, not an error.
	dir := t.TempDir()
	writeFiles(t, dir, "package.json")

	runner := newFakeRunner()
	runner.outputs["npm audit"] = []byte(`{"vulnerabilities":{"x":{"severity":"critical"}}}`)
	runner.exits["npm audit"] = 1
	runner.outputs["npm outdated"] = []byte(`{}`)

	res, err := RunDepScan(context.Background(), Options{
		Workdir:      dir,
		IncludeVulns: true,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("RunDepScan: %v", err)
	}
	if res.Ecosystems["nodejs"].Vulnerabilities == nil ||
		res.Ecosystems["nodejs"].Vulnerabilities.Critical != 1 {
		t.Errorf("npm audit non-zero exit should still be parsed; got %#v",
			res.Ecosystems["nodejs"].Vulnerabilities)
	}
}
