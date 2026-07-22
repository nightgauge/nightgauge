package inbound

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantTyp PipelineCommandType
		wantNum int
		wantRpo string
	}{
		{"empty -> help", "", CmdHelp, 0, ""},
		{"whitespace -> help", "   ", CmdHelp, 0, ""},
		{"trigger-only -> help", "/nightgauge", CmdHelp, 0, ""},
		{"trigger + whitespace -> help", "/nightgauge   ", CmdHelp, 0, ""},
		{"status bare", "status", CmdStatus, 0, ""},
		{"status with trigger", "/nightgauge status", CmdStatus, 0, ""},
		{"status case-insensitive", "STATUS", CmdStatus, 0, ""},

		{"run with issue", "run 1234", CmdRun, 1234, ""},
		{"run with #-prefix", "run #1234", CmdRun, 1234, ""},
		{"run with --repo flag", "run 1234 --repo owner/slug", CmdRun, 1234, "owner/slug"},
		{"run with --repo= form", "run 1234 --repo=owner/slug", CmdRun, 1234, "owner/slug"},
		{"run flag before issue", "run --repo owner/slug 1234", CmdRun, 1234, "owner/slug"},
		{"run without issue", "run", CmdRun, 0, ""},

		{"pause", "pause", CmdPause, 0, ""},
		{"resume", "resume", CmdResume, 0, ""},

		{"stop bare", "stop", CmdStop, 0, ""},
		{"stop with issue", "stop 42", CmdStop, 42, ""},

		{"queue add", "queue add 99", CmdQueueAdd, 99, ""},
		{"queue add #-prefix", "queue add #99", CmdQueueAdd, 99, ""},
		{"queue remove", "queue remove 99", CmdQueueRemove, 99, ""},
		{"queue rm alias", "queue rm 99", CmdQueueRemove, 99, ""},
		{"queue list", "queue list", CmdQueueList, 0, ""},
		{"queue ls alias", "queue ls", CmdQueueList, 0, ""},
		{"queue without verb", "queue", CmdUnknown, 0, ""},
		{"queue unknown verb", "queue zap", CmdUnknown, 0, ""},

		{"health", "health", CmdHealth, 0, ""},
		{"help", "help", CmdHelp, 0, ""},
		{"unknown subcommand", "frobnicate", CmdUnknown, 0, ""},

		{"extra whitespace", "  run    1234    ", CmdRun, 1234, ""},
		{"trigger + extra whitespace", "/nightgauge   run 1234", CmdRun, 1234, ""},
		{"trigger case-insensitive", "/NIGHTGAUGE status", CmdStatus, 0, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.text)
			if got.Type != tc.wantTyp {
				t.Errorf("Parse(%q).Type = %q, want %q", tc.text, got.Type, tc.wantTyp)
			}
			if got.IssueNumber != tc.wantNum {
				t.Errorf("Parse(%q).IssueNumber = %d, want %d", tc.text, got.IssueNumber, tc.wantNum)
			}
			if got.Repo != tc.wantRpo {
				t.Errorf("Parse(%q).Repo = %q, want %q", tc.text, got.Repo, tc.wantRpo)
			}
			if got.RawText != tc.text {
				t.Errorf("Parse(%q).RawText = %q, want %q", tc.text, got.RawText, tc.text)
			}
		})
	}
}

func TestParse_NonNumericIssueIgnored(t *testing.T) {
	got := Parse("run notanumber")
	if got.Type != CmdRun {
		t.Fatalf("Type = %q, want %q", got.Type, CmdRun)
	}
	if got.IssueNumber != 0 {
		t.Fatalf("IssueNumber = %d, want 0 (non-numeric token must be dropped)", got.IssueNumber)
	}
}

func TestParse_RawTextPreserved(t *testing.T) {
	input := "  run #1234 --repo owner/slug  "
	got := Parse(input)
	if got.RawText != input {
		t.Errorf("RawText = %q, want %q", got.RawText, input)
	}
}
