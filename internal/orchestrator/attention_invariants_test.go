package orchestrator

// Two invariants this change DECLARED in prose and then broke, now enforced
// mechanically (#305 round-4 review).
//
// Both defects had the same shape: a document asserted a property, no test read
// the document, and the property was already false in the same commit that
// asserted it. `grep -n accounting internal/orchestrator/terminal_parity_test.go`
// returned nothing — the parity suite checks anchors and fence hashes, which is
// why it stayed green while the manifest's self-declared "exhaustive by
// construction" list was missing a row and its section counts summed to 19.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// accountingManifest re-declares only the fields this suite reads. Separate
// from parityManifest because `accounting` is a []string block that the parity
// suite deliberately does not model.
type accountingManifest struct {
	Behaviors []struct {
		Name       string   `json:"name"`
		Accounting []string `json:"accounting"`
	} `json:"behaviors"`
}

// TestForceClearAccountingIsExhaustive enforces the claim the
// force-clear-terminal-bookkeeping row makes about itself: "EVERY behavior in
// this manifest, accounted for. This list is exhaustive by construction."
//
// It was not. #305 DELETED `run-scoped-attention` from the NOT PERFORMED
// section and added `abandoned-dispatch-attention` under a new heading — two
// different behavior rows — so one name had no disposition anywhere while the
// text still said the list was complete. Nothing failed, because prose no test
// reads is prose that drifts.
//
// The row that OWNS the accounting is exempt from its own list: it is the
// funnel being accounted for, not a behavior the funnel performs, which is what
// "(20 behaviors + this row)" means.
func TestForceClearAccountingIsExhaustive(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "terminal_behaviors.json"))
	if err != nil {
		t.Fatalf("read terminal_behaviors.json: %v", err)
	}
	var m accountingManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse terminal_behaviors.json: %v", err)
	}

	var owner string
	var accounting string
	for _, b := range m.Behaviors {
		if len(b.Accounting) > 0 {
			if owner != "" {
				t.Fatalf("two behaviors carry an `accounting` block (%s and %s) — there is one funnel ledger",
					owner, b.Name)
			}
			owner = b.Name
			accounting = strings.Join(b.Accounting, "\n")
		}
	}
	if owner == "" {
		t.Fatal("no behavior carries an `accounting` block — the force-clear ledger is gone")
	}

	var missing []string
	accounted := 0
	for _, b := range m.Behaviors {
		if b.Name == owner {
			continue
		}
		if !strings.Contains(accounting, b.Name) {
			missing = append(missing, b.Name)
			continue
		}
		accounted++
	}
	if len(missing) > 0 {
		t.Errorf("behaviors with NO disposition in %s's accounting: %v\n"+
			"That field says it is exhaustive by construction. Add each name under a labelled "+
			"disposition (performed / omitted / not-performed / delegated / not-applicable) and "+
			"update the section count in its heading.", owner, missing)
	}

	// The section headings carry counts ("PERFORMED (3):", ...). They are the
	// only place a reader can check the list is whole without re-deriving it,
	// so they must sum to the number of accounted behaviors.
	sum := 0
	for _, m := range regexp.MustCompile(`\((\d+)\):`).FindAllStringSubmatch(accounting, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unparseable section count %q", m[0])
		}
		sum += n
	}
	if sum != accounted {
		t.Errorf("accounting section counts sum to %d, but %d behaviors are accounted for.\n"+
			"A count that does not add up is how a deleted row hides.", sum, accounted)
	}
	if want := fmt.Sprintf("(%d behaviors + this row.)", accounted); !strings.Contains(accounting, want) {
		t.Errorf("accounting does not state its own total as %q", want)
	}
}

// TestProducerLabelsMatchTheADRNumbering enforces the invariant ADR-015 §F
// declares: producer numbers are GLOBAL across §F and its repo-scoped
// companion, and reusing one splits a single identity across two producers
// depending on which file a reader opens.
//
// Round 3 wrote that sentence and left four headers violating it in the same
// file: 8 named the watchdog, the unexercised deliverable AND the branch fork;
// 9 named both `default-branch-health` and the terminal-failure halt. The rule
// enforced here is the whole rule — a number must be unique AND must exist in
// the ADR, and a producer the ADR does not enumerate carries `(unnumbered)`
// rather than a plausible-looking number of its own.
func TestProducerLabelsMatchTheADRNumbering(t *testing.T) {
	adr, err := os.ReadFile(filepath.Join("..", "..", "docs", "decisions", "015-decision-requests.md"))
	if err != nil {
		t.Fatalf("read ADR-015: %v", err)
	}
	// Table rows are `| 12  | Abandoned dispatch (Stop) | ...`.
	declared := map[int]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\|\s*(\d+)\s*\|`).FindAllStringSubmatch(string(adr), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		declared[n] = true
	}
	if len(declared) < 10 {
		t.Fatalf("only %d numbered producer rows found in ADR-015 — the table shape changed and this "+
			"test is no longer reading it", len(declared))
	}

	// Scan every Go file in the package — round-4 review found stale numbered
	// labels hiding in attention_wiring_test.go while the guard read only the
	// wiring file.
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob package files: %v (%d files)", err, len(files))
	}
	var srcBuilder strings.Builder
	for _, f := range files {
		// This file mentions the header prefix in its own regex literal and
		// error strings — scanning it would count those as unparseable headers.
		if f == "attention_invariants_test.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		srcBuilder.Write(b)
		srcBuilder.WriteString("\n")
	}
	src := []byte(srcBuilder.String())
	header := regexp.MustCompile(`(?m)^// --- Producer ([^:]+): (.+?)\s*-{3,}$`)
	matches := header.FindAllStringSubmatch(string(src), -1)
	// A header the regex cannot parse is a header this test silently skips,
	// which is the same failure mode as having no test at all.
	if got, want := len(matches), strings.Count(string(src), "// --- Producer "); got != want {
		t.Fatalf("parsed %d producer headers but the file has %d — one does not match the "+
			"`// --- Producer <label>: <name> ---…` shape and would be checked by nothing", got, want)
	}
	seen := map[int]string{}
	for _, m := range matches {
		label, name := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		if label == "(unnumbered)" {
			continue
		}
		// Sub-letters ("7b") are variants of their parent's row.
		digits := regexp.MustCompile(`^\d+`).FindString(label)
		if digits == "" {
			t.Errorf("producer header %q has no number and is not marked (unnumbered)", label)
			continue
		}
		n, _ := strconv.Atoi(digits)
		if !declared[n] {
			t.Errorf("producer header %q (%s) uses number %d, which ADR-015 does not enumerate. "+
				"Either add the row to the ADR or label the producer `(unnumbered)` — a number the "+
				"registry does not know is a fabricated identity.", label, name, n)
		}
		if prior, dup := seen[n]; dup {
			t.Errorf("producer number %d names TWO producers: %q and %q. ADR-015 §F: reusing a number "+
				"splits one identity across two producers depending on which file a reader opens.",
				n, prior, name)
		}
		seen[n] = name
	}
	if len(seen) == 0 {
		t.Fatal("no numbered producer headers found in attention_wiring.go — the header format changed")
	}
}
