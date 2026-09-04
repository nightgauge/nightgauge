package attention_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every Store.Resolve / Store.Acknowledge caller must pass a FALLBACK-resolved
// actor, never a raw variable (#1418).
//
// The store refuses an empty actor (#1405), which makes every path that feeds
// it load-bearing. Three of the four call sites were routed through a fallback
// and one was not — `ApplyRelayedResolve`, whose two siblings in the SAME FILE
// did it correctly.
//
// That one was the worst of the four to miss. A CLI resolve with an empty actor
// fails loudly with a clear message; a relayed one has its error acked as
// consumed and never retried (#1421), so the dashboard reports success and the
// card stays open. Same defect, opposite observability, and the silent one is
// on the path an operator actually uses.
//
// WHY A SOURCE SCAN. The property is "no caller passes a bare variable", which
// is about call sites rather than about behaviour, so no behavioural test can
// see it — each individual caller passes its own tests either way. The repo
// already uses this shape for the queue-halt policy's matcher ban. It is a
// weaker guard than a type would be; it is much stronger than the nothing that
// let the fourth site through.

// Matched on the attention store's OWN signatures, not on the method names.
//
//	Resolve(ctx, id, optionID, actor, ...)   -- actor is the 4th argument
//	Acknowledge(id, actor)                   -- actor is the 2nd, and there are only two
//
// `.Resolve(ctx` is the discriminator that keeps `models.Resolve(provider,
// band)` and friends out: this Resolve takes a context and those do not. The
// first draft of this test matched every `.Resolve(` in the tree and reported
// adapter code and comment prose as offenders.
var (
	// Anchored on the RECEIVER as well as the method, because `Acknowledge` and
	// `Resolve` are common names. Every attention call site today reaches the
	// store either through a local named `store` or through `attention.New(...)`
	// inline; the companion test below fails if this stops matching them.
	recv = `(?:store|attention\.New\([^()]*\))`

	resolveCall = regexp.MustCompile(recv + `\.Resolve\(\s*ctx\s*,\s*[^,]+,\s*[^,]+,\s*([^,]+),`)
	ackCall     = regexp.MustCompile(recv + `\.Acknowledge\(\s*[^,]+,\s*([^)]+)\)`)
)

// stripComments removes // line comments so prose cannot be read as a call.
func stripComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// A fallback-resolved actor is a CALL, not a bare identifier or field access.
// `attentionActor(actor, root)` passes; `actor` and `p.Actor` do not.
var looksResolved = regexp.MustCompile(`Actor\w*\(`)

func TestEveryResolveCallerSuppliesAFallbackActor(t *testing.T) {
	root := repoRootForActorGuard(t)

	var offenders []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			for _, re := range []*regexp.Regexp{resolveCall, ackCall} {
				for _, m := range re.FindAllStringSubmatch(stripComments(string(src)), -1) {
					arg := strings.TrimSpace(m[1])
					// String literals are self-evidently non-empty.
					if strings.HasPrefix(arg, `"`) || looksResolved.MatchString(arg) {
						continue
					}
					offenders = append(offenders, rel+": passes "+arg)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("these Store.Resolve/Acknowledge callers pass a raw actor instead of a "+
			"fallback-resolved one:\n  %s\n\nAn empty actor is refused at the store boundary "+
			"(#1405). On the CLI that fails loudly; on the platform relay the error is acked "+
			"as consumed and never retried (#1421), so the surface reports success and the card "+
			"stays open.", strings.Join(offenders, "\n  "))
	}
}

// The guard is worthless if the scan matches nothing, so assert it found the
// call sites it is supposed to be policing.
func TestTheResolveCallerScanActuallyFindsCallSites(t *testing.T) {
	root := repoRootForActorGuard(t)
	src, err := os.ReadFile(filepath.Join(root, "internal", "ipc", "attention.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n := len(resolveCall.FindAllString(stripComments(string(src)), -1)); n < 2 {
		t.Errorf("the Resolve scan matched %d call(s) in internal/ipc/attention.go, want at least 2 — "+
			"a scan that matches nothing passes vacuously", n)
	}
	if n := len(ackCall.FindAllString(stripComments(string(src)), -1)); n < 1 {
		t.Errorf("the Acknowledge scan matched %d call(s), want at least 1", n)
	}
}

func repoRootForActorGuard(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
