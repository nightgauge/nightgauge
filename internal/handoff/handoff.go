// Package handoff parses the `<!-- nightgauge:handoff … -->` header block of a
// session handoff file and reports what it says that is no longer true.
//
// A handoff is a markdown file a session rewrites at its end, so the next
// session can start where it stopped. Its header is a YAML mapping inside an
// HTML comment, so it renders invisibly on the forge:
//
//	<!-- nightgauge:handoff
//	repo: acme-api
//	session: 12
//	updated: 2026-09-30
//	tip: 3542de0d
//	open_issues: 41
//	open_prs: 2
//	next: [acme-api#88, acme-api#91]
//	needs:
//	  - { from: acme-web, issue: acme-web#17, what: the login redirect }
//	provides: []
//	-->
//
// Assess is deterministic: the same files, checkouts and clock produce the same
// report. No LLM participates, and nothing here talks to the forge. Staleness is
// measured against a local checkout's ref, so the caller fetches first if it
// wants the forge's answer (#1481).
package handoff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Finding codes. They match the program office's existing checker so a
// consumer can replace that script without renaming its alerts.
const (
	CodeHeaderMissing       = "handoff-header-missing"
	CodeHeaderUnparseable   = "handoff-header-unparseable"
	CodeStale               = "handoff-stale"
	CodeTipUnresolvable     = "handoff-tip-unresolvable"
	CodeTipUnmeasured       = "handoff-tip-unmeasured"
	CodeNeedUnmaterialized  = "cross-repo-need-unmaterialized"
	CodeCrossRepoRowInvalid = "cross-repo-row-malformed"
)

// Severities. A "finding" is something to act on; "info" records that a value
// could not be measured, which is never the same as it being fine.
const (
	SeverityFinding = "finding"
	SeverityInfo    = "info"
)

// DefaultMaxAgeDays is how old a header's `updated` date may be before the
// handoff is reported stale.
const DefaultMaxAgeDays = 7

var headerRE = regexp.MustCompile(`(?s)<!--\s*nightgauge:handoff\s*\n(.*?)\n?-->`)

// Header is the parsed header block. Raw keeps every key, including ones this
// version does not model, so a JSON consumer loses nothing.
type Header struct {
	Repo       string           `json:"repo,omitempty"`
	Session    string           `json:"session,omitempty"`
	Updated    string           `json:"updated,omitempty"`
	Tip        string           `json:"tip,omitempty"`
	OpenIssues *int             `json:"open_issues,omitempty"`
	OpenPRs    *int             `json:"open_prs,omitempty"`
	Next       []string         `json:"next"`
	Needs      []map[string]any `json:"needs"`
	Provides   []map[string]any `json:"provides"`
	Raw        map[string]any   `json:"raw"`

	// malformed rows are kept out of Needs/Provides and reported as findings.
	malformed []string
}

// ErrNoHeader is returned by ParseHeader when the text carries no block.
var ErrNoHeader = errors.New("no <!-- nightgauge:handoff --> block")

// ParseHeader extracts and parses the first header block in text.
func ParseHeader(text string) (*Header, error) {
	m := headerRE.FindStringSubmatch(text)
	if m == nil {
		return nil, ErrNoHeader
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(m[1]), &doc); err != nil {
		return nil, fmt.Errorf("header is not valid YAML: %w", err)
	}
	var raw map[string]any
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode || doc.Decode(&raw) != nil || raw == nil {
		return nil, errors.New("header did not parse to a mapping")
	}
	root := doc.Content[0]
	h := &Header{
		Repo:     verbatim(root, "repo"),
		Session:  verbatim(root, "session"),
		Updated:  verbatim(root, "updated"),
		Tip:      verbatim(root, "tip"),
		Next:     []string{},
		Needs:    []map[string]any{},
		Provides: []map[string]any{},
		Raw:      raw,
	}
	h.OpenIssues = intPtr(raw["open_issues"])
	h.OpenPRs = intPtr(raw["open_prs"])
	if list, ok := raw["next"].([]any); ok {
		for _, v := range list {
			if s := scalar(v); s != "" {
				h.Next = append(h.Next, s)
			}
		}
	}
	h.Needs, h.malformed = rows("needs", raw["needs"], h.malformed)
	h.Provides, h.malformed = rows("provides", raw["provides"], h.malformed)
	return h, nil
}

