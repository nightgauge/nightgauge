package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nightgauge/nightgauge/internal/platform"
)

func TestPlatformGetInquiryScorecard_NoClient(t *testing.T) {
	s := NewServer(nil)
	_, err := s.methods["platform.getInquiryScorecard"](context.Background(), nil)
	if err == nil || err.Error() != ErrMsgPlatformNotConfigured {
		t.Fatalf("err = %v, want %q", err, ErrMsgPlatformNotConfigured)
	}
}

func TestPlatformGetInquiryScorecard_NoSession(t *testing.T) {
	s := NewServer(nil, WithPlatformClient(newTestPlatformClientFor(t, "http://127.0.0.1:1", "license-key")))
	_, err := s.methods["platform.getInquiryScorecard"](context.Background(), nil)
	if err == nil || err.Error() != ErrMsgPlatformNotSignedIn {
		t.Fatalf("err = %v, want %q", err, ErrMsgPlatformNotSignedIn)
	}
}

func TestPlatformGetInquiryScorecard_PassesParamsAndReturnsScorecard(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"scorecard":{"window":"7d","overall":{"answers":3,"impactRatio":0.6667}}}`))
	}))
	defer srv.Close()
	pc := newTestPlatformClientFor(t, srv.URL, "license-key")
	pc.SetSessionToken("header.payload.signature")
	s := NewServer(nil, WithPlatformClient(pc))

	raw, _ := json.Marshal(PlatformInquiryScorecardParams{Window: "7d"})
	res, err := s.methods["platform.getInquiryScorecard"](context.Background(), raw)
	if err != nil {
		t.Fatalf("getInquiryScorecard: %v", err)
	}
	sc, ok := res.(*platform.InquiryScorecard)
	if !ok || sc.Window != "7d" || sc.Overall.Answers != 3 || sc.Overall.ImpactRatio != 0.6667 {
		t.Fatalf("result = %#v", res)
	}
	if gotQuery != "window=7d" {
		t.Fatalf("query %q, want window=7d", gotQuery)
	}
}
