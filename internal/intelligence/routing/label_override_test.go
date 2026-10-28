package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type modelLabelCase struct {
	Name         string   `json:"name"`
	Labels       []string `json:"labels"`
	Base         string   `json:"base"`
	WantPin      string   `json:"want_pin"`
	WantMax      string   `json:"want_max"`
	WantLabel    string   `json:"want_label"`
	WantSource   string   `json:"want_source"`
	WantWarnings int      `json:"want_warnings"`
	WantModel    string   `json:"want_model"`
}

// TestModelLabelOverridesCrossLanguageFixture drives the label rule from the
// fixture the TS mirror (skillRunner.labelPin.test.ts) also consumes (#8).
func TestModelLabelOverridesCrossLanguageFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "model-label-overrides-crosslang.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []modelLabelCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			o, warnings := ParseModelLabels(c.Labels)
			if o.Pin != c.WantPin || o.Max != c.WantMax || o.Label != c.WantLabel {
				t.Errorf("override = %+v, want pin=%q max=%q label=%q", o, c.WantPin, c.WantMax, c.WantLabel)
			}
			if o.Source() != c.WantSource {
				t.Errorf("Source() = %q, want %q", o.Source(), c.WantSource)
			}
			if len(warnings) != c.WantWarnings {
				t.Errorf("warnings = %q, want %d", warnings, c.WantWarnings)
			}
			if got := o.ApplyModelLabelCap(o.ApplyModelLabelPin(c.Base)); got != c.WantModel {
				t.Errorf("cap(pin(%q)) = %q, want %q", c.Base, got, c.WantModel)
			}
		})
	}
}

func TestModelLabelOverrideZeroValueIsIdentity(t *testing.T) {
	var o ModelLabelOverride
	for _, m := range []string{"haiku", "sonnet", "opus", "fable", "", "local"} {
		if got := o.ApplyModelLabelCap(o.ApplyModelLabelPin(m)); got != m {
			t.Errorf("zero override changed %q to %q", m, got)
		}
	}
}
