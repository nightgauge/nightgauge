package attention

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The persisted card must satisfy the platform's card schema (#1405).
//
// These assert on the MARSHALED BYTES, not on the Go struct. The struct was
// always fine; what the platform rejected was the wire form Go's zero values
// produce. A test that inspected `req.Options` would have passed throughout the
// entire period 27 cards were being rejected.
//
// The two shapes, and why each is fatal on arrival:
//
//	options: z.array(OptionSchema).max(20)   -- required, not nullable, not
//	                                            optional. `null` fails. So does
//	                                            ABSENT, which is why `omitempty`
//	                                            would have moved the error
//	                                            rather than fixed it.
//	actor:   z.string().min(1)               -- an empty resolver is rejected.

// readPersisted returns the raw JSON the store wrote for id.
func readPersisted(t *testing.T, dir, id string) string {
	t.Helper()
	// New(root) nests its store at <root>/.nightgauge/attention.
	data, err := os.ReadFile(filepath.Join(dir, ".nightgauge", "attention", id+".json"))
	if err != nil {
		t.Fatalf("read persisted card: %v", err)
	}
	return string(data)
}

// TestPersistedCardNeverCarriesNullOptions is the defect verbatim.
//
// A producer that simply never sets Options — which is what
// cmd/nightgauge/post_merge_report.go did — persisted `"options":null`. The
// platform rejected every such card on arrival and retried forever, so 27 cards
// were invisible on every remote surface with nothing failing anywhere.
func TestPersistedCardNeverCarriesNullOptions(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	req := validRequest(mustID(t), "k:no-options")
	req.Options = nil              // the shape the bug produced
	req.DefaultAction = ExpireNoop // a request with no options can only expire

	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	raw := readPersisted(t, dir, req.ID)
	if strings.Contains(raw, `"options": null`) || strings.Contains(raw, `"options":null`) {
		t.Errorf("persisted card carries a null options array — the platform's schema is "+
			"`z.array(...)`, so this card is rejected on arrival and is invisible on every "+
			"remote surface:\n%s", raw)
	}

	// Present AND an array. Absent would fail the same schema, which is why
	// `omitempty` is not the fix.
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("persisted card is not JSON: %v", err)
	}
	opts, ok := decoded["options"]
	if !ok {
		t.Fatalf("persisted card has no `options` key at all — the schema requires one:\n%s", raw)
	}
	if string(opts) != "[]" {
		t.Errorf("options = %s, want []", opts)
	}
}

// TestPersistedCardKeepsRealOptions: normalization must not flatten a producer
// that DID supply options — otherwise the fix would silently disarm every card
// that has buttons.
func TestPersistedCardKeepsRealOptions(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	req := validRequest(mustID(t), "k:real-options")
	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	var decoded struct {
		Options []Option `json:"options"`
	}
	if err := json.Unmarshal([]byte(readPersisted(t, dir, req.ID)), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Options) != 2 {
		t.Errorf("persisted %d option(s), want the 2 the producer supplied", len(decoded.Options))
	}
}

// TestResolveRefusesAnEmptyActor — the second instance of the same class.
//
// The platform requires `actor: z.string().min(1)`, and the store wrote
// whatever the caller passed. Refused rather than defaulted: the store cannot
// know who the operator is, and inventing a name puts a false entry in an audit
// record. The callers that DO know supply it.
func TestResolveRefusesAnEmptyActor(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	req := validRequest(mustID(t), "k:empty-actor")
	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	for _, actor := range []string{"", "   "} {
		if _, err := s.Resolve(context.Background(), req.ID, "leave", actor, "", "", nil); err == nil {
			t.Errorf("Resolve accepted actor %q — the platform rejects an empty resolver", actor)
		}
	}

	// Refused BEFORE any mutation: the card must still be open, so a caller
	// that supplies an actor on retry gets a clean resolve.
	loaded, found, err := s.Get(req.ID)
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if loaded.Lifecycle.State.IsTerminal() {
		t.Errorf("a refused resolve left the card in %q — it must stay open", loaded.Lifecycle.State)
	}
}

// TestAcknowledgeRefusesAnEmptyActor: the ack record carries an actor too.
func TestAcknowledgeRefusesAnEmptyActor(t *testing.T) {
	s := New(t.TempDir())
	req := validRequest(mustID(t), "k:empty-ack-actor")
	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, err := s.Acknowledge(req.ID, ""); err == nil {
		t.Error("Acknowledge accepted an empty actor")
	}
}
