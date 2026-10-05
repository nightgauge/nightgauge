package contractrollout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// ErrJobConflict is a workflow that already has a job with the contract's id
// and a different body. The rollout refuses to overwrite it.
var ErrJobConflict = errors.New("the workflow already has a job with this id and a different body")

// CIJobResult is what inserting the job did.
type CIJobResult struct {
	Workflow string `json:"workflow"`
	ID       string `json:"id"`
	// Action is "created" (a new workflow file), "inserted" (a job added to
	// an existing workflow) or "present" (the same job was already there).
	Action string `json:"action"`
}

// parseJob decodes a job body and refuses one that is not a mapping.
func parseJob(src string) (map[string]any, error) {
	var job map[string]any
	if err := yaml.Unmarshal([]byte(src), &job); err != nil {
		return nil, fmt.Errorf("job: %w", err)
	}
	if len(job) == 0 {
		return nil, fmt.Errorf("job: empty, want a mapping with runs-on and steps")
	}
	return job, nil
}

// InsertCIJob makes j's job part of root's workflow file. A missing file is
// created with the job alone. An existing one gets the job spliced in as the
// last entry under `jobs:`, every other byte of the file unchanged, so its
// comments and layout survive; the result is re-parsed and must hold the
// job exactly. A job already there with the same body is left alone; one
// with a different body is ErrJobConflict. With write false nothing is
// written and Action says what writing would do.
func InsertCIJob(root string, j CIJob, write bool) (CIJobResult, error) {
	res := CIJobResult{Workflow: j.Workflow, ID: j.ID}
	if err := checkRelPath(j.Workflow); err != nil {
		return res, err
	}
	if err := refuseSymlinkPath(root, j.Workflow); err != nil {
		return res, err
	}
	want, err := parseJob(j.Job)
	if err != nil {
		return res, err
	}
	path := filepath.Join(root, filepath.FromSlash(j.Workflow))
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		res.Action = "created"
		out := newWorkflow(j)
		if err := verifyJob(out, j.ID, want); err != nil {
			return res, err
		}
		if write {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return res, err
			}
			if err := writeFileAtomic(path, out, 0o644); err != nil {
				return res, err
			}
		}
		return res, nil
	}
	if err != nil {
		return res, err
	}

	out, present, err := spliceJob(src, j.ID, j.Job, want)
	if err != nil {
		return res, fmt.Errorf("%s: %w", j.Workflow, err)
	}
	if present {
		res.Action = "present"
		return res, nil
	}
	res.Action = "inserted"
	if write {
		info, err := os.Stat(path)
		if err != nil {
			return res, err
		}
		if err := writeFileAtomic(path, out, info.Mode().Perm()); err != nil {
			return res, err
		}
	}
	return res, nil
}

// newWorkflow renders a workflow file holding j alone.
func newWorkflow(j CIJob) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n\non:\n%s\njobs:\n  %s:\n%s", yamlString(j.WorkflowName),
		indentBlock(j.On, "  "), j.ID, indentBlock(j.Job, "    "))
	return []byte(b.String())
}

// spliceJob inserts the job into src's `jobs:` mapping as text. present is
// true when an equal job is already there.
func spliceJob(src []byte, id, body string, want map[string]any) (out []byte, present bool, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, false, fmt.Errorf("parse: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("not a workflow mapping")
	}
	top := doc.Content[0]
	jobsIdx := -1
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "jobs" {
			jobsIdx = i
		}
	}
	if jobsIdx < 0 {
		return nil, false, fmt.Errorf("no jobs: mapping")
	}
	jobs := top.Content[jobsIdx+1]
	if jobs.Kind != yaml.MappingNode || len(jobs.Content) == 0 {
		return nil, false, fmt.Errorf("jobs: is not a non-empty block mapping")
	}
	if jobs.Style&yaml.FlowStyle != 0 {
		return nil, false, fmt.Errorf("jobs: is a flow mapping; edit it by hand")
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		if jobs.Content[i].Value != id {
			continue
		}
		var have map[string]any
		if err := jobs.Content[i+1].Decode(&have); err != nil {
			return nil, false, err
		}
		if reflect.DeepEqual(normalize(have), normalize(want)) {
			return src, true, nil
		}
		return nil, false, fmt.Errorf("job %q: %w", id, ErrJobConflict)
	}

	lines := strings.SplitAfter(string(src), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// The jobs mapping ends before the next top-level key, or at the end of
	// the file; trailing blank lines and column-0 comments belong to what
	// follows, so the job goes after the last line that is neither.
	end := len(lines) // exclusive, 0-indexed
	if jobsIdx+2 < len(top.Content) {
		end = top.Content[jobsIdx+2].Line - 1
	}
	insertAt := end
	for insertAt > 0 {
		l := strings.TrimRight(lines[insertAt-1], "\r\n")
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#") {
			insertAt--
			continue
		}
		break
	}
	keyIndent := strings.Repeat(" ", jobs.Content[0].Column-1)
	step := jobs.Content[0].Column - top.Content[jobsIdx].Column
	if step <= 0 {
		step = 2
	}
	block := "\n" + keyIndent + id + ":\n" + indentBlock(body, keyIndent+strings.Repeat(" ", step))
	if insertAt > 0 && !strings.HasSuffix(lines[insertAt-1], "\n") {
		lines[insertAt-1] += "\n"
	}
	merged := strings.Join(lines[:insertAt], "") + block + strings.Join(lines[insertAt:], "")
	if err := verifyJob([]byte(merged), id, want); err != nil {
		return nil, false, err
	}
	// Every other top-level key must be unchanged.
	if err := sameExcept([]byte(merged), src, id); err != nil {
		return nil, false, err
	}
	return []byte(merged), false, nil
}

// verifyJob re-parses a workflow and requires jobs.<id> to equal want.
func verifyJob(src []byte, id string, want map[string]any) error {
	var wf struct {
		Jobs map[string]map[string]any `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(src, &wf); err != nil {
		return fmt.Errorf("the edited workflow does not parse: %w", err)
	}
	if !reflect.DeepEqual(normalize(wf.Jobs[id]), normalize(want)) {
		return fmt.Errorf("the edited workflow does not hold job %q as given", id)
	}
	return nil
}

// sameExcept requires a and b to decode equal once job id is removed from a.
func sameExcept(a, b []byte, id string) error {
	var da, db map[string]any
	if err := yaml.Unmarshal(a, &da); err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, &db); err != nil {
		return err
	}
	if jobs, ok := da["jobs"].(map[string]any); ok {
		delete(jobs, id)
	}
	if !reflect.DeepEqual(normalize(da), normalize(db)) {
		return fmt.Errorf("inserting the job changed another part of the workflow")
	}
	return nil
}

// normalize makes decoded YAML comparable: nil and empty collections agree.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			return nil
		}
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normalize(x)
		}
		return out
	case []any:
		if len(t) == 0 {
			return nil
		}
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalize(x)
		}
		return out
	default:
		return v
	}
}

// indentBlock strips src's common leading indentation and prefixes every
// non-blank line with prefix. The result ends in a newline.
func indentBlock(src, prefix string) string {
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	common := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if common < 0 || n < common {
			common = n
		}
	}
	if common < 0 {
		common = 0
	}
	var b strings.Builder
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(prefix + l[common:] + "\n")
	}
	return b.String()
}

// yamlString renders s as a YAML scalar.
func yamlString(s string) string {
	out, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return strings.TrimSpace(string(out))
}
