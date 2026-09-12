package main

// Exit-code contract of `nightgauge ci checks-complete` (#1691): 1 means a
// completed check failed and nothing else. Every failure to measure exits 2
// with a "could not run:" line, because scripts/post-merge-check.sh execs this
// verb and an exit 1 there tells the operator to fix a main that may be green.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// rewriteTransport sends every request to server, whatever host it named.
type rewriteTransport struct{ server *httptest.Server }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = t.server.Listener.Addr().String()
	return http.DefaultTransport.RoundTrip(req)
}

// failingTransport fails every request before it reaches the network.
type failingTransport struct{ err error }

func (t failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// clientOver builds the real client over rt. The client records every request
// in the API ledger, by default under the working directory — the package
// source tree — so each test points it at its own temp file.
func clientOver(t *testing.T, rt http.RoundTripper) func() (*gh.Client, error) {
	t.Helper()
	t.Setenv("NIGHTGAUGE_GITHUB_API_LOG", filepath.Join(t.TempDir(), "github-api.jsonl"))
	return func() (*gh.Client, error) {
		return gh.NewClientWithHTTPClient(&http.Client{Transport: rt}), nil
	}
}

// forgeServing answers the commit's two status surfaces with the given bodies.
// Every other path (branch protection, rulesets, actions runs) is a 404, which
// the verb treats as "nothing required" and "no per-run data".
func forgeServing(t *testing.T, checkRuns, statuses string) func() (*gh.Client, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			_, _ = w.Write([]byte(checkRuns))
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(statuses))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return clientOver(t, rewriteTransport{srv})
}

// forgeFailing answers every request with status and body.
func forgeFailing(t *testing.T, status int, body string) func() (*gh.Client, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return clientOver(t, rewriteTransport{srv})
}

func runVerb(t *testing.T, newClient func() (*gh.Client, error), outputJSON bool) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	opts := checksCompleteOptions{owner: "acme", repo: "acme/widget", branch: "main", outputJSON: outputJSON}
	code = runChecksComplete(context.Background(), "deadbeef", opts, newClient, noSleepCmd, &out, &errOut)
	return code, out.String(), errOut.String()
}

const emptyStatuses = `{"sha":"deadbeef","statuses":[]}`

func TestRunChecksComplete_MeasurementErrorsExitTwo(t *testing.T) {
	cases := []struct {
		name      string
		newClient func(t *testing.T) func() (*gh.Client, error)
		reason    string
	}{
		{"401 unauthorized", func(t *testing.T) func() (*gh.Client, error) {
			return forgeFailing(t, http.StatusUnauthorized, "{\n  \"message\": \"Bad credentials\"\n}")
		}, "unauthorized"},
		{"token resolution failure", func(*testing.T) func() (*gh.Client, error) {
			return func() (*gh.Client, error) {
				return nil, errors.New("no GitHub token available for configured github_user \"someone\"")
			}
		}, "resolve GitHub client: no GitHub token available"},
		{"network error", func(t *testing.T) func() (*gh.Client, error) {
			return clientOver(t, failingTransport{errors.New("dial tcp: connection refused")})
		}, "connection refused"},
		{"5xx", func(t *testing.T) func() (*gh.Client, error) {
			return forgeFailing(t, http.StatusBadGateway, `{"message":"Server Error"}`)
		}, "GitHub API returned 502"},
		{"429 rate limited", func(t *testing.T) func() (*gh.Client, error) {
			return forgeFailing(t, http.StatusTooManyRequests, `{"message":"slow down"}`)
		}, "rate limit"},
		{"unparseable response", func(t *testing.T) func() (*gh.Client, error) {
			return forgeServing(t, `<html>not json</html>`, emptyStatuses)
		}, "decode check runs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runVerb(t, tc.newClient(t), false)
			if code != exitChecksNotObservable {
				t.Fatalf("exit = %d, want %d — a failure to measure is never red\nstdout: %s\nstderr: %s",
					code, exitChecksNotObservable, stdout, stderr)
			}
			if strings.Contains(stdout, "RED") {
				t.Errorf("stdout reports RED for a measurement error:\n%s", stdout)
			}
			var line string
			for _, l := range strings.Split(stdout, "\n") {
				if strings.Contains(l, "could not run: ") {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("no \"could not run:\" line on stdout:\n%s", stdout)
			}
			if !strings.Contains(line, tc.reason) {
				t.Errorf("could-not-run line %q does not name the reason %q", line, tc.reason)
			}
			if !strings.Contains(stderr, "not a verdict") {
				t.Errorf("stderr does not say the commit was not measured:\n%s", stderr)
			}
		})
	}
}

