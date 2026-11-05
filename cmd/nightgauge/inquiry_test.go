package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/platform"
)

func loadInquiryFixture(t *testing.T) *platform.InquiryScorecard {
	t.Helper()
	raw, err := os.ReadFile("../../internal/platform/testdata/inquiry-scorecard.json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Scorecard *platform.InquiryScorecard }
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Scorecard
}

func runInquiry(t *testing.T, sc *platform.InquiryScorecard, fetchErr error, args ...string) (string, string, error, ipc.PlatformInquiryScorecardParams) {
	t.Helper()
	var got ipc.PlatformInquiryScorecardParams
	prev := fetchInquiryScorecardFn
	fetchInquiryScorecardFn = func(_ context.Context, _ string, p ipc.PlatformInquiryScorecardParams) (*platform.InquiryScorecard, error) {
		got = p
		return sc, fetchErr
	}
	t.Cleanup(func() { fetchInquiryScorecardFn = prev })
	cmd := inquiryScorecardCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err, got
}

func TestInquiryScorecard_JSONIsThePlatformObject(t *testing.T) {
	sc := loadInquiryFixture(t)
	out, _, err, p := runInquiry(t, sc, nil, "--json", "--window", "7d", "--floor", "0.25")
	if err != nil {
		t.Fatal(err)
	}
	if p.Window != "7d" || p.Floor == nil || *p.Floor != 0.25 || p.MinSample != nil {
		t.Fatalf("params = %+v", p)
	}
	var got platform.InquiryScorecard
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&got, sc) {
		t.Fatalf("json output differs from the platform object")
	}
}

func TestInquiryScorecard_HumanSummaryShowsPlatformNumbers(t *testing.T) {
	out, _, err, _ := runInquiry(t, loadInquiryFixture(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"window 30d", "impact ratio 0.4286", "preference", "0.1667",
		"Median time to answer: n/a", "spend avoided $12.50", "history rich: 12 sets", "under floor 0.3 over 12 answers — cut"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
}

func TestInquiryScorecard_EmptyWindow(t *testing.T) {
	out, _, err, _ := runInquiry(t, &platform.InquiryScorecard{Window: "30d"}, nil)
	if err != nil || strings.TrimSpace(out) != "No inquiry answers in window 30d." {
		t.Fatalf("err %v out %q", err, out)
	}
}

func TestInquiryScorecard_ErrorsExitDistinctly(t *testing.T) {
	cases := []struct {
		err  error
		code int
		msg  string
	}{
		{errInquiryDaemonUnreachable, inquiryExitDaemonUnreachable, "nightgauge serve"},
		{errors.New("ipc client: " + ipc.ErrMsgPlatformNotConfigured + " (code -32000)"), inquiryExitNotConfigured, "no platform is configured"},
		{errors.New("ipc client: " + ipc.ErrMsgPlatformNotSignedIn + " (code -32000)"), inquiryExitNotSignedIn, "sign in"},
		{errors.New("ipc client: inquiry scorecard: 500 boom"), inquiryExitPlatformError, "500 boom"},
	}
	for _, c := range cases {
		_, stderr, err, _ := runInquiry(t, nil, c.err)
		var ve verdictExit
		if !errors.As(err, &ve) || ve.code != c.code {
			t.Errorf("%v: err %v, want exit %d", c.err, err, c.code)
		}
		if !strings.Contains(stderr, c.msg) {
			t.Errorf("%v: stderr %q lacks %q", c.err, stderr, c.msg)
		}
	}
}
