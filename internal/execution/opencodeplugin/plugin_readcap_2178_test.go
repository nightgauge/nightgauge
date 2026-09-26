package opencodeplugin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// readCapDriver imports the embedded gates.js and runs capReadLimit over
// each case's args, printing the resulting args as one JSON array.
const readCapDriver = `
const mod = await import(process.env.NG_GATES_PATH);
const cases = JSON.parse(process.env.NG_CASES);
const out = cases.map((args) => { const o = { args }; mod.capReadLimit(o); return o.args; });
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
	gates := filepath.Join(filepath.Dir(entry), "nightgauge", "gates.js")
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(readCapDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []map[string]any{
		{"filePath": "/w/a.go"},
		{"filePath": "/w/a.go", "limit": 5000},
		{"filePath": "/w/a.go", "limit": 50, "offset": 401},
		{"filePath": "/w/a.go", "limit": 0},
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