func TestRunChecksComplete_MeasurementErrorJSON(t *testing.T) {
	code, stdout, stderr := runVerb(t, forgeFailing(t, http.StatusUnauthorized, `{"message":"Bad credentials"}`), true)
	if code != exitChecksNotObservable {
		t.Fatalf("exit = %d, want %d", code, exitChecksNotObservable)
	}
	var res checksCompleteResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not the JSON result: %v\n%s", err, stdout)
	}
	if res.Verdict != gh.ChecksNotYet || !strings.Contains(res.CouldNotRun, "unauthorized") {
		t.Errorf("verdict = %q couldNotRun = %q, want not-yet with the 401 reason", res.Verdict, res.CouldNotRun)
	}
	if !strings.Contains(stderr, "could not run: ") {
		t.Errorf("stderr has no could-not-run line:\n%s", stderr)
	}
}

func TestRunChecksComplete_VerdictExitCodes(t *testing.T) {
	cases := []struct {
		name      string
		checkRuns string
		statuses  string
		want      int
		word      string
	}{
		{"all green", `{"check_runs":[{"name":"build","status":"completed","conclusion":"success"}]}`,
			`{"sha":"deadbeef","statuses":[{"context":"cla","state":"success"}]}`, exitChecksGreen, "GREEN"},
		{"a completed check failed", `{"check_runs":[{"name":"build","status":"completed","conclusion":"success"},` +
			`{"name":"lint","status":"completed","conclusion":"failure"}]}`, emptyStatuses, exitChecksRed, "RED"},
		{"a failed commit status", `{"check_runs":[{"name":"build","status":"completed","conclusion":"success"}]}`,
			`{"sha":"deadbeef","statuses":[{"context":"cla","state":"failure"}]}`, exitChecksRed, "RED"},
		{"pending", `{"check_runs":[{"name":"build","status":"in_progress","conclusion":null}]}`,
			emptyStatuses, exitChecksNotObservable, "NOT-YET"},
		{"nothing yet", `{"check_runs":[]}`, emptyStatuses, exitChecksNotObservable, "NOT-YET"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, _ := runVerb(t, forgeServing(t, tc.checkRuns, tc.statuses), false)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d\n%s", code, tc.want, stdout)
			}
			if first := strings.SplitN(stdout, "\n", 2)[0]; first != tc.word {
				t.Errorf("first line = %q, want %q", first, tc.word)
			}
			if strings.Contains(stdout, "could not run") {
				t.Errorf("a measured verdict reports could-not-run:\n%s", stdout)
			}
		})
	}
}

// TestChecksCompleteCobraErrorsExitTwo covers the path RunE never sees:
// cobra's own argument and flag errors, which main() used to map to exit 1.
func TestChecksCompleteCobraErrorsExitTwo(t *testing.T) {
	cases := map[string][]string{
		"missing sha":  {"ci", "checks-complete"},
		"unknown flag": {"ci", "checks-complete", "deadbeef", "--no-such-flag"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			root := rootCmd()
			root.SetArgs(args)
			var cobraOut bytes.Buffer
			root.SetOut(&cobraOut)
			root.SetErr(&cobraOut)
			cmd, err := root.ExecuteC()
			var stderr bytes.Buffer
			if code := exitCodeFor(&stderr, cmd, err); code != exitCouldNotRun {
				t.Fatalf("exit = %d, want %d (err = %v)", code, exitCouldNotRun, err)
			}
			if !strings.HasPrefix(stderr.String(), "could not run: ") {
				t.Errorf("stderr = %q, want a could-not-run line", stderr.String())
			}
			if strings.Contains(cobraOut.String(), "Error:") {
				t.Errorf("cobra printed its own error line too:\n%s", cobraOut.String())
			}
		})
	}
}

func TestExitCodeFor(t *testing.T) {
	plain := &cobra.Command{Use: "plain"}
	marked := &cobra.Command{Use: "marked"}
	markCouldNotRunExit(marked)

	cases := []struct {
		name string
		cmd  *cobra.Command
		err  error
		want int
	}{
		{"success", marked, nil, 0},
		{"verdict red passes through", marked, verdictExit{code: exitChecksRed}, exitChecksRed},
		{"verdict not-yet passes through", marked, verdictExit{code: exitChecksNotObservable}, exitChecksNotObservable},
		{"error on a verdict command is could-not-run", marked, errors.New("boom"), exitCouldNotRun},
		{"error on any other command keeps exit 1", plain, errors.New("boom"), 1},
		{"no command keeps exit 1", nil, errors.New("boom"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(&bytes.Buffer{}, tc.cmd, tc.err); got != tc.want {
				t.Errorf("exitCodeFor = %d, want %d", got, tc.want)
			}
		})
	}
}
