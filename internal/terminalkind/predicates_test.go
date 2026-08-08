package terminalkind

// The PREDICATE FIELD SURFACE — Go half of a two-language pin.
//
// TestPredicateProbes (table_test.go) asserts what the predicate ANSWERS for a
// handful of declared strings. This file asserts something the probes cannot:
// which registry FIELDS it is allowed to look at.
//
// The distinction is the whole point. Every other fence in this package bounds
// string LITERALS, and widening a predicate to read one more field introduces
// no literal, moves no derived answer and changes no generated artifact — the
// review that found this widened both languages symmetrically and every suite,
// every drift check and every golden stayed green while six clauses started
// firing for any text that merely names a vendor.
//
// The fixture is shared with
// packages/nightgauge-sdk/tests/analysis/health/terminalKind.predicateFields.test.ts,
// so the two hand-written predicate implementations are pinned to ONE field set
// rather than to two independently maintained ones.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
)

type poisonFixture struct {
	Comment   []string                 `json:"$comment"`
	Predicate string                   `json:"predicate"`
	Reads     []string                 `json:"reads"`
	Models    []map[string]any         `json:"models"`
	typed     []models.ModelDescriptor `json:"-"`
}

func loadPoisonFixture(t *testing.T) poisonFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(corpusDir, "predicate-registry-poison.json"))
	if err != nil {
		t.Fatalf("read predicate-registry-poison.json: %v", err)
	}
	var f poisonFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse predicate-registry-poison.json: %v", err)
	}
	if len(f.Models) == 0 {
		t.Fatal("the poison fixture declares no models — it would pin nothing")
	}
	if len(f.Reads) == 0 {
		t.Fatal("the poison fixture declares no `reads` — every field would count as poison")
	}

	// Decoding the SAME entries as ModelDescriptor with unknown fields REJECTED
	// is the other direction of the schema check: a key that the Go struct does
	// not have cannot sit in this fixture pretending to be pinned.
	var wrapper struct {
		Models []models.ModelDescriptor `json:"models"`
	}
	// The fixture carries its own metadata keys, so decode only `models` by
	// re-marshalling that slice on its own.
	inner, err := json.Marshal(map[string]any{"models": f.Models})
	if err != nil {
		t.Fatalf("re-marshal poison models: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(inner))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wrapper); err != nil {
		t.Fatalf("the poison entry is not a valid models.ModelDescriptor: %v\n"+
			"Every key here must exist on the Go struct, or the fixture would declare a field "+
			"the predicate could never have read anyway.", err)
	}
	f.typed = wrapper.Models
	return f
}

// jsonKeyPaths returns every key path in a decoded JSON value. Array elements
// contribute their parent's path with `[]` appended, so `tiers` and `tiers[]`
// are distinct and a field that only ever appears inside an array is still seen.
func jsonKeyPaths(v any, prefix string, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, sub := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			jsonKeyPaths(sub, p, out)
		}
	case []any:
		for _, sub := range x {
			jsonKeyPaths(sub, prefix+"[]", out)
		}
	}
}

// stringLeaves returns every string value in a decoded JSON value, keyed by the
// path it was found at.
func stringLeaves(v any, prefix string, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, sub := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			stringLeaves(sub, p, out)
		}
	case []any:
		for i, sub := range x {
			stringLeaves(sub, fmt.Sprintf("%s[%d]", prefix, i), out)
		}
	case string:
		out[prefix] = x
	}
}

// topLevelOf returns the first segment of a key path (`behavior.propensity.x` →
// `behavior`), which is the granularity `reads` is declared at.
func topLevelOf(path string) string {
	if i := strings.IndexAny(path, ".["); i >= 0 {
		return path[:i]
	}
	return path
}

