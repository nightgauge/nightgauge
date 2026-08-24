package failure

import "testing"

// TestGitTransportAuthIsPermission pins the classification #878 turned on.
//
// The observed run's push failed with go-git's `invalid auth method`. That
// string matched nothing in the ladder, so the failure classified CatUnknown
// and the scheduler treated a missing credential as a capability shortfall:
// it escalated haiku → sonnet and re-dispatched a 67,610-character prompt that
// died at the same line 44 seconds later.
func TestGitTransportAuthIsPermission(t *testing.T) {
	c := NewClassifier()
	for _, stderr := range []string{
		"error: failed to push some refs: invalid auth method",
		"remote: Authentication failed for 'https://github.com/o/r.git/'",
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled",
		"authentication required: Repository not found",
		"POST /graphql: 401 Bad credentials",
		"ssh: unable to authenticate, attempted methods [none publickey]",
		"invalid username or password",
	} {
		if got := c.Classify("feature-validate", 1, stderr).Category; got != CatPermission {
			t.Errorf("Classify(%q).Category = %q, want %q", stderr, got, CatPermission)
		}
	}
}

// TestNonAuthFailuresAreNotPermission is the discriminator: the clauses added
// for #878 must not swallow unrelated failures into a non-retryable bucket.
func TestNonAuthFailuresAreNotPermission(t *testing.T) {
	c := NewClassifier()
	for _, stderr := range []string{
		"compile error: undefined: authenticateUser",
		"test failed: expected 3 got 4",
		"could not read the plan file",
	} {
		if got := c.Classify("feature-dev", 1, stderr).Category; got == CatPermission {
			t.Errorf("Classify(%q).Category = %q, want anything but %q", stderr, got, CatPermission)
		}
	}
}
