package github

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

type failOnRequest struct{ t *testing.T }

func (f failOnRequest) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL)
	return nil, errors.New("no GitHub request was expected")
}

// TestCompleteEpic_RefusesACheckoutOfAnotherRepository (#2388): epic complete
// finds, merges and deletes epic/<N>-* in the checkout it runs in, where that
// branch names #N of the checkout's own repository. From a checkout of
// example-org/app, completing example-org/platform#20 is refused before the
// epic is read or closed.
func TestCompleteEpic_RefusesACheckoutOfAnotherRepository(t *testing.T) {
	t.Setenv("NIGHTGAUGE_GITHUB_API_LOG", filepath.Join(t.TempDir(), "github-api.jsonl"))
	root := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	gittest.Run(t, root, "remote", "add", "origin", "https://github.com/example-org/app.git")
	client := NewClientWithHTTPClient(&http.Client{Transport: failOnRequest{t}})

	res, err := NewEpicService(client).CompleteEpic(context.Background(), "example-org", "platform", 20, root)
	if err == nil {
		t.Fatalf("CompleteEpic from a checkout of another repository = %+v, want a refusal", res)
	}
	if !strings.Contains(err.Error(), "a checkout of example-org/app, not example-org/platform") {
		t.Errorf("err = %v, want it to name the checkout's repository", err)
	}
}
