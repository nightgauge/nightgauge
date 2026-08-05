package reclaim

import (
	"reflect"
	"testing"
)

func TestClassifyStatus(t *testing.T) {
	tests := []struct {
		name         string
		porcelain    string
		wantExhaust  []string
		wantBlocking []string
	}{
		{
			name: "untracked bookkeeping is exhaust",
			// The literal #332 case: the knowledge scaffold the pipeline
			// writes at issue pickup, in a repo whose .nightgauge/.gitignore
			// predates the /knowledge/ rule.
			porcelain:   "?? .nightgauge/knowledge/README.md\n",
			wantExhaust: []string{".nightgauge/knowledge/README.md"},
		},
		{
			name:        "untracked containment and adapter scribbles are exhaust",
			porcelain:   "?? .nightgauge/containment/run-7.json\n?? .claude/settings.local.json\n",
			wantExhaust: []string{".nightgauge/containment/run-7.json", ".claude/settings.local.json"},
		},
		{
			name: "a staged deletion of a TRACKED bookkeeping file blocks",
			// #701: 209 staged deletions under .nightgauge/pipeline/assessments
			// were the deliverable. A sweep that excluded .nightgauge wholesale
			// would have destroyed them and reported success.
			porcelain:    "D  .nightgauge/pipeline/assessments/issue-42.json\n",
			wantBlocking: []string{".nightgauge/pipeline/assessments/issue-42.json"},
		},
		{
			name:         "a modified tracked bookkeeping file blocks",
			porcelain:    " M .nightgauge/config.yaml\n",
			wantBlocking: []string{".nightgauge/config.yaml"},
		},
		{
			name:         "untracked deliverable content blocks",
			porcelain:    "?? scratch-notes.md\n",
			wantBlocking: []string{"scratch-notes.md"},
		},
		{
			name:         "exhaust never masks real work in the same tree",
			porcelain:    "?? .nightgauge/knowledge/README.md\n M src/app.ts\n",
			wantExhaust:  []string{".nightgauge/knowledge/README.md"},
			wantBlocking: []string{"src/app.ts"},
		},
		{
			name: "a rename reports its destination",
			// git renders a rename as `XY old -> new`; the destination is the
			// path that exists on disk.
			porcelain:    "R  old/name.ts -> new/name.ts\n",
			wantBlocking: []string{"new/name.ts"},
		},
		{
			name:        "a quoted path is unquoted before classification",
			porcelain:   "?? \".nightgauge/knowledge/some file.md\"\n",
			wantExhaust: []string{".nightgauge/knowledge/some file.md"},
		},
		{
			name: "a path merely containing .nightgauge is not bookkeeping",
			// IsBookkeepingPath anchors at a path boundary. A source file named
			// after the directory must never be mistaken for exhaust.
			porcelain:    "?? docs/.nightgauge-layout.md\n?? src/dot.nightgauge/x.ts\n",
			wantBlocking: []string{"docs/.nightgauge-layout.md", "src/dot.nightgauge/x.ts"},
		},
		{
			name:      "empty status classifies as nothing at all",
			porcelain: "\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyStatus(tc.porcelain)
			if !reflect.DeepEqual(got.Exhaust, tc.wantExhaust) {
				t.Errorf("Exhaust = %#v, want %#v", got.Exhaust, tc.wantExhaust)
			}
			if !reflect.DeepEqual(got.Blocking, tc.wantBlocking) {
				t.Errorf("Blocking = %#v, want %#v", got.Blocking, tc.wantBlocking)
			}
			if got.Blocked() != (len(tc.wantBlocking) > 0) {
				t.Errorf("Blocked() = %v, want %v", got.Blocked(), len(tc.wantBlocking) > 0)
			}
		})
	}
}

func TestTrackedBookkeeping(t *testing.T) {
	// The #237 shape: a stage whose entire deliverable is under .nightgauge/.
	// `worktree recover` must commit these, not strip them.
	porcelain := "D  .nightgauge/pipeline/assessments/a.json\n" +
		"?? .nightgauge/knowledge/README.md\n" +
		" M src/app.ts\n"
	got := TrackedBookkeeping(porcelain)
	want := []string{".nightgauge/pipeline/assessments/a.json"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TrackedBookkeeping = %#v, want %#v", got, want)
	}
}

func TestParseStatus_SkipsMalformedLines(t *testing.T) {
	// Short lines carry no path. Returning a phantom empty path from one would
	// classify as deliverable and block every reclamation in the repo.
	entries := ParseStatus("??\n M\n\n?? real.txt\n")
	if len(entries) != 1 || entries[0].Path != "real.txt" {
		t.Fatalf("ParseStatus = %#v, want exactly the one real entry", entries)
	}
}

func TestStatusPaths(t *testing.T) {
	got := StatusPaths("?? a.txt\n M b.txt\n")
	want := []string{"a.txt", "b.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("StatusPaths = %#v, want %#v", got, want)
	}
}
