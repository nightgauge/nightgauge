package depgraph

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// reIssueArg is one issue named on a command line: an optional repository
	// token (short or owner/name, spelled as a body reference spells it), then
	// an optional '#', then the number. Anchored: the whole argument must match.
	reIssueArg = regexp.MustCompile(`^(?:(` + repoToken + `)#|#)?(\d+)$`)

	// reIssueURLArg is a GitHub issue URL as the whole argument. Each segment
	// is a repoSegment, so a dot-only segment ("..") never reaches a REST path.
	reIssueURLArg = regexp.MustCompile(
		`^https://github\.com/(` + repoSegment + `)/(` + repoSegment + `)/issues/(\d+)/?$`,
	)
)

// ParseIssueArg resolves one issue named on a command line (#1508) to its
// "owner/name" repository and number. It accepts:
//
//   - N or #N: issue N of selfRepo;
//   - owner/name#N: as written;
//   - name#N: the repository the workspace alias map gives for name, or
//     name under selfRepo's owner when the map does not hold it;
//   - https://github.com/owner/name/issues/N.
//
// Anything else is an error, as is a short name several workspace
// repositories share and a number that is not positive. selfRepo must be
// "owner/name".
func ParseIssueArg(arg, selfRepo string, aliases map[string]string) (repo string, number int, err error) {
	selfOwner, _, ok := strings.Cut(selfRepo, "/")
	if !ok || selfOwner == "" {
		return "", 0, fmt.Errorf("repository %q is not owner/name", selfRepo)
	}
	arg = strings.TrimSpace(arg)
	var token, digits string
	if m := reIssueURLArg.FindStringSubmatch(arg); m != nil {
		token, digits = m[1]+"/"+m[2], m[3]
	} else if m := reIssueArg.FindStringSubmatch(arg); m != nil {
		token, digits = m[1], m[2]
	} else {
		return "", 0, fmt.Errorf("want N, owner/repo#N, repo#N or https://github.com/owner/repo/issues/N")
	}
	number, err = strconv.Atoi(digits)
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("issue number must be a positive integer")
	}
	switch {
	case token == "":
		return selfRepo, number, nil
	case strings.Contains(token, "/"):
		return token, number, nil
	}
	full, kind := lookupAlias(token, aliases)
	switch kind {
	case aliasAmbiguous:
		return "", 0, fmt.Errorf("%q is ambiguous: several workspace repositories share the name; spell it owner/repo#%d", token, number)
	case aliasRepo:
		return full, number, nil
	}
	return selfOwner + "/" + token, number, nil
}
