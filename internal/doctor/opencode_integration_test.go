//go:build opencode_integration

package doctor

// The doctor's OpenCode catalog probe against the installed opencode binary.
// The binary runs only through adapters.OpenCodeProbe: a throwaway HOME,
// TMPDIR and four XDG directories, no credential, the probe's timeout, and
// its process group killed once it exits:
//
//	go test -tags opencode_integration ./internal/doctor/ -run OnTheBinary -count=1

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// TestOpenCodeCatalogProbeOnTheBinary: for a hosted provider whose block the
// per-run config does not declare, the row reads the listing the installed
// binary prints for it. The binary lists openai's models only when one of its
// variables is set, so the probe sets the one the environment holds to a
// placeholder, and the row names it when the environment holds none.
func TestOpenCodeCatalogProbeOnTheBinary(t *testing.T) {
	bin, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("opencode is not on PATH")
	}
	const key = "set-by-the-test"
	for _, c := range []struct {
		name, model, credential, want string
		modelOK                       bool
	}{
		{"listed", "openai/gpt-4.1", key, "", true},
		{"not listed", "openai/nightgauge-no-such-model", key, "does not list opencode.model openai/nightgauge-no-such-model", false},
		{"no credential", "openai/gpt-4.1", "", "OPENAI_API_KEY", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", c.credential)
			f := newOpenCodeFixture(t, config.OpenCodeConfig{Model: c.model})
			if c.credential != "" {
				f.env["OPENAI_API_KEY"] = c.credential
			}
			f.probe.lookPath = func(string) (string, error) { return bin, nil }
			f.probe.version = adapters.OpenCodeVersionOf
			f.probe.models = runOpenCodeModels
			h := f.check()
			text := rowText(t, h)
			said := "remediation: " + h.Remediation + "\nwarnings: " + strings.Join(h.Warnings, "\n")
			if got := derefBool(h.ModelOK); got != fmt.Sprint(c.modelOK) {
				t.Fatalf("ModelOK = %s, want %v\n%s", got, c.modelOK, said)
			}
			if c.want != "" && !strings.Contains(h.Remediation, c.want) {
				t.Errorf("the remediation does not say %q\n%s", c.want, said)
			}
			if strings.Contains(text, "could not parse") {
				t.Errorf("the row reports an unparseable listing\n%s", said)
			}
			if strings.Contains(text, key) {
				t.Error("the row prints the credential's value")
			}
		})
	}
}
