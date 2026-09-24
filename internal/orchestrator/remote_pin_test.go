package orchestrator

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

const remotePinLocalModel = "lmstudio/qwen/qwen3.8-27b"

// remotePinFakes is a machine where every adapter is usable, no variable is
// set, and the opencode catalog lists the one custom local model (the #1627
// fixture's).
type remotePinFakes struct {
	usable      bool
	usableWhy   string
	env         map[string]string
	catalog     []string
	catalogHits int
}

func newRemotePinFakes() *remotePinFakes {
	return &remotePinFakes{usable: true, env: map[string]string{}, catalog: []string{remotePinLocalModel}}
}

func (f *remotePinFakes) deps() RemotePinDeps {
	return RemotePinDeps{
		AdapterUsable: func(string) (bool, string) { return f.usable, f.usableWhy },
		LookupEnv: func(k string) (string, bool) {
			v, ok := f.env[k]
			return v, ok
		},
		Catalog: func(string) ([]string, error) {
			f.catalogHits++
			return f.catalog, nil
		},
	}
}

func TestRemotePinAcceptsACatalogModel(t *testing.T) {
	f := newRemotePinFakes()
	if err := ValidateRemotePin("opencode", remotePinLocalModel, f.deps()); err != nil {
		t.Fatalf("a model in the local catalog was refused: %v", err)
	}
	if f.catalogHits != 1 {
		t.Errorf("catalog read %d times, want 1", f.catalogHits)
	}
}

func TestRemotePinNoFieldsIsNoRequest(t *testing.T) {
	f := newRemotePinFakes()
	f.usable = false
	if err := ValidateRemotePin("", "", f.deps()); err != nil {
		t.Fatalf("an empty request must be accepted without any check: %v", err)
	}
}

func TestRemotePinRefusalsNameTheCause(t *testing.T) {
	for _, tc := range []struct {
		name, adapter, model, want string
		setup                      func(*remotePinFakes)
	}{
		{name: "unknown adapter", adapter: "nope", want: "not one this machine can run"},
		{name: "model without adapter", model: remotePinLocalModel, want: "without an adapter"},
		{name: "model absent from the catalog", adapter: "opencode", model: "lmstudio/qwen/other-model", want: "not in this machine's opencode catalog"},
		{name: "hosted provider without a key", adapter: "opencode", model: "openai/gpt-x", want: "no credentials configured"},
		{name: "anthropic without a key", adapter: "opencode", model: "anthropic/claude-sonnet-5", want: "ANTHROPIC_API_KEY"},
		{name: "prerequisites absent", adapter: "opencode", model: remotePinLocalModel, want: "cannot enable it: " + adapters.ExperimentalOpenCodeEnvVar,
			setup: func(f *remotePinFakes) {
				f.usable, f.usableWhy = false, adapters.ExperimentalOpenCodeEnvVar+" is not set"
			}},
		{name: "adapter model check", adapter: "opencode", model: "lmstudio/-x", want: "refuses model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemotePinFakes()
			if tc.setup != nil {
				tc.setup(f)
			}
			err := ValidateRemotePin(tc.adapter, tc.model, f.deps())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

// A value failing its shape is refused before any other check runs, so no
// probe, catalog read or argv ever sees it. Each row names the check that
// must refuse it: removing the leading-dash check turns the --auto and -m
// rows red, because the pattern then refuses them with another reason.
func TestRemotePinShapeRefusedFirst(t *testing.T) {
	long := "lmstudio/" + strings.Repeat("a", 300-len("lmstudio/"))
	for _, tc := range []struct{ adapter, model, want string }{
		{"opencode", "-m", "must not start with '-'"},
		{"opencode", "--auto", "must not start with '-'"},
		{"claude", "--auto", "must not start with '-'"},
		{"opencode", "../../x", "must not contain '..'"},
		{"opencode", "lmstudio/../../x", "must not contain '..'"},
		{"opencode", long, "longer than 200 bytes"},
		{"opencode", "a b", "not an opencode <provider>/<model> id"},
		{"claude", "a b", "not a model id"},
		{"opencode", "lmstudio/qwen\n", "not an opencode <provider>/<model> id"},
	} {
		f := newRemotePinFakes()
		probed := false
		deps := f.deps()
		deps.AdapterUsable = func(string) (bool, string) { probed = true; return true, "" }
		err := ValidateRemotePin(tc.adapter, tc.model, deps)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %q: got %v, want %q", tc.adapter, tc.model, err, tc.want)
		}
		if probed || f.catalogHits != 0 {
			t.Errorf("%s %q: a check ran after the shape refusal", tc.adapter, tc.model)
		}
		if err != nil && len(tc.model) > 20 && strings.Contains(err.Error(), tc.model) {
			t.Errorf("%s: the refusal echoes the refused value", tc.adapter)
		}
	}
}

func TestRemotePinAdapterWithoutModel(t *testing.T) {
	f := newRemotePinFakes()
	if err := ValidateRemotePin("opencode", "", f.deps()); err != nil {
		t.Fatalf("an adapter-only request was refused: %v", err)
	}
	if f.catalogHits != 0 {
		t.Error("the catalog was read for a request without a model")
	}
}

// The allow-list is the extension's dispatch vocabulary, because the
// extension executes the pin, and every entry must resolve in the Go
// registry, which supplies Agentic and ValidateModel.
func TestRemotePinAllowListMatchesTheExtension(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "packages", "nightgauge-vscode", "src", "config", "schema.ts"))
	if err != nil {
		t.Fatalf("read schema.ts: %v", err)
	}
	block := regexp.MustCompile(`(?s)export const AdapterEnumSchema = z\.enum\(\[(.*?)\]\)`).FindSubmatch(src)
	if block == nil {
		t.Fatal("AdapterEnumSchema not found in schema.ts")
	}
	var ext []string
	for _, m := range regexp.MustCompile(`"([a-z0-9-]+)"`).FindAllSubmatch(block[1], -1) {
		ext = append(ext, string(m[1]))
	}
	slices.Sort(ext)
	got := slices.Clone(remotePinAdapters)
	slices.Sort(got)
	if !slices.Equal(got, ext) {
		t.Fatalf("allow-list %v differs from the extension's AdapterEnumSchema %v", got, ext)
	}
	reg := adapters.NewRegistry()
	for _, name := range remotePinAdapters {
		if _, err := reg.Get(name); err != nil {
			t.Errorf("allow-listed adapter %q does not resolve in the registry: %v", name, err)
		}
	}
}
