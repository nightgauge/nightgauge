package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	cacheMiss = "Marketplace nightgauge-plugins failed to load: cache-miss"

	// Captured from Claude Code 2.1.316 after removing a marketplace's directory.
	claudeOutageList = `[{"id":"nightgauge@nightgauge-plugins","version":"1.25.0","scope":"user",
		"enabled":true,"installPath":"/c/plugins/cache/nightgauge-plugins/nightgauge/1.25.0",
		"errors":["Marketplace nightgauge-plugins failed to load: cache-miss"],
		"errorDetails":[{"type":"marketplace-load-failed","marketplace":"nightgauge-plugins"}],
		"projectEnabled":false}]`
	claudeHealthyList = `[{"id":"nightgauge@nightgauge-plugins","version":"1.25.0","scope":"user",
		"enabled":true,"readFromFolder":"/c/m/claude-plugins/nightgauge","projectEnabled":false}]`
)

func stubClaude(t *testing.T, cliErr error, out string, listErr error) {
	t.Helper()
	origLook, origList := claudeLookPath, claudePluginList
	claudeLookPath = func(string) (string, error) { return "/usr/bin/claude", cliErr }
	claudePluginList = func(context.Context, string) ([]byte, error) { return []byte(out), listErr }
	t.Cleanup(func() { claudeLookPath, claudePluginList = origLook, origList })
}

func TestClaudePluginFailedCarriesClaudesError(t *testing.T) {
	stubClaude(t, nil, claudeOutageList, nil)
	fs, detail := claudePluginFindings(context.Background(), "")
	if len(fs) != 1 {
		t.Fatalf("findings = %d (%s); want one", len(fs), findingsText(fs))
	}
	f := fs[0]
	if f.Code != "NGD051" || f.Severity != SeverityWarning {
		t.Errorf("code %s severity %s; want NGD051 warning", f.Code, f.Severity)
	}
	if !strings.Contains(f.Cause, cacheMiss) || f.Evidence["error"] != cacheMiss {
		t.Errorf("finding does not carry Claude's error: cause %q evidence %v", f.Cause, f.Evidence)
	}
	if f.Evidence["plugin"] != "nightgauge@nightgauge-plugins" {
		t.Errorf("evidence plugin = %q", f.Evidence["plugin"])
	}
	if len(f.Remedies) != 1 || f.Remedies[0].Kind != RemedyManual {
		t.Errorf("remedies = %+v; want one manual remedy", f.Remedies)
	}
	if !strings.Contains(detail, "1 failed") {
		t.Errorf("detail = %q", detail)
	}
}

func TestClaudePluginOneFindingPerScope(t *testing.T) {
	stubClaude(t, nil, `[{"id":"nightgauge@a","scope":"user","errors":["x"]},
		{"id":"nightgauge@a","scope":"project","errors":["x"]}]`, nil)
	fs, _ := claudePluginFindings(context.Background(), "")
	if len(fs) != 2 || fs[0].Fingerprint == fs[1].Fingerprint {
		t.Fatalf("want two findings with distinct fingerprints, got %s", findingsText(fs))
	}
}

func TestClaudePluginPasses(t *testing.T) {
	for name, tc := range map[string]struct {
		cliErr error
		out    string
		detail string
	}{
		"healthy":      {out: claudeHealthyList, detail: "0 failed"},
		"absent":       {out: `[]`, detail: "not installed"},
		"foreign fail": {out: `[{"id":"other@x","errors":["broken"]}]`, detail: "not installed"},
		"similar name": {out: `[{"id":"nightgauge-extras@x","errors":["broken"]}]`, detail: "not installed"},
		"no CLI":       {cliErr: errors.New("not found"), detail: "no claude CLI"},
	} {
		t.Run(name, func(t *testing.T) {
			stubClaude(t, tc.cliErr, tc.out, nil)
			fs, detail := claudePluginFindings(context.Background(), "")
			if len(fs) != 0 {
				t.Fatalf("findings = %s; want a pass", findingsText(fs))
			}
			if !strings.Contains(detail, tc.detail) {
				t.Errorf("detail = %q; want %q", detail, tc.detail)
			}
		})
	}
}

func TestClaudePluginBlindScanIsNotAPass(t *testing.T) {
	for name, tc := range map[string]struct {
		out     string
		listErr error
		want    string
	}{
		"malformed":        {out: "Error: not json", want: "expected JSON"},
		"wrong shape":      {out: `{"plugins":[]}`, want: "expected JSON"},
		"non-zero or hang": {listErr: errors.New("signal: killed"), want: "signal: killed"},
	} {
		t.Run(name, func(t *testing.T) {
			stubClaude(t, nil, tc.out, tc.listErr)
			fs, _ := claudePluginFindings(context.Background(), "")
			if len(fs) != 1 || fs[0].Evidence["state"] != "unverifiable" {
				t.Fatalf("findings = %s; want one unverifiable", findingsText(fs))
			}
			if fs[0].Severity != SeverityWarning || !strings.Contains(fs[0].Evidence["reason"], tc.want) {
				t.Errorf("severity %s reason %q; want warning with %q", fs[0].Severity, fs[0].Evidence["reason"], tc.want)
			}
		})
	}
}
