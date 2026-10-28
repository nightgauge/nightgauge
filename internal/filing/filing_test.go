package filing

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(dir), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveGate(t *testing.T) {
	cases := []struct {
		name       string
		body       *string
		wantMode   Mode
		wantCap    int
		wantReason string
	}{
		{"missing config fails closed", nil, ModeReport, DefaultCap, "fail closed: config missing"},
		{"unparseable fails closed", ptr("follow_up_filing: [unclosed"), ModeReport, DefaultCap, "fail closed: config unparseable"},
		{"absent key defaults to report", ptr("project:\n  number: 1\n"), ModeReport, DefaultCap, "defaults to report"},
		{"file mode", ptr("follow_up_filing:\n  mode: file\n  max_per_run: 3\n"), ModeFile, 3, ""},
		{"kill switch overrides file", ptr("follow_up_filing:\n  enabled: false\n  mode: file\n"), ModeReport, DefaultCap, "kill switch"},
		{"unknown mode fails closed", ptr("follow_up_filing:\n  mode: yolo\n"), ModeReport, DefaultCap, "fail closed: follow_up_filing.mode"},
		{"negative cap fails closed", ptr("follow_up_filing:\n  mode: file\n  max_per_run: -1\n"), ModeReport, DefaultCap, "fail closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.body != nil {
				dir = writeConfig(t, *tc.body)
			}
			g := ResolveGate(dir)
			if g.Mode != tc.wantMode || g.Cap != tc.wantCap {
				t.Fatalf("gate = %+v, want mode %s cap %d", g, tc.wantMode, tc.wantCap)
			}
			if tc.wantReason != "" && !strings.Contains(g.Reason, tc.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", g.Reason, tc.wantReason)
			}
			if g.Files() != (tc.wantMode == ModeFile) {
				t.Fatalf("Files() = %v", g.Files())
			}
		})
	}
}

func TestResolveGate_UnreadableFailsClosed(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should be: ReadFile fails with a non-ENOENT error.
	if err := os.MkdirAll(ConfigPath(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	g := ResolveGate(dir)
	if g.Files() || !strings.Contains(g.Reason, "config unreadable") {
		t.Fatalf("gate = %+v, want fail-closed unreadable", g)
	}
}

func TestApplyCap(t *testing.T) {
	cases := []struct{ n, limit, want, refused int }{
		{0, 0, 0, 0}, {2, 2, 2, 0}, {3, 2, 2, 1}, {5, 0, 0, 5},
	}
	for _, tc := range cases {
		got, err := ApplyCap(tc.n, tc.limit)
		if got != tc.want {
			t.Errorf("ApplyCap(%d,%d) = %d, want %d", tc.n, tc.limit, got, tc.want)
		}
		var ce *CapError
		if tc.refused == 0 {
			if err != nil {
				t.Errorf("ApplyCap(%d,%d) err = %v", tc.n, tc.limit, err)
			}
			continue
		}
		if !errors.As(err, &ce) || ce.Refused != tc.refused || ce.Total != tc.n {
			t.Errorf("ApplyCap(%d,%d) err = %v, want refused %d", tc.n, tc.limit, err, tc.refused)
		}
	}
	_, err := ApplyCap(3, 2)
	if !strings.Contains(err.Error(), "refused: 1 of 3") {
		t.Fatalf("message %q", err)
	}
}

func TestValidateFields(t *testing.T) {
	if err := ValidateFields("bug", "high", "S"); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][3]string{{"epic", "high", "S"}, {"bug", "urgent", "S"}, {"bug", "high", "XXL"}} {
		if err := ValidateFields(c[0], c[1], c[2]); err == nil {
			t.Errorf("ValidateFields(%v) accepted", c)
		}
	}
}

func TestPriorityToBoardOption(t *testing.T) {
	want := map[string]string{"critical": "P0", "high": "P1", "medium": "P2", "low": "P3", "": "P2"}
	for in, out := range want {
		if got := PriorityToBoardOption(in); got != out {
			t.Errorf("%q -> %q, want %q", in, got, out)
		}
	}
}

func TestRenderUntrusted_FencedBoundedAndEscaped(t *testing.T) {
	got := RenderUntrusted("x```\n@here <script>", 0)
	if strings.Count(got, "```") != 2 || !strings.HasPrefix(got, "```text\n") {
		t.Fatalf("fence not intact: %q", got)
	}
	long := RenderUntrusted(strings.Repeat("a", 50), 10)
	if !strings.Contains(long, strings.Repeat("a", 10)+"…") || strings.Contains(long, strings.Repeat("a", 11)) {
		t.Fatalf("not bounded: %q", long)
	}
}

func TestProvenanceLine(t *testing.T) {
	if got := ProvenanceLine(42, "r1", "feature-dev"); got != "Discovered in #42 (run r1, stage feature-dev)" {
		t.Fatal(got)
	}
	if got := ProvenanceLine(42, "", "pr-merge"); got != "Discovered in #42 (run unknown, stage pr-merge)" {
		t.Fatal(got)
	}
}

func ptr(s string) *string { return &s }
