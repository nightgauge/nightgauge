package github

import (
	"context"
	"errors"
	"testing"
)

// fakeLabelEnsurer is a LabelEnsurer that records every Create it is asked to
// perform, so a test can assert on the MUTATIONS issued rather than only on the
// returned slice. The distinction matters for the idempotency claim: a second
// call that returns an empty "created" list while still issuing five create
// mutations would satisfy a return-value-only assertion and still hammer the
// API on every preflight.
type fakeLabelEnsurer struct {
	existing []*Label
	creates  []string
	failOn   string
}

func (f *fakeLabelEnsurer) List(context.Context) ([]*Label, error) {
	out := make([]*Label, len(f.existing))
	copy(out, f.existing)
	return out, nil
}

func (f *fakeLabelEnsurer) Create(_ context.Context, name, description, color string) (*Label, error) {
	f.creates = append(f.creates, name)
	if f.failOn == name {
		return nil, errors.New("boom")
	}
	l := &Label{ID: "L_" + name, Name: name, Description: description, Color: color}
	f.existing = append(f.existing, l)
	return l, nil
}

func TestEnsureRequiredLabels_CreatesMissingAndIsIdempotent(t *testing.T) {
	// A repo carrying only type:epic — the shape every repo in this workspace
	// was actually in when #993 was written.
	f := &fakeLabelEnsurer{existing: []*Label{{ID: "L_epic", Name: LabelEpic}}}

	created, err := EnsureRequiredLabels(context.Background(), f)
	if err != nil {
		t.Fatalf("EnsureRequiredLabels: %v", err)
	}

	want := []string{}
	for _, l := range RequiredLabels {
		if l.Name != LabelEpic {
			want = append(want, l.Name)
		}
	}
	if len(created) != len(want) {
		t.Fatalf("created %v (%d), want %d labels", created, len(created), len(want))
	}
	for i, name := range want {
		if created[i] != name {
			t.Errorf("created[%d] = %q, want %q", i, created[i], name)
		}
	}
	if len(f.creates) != len(want) {
		t.Errorf("issued %d create mutations, want %d", len(f.creates), len(want))
	}
	// The label that already existed must never be re-created.
	for _, c := range f.creates {
		if c == LabelEpic {
			t.Errorf("re-created already-present label %q", LabelEpic)
		}
	}

	// Second call: zero mutations. This is the assertion that makes the verb
	// safe to run from a per-cycle preflight or a provisioning script.
	f.creates = nil
	created2, err := EnsureRequiredLabels(context.Background(), f)
	if err != nil {
		t.Fatalf("second EnsureRequiredLabels: %v", err)
	}
	if len(created2) != 0 {
		t.Errorf("second call created %v, want none", created2)
	}
	if len(f.creates) != 0 {
		t.Errorf("second call issued %d create mutations, want 0", len(f.creates))
	}
}

func TestEnsureRequiredLabels_ReturnsPartialProgressOnFailure(t *testing.T) {
	// A failure mid-batch must not discard what was already created, or a
	// retry-after-failure loses the record of what it does not need to redo.
	f := &fakeLabelEnsurer{failOn: RequiredLabels[1].Name}

	created, err := EnsureRequiredLabels(context.Background(), f)
	if err == nil {
		t.Fatal("expected an error when Create fails")
	}
	if len(created) != 1 || created[0] != RequiredLabels[0].Name {
		t.Errorf("created = %v, want exactly [%q] preserved across the failure",
			created, RequiredLabels[0].Name)
	}
}

func TestEnsureRequiredLabels_NilService(t *testing.T) {
	if _, err := EnsureRequiredLabels(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a nil label service")
	}
}

func TestMissingRequiredLabels(t *testing.T) {
	// GetRepoLabels returns name → nodeID; presence is what matters.
	repoLabels := map[string]string{LabelEpic: "L_epic", "type:bug": "L_bug"}
	missing := MissingRequiredLabels(repoLabels)

	for _, m := range missing {
		if m == LabelEpic {
			t.Errorf("reported present label %q as missing", LabelEpic)
		}
	}
	found := map[string]bool{}
	for _, m := range missing {
		found[m] = true
	}
	if !found[LabelRefined] {
		t.Errorf("did not report %q missing", LabelRefined)
	}
	if !found[LabelAutoProcess] {
		t.Errorf("did not report %q missing", LabelAutoProcess)
	}
	if got, want := len(missing), len(RequiredLabels)-1; got != want {
		t.Errorf("missing count = %d, want %d", got, want)
	}

	// A fully-provisioned repo reports nothing.
	full := map[string]string{}
	for _, l := range RequiredLabels {
		full[l.Name] = "L_" + l.Name
	}
	if got := MissingRequiredLabels(full); len(got) != 0 {
		t.Errorf("fully-provisioned repo reported missing %v", got)
	}
}

func TestRequiredLabels_RegistryIsWellFormed(t *testing.T) {
	if len(RequiredLabels) == 0 {
		t.Fatal("RequiredLabels is empty — the preflight and the ensure verb both become no-ops")
	}
	seen := map[string]bool{}
	for _, l := range RequiredLabels {
		if l.Name == "" {
			t.Error("required label with an empty name")
		}
		if seen[l.Name] {
			t.Errorf("duplicate required label %q — Create is name-idempotent so the second is a silent no-op", l.Name)
		}
		seen[l.Name] = true
		if l.Description == "" {
			t.Errorf("required label %q has no description", l.Name)
		}
		if len(l.Color) != 6 {
			t.Errorf("required label %q colour %q is not a 6-digit hex without '#'", l.Name, l.Color)
		}
	}
	// RequiredLabelNames must agree with the registry, in order.
	names := RequiredLabelNames()
	if len(names) != len(RequiredLabels) {
		t.Fatalf("RequiredLabelNames returned %d names for %d labels", len(names), len(RequiredLabels))
	}
	for i, l := range RequiredLabels {
		if names[i] != l.Name {
			t.Errorf("RequiredLabelNames()[%d] = %q, want %q", i, names[i], l.Name)
		}
	}
}

func TestMissingRefinementBlockers_OnlyBlocksOnTheHardFailure(t *testing.T) {
	// Empty repo: everything is missing, but only the hard-failure label blocks.
	blockers := MissingRefinementBlockers(map[string]string{})
	if len(blockers) != 1 || blockers[0] != LabelRefined {
		t.Errorf("blockers on an empty repo = %v, want exactly [%q]", blockers, LabelRefined)
	}
	if all := MissingRequiredLabels(map[string]string{}); len(all) != len(RequiredLabels) {
		t.Errorf("MissingRequiredLabels on an empty repo = %d, want %d", len(all), len(RequiredLabels))
	}
}

func TestRequiredLabels_ExactlyOneBlocksRefinement(t *testing.T) {
	// The registry's own invariant. MarkRefined is the only call that hard-errors
	// on a missing label, so pipeline:refined is the only one that may gate the
	// refinement loop. Marking a second would disable refinement on repos that
	// have no need of that label at all.
	var blocking []string
	for _, l := range RequiredLabels {
		if l.BlocksRefinement {
			blocking = append(blocking, l.Name)
		}
	}
	if len(blocking) != 1 || blocking[0] != LabelRefined {
		t.Errorf("BlocksRefinement set = %v, want exactly [%q]", blocking, LabelRefined)
	}
}
