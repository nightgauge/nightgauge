package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
)

// TestOpenCodeModelArg_AgreesWithTheModelParser: the adapter's pre-spawn model
// check and internal/models' parser apply one provider-key rule and one
// model-id rule (#1707), so a model the adapter would put on -m is exactly a
// model the parser gives a provider and a bare id. The adapter trims
// surrounding space first; the parser never trims. The rows are the shared
// provider-for table's opencode models plus edge cases of each rule.
func TestOpenCodeModelArg_AgreesWithTheModelParser(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "models", "testdata", "provider-for-cases.json"))
	if err != nil {
		t.Fatalf("reading the provider-for table: %v", err)
	}
	var table struct {
		ProviderFor []struct {
			Adapter string `json:"adapter"`
			Model   string `json:"model"`
		} `json:"provider_for"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parsing the provider-for table: %v", err)
	}
	inputs := []string{
		"a/b", "a-1/b.c", "0/x", "-a/b", "a./b", "A/b", "a_b/c", "a/-b", "a/b c", "a/b\x7f",
		"a/b ", "a/​", "a//b", "/", "a/", "", "sonnet", "lmstudio/qwen/qwen3.8-27b",
	}
	for _, row := range table.ProviderFor {
		if row.Adapter == "opencode" {
			inputs = append(inputs, row.Model)
		}
	}
	accepted := 0
	for _, m := range inputs {
		_, argErr := OpenCodeModelArg(m)
		_, bareID, _ := models.ParseOpenCodeModel(strings.TrimSpace(m))
		if (argErr == nil) != (bareID != "") {
			t.Errorf("model %q: OpenCodeModelArg err = %v, but ParseOpenCodeModel bare id = %q", m, argErr, bareID)
		}
		if argErr == nil {
			accepted++
		}
	}
	if accepted == 0 || accepted == len(inputs) {
		t.Fatalf("%d of %d models accepted; the rows must exercise both answers", accepted, len(inputs))
	}
}
