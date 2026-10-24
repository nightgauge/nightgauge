package main

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/trust"
)

type fakeAsker struct {
	d     trust.Decision
	asked []string
}

func (f *fakeAsker) Eligibility(_ context.Context, owner, repo string, number int) trust.Decision {
	f.asked = append(f.asked, owner+"/"+repo)
	return f.d
}

// `nightgauge run <issue>` refuses an issue the gate does not pass, with the
// gate's reason, and runs one it does (#2130).
func TestManualDispatchGate(t *testing.T) {
	refused := &fakeAsker{d: trust.Decision{Reason: "acme/web#7 is not eligible: @outsider has read access to acme/web, " +
		"and the issue does not carry `pipeline:approved`; a user with write access or higher must apply `pipeline:approved` to approve it"}}
	err := manualDispatchGate(context.Background(), refused, "acme/web", 7)
	if err == nil {
		t.Fatal("an ineligible issue was allowed to run")
	}
	for _, want := range []string{"@outsider", "read access", "pipeline:approved"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if len(refused.asked) != 1 || refused.asked[0] != "acme/web" {
		t.Errorf("asked = %v", refused.asked)
	}

	if err := manualDispatchGate(context.Background(), &fakeAsker{d: trust.Decision{Eligible: true}}, "acme/web", 7); err != nil {
		t.Fatalf("an eligible issue was refused: %v", err)
	}
	if err := manualDispatchGate(context.Background(), &fakeAsker{d: trust.Decision{Eligible: true}}, "web", 7); err == nil {
		t.Fatal("a malformed repository passed the gate")
	}
}
