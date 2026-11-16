package opencodeplugin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// readCapDriver imports the embedded gates.js and runs capReadLimit over
// each case's 2.x `input`, printing the resulting inputs as one JSON array.
const readCapDriver = `
const mod = await import(process.env.NG_GATES_PATH);
const cases = JSON.parse(process.env.NG_CASES);
const out = cases.map((input) => { mod.capReadLimit(input); return input; });
process.stdout.write(JSON.stringify(out));
`

// TestCapReadLimit2178 proves a read is given the stage's line cap when it
// names no limit or a larger one, keeps a smaller limit, and is left alone
// when no cap is set (#2178).
func TestCapReadLimit2178(t *testing.T) {
	node := requireNode(t)
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	entry, err := Write(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	gates := filepath.Join(entry, "nightgauge", "gates.js")
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(readCapDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []map[string]any{
		{"path": "/w/a.go"},
		{"path": "/w/a.go", "limit": 5000},
		{"path": "/w/a.go", "limit": 50, "offset": 401},
		{"path": "/w/a.go", "limit": 0},
	}
	raw, _ := json.Marshal(cases)
	run := func(capValue string) []map[string]any {
		t.Helper()
		cmd := exec.Command(node, driver)
		cmd.Env = append(removeEnv(os.Environ(), EnvReadMaxLines),
			"NG_GATES_PATH="+gates, "NG_CASES="+string(raw))
		if capValue != "" {
			cmd.Env = append(cmd.Env, EnvReadMaxLines+"="+capValue)
		}
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Fatalf("driver: %v\n%s", err, ee.Stderr)
			}
			t.Fatal(err)
		}
		var got []map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("driver output %q: %v", out, err)
		}
		return got
	}

	got := run("400")
	wantLimits := []float64{400, 400, 50, 400}
	for i, want := range wantLimits {
		if got[i]["limit"] != want {
			t.Errorf("case %d: limit = %v, want %v", i, got[i]["limit"], want)
		}
	}
	if got[2]["offset"] != float64(401) {
		t.Errorf("offset changed: %v", got[2]["offset"])
	}

	for _, capValue := range []string{"", "abc", "-1"} {
		got := run(capValue)
		if _, has := got[0]["limit"]; has {
			t.Errorf("cap %q: an unset or invalid cap set limit %v", capValue, got[0]["limit"])
		}
		if got[1]["limit"] != float64(5000) {
			t.Errorf("cap %q: a model limit changed to %v", capValue, got[1]["limit"])
		}
	}
}

// readCapThroughHookDriver drives a read through the entry's registered
// execute.before hook on the fake 2.x host and prints the input the read
// would run with: on 2.2.7 the hook's arg.input is what the tool executes.
var readCapThroughHookDriver = hostDriver(`
const arg = toolArg("read", { path: "a.go" });
const res = await runToolCall(host, arg);
process.stdout.write(JSON.stringify({ ...res, input: arg.input }));
`)

// TestCapReadLimitAppliesThroughTheRegisteredHook (#3302): the cap reaches a
// read through the hook OpenCode 2.x calls, on the 2.x argument's `input`,
// not only through capReadLimit called directly.
func TestCapReadLimitAppliesThroughTheRegisteredHook(t *testing.T) {
	node := requireNode(t)
	dir, err := Write(filepath.Join(t.TempDir(), "plugin"))
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(readCapThroughHookDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	// A gate verb that drains its stdin (writeShFixture) and allows: the read
	// gate's verb must run for the cap to be reached.
	allow := filepath.Join(t.TempDir(), "allow.sh")
	writeShFixture(t, allow, "exec >/dev/null\nexit 0\n", 0o700)
	root := t.TempDir()
	cmd := exec.Command(node, driver)
	cmd.Env = append(withHookBin(removeEnv(os.Environ(), EnvReadMaxLines), allow),
		"NG_PLUGIN_PATH="+filepath.Join(dir, EntryFile), "NG_CWD="+root, EnvReadMaxLines+"=400")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("driver: %v (%s)", err, out)
	}
	var got struct {
		Threw   bool           `json:"threw"`
		Message string         `json:"message"`
		Input   map[string]any `json:"input"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("driver output %q: %v", out, err)
	}
	if got.Threw {
		t.Fatalf("the read was refused: %s", got.Message)
	}
	if got.Input["limit"] != float64(400) {
		t.Errorf("input.limit = %v, want the stage cap 400", got.Input["limit"])
	}
}
