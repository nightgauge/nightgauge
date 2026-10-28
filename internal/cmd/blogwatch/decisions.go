package blogwatch

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadLog reads the JSONL decision log. A missing file is an empty log.
func LoadLog(path string) ([]Decision, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Decision
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var d Decision
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, d)
	}
	return out, sc.Err()
}

// RecordedURLs is the set of URLs that already have a verdict.
func RecordedURLs(log []Decision) map[string]bool {
	m := make(map[string]bool, len(log))
	for _, d := range log {
		m[d.URL] = true
	}
	return m
}

// New returns the URLs in current that are not recorded, in input order.
func New(current []string, recorded map[string]bool) []string {
	out := []string{}
	for _, u := range current {
		if !recorded[u] {
			out = append(out, u)
		}
	}
	return out
}

// Validate checks a decision before it is appended.
func Validate(d Decision) error {
	if _, ok := Normalize(d.URL); !ok {
		return fmt.Errorf("url %q is not an absolute https URL", d.URL)
	}
	if _, err := ParseVerdict(string(d.Verdict)); err != nil {
		return err
	}
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("a reason is required for every verdict")
	}
	switch d.Verdict {
	case VerdictAlreadyCovered:
		if strings.TrimSpace(d.Ref) == "" {
			return fmt.Errorf("%q needs --ref naming the doc or code that covers it", d.Verdict)
		}
	case VerdictAlreadyFiled, VerdictAdopted:
		if d.Issue <= 0 {
			return fmt.Errorf("%q needs an issue number", d.Verdict)
		}
	}
	return nil
}

// Append validates d and appends it to the log at path, refusing a URL that
// already has a verdict.
func Append(path string, d Decision) error {
	n, ok := Normalize(d.URL)
	if ok {
		d.URL = n
	}
	if err := Validate(d); err != nil {
		return err
	}
	log, err := LoadLog(path)
	if err != nil {
		return err
	}
	if RecordedURLs(log)[d.URL] {
		return &DuplicateError{URL: d.URL}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(d)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// DuplicateError is returned for a URL that already has a verdict.
type DuplicateError struct{ URL string }

func (e *DuplicateError) Error() string { return "already recorded: " + e.URL }
