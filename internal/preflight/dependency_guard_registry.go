package preflight

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// popularPackages is a small curated set of high-download packages per
// ecosystem. A newly-added name that is within one edit of one of these (but is
// not exactly it) is the classic slopsquat shape (`reqeusts`, `loadsh`,
// `expres`). Intentionally small + high-signal — broadening it trades false
// positives for marginal coverage.
var popularPackages = map[Ecosystem][]string{
	EcoNPM: {
		"react", "react-dom", "lodash", "express", "axios", "chalk", "commander",
		"debug", "request", "moment", "webpack", "typescript", "eslint", "prettier",
		"jest", "vitest", "zod", "next", "vue", "dotenv", "uuid", "yargs", "rxjs",
		"socket.io", "mongoose", "redux", "babel", "rollup", "esbuild", "tslib",
	},
	EcoGo: {
		"github.com/spf13/cobra", "github.com/spf13/viper", "github.com/stretchr/testify",
		"github.com/gorilla/mux", "github.com/sirupsen/logrus", "gopkg.in/yaml.v3",
		"github.com/google/uuid", "github.com/pkg/errors", "github.com/gin-gonic/gin",
		"google.golang.org/grpc", "github.com/prometheus/client_golang",
	},
	EcoPip: {
		"requests", "numpy", "pandas", "flask", "django", "pytest", "pydantic",
		"fastapi", "boto3", "scipy", "sqlalchemy", "click", "pyyaml", "urllib3",
		"setuptools", "pillow", "matplotlib", "scikit-learn", "tensorflow", "torch",
	},
}

// typosquatMatch reports whether name is a likely typosquat of a popular package
// and returns that package. Skips very short names (too noisy) and exact matches.
func typosquatMatch(eco Ecosystem, name string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	if len(lower) < 4 {
		return "", false
	}
	for _, pop := range popularPackages[eco] {
		if lower == pop {
			return "", false // exact match is the real package, not a squat
		}
		if editDistanceWithin(lower, pop, 1) {
			return pop, true
		}
	}
	return "", false
}

// editDistanceWithin reports whether the optimal-string-alignment (Damerau)
// edit distance between a and b is ≤ max. Unlike plain Levenshtein it counts an
// adjacent transposition as ONE edit, which is the most common typosquat shape
// (`reqeust`→`request`, `lodahs`→`lodash`). Names are short, so a full matrix
// is fine; the length-difference lower bound short-circuits the obvious misses.
func editDistanceWithin(a, b string, max int) bool {
	la, lb := len(a), len(b)
	if la-lb > max || lb-la > max {
		return false
	}
	d := make([][]int, la+1)
	for i := range d {
		d[i] = make([]int, lb+1)
		d[i][0] = i
	}
	for j := 0; j <= lb; j++ {
		d[0][j] = j
	}
	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min3(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				if t := d[i-2][j-2] + 1; t < d[i][j] {
					d[i][j] = t
				}
			}
		}
	}
	return d[la][lb] <= max
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// httpRegistryChecker resolves package existence over HTTP. A non-200/404
// response (timeout, 5xx, rate-limit) is reported as inconclusive so a flaky
// registry never blocks a merge.
type httpRegistryChecker struct {
	client *http.Client
}

// NewHTTPRegistryChecker returns the production network-backed checker.
func NewHTTPRegistryChecker() RegistryChecker {
	return &httpRegistryChecker{client: &http.Client{Timeout: 6 * time.Second}}
}

func (h *httpRegistryChecker) Exists(ctx context.Context, eco Ecosystem, name string) RegistryStatus {
	endpoint, ok := registryURL(eco, name)
	if !ok {
		return RegistryInconclusive
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return RegistryInconclusive
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return RegistryInconclusive
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return RegistryExists
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return RegistryMissing
	default:
		return RegistryInconclusive
	}
}

// registryURL builds the existence-check URL for a package.
func registryURL(eco Ecosystem, name string) (string, bool) {
	switch eco {
	case EcoNPM:
		return "https://registry.npmjs.org/" + npmEscape(name), true
	case EcoPip:
		return "https://pypi.org/pypi/" + url.PathEscape(name) + "/json", true
	case EcoGo:
		return "https://proxy.golang.org/" + goModuleEscape(name) + "/@latest", true
	}
	return "", false
}

// npmEscape encodes a scoped package name (@scope/name) for the registry path.
func npmEscape(name string) string {
	if strings.HasPrefix(name, "@") && strings.Contains(name, "/") {
		return strings.Replace(name, "/", "%2f", 1)
	}
	return name
}

// goModuleEscape applies the Go module proxy's case-encoding: each uppercase
// letter becomes "!"+lowercase (per golang.org/ref/mod#goproxy-protocol).
func goModuleEscape(mod string) string {
	var b strings.Builder
	for _, r := range mod {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
