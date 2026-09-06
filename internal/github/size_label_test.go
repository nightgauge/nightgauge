package github

import (
	"context"
	"testing"
)

// The whole point of this write is that it costs ONE REST call: the caller is a
// per-run hook, so a version that resolved a node ID and a label ID first would
// bill three calls per planned run forever for a write that is usually a no-op.
// The recorded call log is the only thing that can prove it.
func TestApplySizeLabel_IsOneRESTCall(t *testing.T) {
	client, seen, cleanup := mockForgeServerRecording(t,
		map[string]string{"POST /repos/o/r/issues/42/labels": `[{"name":"size:M"}]`})
	defer cleanup()

	if err := NewIssueService(client).ApplySizeLabel(context.Background(), "o", "r", 42, "M"); err != nil {
		t.Fatalf("ApplySizeLabel: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("made %d calls (%v), want exactly 1 — labels are added BY NAME so no id lookups are needed",
			len(*seen), *seen)
	}
	if (*seen)[0] != "POST /repos/o/r/issues/42/labels" {
		t.Errorf("called %q, want the issue-labels REST endpoint", (*seen)[0])
	}
}

// A lower-case bucket from a plan must reach the forge as the label the repos
// actually define. `size:m` would be a second, parallel label the router does
// not read.
func TestApplySizeLabel_NormalizesCase(t *testing.T) {
	client, cleanup := mockForgeServer(t,
		map[string]string{"POST /repos/o/r/issues/7/labels": `[]`})
	defer cleanup()

	if err := NewIssueService(client).ApplySizeLabel(context.Background(), "o", "r", 7, " l "); err != nil {
		t.Fatalf("ApplySizeLabel: %v", err)
	}
}

// An unrecognized bucket must never reach the forge: creating `size:medium`
// alongside `size:M` splits the router's own input in two.
func TestApplySizeLabel_RejectsUnknownBucket(t *testing.T) {
	client, seen, cleanup := mockForgeServerRecording(t, map[string]string{})
	defer cleanup()

	if err := NewIssueService(client).ApplySizeLabel(context.Background(), "o", "r", 7, "medium"); err == nil {
		t.Fatal("ApplySizeLabel accepted \"medium\" — the label vocabulary is XS|S|M|L|XL")
	}
	if len(*seen) != 0 {
		t.Errorf("made %v — an invalid bucket must be rejected before any forge call", *seen)
	}
}

func TestSizeLabelName(t *testing.T) {
	for in, want := range map[string]string{"xs": "size:XS", "S": "size:S", " m ": "size:M", "L": "size:L", "xl": "size:XL"} {
		got, err := SizeLabelName(in)
		if err != nil || got != want {
			t.Errorf("SizeLabelName(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "XXL", "medium", "3"} {
		if got, err := SizeLabelName(bad); err == nil {
			t.Errorf("SizeLabelName(%q) = %q, nil; want an error", bad, got)
		}
	}
}

func TestHasSizeLabel(t *testing.T) {
	if !HasSizeLabel([]string{"type:bug", "Size: L"}) {
		t.Error("HasSizeLabel missed a differently-cased size label — a second one would then be added alongside it")
	}
	if HasSizeLabel([]string{"type:bug", "priority:p1"}) {
		t.Error("HasSizeLabel reported a size label where there is none")
	}
}
