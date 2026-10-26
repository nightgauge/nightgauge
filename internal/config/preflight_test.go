package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadPreflightYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "project:\n  owner: acme\n  repo: app\n" + body
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(root)
}

func TestEnvironmentConfig_AbsentKeyMeansNoChecks(t *testing.T) {
	cfg, err := loadPreflightYAML(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Preflight != nil {
		t.Fatalf("Preflight = %+v, want nil", cfg.Preflight)
	}
}

func TestEnvironmentConfig_ParsesTypedChecks(t *testing.T) {
	cfg, err := loadPreflightYAML(t, `preflight:
  environment:
    - name: local-stack
      run: [curl, -sf, http://localhost:3001/health]
      remedy: "Run ./scripts/dev-up.sh"
    - name: emulator-storage
      run: [adb, shell, df, -m, /data]
      expect:
        stdout_number: {capture: '(\d+)\s+\d+%', min: 2000}
      timeout: 45s
      remedy: "Run scripts/sweep-avd.sh --recreate"
`)
	if err != nil {
		t.Fatal(err)
	}
	checks := cfg.Preflight.Environment
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(checks))
	}
	if got := strings.Join(checks[0].Run, " "); got != "curl -sf http://localhost:3001/health" {
		t.Errorf("run = %q", got)
	}
	if checks[0].EffectiveTimeout() != DefaultEnvironmentCheckTimeout {
		t.Errorf("default timeout = %s", checks[0].EffectiveTimeout())
	}
	n := checks[1].Expect.StdoutNumber
	if n == nil || n.Min == nil || *n.Min != 2000 || checks[1].EffectiveTimeout().String() != "45s" {
		t.Errorf("stdout_number/timeout not parsed: %+v %s", n, checks[1].EffectiveTimeout())
	}
}

func TestEnvironmentConfig_Rejections(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"shell string run", "    - name: a\n      run: \"curl -sf http://x | grep ok\"\n      remedy: r\n", "must be a list of arguments"},
		{"empty run", "    - name: a\n      run: []\n      remedy: r\n", "non-empty list"},
		{"expression expect", "    - name: a\n      run: [df]\n      expect: \"free_mb > 2000\"\n      remedy: r\n", "expressions are not accepted"},
		{"unknown expect key", "    - name: a\n      run: [df]\n      expect: {free_mb: 2000}\n      remedy: r\n", `unknown key "free_mb"`},
		{"two capture groups", "    - name: a\n      run: [df]\n      expect: {stdout_number: {capture: '(\\d+) (\\d+)', min: 1}}\n      remedy: r\n", "exactly one capture group"},
		{"no bounds", "    - name: a\n      run: [df]\n      expect: {stdout_number: {capture: '(\\d+)'}}\n      remedy: r\n", "needs min, max"},
		{"min over max", "    - name: a\n      run: [df]\n      expect: {stdout_number: {capture: '(\\d+)', min: 5, max: 1}}\n      remedy: r\n", "exceeds max"},
		{"bad regex", "    - name: a\n      run: [df]\n      expect: {stdout_matches: '('}\n      remedy: r\n", "stdout_matches"},
		{"missing remedy", "    - name: a\n      run: [df]\n", "remedy is required"},
		{"missing name", "    - run: [df]\n      remedy: r\n", "name is required"},
		{"duplicate name", "    - name: a\n      run: [df]\n      remedy: r\n    - name: a\n      run: [df]\n      remedy: r\n", "duplicate"},
		{"timeout too long", "    - name: a\n      run: [df]\n      timeout: 11m\n      remedy: r\n", "at most"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadPreflightYAML(t, "preflight:\n  environment:\n"+tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