// TestPredicateFieldSurfaceIsPinnedByThePoisonFixture is the exact-set fence on
// the predicate's registry reads: no non-declared field may influence it, and
// every declared field must.
func TestPredicateFieldSurfaceIsPinnedByThePoisonFixture(t *testing.T) {
	f := loadPoisonFixture(t)
	if _, ok := predicates[f.Predicate]; !ok {
		t.Fatalf("the poison fixture pins predicate %q, which this package does not implement",
			f.Predicate)
	}
	reads := map[string]bool{}
	for _, r := range f.Reads {
		reads[r] = true
	}

	// SCHEMA. Every key path the real registry uses must appear in the fixture,
	// or a field added to the registry would be unpinned and silent.
	realPaths := map[string]bool{}
	var realFile struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(models.RawJSON(), &realFile); err != nil {
		t.Fatalf("parse the embedded model registry: %v", err)
	}
	for _, m := range realFile.Models {
		jsonKeyPaths(m, "", realPaths)
	}
	poisonPaths := map[string]bool{}
	for _, m := range f.Models {
		jsonKeyPaths(m, "", poisonPaths)
	}
	var undeclared []string
	for p := range realPaths {
		if !poisonPaths[p] {
			undeclared = append(undeclared, p)
		}
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("the model registry has field(s) the poison fixture does not declare: %v\n"+
			"Add them to testdata/predicate-registry-poison.json with a unique sentinel value "+
			"(or to `reads` if the predicate is meant to read them). Until then the predicate "+
			"could start reading one of them with no test able to notice.", undeclared)
	}

	// NEGATIVE. Every string that is not under a declared read field is a
	// sentinel, and no sentinel may make the predicate fire.
	sentinels := map[string]string{}
	for _, m := range f.Models {
		leaves := map[string]string{}
		stringLeaves(m, "", leaves)
		for path, val := range leaves {
			if reads[topLevelOf(path)] {
				continue
			}
			if val == "" {
				t.Errorf("field %q carries an empty value; an empty sentinel pins nothing", path)
				continue
			}
			if prev, dup := sentinels[val]; dup {
				t.Errorf("fields %q and %q share the sentinel %q — a hit could not be attributed "+
					"to one field", prev, path, val)
			}
			sentinels[val] = path
		}
	}
	if len(sentinels) < 8 {
		t.Fatalf("only %d sentinels derived from the fixture — the walk is broken and this guard "+
			"now checks almost nothing", len(sentinels))
	}
	for val, path := range sentinels {
		if mentionsAnyModel(f.typed, strings.ToLower(val)) {
			t.Errorf("the predicate fires on %q, which is the sentinel for the registry field %q.\n"+
				"That field is not in the fixture's `reads` set, so the predicate must not read "+
				"it. Every clause gated on @%s now claims any text containing that field's real "+
				"value — for `provider` that is every message naming a vendor — with no string "+
				"literal, no golden movement and no corpus row able to see it.",
				val, path, f.Predicate)
		}
	}

	// POSITIVE. Every declared read must actually be read, so the set is exact
	// rather than an upper bound.
	for _, m := range f.Models {
		leaves := map[string]string{}
		stringLeaves(m, "", leaves)
		fired := map[string]bool{}
		for path, val := range leaves {
			top := topLevelOf(path)
			if !reads[top] || val == "" {
				continue
			}
			if mentionsAnyModel(f.typed, strings.ToLower(val)) {
				fired[top] = true
			}
		}
		for _, r := range f.Reads {
			if !fired[r] {
				t.Errorf("the predicate does NOT fire on the value of the declared read field %q. "+
					"Either the read was dropped — a live narrowing of every clause gated on "+
					"@%s — or the fixture no longer carries a value for it.", r, f.Predicate)
			}
		}
	}
}

// TestPredicateWrapperIsAPureDelegation keeps the fence meaningful. The poison
// fixture can only see the function it is handed a registry through, so the
// wrapper that supplies the REAL registry must contain nothing else — a
// condition there would read fields the fixture never sees.
func TestPredicateWrapperIsAPureDelegation(t *testing.T) {
	src, err := os.ReadFile("predicates.go")
	if err != nil {
		t.Fatalf("read predicates.go: %v", err)
	}
	body := goFuncBody(t, string(src), "func mentionsRegistryModel(t string) bool {")
	got := strings.Join(strings.Fields(body), " ")
	const want = "{ return mentionsAnyModel(models.All(), t) }"
	if got != want {
		t.Errorf("mentionsRegistryModel must be exactly `%s`.\n  got: %s\n"+
			"Everything that inspects a registry entry belongs in mentionsAnyModel, which the "+
			"shared poison fixture drives; a read added here is invisible to it.", want, got)
	}
}
