package release

import (
	"fmt"
	"strconv"
	"strings"
)

// parseSemver splits a `vX.Y.Z`-style tag into an integer slice. Empty input is
// treated as `0`. It tolerates a leading `v`/`V` and a multi-char non-numeric
// prefix such as `rust-v` (openai/codex) or `release-` (#4056). A pre-release
// tag (`…-alpha.4`, `…-rc.1`) is left UNPARSEABLE on purpose so IsNewer excludes
// it — a safety net beside the fetcher's explicit Draft/Prerelease skip.
// (Originally mirrored the Python `compare_versions` in the release-watch skill;
// the Go binary is now the canonical, broader implementation.)
func parseSemver(v string) ([]int, error) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if v == "" {
		return []int{0}, nil
	}
	// Strip any remaining non-numeric tag prefix up to the first digit (e.g.
	// `rust-v0.141.0` -> `0.141.0`). A pre-release/build SUFFIX is intentionally
	// NOT stripped: a `-rc`/`-alpha` tag stays unparseable so IsNewer treats it
	// as "not newer" and it is excluded — a safety net beside the fetcher's
	// explicit Draft/Prerelease skip (#4056).
	if i := strings.IndexFunc(v, func(r rune) bool { return r >= '0' && r <= '9' }); i > 0 {
		v = v[i:]
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid semver component %q in %q", p, v)
		}
		out[i] = n
	}
	return out, nil
}

// compareSemver returns -1 / 0 / 1 if a is less / equal / greater than b.
// Components are zero-padded to the longest length before comparison so
// `1.2` == `1.2.0`. Errors propagate from parseSemver.
func compareSemver(a, b string) (int, error) {
	pa, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	max := len(pa)
	if len(pb) > max {
		max = len(pb)
	}
	for i := 0; i < max; i++ {
		var ai, bi int
		if i < len(pa) {
			ai = pa[i]
		}
		if i < len(pb) {
			bi = pb[i]
		}
		if ai < bi {
			return -1, nil
		}
		if ai > bi {
			return 1, nil
		}
	}
	return 0, nil
}

// IsNewer reports whether tag is strictly greater than baseline. Returns false
// (without an error) when either side is unparseable — preserving the
// release-watch skill's defensive behavior of falling through on malformed
// tags rather than aborting the fetch.
func IsNewer(tag, baseline string) bool {
	cmp, err := compareSemver(tag, baseline)
	if err != nil {
		return false
	}
	return cmp > 0
}
