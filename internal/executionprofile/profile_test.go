package executionprofile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// Spike #1568 adopted claude-headless and deferred or skipped every other
// adapter, and the daemon's turn host (#1569) registers a conversational
// builder for claude-headless only. The advertised capability follows the
// builder: an adapter is viable exactly when a turn can be dispatched to it.
func TestConversation_AdvertisedExactlyForAdaptersWithATurnBuilder(t *testing.T) {
	reg := adapters.NewRegistry()
	for _, name := range reg.Names() {
		_, hasBuilder := reg.ConversationBuilder(name)
		if got := ConversationViable(name); got != hasBuilder {
			t.Errorf("ConversationViable(%s) = %v, has a conversational builder = %v", name, got, hasBuilder)
		}
		want := []string{"headless"}
		if hasBuilder {
			want = append(want, platform.AgentCapabilityConversation)
		}
		caps := Capabilities([]string{"headless"}, platform.ExecutionProfile{Adapter: name})
		if !reflect.DeepEqual(caps, want) {
			t.Errorf("%s capabilities = %v, want %v", name, caps, want)
		}
	}
	if got := reg.ConversationAdapters(); !reflect.DeepEqual(got, []string{"claude-headless"}) {
		t.Errorf("conversation adapters = %v, want [claude-headless] (spike #1568 adopted it alone)", got)
	}
	if !ConversationViable("claude") {
		t.Error("the claude alias of claude-headless is not viable")
	}
}

// The capability travels on the heartbeat (#1569): the daemon's profile
// source is read on every beat, so switching the workspace's adapter to one
// outside the viable set drops `conversation` from the next beat.
func TestAdvertised_HeartbeatDropsConversationOnAnAdapterSwitch(t *testing.T) {
	hermetic(t)
	root := workspace(t, "ui:\n  core:\n    adapter: claude-headless\n", "")
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		_, _ = w.Write([]byte(`{"ttl_seconds":90}`))
	}))
	defer srv.Close()
	client, err := platform.NewClient(platform.Config{BaseURL: srv.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	reg := platform.NewAgentRegistrationService(client, "test").WithExecutionProfile(Advertised(root))
	capabilities := func() []string {
		t.Helper()
		if err := reg.Heartbeat(context.Background(), "agent-1"); err != nil {
			t.Fatal(err)
		}
		var beat struct {
			Capabilities []string `json:"capabilities"`
		}
		if err := json.Unmarshal([]byte(bodies[len(bodies)-1]), &beat); err != nil {
			t.Fatal(err)
		}
		return beat.Capabilities
	}
	if got := capabilities(); !reflect.DeepEqual(got, []string{"attention_resolve", platform.AgentCapabilityConversation}) {
		t.Errorf("claude-headless beat capabilities = %v", got)
	}
	if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"),
		[]byte("owner: acme\nproject:\n  number: 1\nui:\n  core:\n    adapter: codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := capabilities(); !reflect.DeepEqual(got, []string{"attention_resolve"}) {
		t.Errorf("after switching to codex, beat capabilities = %v", got)
	}
}
