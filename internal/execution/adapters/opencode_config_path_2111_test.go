package adapters

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/models"
)

// TestOpenCodeRefusalsNameTheResolvedConfigFile: every OpenCode refusal that
// tells the operator to set a key in the machine-tier config names the file
// the loader actually reads, which follows XDG_CONFIG_HOME, and never a
// hardcoded ~/.nightgauge/config.yaml that this process does not read.
func TestOpenCodeRefusalsNameTheResolvedConfigFile(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	want := filepath.Join(xdg, "nightgauge", "config.yaml")
	if got := config.MachineConfigFile(); got != want {
		t.Fatalf("config.MachineConfigFile() = %q, want %q", got, want)
	}

	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: no refusal", name)
			return
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s does not name %s: %v", name, want, err)
		}
		if strings.Contains(err.Error(), "~/.nightgauge/config.yaml") {
			t.Errorf("%s still names the hardcoded ~/.nightgauge/config.yaml: %v", name, err)
		}
	}

	// The zero-limit refusal: no limit set and none discovered.
	settings := lmStudioSettings()
	settings.Limit = config.OpenCodeLimit{}
	in, err := OpenCodeConfigInputFor(settings, RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b"}, goldenRunRoot, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	in.Discover = func(OpenCodeEndpoint, string) (models.LocalDescriptor, error) {
		return models.LocalDescriptor{}, errors.New("the server refused the connection")
	}
	_, err = BuildOpenCodeConfig(in)
	check("the zero-limit refusal", err)

	// The undeclared-local-endpoint refusal: a local provider key with no
	// endpoint declared.
	_, err = buildOpenCodeConfigFor(t, config.OpenCodeConfig{}, RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b"}, nil)
	check("the undeclared-local-endpoint refusal", err)

	// The managed-config refusal.
	managed := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(managed, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	check("the managed-config refusal", openCodeManagedConfigRefusal([]string{managed}))

	// The binary refusals: a relative pin, and no opencode on PATH.
	_, err = ResolveOpenCodeBinary("opencode", func(string) (string, error) { return "", errors.New("not found") })
	check("the relative-pin refusal", err)
	_, err = ResolveOpenCodeBinary("", func(string) (string, error) { return "", errors.New("not found") })
	check("the not-on-PATH refusal", err)
}
