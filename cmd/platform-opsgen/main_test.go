package main

import (
	"strings"
	"testing"
)

func validOp() manifestOperation {
	return manifestOperation{
		ID:       "analytics.getHealthScore",
		GoName:   "OpAnalyticsHealth",
		Method:   "GET",
		Path:     "/v1/analytics/health",
		Security: "user_jwt",
		Upstream: "declared",
	}
}

func TestValidateAcceptsAWellFormedManifest(t *testing.T) {
	op := validOp()
	withParams := validOp()
	withParams.ID = "agents.heartbeat"
	withParams.GoName = "OpAgentsHeartbeat"
	withParams.Method = "PUT"
	withParams.Path = "/v1/agents/{agentId}/heartbeat"
	withParams.Security = "pipeline"

	if err := validate([]manifestOperation{op, withParams}); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidateRejectsMalformedEntries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*manifestOperation)
		want   string
	}{
		{"empty id", func(o *manifestOperation) { o.ID = "" }, "id is required"},
		{"bad go_name", func(o *manifestOperation) { o.GoName = "analyticsHealth" }, "go_name"},
		{"unknown method", func(o *manifestOperation) { o.Method = "FETCH" }, "not a supported HTTP method"},
		{"relative path", func(o *manifestOperation) { o.Path = "v1/analytics/health" }, "must be absolute"},
		{"query in path", func(o *manifestOperation) { o.Path = "/v1/analytics/trends?period=7d" }, "must not carry a query string"},
		{"unbalanced braces", func(o *manifestOperation) { o.Path = "/v1/agents/{agentId/heartbeat" }, "malformed placeholder"},
		// A credential requirement that is neither of the two the platform's
		// middleware distinguishes would silently satisfy every call site.
		{"unknown security", func(o *manifestOperation) { o.Security = "bearerAuth" }, "must be one of user_jwt, pipeline"},
		{"unknown upstream", func(o *manifestOperation) { o.Upstream = "maybe" }, "must be one of declared, undeclared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := validOp()
			tc.mutate(&op)
			err := validate([]manifestOperation{op})
			if err == nil {
				t.Fatalf("validate accepted %+v", op)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestValidateRejectsDuplicates: two entries claiming the same id, Go name or
// route means one of them is unreachable and the contract silently disagrees
// with the code.
func TestValidateRejectsDuplicates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*manifestOperation)
		want   string
	}{
		{"duplicate id", func(o *manifestOperation) { o.GoName = "OpOther"; o.Path = "/v1/other" }, "duplicate id"},
		{"duplicate go_name", func(o *manifestOperation) { o.ID = "other.op"; o.Path = "/v1/other" }, "duplicate go_name"},
		{"duplicate route", func(o *manifestOperation) { o.ID = "other.op"; o.GoName = "OpOther" }, "already declared by"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := validOp()
			second := validOp()
			tc.mutate(&second)
			err := validate([]manifestOperation{first, second})
			if err == nil {
				t.Fatalf("validate accepted a duplicate: %+v", second)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestRenderIsDeterministicAndOrderIndependent: the drift check in CI compares
// bytes, so the output must be a function of the manifest's content and not of
// the order its entries happen to be written in.
func TestRenderIsDeterministicAndOrderIndependent(t *testing.T) {
	a := validOp()
	b := validOp()
	b.ID = "queue.sync"
	b.GoName = "OpQueueSync"
	b.Method = "PUT"
	b.Path = "/v1/queue/sync"
	b.Security = "pipeline"

	forward, err := render("api/platform-operations.yaml", sortForRender([]manifestOperation{a, b}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	reverse, err := render("api/platform-operations.yaml", sortForRender([]manifestOperation{b, a}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if string(forward) != string(reverse) {
		t.Fatal("render output depends on manifest entry order; the CI drift check would be a coin flip")
	}
	if !strings.Contains(string(forward), "Security: SecurityUserJWT") {
		t.Error("rendered output does not carry the security requirement")
	}
	if !strings.Contains(string(forward), "Security: SecurityPipeline") {
		t.Error("rendered output does not carry the pipeline security requirement")
	}
}
