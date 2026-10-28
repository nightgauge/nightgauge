package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type copilotToolPolicyCase struct {
	Name         string   `json:"name"`
	AllowedTools []string `json:"allowed_tools"`
	WantArgs     []string `json:"want_args"`
	WantDropped  []string `json:"want_dropped"`
}

// TestCopilotToolPolicy_Fixture asserts the shared Go/SDK parity fixture.
func TestCopilotToolPolicy_Fixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "copilot-tool-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []copilotToolPolicyCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			args, dropped := copilotToolPolicy(c.AllowedTools)
			if !slices.Equal(args, c.WantArgs) {
				t.Errorf("args = %q, want %q", args, c.WantArgs)
			}
			if len(dropped)+len(c.WantDropped) > 0 && !slices.Equal(dropped, c.WantDropped) {
				t.Errorf("dropped = %q, want %q", dropped, c.WantDropped)
			}
			if len(c.AllowedTools) > 0 && slices.Contains(args, "--allow-all-tools") {
				t.Errorf("an allow list must never emit --allow-all-tools: %q", args)
			}
		})
	}
}
