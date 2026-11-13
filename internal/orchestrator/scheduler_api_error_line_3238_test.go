package orchestrator

import (
	"strings"
	"testing"
)

// The two lines the CLI printed when the #3231 session lost the network (#3238).
const (
	apiErrENOTFOUND    = "API Error: Can't reach the API server — check your internet or DNS (ENOTFOUND)"
	apiErrECONNREFUSED = "API Error: Connection refused — a firewall or proxy may be blocking it (ECONNREFUSED)"
)

const (
	transcriptAssistant = `{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}`
	retryNull           = `{"type":"system","subtype":"api_retry","attempt":1,"error_status":null,"error":"unknown"}`
	retry529            = `{"type":"system","subtype":"api_retry","attempt":1,"error_status":529,"error":"overloaded"}`
)

func TestCliFailureText_APIErrorLine_3238(t *testing.T) {
	noise := "hook: post-tool ok\n[skillRunner] self-check passed\nwarning: deprecated flag\n"
	cases := []struct {
		name, stdout, stderr, want string
	}{
		{"enotfound stderr", "", apiErrENOTFOUND, "api_connection_lost"},
		{"econnrefused stderr", "", apiErrECONNREFUSED, "api_connection_lost"},
		{"enotfound stderr trailing newline", "", apiErrENOTFOUND + "\n", "api_connection_lost"},
		{"enotfound stderr then noise", "", apiErrENOTFOUND + "\n" + noise, "api_connection_lost"},
		{"econnrefused stderr then noise", "", apiErrECONNREFUSED + "\n" + noise, "api_connection_lost"},
		{"enotfound stdout beside transcript", transcriptAssistant + "\n" + apiErrENOTFOUND + "\n", "", "api_connection_lost"},
		{"econnrefused stdout beside transcript", transcriptAssistant + "\n" + apiErrECONNREFUSED + "\n" + transcriptAssistant, "", "api_connection_lost"},
		{"econnrefused stdout with stderr noise", transcriptAssistant + "\n" + apiErrECONNREFUSED, noise, "api_connection_lost"},
		{"api_retry null exhaustion", transcriptAssistant + "\n" + strings.Repeat(retryNull+"\n", 10), "", "api_connection_lost"},
		{"api_retry null exhaustion, overload on stderr wins", transcriptAssistant + "\n" + retryNull + "\n", "API Error: Overloaded", "api_overloaded"},
		{"api_retry with http status is not transport loss", transcriptAssistant + "\n" + retry529 + "\n", "", "subagent_crash"},
		{"transcript without api_retry stays unclassified", transcriptAssistant + "\n", "", "subagent_crash"},
		{"api_retry before a later assistant turn is not exhaustion", retryNull + "\n" + transcriptAssistant + "\n", "", "subagent_crash"},
		{"API Error inside transcript JSON is not read", `{"type":"assistant","text":"API Error: x (ENOTFOUND)"}`, "", "subagent_crash"},
		{"ECONNREFUSED without API Error is not excused", "", "integration test failed: ECONNREFUSED 127.0.0.1:5432", "subagent_crash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, _ := cliFailureText(tc.stdout, tc.stderr)
			if got := ClassifyTerminalKind("exit 1: " + reason); got != tc.want {
				t.Errorf("kind = %q, want %q (reason %q)", got, tc.want, reason)
			}
		})
	}
}
