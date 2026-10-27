package preflight

import (
	"reflect"
	"testing"
)

func TestACFingerprint(t *testing.T) {
	base := "## AC\n- [ ] first criterion\n- [ ] second criterion\n"
	fp := ACFingerprint(base)
	if fp[:7] != "sha256:" {
		t.Fatalf("prefix: %s", fp)
	}
	if got := ACFingerprint("intro\n- [x] first   criterion\n  * [ ]  second criterion \n"); got != fp {
		t.Errorf("whitespace/checkbox state changed fingerprint")
	}
	if ACFingerprint("- [ ] second criterion\n- [ ] first criterion\n") == fp {
		t.Errorf("order must change fingerprint")
	}
	if ACFingerprint(base+"- [ ] third criterion\n") == fp {
		t.Errorf("added AC must change fingerprint")
	}
	if ACFingerprint("") != ACFingerprint("no criteria here") {
		t.Errorf("empty AC list must be stable")
	}
}

func TestReconcileACsRecordsFingerprint(t *testing.T) {
	body := "- [ ] something vague\n"
	if got := ReconcileACs(t.TempDir(), 1, body).ACFingerprint; got != ACFingerprint(body) {
		t.Errorf("fingerprint = %q", got)
	}
}

func TestUncoveredACs(t *testing.T) {
	rec := &ACReconcileResult{AcceptanceCriteria: []ReconciledCriterion{{Text: "a  one"}, {Text: "b two"}}}
	body := "- [ ] a one\n- [ ] b two\n- [ ] c three\n- [x] d four\n"
	if got := UncoveredACs(body, rec); !reflect.DeepEqual(got, []string{"c three"}) {
		t.Errorf("got %v", got)
	}
	if got := UncoveredACs(body, nil); !reflect.DeepEqual(got, []string{"a one", "b two", "c three"}) {
		t.Errorf("nil rec: got %v", got)
	}
	if got := UncoveredACs("- [ ] a one\n", rec); got != nil {
		t.Errorf("covered: got %v", got)
	}
}
