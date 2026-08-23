package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestRetentionClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	return c
}

func TestAuditRetentionService_GetRetentionConfig(t *testing.T) {
	t.Run("returns retention config", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/audit/retention" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(RetentionConfig{RetentionDays: 730, UpdatedAt: "2026-01-01T00:00:00Z"})
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		result, err := svc.GetRetentionConfig(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RetentionDays != 730 {
			t.Errorf("expected 730, got %d", result.RetentionDays)
		}
	})

	t.Run("returns error when platform offline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()
		c, err := NewClient(Config{BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		// Leave mode as offline (default)
		svc := NewAuditRetentionService(c)
		_, err = svc.GetRetentionConfig(context.Background())
		if err == nil {
			t.Fatal("expected error but got nil")
		}
	})

	t.Run("wraps 403 as enterprise-only error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		_, err := svc.GetRetentionConfig(context.Background())
		if err == nil {
			t.Fatal("expected enterprise-only error")
		}
	})
}

func TestAuditRetentionService_UpdateRetentionConfig(t *testing.T) {
	t.Run("sends PUT and returns updated config", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut || r.URL.Path != "/v1/audit/retention" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			var body map[string]int
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if body["retentionDays"] != 365 {
				t.Errorf("expected 365, got %d", body["retentionDays"])
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(RetentionConfig{RetentionDays: 365, UpdatedAt: "2026-05-01T00:00:00Z"})
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		result, err := svc.UpdateRetentionConfig(context.Background(), 365)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RetentionDays != 365 {
			t.Errorf("expected 365, got %d", result.RetentionDays)
		}
	})

	t.Run("returns error when platform offline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()
		c, err := NewClient(Config{BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		svc := NewAuditRetentionService(c)
		_, err = svc.UpdateRetentionConfig(context.Background(), 365)
		if err == nil {
			t.Fatal("expected error but got nil")
		}
	})
}

// integrityBodyValid is the route's 200 for an intact chain, copied from
// audit-integrity.test.ts's `mockVerifyIntegrity.mockResolvedValue` and from
// the handler's own empty-account return. Written as a literal, not as
// json.Encode(IntegrityResult{...}): a fake that serialises the struct under
// test can only prove the client agrees with itself, which is exactly how the
// invented windowDays/message/checkedAt fields stayed green (#803, #822).
const integrityBodyValid = `{"valid": true, "checkedCount": 12, "brokenLinks": []}`

// integrityBodyBroken is the same route's 200 when the chain is tampered with.
const integrityBodyBroken = `{
  "valid": false,
  "checkedCount": 20,
  "brokenLinks": [{"entryId": "entry-7", "position": 7}]
}`

func TestAuditRetentionService_VerifyIntegrity(t *testing.T) {
	t.Run("posts the mounted path with the window the route validates", func(t *testing.T) {
		var sent []byte
		var gotMethod, gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
			}
			sent = raw
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(integrityBodyValid)); err != nil {
				t.Errorf("write body: %v", err)
			}
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		if _, err := svc.VerifyIntegrity(context.Background(), 30); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// app.ts mounts createAuditIntegrityRoute() at /v1/audit/integrity and
		// the route is app.post('/'). /v1/audit/integrity/verify is a 404.
		if gotMethod != http.MethodPost || gotPath != "/v1/audit/integrity" {
			t.Errorf("request = %s %s, want POST /v1/audit/integrity", gotMethod, gotPath)
		}

		var body map[string]string
		if err := json.Unmarshal(sent, &body); err != nil {
			t.Fatalf("decode recorded body: %v", err)
		}
		// VerifyIntegritySchema is z.object({startDate: z.string().datetime(),
		// endDate: z.string().datetime()}) — a windowDays key is a 422, and so
		// is a bare YYYY-MM-DD in either bound.
		if _, ok := body["windowDays"]; ok {
			t.Errorf("body carries windowDays %q — the route rejects it as an unknown-shaped request", sent)
		}
		for _, field := range []string{"startDate", "endDate"} {
			value, ok := body[field]
			if !ok {
				t.Fatalf("body is missing %s: %s", field, sent)
			}
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				t.Errorf("%s = %q is not the RFC 3339 instant z.string().datetime() requires", field, value)
			}
		}
		if len(body) != 2 {
			t.Errorf("body = %s, want exactly startDate and endDate", sent)
		}
	})

	t.Run("decodes the fields the route returns", func(t *testing.T) {
		var sent []byte
		srv := bodyRecordingServer(t, integrityBodyBroken, http.StatusOK, &sent)
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		result, err := svc.VerifyIntegrity(context.Background(), 90)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Valid {
			t.Error("Valid = true for a body reporting a broken chain")
		}
		if result.CheckedCount != 20 {
			t.Errorf("CheckedCount = %d, want 20", result.CheckedCount)
		}
		// brokenLinks is the whole point of the endpoint and the client used
		// to drop it on the floor while decoding three fields that were never
		// sent.
		if len(result.BrokenLinks) != 1 {
			t.Fatalf("BrokenLinks = %+v, want one entry", result.BrokenLinks)
		}
		if result.BrokenLinks[0] != (IntegrityBrokenLink{EntryID: "entry-7", Position: 7}) {
			t.Errorf("BrokenLinks[0] = %+v, want {entry-7 7}", result.BrokenLinks[0])
		}
	})

	t.Run("an intact chain decodes to a non-nil empty broken-link list", func(t *testing.T) {
		var sent []byte
		srv := bodyRecordingServer(t, integrityBodyValid, http.StatusOK, &sent)
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		result, err := svc.VerifyIntegrity(context.Background(), 365)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Valid || result.CheckedCount != 12 {
			t.Errorf("result = %+v, want valid with 12 checked", result)
		}
		// The IPC result crosses into TypeScript, where `null` and `[]` are
		// not interchangeable for a list the panel iterates.
		if result.BrokenLinks == nil {
			t.Error("BrokenLinks = nil; the panel maps over this, so an empty chain must decode to []")
		}
	})

	t.Run("quotes the route's rejection instead of a bare status", func(t *testing.T) {
		// makeErrorBody('VALIDATION_ERROR', 'Invalid request body', requestId,
		// {fields: parsed.error.issues}) — what the route answers a body it
		// cannot parse, which is what {windowDays: 30} used to produce.
		const rejection = `{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Invalid request body",
    "details": {"fields": [{"path": ["startDate"], "message": "Required"}]}
  }
}`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			if _, err := w.Write([]byte(rejection)); err != nil {
				t.Errorf("write body: %v", err)
			}
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		_, err := svc.VerifyIntegrity(context.Background(), 30)
		if err == nil {
			t.Fatal("expected an error for a 422")
		}
		// platformResult.ts's STATUS_RE parses this literal text to classify
		// the failure as bad_request rather than a retry invitation.
		if !strings.Contains(err.Error(), "server returned 422") {
			t.Errorf("error = %q, must keep the literal `server returned 422`", err)
		}
		if !strings.Contains(err.Error(), "startDate") {
			t.Errorf("error = %q, drops the field the route named", err)
		}
	})

	t.Run("wraps 403 as enterprise-only error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		_, err := svc.VerifyIntegrity(context.Background(), 30)
		if err == nil {
			t.Fatal("expected enterprise-only error")
		}
	})

	t.Run("returns error when platform offline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()
		c, err := NewClient(Config{BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		svc := NewAuditRetentionService(c)
		_, err = svc.VerifyIntegrity(context.Background(), 90)
		if err == nil {
			t.Fatal("expected error but got nil")
		}
	})
}

