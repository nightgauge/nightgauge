package executionprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/credshape"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// hermetic isolates a test from the operator's machine tier and from every
// environment layer the three chains read.
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{
		"NIGHTGAUGE_ADAPTER",
		"NIGHTGAUGE_PERFORMANCE_MODE",
		"NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT",
	} {
		t.Setenv(k, "")
	}
}

// workspace builds a repository with the given config.yaml body and, when
// mode is non-empty, a performance-mode.yaml in its CHECKOUT.
func workspace(t *testing.T, configYAML, mode string) string {
	t.Helper()
	root := layouttest.Repo(t)
	if configYAML != "" {
		configYAML = "owner: acme\nproject:\n  number: 1\n" + configYAML
		if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte(configYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if mode != "" {
		if err := os.WriteFile(layouttest.CheckoutPath(t, root, "performance-mode.yaml"), []byte("mode: "+mode+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The advertised triple is what the existing resolvers return for the same
// root. Hardcoding any of the three in Resolve turns this red, because the
// fixtures below move every one of them off its default.
func TestResolve_EqualsTheExistingResolvers(t *testing.T) {
	cases := []struct {
		name, config, mode string
		want               platform.ExecutionProfile
	}{
		{
			name:   "config adapter, file mode, config effort inside the envelope",
			config: "ui:\n  core:\n    adapter: codex\nmodel_routing:\n  default_effort: high\n",
			mode:   "frontier",
			want: platform.ExecutionProfile{
				Adapter: "codex", AdapterDisplayName: "Codex", AdapterSource: "config",
				PerformanceMode: "frontier", PerformanceModeSource: "file",
				Effort: "high", EffortSource: "config",
			},
		},
		{
			name:   "efficiency caps a configured high effort at medium",
			config: "ui:\n  core:\n    adapter: claude\nmodel_routing:\n  default_effort: high\n",
			mode:   "efficiency",
			want: platform.ExecutionProfile{
				Adapter: "claude-headless", AdapterDisplayName: "Claude Headless", AdapterSource: "config",
				PerformanceMode: "efficiency", PerformanceModeSource: "file",
				Effort: "medium", EffortSource: EffortSourceMode,
			},
		},
		{
			name: "maximum's effort floor names an effort nothing configured",
			mode: "maximum",
			want: platform.ExecutionProfile{
				Adapter: "claude-headless", AdapterDisplayName: "Claude Headless", AdapterSource: "default",
				PerformanceMode: "maximum", PerformanceModeSource: "file",
				Effort: "high", EffortSource: EffortSourceMode,
			},
		},
		{
			name: "nothing configured",
			want: platform.ExecutionProfile{
				Adapter: "claude-headless", AdapterDisplayName: "Claude Headless", AdapterSource: "default",
				PerformanceMode: "elevated", PerformanceModeSource: "default",
				Effort: "", EffortSource: EffortSourceDefault,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hermetic(t)
			root := workspace(t, tc.config, tc.mode)

			got, err := Resolve(root)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != tc.want {
				t.Errorf("profile =\n  %+v\nwant\n  %+v", got, tc.want)
			}

			// And independently, the same answers from the resolvers themselves.
			cfg, err := config.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			configAdapter := ""
			if cfg.UI != nil && cfg.UI.Core != nil {
				configAdapter = cfg.UI.Core.Adapter
			}
			name, _, err := adapters.NewRegistry().ResolveName("", configAdapter)
			if err != nil {
				t.Fatal(err)
			}
			mode := routing.ResolvePerformanceMode(root)
			raw, _ := config.ResolveDefaultEffort(cfg, os.Getenv)
			effort := routing.ClampEffortToEnvelope(raw, routing.Envelope(mode))
			if got.Adapter != name || got.PerformanceMode != string(mode) || got.Effort != effort {
				t.Errorf("advertised (%s, %s, %q) != resolvers (%s, %s, %q)",
					got.Adapter, got.PerformanceMode, got.Effort, name, mode, effort)
			}
		})
	}
}

func TestResolve_EnvironmentLayersReportEnvProvenance(t *testing.T) {
	hermetic(t)
	root := workspace(t, "ui:\n  core:\n    adapter: codex\nmodel_routing:\n  default_effort: low\n", "frontier")
	t.Setenv("NIGHTGAUGE_ADAPTER", "gemini")
	t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "elevated")
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT", "medium")

	got, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := platform.ExecutionProfile{
		Adapter: "gemini", AdapterDisplayName: "Gemini Headless", AdapterSource: "env",
		PerformanceMode: "elevated", PerformanceModeSource: "env",
		Effort: "medium", EffortSource: "env",
	}
	if got != want {
		t.Errorf("profile = %+v, want %+v", got, want)
	}
}

// Changing any of the three inputs on disk changes the next resolution: the
// heartbeat resolves fresh, so nothing is cached between calls.
func TestResolve_ReflectsOnDiskChanges(t *testing.T) {
	hermetic(t)
	root := workspace(t, "ui:\n  core:\n    adapter: codex\n", "elevated")
	before, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"),
		[]byte("owner: acme\nproject:\n  number: 1\nui:\n  core:\n    adapter: opencode\nmodel_routing:\n  default_effort: low\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layouttest.CheckoutPath(t, root, "performance-mode.yaml"), []byte("mode: frontier\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if before.Adapter != "codex" || after.Adapter != "opencode" {
		t.Errorf("adapter %s -> %s, want codex -> opencode", before.Adapter, after.Adapter)
	}
	if after.PerformanceMode != "frontier" || after.Effort != "low" {
		t.Errorf("after = %+v, want frontier/low", after)
	}
}

func TestResolve_UnknownAdapterAdvertisesNothing(t *testing.T) {
	hermetic(t)
	root := workspace(t, "ui:\n  core:\n    adapter: not-an-adapter\n", "")
	if _, err := Resolve(root); err == nil {
		t.Fatal("Resolve accepted an unknown adapter; a profile must not guess")
	}
}

// The serialized profile is an allowlist of field names, carries no path and
// nothing credential-shaped. Adding a field (say, the workspace root) turns
// the first assertion red; leaking a value turns the others red.
func TestResolve_SerializedProfileIsScopeBounded(t *testing.T) {
	hermetic(t)
	root := workspace(t, "ui:\n  core:\n    adapter: codex\nmodel_routing:\n  default_effort: high\n", "frontier")
	p, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	allow := []string{
		"adapter", "adapter_display_name", "adapter_source", "effort", "effort_source",
		"performance_mode", "performance_mode_source",
	}
	if !reflect.DeepEqual(keys, allow) {
		t.Errorf("serialized fields = %v, want exactly %v", keys, allow)
	}
	for k, v := range fields {
		s, ok := v.(string)
		if !ok {
			t.Errorf("%s is %T, want string", k, v)
			continue
		}
		if strings.Contains(s, "/") || strings.Contains(s, root) {
			t.Errorf("%s = %q looks like a path", k, s)
		}
		if credshape.Contains(s) {
			t.Errorf("%s = %q is credential-shaped", k, s)
		}
	}
}

// Spike #1568 adopted claude-headless, but no code path serves a
// conversational turn until the daemon's turn runner (#1569) lands, so none
// may advertise the capability yet. When the runner registers an adapter's
// turn builder, this test is where that decision becomes visible.
func TestConversation_NotAdvertisedUntilARunnerServesTurns(t *testing.T) {
	for _, name := range adapters.NewRegistry().Names() {
		if ConversationViable(name) {
			t.Errorf("%s is marked conversation-viable with no turn runner behind it", name)
		}
		caps := Capabilities([]string{"headless"}, platform.ExecutionProfile{Adapter: name})
		if !reflect.DeepEqual(caps, []string{"headless"}) {
			t.Errorf("%s capabilities = %v, want [headless]", name, caps)
		}
	}

	conversationViableAdapters["codex"] = true
	t.Cleanup(func() { delete(conversationViableAdapters, "codex") })
	caps := Capabilities([]string{"headless"}, platform.ExecutionProfile{Adapter: "codex"})
	if !reflect.DeepEqual(caps, []string{"headless", platform.AgentCapabilityConversation}) {
		t.Errorf("viable adapter capabilities = %v, want conversation appended", caps)
	}
}