func rows(key string, v any, malformed []string) ([]map[string]any, []string) {
	out := []map[string]any{}
	list, ok := v.([]any)
	if !ok {
		if v != nil {
			malformed = append(malformed, fmt.Sprintf("%s is not a list: %v", key, v))
		}
		return out, malformed
	}
	for _, row := range list {
		if row == nil {
			continue
		}
		m, ok := row.(map[string]any)
		if !ok {
			malformed = append(malformed,
				fmt.Sprintf("%s row is not a { from|to, issue, what } mapping: %v", key, row))
			continue
		}
		out = append(out, m)
	}
	return out, malformed
}

// verbatim returns a top-level scalar exactly as written. A tip such as
// 12791854 or 0123abcd must not round-trip through YAML's int or float
// resolution, and an unquoted date must not become a timestamp.
func verbatim(mapping *yaml.Node, key string) string {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key && mapping.Content[i+1].Kind == yaml.ScalarNode {
			if mapping.Content[i+1].Tag == "!!null" {
				return ""
			}
			return strings.TrimSpace(mapping.Content[i+1].Value)
		}
	}
	return ""
}

func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case time.Time:
		// yaml.v3 decodes an unquoted 2026-09-30 into time.Time in an `any`.
		return t.Format("2006-01-02")
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func intPtr(v any) *int {
	switch t := v.(type) {
	case int:
		return &t
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return &n
		}
	}
	return nil
}

// Finding is one structured problem with one handoff.
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Repo     string `json:"repo,omitempty"`
	Message  string `json:"message"`
}

// Entry is one handoff file's assessment.
type Entry struct {
	Path             string    `json:"path"`
	Header           *Header   `json:"header"`
	Checkout         string    `json:"checkout,omitempty"`
	Ref              string    `json:"ref"`
	TipBehind        *int      `json:"tip_behind"`
	DaysSinceUpdated *int      `json:"days_since_updated"`
	Findings         []Finding `json:"findings"`
}

// Report is the whole roll-up. Its JSON shape is documented in
// docs/GO_BINARY.md § Handoff and Work-Order Operations.
type Report struct {
	Ref        string    `json:"ref"`
	MaxAgeDays int       `json:"max_age_days"`
	AsOf       string    `json:"as_of"`
	Handoffs   []Entry   `json:"handoffs"`
	Skipped    []string  `json:"skipped"`
	Findings   []Finding `json:"findings"`
}

// HasFindings reports whether any finding (not info) was recorded.
func (r *Report) HasFindings() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityFinding {
			return true
		}
	}
	return false
}

// RevCounter answers "how many commits is tip behind ref in this checkout?".
// resolved is false when tip is not a commit there. err is returned when the
// checkout or the ref itself cannot be read.
type RevCounter func(ctx context.Context, checkout, tip, ref string) (behind int, resolved bool, err error)

// Options configures Assess.
type Options struct {
	// Ref is what the header tip is measured against (default origin/main).
	Ref string
	// MaxAgeDays bounds the header's `updated` date (default 7).
	MaxAgeDays int
	// Checkouts maps a header's `repo` to a local checkout. A repo absent here
	// falls back to WorkspaceRoot/<repo>.
	Checkouts map[string]string
	// WorkspaceRoot is the directory holding one checkout per repo, named for
	// the repo. Empty disables the fallback.
	WorkspaceRoot string
	// Now is the clock; zero means time.Now().
	Now time.Time
	// Revs counts commits; nil uses git.
	Revs RevCounter
}

// Input is one file to assess. Explicit files report a missing header as a
// finding; files found by scanning a directory are skipped instead, because a
// handoff directory also holds READMEs and indexes.
type Input struct {
	Path     string
	Text     string
	Explicit bool
}