// TestIntegrityResultCarriesOnlyTheRouteSFields fails if a field the route
// never sends is re-added to the struct. Dropping windowDays/message/checkedAt
// from the type is otherwise only enforced by the compiler, and a field added
// back would decode to its zero value and render as "0 days" and an empty
// timestamp — silently, which is how it survived until #822.
func TestIntegrityResultCarriesOnlyTheRouteSFields(t *testing.T) {
	raw, err := json.Marshal(IntegrityResult{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// audit-integrity.ts returns AuditService.verifyIntegrity's IntegrityResult
	// verbatim, and its empty-account guard returns the same three keys.
	want := map[string]bool{"valid": true, "checkedCount": true, "brokenLinks": true}
	for key := range keys {
		if !want[key] {
			t.Errorf("IntegrityResult declares %q, which POST /v1/audit/integrity does not return", key)
		}
	}
	for key := range want {
		if _, ok := keys[key]; !ok {
			t.Errorf("IntegrityResult is missing %q, which the route does return", key)
		}
	}
}

func TestIntegrityWindowBounds(t *testing.T) {
	// Mid-afternoon, so a bound truncated to midnight is visibly wrong.
	now := time.Date(2026, 8, 22, 15, 30, 0, 0, time.UTC)

	for _, tc := range []struct {
		windowDays int
		wantStart  string
	}{
		{30, "2026-07-23T00:00:00Z"},
		{90, "2026-05-24T00:00:00Z"},
		{365, "2025-08-22T00:00:00Z"},
	} {
		start, end := integrityWindowBounds(now, tc.windowDays)
		gotStart, err := time.Parse(time.RFC3339Nano, start)
		if err != nil {
			t.Fatalf("start %q is not RFC 3339: %v", start, err)
		}
		if want, _ := time.Parse(time.RFC3339Nano, tc.wantStart); !gotStart.Equal(want) {
			t.Errorf("windowDays=%d start = %s, want %s", tc.windowDays, start, tc.wantStart)
		}
		gotEnd, err := time.Parse(time.RFC3339Nano, end)
		if err != nil {
			t.Fatalf("end %q is not RFC 3339: %v", end, err)
		}
		// The end bound must cover the operator's whole current day. Truncated
		// to midnight it excludes `now` itself, so a verification run at
		// 15:30 would silently skip everything written today (#821's bug in
		// the sibling endpoint).
		if !gotEnd.After(now) {
			t.Errorf("windowDays=%d end = %s, does not include now (%s)", tc.windowDays, end, now)
		}
		if gotEnd.Sub(gotStart) < time.Duration(tc.windowDays)*24*time.Hour {
			t.Errorf("windowDays=%d spans %s, shorter than the requested window", tc.windowDays, gotEnd.Sub(gotStart))
		}
	}
}
