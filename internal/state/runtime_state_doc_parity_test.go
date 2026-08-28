package state

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// schemaDoc is the document that describes the on-disk pipeline files.
const schemaDoc = "../../docs/PIPELINE_STATE_SCHEMA.md"

// TestRuntimeStateFieldsAreDocumented is the guard #1012 needed and did not
// have.
//
// `RuntimeState` accreted 56 serialised fields with its contract living
// entirely in Go doc-comments, several of which were themselves stale. Nothing
// mechanical compared the struct's json tags to any document, so the doc could
// never go red for omitting `phaseHistory`, `stageErrors`, or the terminal-latch
// fields — and for years it did omit all of them.
//
// This is the same cross-file idiom history_schema_parity_test.go already uses:
// read the other artifact at test time and diff, rather than trusting two
// hand-maintained lists to stay equal.
func TestRuntimeStateFieldsAreDocumented(t *testing.T) {
	raw, err := os.ReadFile(schemaDoc)
	if err != nil {
		t.Fatalf("read %s: %v", schemaDoc, err)
	}
	doc := string(raw)

	section := runtimeSnapshotSection(t, doc)

	var missing []string
	rt := reflect.TypeOf(RuntimeState{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		if !strings.Contains(section, "`"+name+"`") {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		t.Errorf("RuntimeState serialises these fields and the runtime-snapshot section of\n"+
			"%s does not mention them: %v\n\n"+
			"A field that ships on disk and on the IPC wire with no written contract is how\n"+
			"this document came to describe none of them (#1012).",
			schemaDoc, missing)
	}
}

// TestPhaseRecordStatusesAreDocumented pins the value set, which is the part
// that was actively WRONG rather than merely absent: the Go comment advertised
// "skipped" while no writer produced one, and "failed"/"abandoned" did not
// exist at all until #1026 and #1009.
func TestPhaseRecordStatusesAreDocumented(t *testing.T) {
	raw, err := os.ReadFile(schemaDoc)
	if err != nil {
		t.Fatalf("read %s: %v", schemaDoc, err)
	}
	// Scope to the phaseHistory sub-section. Searching the whole runtime section
	// would match the RuntimeState fields `abandoned`/`paused`, which share names
	// with phase statuses — the assertion would then pass for the wrong reason,
	// which the mutation run caught.
	section := phaseRecordSection(t, string(raw))

	// Every status the production writers can produce, and the writer of each.
	// Adding a sixth without documenting it fails here.
	for _, status := range []string{"running", "complete", "skipped", "failed", "abandoned"} {
		if !strings.Contains(section, "`"+status+"`") {
			t.Errorf("PhaseRecord.Status can be %q and the phaseHistory table does not list it", status)
		}
	}

	// The doc must not claim a status no writer produces — the exact rot that
	// made the Go comment misleading for the life of the field.
	if strings.Contains(section, "`pending`") {
		t.Error("the doc lists `pending`, which no Go writer produces")
	}
}

var runtimeSectionRE = regexp.MustCompile(`(?s)### Runtime snapshot.*?\n(## |### (?:Workflow|Recovery))`)

func runtimeSnapshotSection(t *testing.T, doc string) string {
	t.Helper()
	m := runtimeSectionRE.FindString(doc)
	if m == "" {
		// Fall back to "everything after the heading" rather than passing
		// vacuously — a heading that moved must fail loudly, not silently
		// match nothing.
		i := strings.Index(doc, "### Runtime snapshot")
		if i < 0 {
			t.Fatalf("%s has no '### Runtime snapshot' section — the parity guard cannot run", schemaDoc)
		}
		return doc[i:]
	}
	return m
}

// phaseRecordSection narrows to the `#### phaseHistory[]` sub-section, so a
// status assertion cannot be satisfied by a same-named RuntimeState field.
func phaseRecordSection(t *testing.T, doc string) string {
	t.Helper()
	i := strings.Index(doc, "#### `phaseHistory[]`")
	if i < 0 {
		t.Fatalf("%s has no phaseHistory sub-section — the status guard cannot run", schemaDoc)
	}
	rest := doc[i:]
	if j := strings.Index(rest, "\n## "); j > 0 {
		return rest[:j]
	}
	return rest
}