// Assess parses each input's header and measures it.
func Assess(ctx context.Context, inputs []Input, opts Options) *Report {
	if opts.Ref == "" {
		opts.Ref = "origin/main"
	}
	if opts.MaxAgeDays <= 0 {
		opts.MaxAgeDays = DefaultMaxAgeDays
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	revs := opts.Revs
	if revs == nil {
		revs = GitRevCounter
	}

	r := &Report{
		Ref:        opts.Ref,
		MaxAgeDays: opts.MaxAgeDays,
		AsOf:       now.Format("2006-01-02"),
		Handoffs:   []Entry{},
		Skipped:    []string{},
		Findings:   []Finding{},
	}
	for _, in := range inputs {
		e := Entry{Path: in.Path, Ref: opts.Ref, Findings: []Finding{}}
		h, err := ParseHeader(in.Text)
		switch {
		case errors.Is(err, ErrNoHeader):
			if !in.Explicit {
				r.Skipped = append(r.Skipped, in.Path)
				continue
			}
			e.add(CodeHeaderMissing, SeverityFinding, "", "no <!-- nightgauge:handoff --> block")
		case err != nil:
			e.add(CodeHeaderUnparseable, SeverityFinding, "", err.Error())
		default:
			e.Header = h
			assessHeader(ctx, &e, h, opts, now, revs)
		}
		r.Handoffs = append(r.Handoffs, e)
		r.Findings = append(r.Findings, e.Findings...)
	}
	return r
}

func (e *Entry) add(code, severity, repo, msg string) {
	e.Findings = append(e.Findings, Finding{
		Code: code, Severity: severity, Path: e.Path, Repo: repo, Message: msg,
	})
}

func assessHeader(ctx context.Context, e *Entry, h *Header, opts Options, now time.Time, revs RevCounter) {
	repo := h.Repo

	if h.Updated != "" {
		if d, err := time.ParseInLocation("2006-01-02", h.Updated, now.Location()); err == nil {
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			days := int(today.Sub(d).Hours() / 24)
			e.DaysSinceUpdated = &days
			if days > opts.MaxAgeDays {
				e.add(CodeStale, SeverityFinding, repo,
					fmt.Sprintf("header `updated` is %d days old (limit %d)", days, opts.MaxAgeDays))
			}
		} else {
			e.add(CodeHeaderUnparseable, SeverityFinding, repo,
				fmt.Sprintf("`updated` is not a YYYY-MM-DD date: %q", h.Updated))
		}
	}

	if h.Tip != "" {
		checkout := checkoutFor(repo, opts)
		e.Checkout = checkout
		switch {
		case checkout == "":
			e.add(CodeTipUnmeasured, SeverityInfo, repo,
				"no checkout for this repo; tip staleness not measured")
		default:
			behind, resolved, err := revs(ctx, checkout, h.Tip, opts.Ref)
			switch {
			case err != nil:
				e.add(CodeTipUnmeasured, SeverityInfo, repo,
					fmt.Sprintf("tip staleness not measured in %s: %v", checkout, err))
			case !resolved:
				e.add(CodeTipUnresolvable, SeverityFinding, repo,
					fmt.Sprintf("%s is not a commit in %s", h.Tip, checkout))
			default:
				e.TipBehind = &behind
				if behind > 0 {
					e.add(CodeStale, SeverityFinding, repo,
						fmt.Sprintf("header tip is %d commits behind %s", behind, opts.Ref))
				}
			}
		}
	}

	for _, msg := range h.malformed {
		e.add(CodeCrossRepoRowInvalid, SeverityFinding, repo, msg)
	}
	for _, key := range []string{"needs", "provides"} {
		list := h.Needs
		if key == "provides" {
			list = h.Provides
		}
		for _, row := range list {
			if scalar(row["issue"]) == "" {
				e.add(CodeNeedUnmaterialized, SeverityFinding, repo,
					fmt.Sprintf("%s row without an issue number: %s", key, describeRow(row)))
			}
		}
	}
}

func describeRow(row map[string]any) string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", k, scalar(row[k])))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func checkoutFor(repo string, opts Options) string {
	if p, ok := opts.Checkouts[repo]; ok {
		return p
	}
	if repo == "" || opts.WorkspaceRoot == "" {
		return ""
	}
	p := filepath.Join(opts.WorkspaceRoot, repo)
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		return ""
	}
	return p
}

// GitRevCounter measures with the git binary on PATH. It never fetches.
func GitRevCounter(ctx context.Context, checkout, tip, ref string) (int, bool, error) {
	if _, err := runGit(ctx, checkout, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return 0, false, fmt.Errorf("ref %s does not resolve", ref)
	}
	if _, err := runGit(ctx, checkout, "rev-parse", "--verify", "--quiet", tip+"^{commit}"); err != nil {
		return 0, false, nil
	}
	out, err := runGit(ctx, checkout, "rev-list", "--count", tip+".."+ref)
	if err != nil {
		return 0, false, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, false, fmt.Errorf("rev-list --count: %q", out)
	}
	return n, true, nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// CollectInputs expands paths into inputs. A directory contributes its *.md
// files (not recursively) as non-explicit inputs, in name order.
func CollectInputs(paths []string) ([]Input, error) {
	var out []Input
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			out = append(out, Input{Path: p, Text: string(b), Explicit: true})
			continue
		}
		matches, err := filepath.Glob(filepath.Join(p, "*.md"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				return nil, err
			}
			out = append(out, Input{Path: m, Text: string(b)})
		}
	}
	return out, nil
}
