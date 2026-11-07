package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func inquiryFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/inquiry-scorecard.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

func inquiryClient(t *testing.T, url string, session bool) *Client {
	t.Helper()
	c, err := NewClient(Config{BaseURL: url, APIKey: "license-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if session {
		c.SetSessionToken(conformanceSessionJWT)
	}
	return c
}

// The decoded scorecard is the platform's, field for field: re-encoding it
// reproduces the fixture, so no number is dropped, renamed or recomputed.
func TestReadInquiryScorecard_DecodesThePlatformNumbers(t *testing.T) {
	fixture := inquiryFixture(t)
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	sc, err := inquiryClient(t, srv.URL, true).ReadInquiryScorecard(context.Background(), InquiryScorecardQuery{})
	if err != nil {
		t.Fatalf("ReadInquiryScorecard: %v", err)
	}
	if gotPath != "/v1/inquiry/scorecard" || gotQuery != "" {
		t.Fatalf("request %s?%s, want /v1/inquiry/scorecard with no query", gotPath, gotQuery)
	}
	if gotAuth != "Bearer "+conformanceSessionJWT {
		t.Fatalf("Authorization %q, want the session token", gotAuth)
	}
	if sc.MedianTimeToAnswerSeconds != nil {
		t.Fatalf("median = %v, want null kept as nil", *sc.MedianTimeToAnswerSeconds)
	}
	if len(sc.ByClass) != 2 || sc.ByClass[1].Class != "preference" || sc.ByClass[1].ImpactRatio != 0.1667 {
		t.Fatalf("byClass = %+v", sc.ByClass)
	}
	if len(sc.WeakClasses) != 1 || sc.WeakClasses[0].Recommendation != "cut" {
		t.Fatalf("weakClasses = %+v", sc.WeakClasses)
	}

	var want, got map[string]any
	_ = json.Unmarshal(fixture, &want)
	reencoded, _ := json.Marshal(map[string]any{"scorecard": sc})
	_ = json.Unmarshal(reencoded, &got)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip differs:\nwant %v\ngot  %v", want, got)
	}
}

// Only the parameters the caller set are sent.
func TestReadInquiryScorecard_EncodesOnlySetParams(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write(inquiryFixture(t))
	}))
	defer srv.Close()
	floor, min := 0.25, 3
	_, err := inquiryClient(t, srv.URL, true).ReadInquiryScorecard(context.Background(),
		InquiryScorecardQuery{Window: "7d", Floor: &floor, MinSample: &min})
	if err != nil {
		t.Fatalf("ReadInquiryScorecard: %v", err)
	}
	if gotQuery != "floor=0.25&minSample=3&window=7d" {
		t.Fatalf("query %q", gotQuery)
	}
}

// A license key alone is refused before anything is sent.
func TestReadInquiryScorecard_RefusesWithoutSession(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	_, err := inquiryClient(t, srv.URL, false).ReadInquiryScorecard(context.Background(), InquiryScorecardQuery{})
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
	if hits != 0 {
		t.Fatalf("%d requests sent, want none", hits)
	}
}

func TestReadInquiryScorecard_SurfacesPlatformErrorAndMissingScorecard(t *testing.T) {
	status, body := http.StatusUnauthorized, `{"error":{"code":"UNAUTHORIZED","message":"session expired"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := inquiryClient(t, srv.URL, true)
	if _, err := c.ReadInquiryScorecard(context.Background(), InquiryScorecardQuery{}); err == nil ||
		!strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want the 401", err)
	}
	status, body = http.StatusOK, `{}`
	if _, err := c.ReadInquiryScorecard(context.Background(), InquiryScorecardQuery{}); err == nil ||
		!strings.Contains(err.Error(), "no scorecard") {
		t.Fatalf("err = %v, want no scorecard", err)
	}
}
