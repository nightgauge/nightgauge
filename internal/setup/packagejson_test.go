package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPackageJSON_Missing(t *testing.T) {
	dir := t.TempDir()
	det, warnings, err := readPackageJSON(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if det.PackageJSONFound {
		t.Errorf("PackageJSONFound = true, want false")
	}
	if det.NodeVersion != defaultNodeVersion {
		t.Errorf("NodeVersion = %q, want %q", det.NodeVersion, defaultNodeVersion)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for missing package.json (warning is added by caller)", warnings)
	}
}

func TestReadPackageJSON_Malformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	det, warnings, err := readPackageJSON(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !det.PackageJSONFound {
		t.Errorf("PackageJSONFound = false, want true (file existed even if malformed)")
	}
	if det.NodeVersion != defaultNodeVersion {
		t.Errorf("NodeVersion = %q, want default %q on malformed input", det.NodeVersion, defaultNodeVersion)
	}
	if len(warnings) == 0 {
		t.Errorf("expected at least one warning on malformed package.json")
	}
}

func TestReadPackageJSON_NodeVersionExtraction(t *testing.T) {
	cases := []struct {
		engines string
		want    string
	}{
		{"^20", "20"},
		{">=18.0.0", "18"},
		{"20.x", "20"},
		{"node@22", "22"},
		{"", defaultNodeVersion},
		{"latest", defaultNodeVersion},
	}
	for _, tc := range cases {
		t.Run(tc.engines, func(t *testing.T) {
			dir := t.TempDir()
			body := `{"engines":{"node":` + jsonString(tc.engines) + `}}`
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			det, _, err := readPackageJSON(dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if det.NodeVersion != tc.want {
				t.Errorf("engines.node=%q -> NodeVersion=%q, want %q", tc.engines, det.NodeVersion, tc.want)
			}
		})
	}
}

func TestReadPackageJSON_DepDetection(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"dependencies": {"typescript": "^5.0.0"},
		"devDependencies": {"vitest": "^1", "eslint": "^9", "prettier": "^3"}
	}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	det, _, err := readPackageJSON(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !det.HasTypeScript {
		t.Errorf("HasTypeScript = false, want true (in dependencies)")
	}
	if !det.HasVitest {
		t.Errorf("HasVitest = false, want true")
	}
	if !det.HasESLint {
		t.Errorf("HasESLint = false, want true")
	}
	if !det.HasPrettier {
		t.Errorf("HasPrettier = false, want true")
	}
}

// jsonString quotes s for safe embedding in a JSON literal in tests.
func jsonString(s string) string {
	out := []byte{'"'}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			out = append(out, '\\', c)
		default:
			out = append(out, c)
		}
	}
	out = append(out, '"')
	return string(out)
}
