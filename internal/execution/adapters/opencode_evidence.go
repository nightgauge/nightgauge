package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// OpenCodeEvidenceDir is where a failed run's OpenCode evidence is kept
// (#2171), STATE/opencode/evidence beside the runs directory and never inside
// any repository. state is the machine-state root (layout.StateHome).
func OpenCodeEvidenceDir(state string) string {
	return filepath.Join(OpenCodeStateDir(state), "evidence")
}

// OpenCodeToolEvidenceFile is the run root's tool call evidence (#3305): one
// OpenCodeToolEvidence JSON line per tool call the run's stages streamed,
// kept with a failed run's other evidence.
const OpenCodeToolEvidenceFile = "tool-calls.jsonl"

// OpenCodeToolEvidenceMaxCalls bounds the tool calls one stage records. The
// calls past it are counted in a trailing {"dropped":N} line instead.
const OpenCodeToolEvidenceMaxCalls = 500

// PreserveOpenCodeRunEvidence copies a failed run's OpenCode session database
// (data/opencode/opencode.db*), log directory (data/opencode/log/) and tool
// call evidence (OpenCodeToolEvidenceFile, #3305) out of its per-run root into
// OpenCodeEvidenceDir/<id>, before the root is deleted (#2171). On failure
// they are the only record of what the model did.
//
// Nothing else from the root is kept: not home/, not config/, not auth.json
// or any other credential. Only regular files are copied and no symbolic link
// is followed. Directories are 0700 and files 0600. Evidence older than
// OpenCodeOrphanMaxAge is swept on every call, the run roots' own bound.
//
// Returns the evidence directory, or "" when the root held nothing to keep
// (including a root that does not exist).
func PreserveOpenCodeRunEvidence(state, id string, now time.Time) (string, error) {
	root, err := OpenCodeRunRoot(state, id)
	if err != nil {
		return "", err
	}
	// Sweep first, so a failure below still bounds the directory.
	_, sweepErr := SweepOpenCodeRunEvidence(state, OpenCodeOrphanMaxAge, now)

	// Each file is copied from root/<from> to <evidence>/<to>.
	type evidenceFile struct{ from, to string }
	var files []evidenceFile
	fi, err := os.Lstat(filepath.Join(root, OpenCodeToolEvidenceFile))
	switch {
	case err == nil && fi.Mode().IsRegular():
		files = append(files, evidenceFile{OpenCodeToolEvidenceFile, OpenCodeToolEvidenceFile})
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", errors.Join(fmt.Errorf("opencode run evidence: %w", err), sweepErr)
	}

	data := filepath.Join("data", "opencode")
	src := filepath.Join(root, data)
	entries, err := os.ReadDir(src)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", errors.Join(fmt.Errorf("opencode run evidence: %w", err), sweepErr)
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), "opencode.db") {
			files = append(files, evidenceFile{filepath.Join(data, e.Name()), e.Name()})
		}
	}
	logDir := filepath.Join(src, "log")
	if fi, lerr := os.Lstat(logDir); lerr == nil && fi.IsDir() {
		walkErr := filepath.WalkDir(logDir, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.Type().IsRegular() {
				rel, rerr := filepath.Rel(src, path)
				if rerr != nil {
					return rerr
				}
				files = append(files, evidenceFile{filepath.Join(data, rel), rel})
			}
			return nil
		})
		if walkErr != nil {
			return "", errors.Join(fmt.Errorf("opencode run evidence: %w", walkErr), sweepErr)
		}
	}
	if len(files) == 0 {
		return "", sweepErr
	}

	// STATE/opencode, evidence/ and <id>/ are 0700 and none may be a link.
	dst, err := ensureOpenCodeStateDirs(state, layout.StateOpenCode, "evidence", id)
	if err != nil {
		return "", errors.Join(fmt.Errorf("opencode run evidence: %w", err), sweepErr)
	}
	for _, f := range files {
		if err := copyEvidenceFile(filepath.Join(root, f.from), filepath.Join(dst, f.to)); err != nil {
			return "", errors.Join(err, sweepErr)
		}
	}
	return dst, sweepErr
}

// OpenCodeToolEvidence is what one 2.x tool_use event records (#3305): the
// facts of the call, never its transcript. Tool input, output, raw input,
// content and the error text are not read, so none of them can be recorded.
type OpenCodeToolEvidence struct {
	// Tool is opencode's own tool name (part.tool), such as "read".
	Tool string `json:"tool"`
	// CallID is the provider's call id (part.id).
	CallID string `json:"call_id,omitempty"`
	// SessionID is the session that made the call (part.sessionID, else the
	// event's sessionID).
	SessionID string `json:"session_id,omitempty"`
	// Status is part.state.status: "completed" or "error" on 2.2.7.
	Status string `json:"status"`
	// Errored is true when the call ended in error.
	Errored bool `json:"errored"`
	// ProviderCallExecuted and ProviderResultExecuted are
	// part.state.metadata.providerCall.executed and providerResult.executed:
	// whether the provider, not opencode, ran the call and returned its
	// result. Nil when the field is absent, as on 1.x.
	ProviderCallExecuted   *bool `json:"provider_call_executed,omitempty"`
	ProviderResultExecuted *bool `json:"provider_result_executed,omitempty"`
	// DurationMS is part.state.time end minus start. Nil when either is
	// absent or end is before start.
	DurationMS *int64 `json:"duration_ms,omitempty"`
	// SubagentSessionID is part.state.metadata.metadata.sessionID: the child
	// session a 2.x `subagent` call ran in.
	SubagentSessionID string `json:"subagent_session_id,omitempty"`
}

// openCodeEvidenceEvent declares only the fields OpenCodeToolEvidence
// records. The metadata is held raw and each field decoded on its own, so a
// tool whose metadata has another shape still records the rest.
type openCodeEvidenceEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      *struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
		Tool      string `json:"tool"`
		State     *struct {
			Status string `json:"status"`
			Time   *struct {
				Start *int64 `json:"start"`
				End   *int64 `json:"end"`
			} `json:"time"`
			Metadata json.RawMessage `json:"metadata"`
		} `json:"state"`
	} `json:"part"`
}

type openCodeEvidenceMetadata struct {
	ProviderCall   json.RawMessage `json:"providerCall"`
	ProviderResult json.RawMessage `json:"providerResult"`
	Metadata       json.RawMessage `json:"metadata"`
}

// openCodeExecuted reads {"executed": bool}; nil when it is absent or not a bool.
func openCodeExecuted(raw json.RawMessage) *bool {
	var v struct {
		Executed *bool `json:"executed"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v.Executed
}

// ReadOpenCodeToolEvidence reads one stream line's tool call evidence. It
// returns false for any line that is not a tool_use event with a part.
func ReadOpenCodeToolEvidence(line []byte) (OpenCodeToolEvidence, bool) {
	var ev openCodeEvidenceEvent
	if json.Unmarshal(line, &ev) != nil || ev.Type != "tool_use" || ev.Part == nil {
		return OpenCodeToolEvidence{}, false
	}
	rec := OpenCodeToolEvidence{Tool: ev.Part.Tool, CallID: ev.Part.ID, SessionID: ev.Part.SessionID}
	if rec.SessionID == "" {
		rec.SessionID = ev.SessionID
	}
	st := ev.Part.State
	if st == nil {
		return rec, true
	}
	rec.Status = st.Status
	rec.Errored = st.Status == "error"
	if st.Time != nil && st.Time.Start != nil && st.Time.End != nil && *st.Time.End >= *st.Time.Start {
		d := *st.Time.End - *st.Time.Start
		rec.DurationMS = &d
	}
	var md openCodeEvidenceMetadata
	if len(st.Metadata) > 0 && json.Unmarshal(st.Metadata, &md) == nil {
		rec.ProviderCallExecuted = openCodeExecuted(md.ProviderCall)
		rec.ProviderResultExecuted = openCodeExecuted(md.ProviderResult)
		var inner struct {
			SessionID string `json:"sessionID"`
		}
		if len(md.Metadata) > 0 && json.Unmarshal(md.Metadata, &inner) == nil {
			rec.SubagentSessionID = inner.SessionID
		}
	}
	return rec, true
}

// AppendOpenCodeToolEvidence appends calls, then a {"dropped":N} line when
// dropped > 0, to the run root's OpenCodeToolEvidenceFile. Each stage of a
// run appends its own. The file is 0600, and a path that exists but is not a
// regular file (a symbolic link, a FIFO) is refused, at its path and again on
// the opened descriptor. Nothing to record writes nothing.
func AppendOpenCodeToolEvidence(root string, calls []OpenCodeToolEvidence, dropped int) error {
	if len(calls) == 0 && dropped <= 0 {
		return nil
	}
	if fi, err := os.Lstat(root); err != nil {
		return fmt.Errorf("opencode tool evidence: %w", err)
	} else if !fi.IsDir() {
		return fmt.Errorf("opencode tool evidence: run root %s is not a directory", root)
	}
	path := filepath.Join(root, OpenCodeToolEvidenceFile)
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("opencode tool evidence: %s is not a regular file", path)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("opencode tool evidence: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, c := range calls {
		if err := enc.Encode(c); err != nil {
			return fmt.Errorf("opencode tool evidence: %w", err)
		}
	}
	if dropped > 0 {
		if err := enc.Encode(struct {
			Dropped int `json:"dropped"`
		}{dropped}); err != nil {
			return fmt.Errorf("opencode tool evidence: %w", err)
		}
	}
	f, err := os.OpenFile(path, openCodeToolEvidenceOpenFlags, 0o600)
	if err != nil {
		return fmt.Errorf("opencode tool evidence: %w", err)
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return fmt.Errorf("opencode tool evidence: %s is not a regular file", path)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return fmt.Errorf("opencode tool evidence: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("opencode tool evidence: %w", err)
	}
	return nil
}

func copyEvidenceFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return fmt.Errorf("opencode run evidence: %w", err)
	}
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("opencode run evidence: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("opencode run evidence: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("opencode run evidence: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("opencode run evidence: %w", err)
	}
	return nil
}

// SweepOpenCodeRunEvidence deletes every run's evidence directory older than
// maxAge and returns the ids it deleted. Only a real directory named by a run
// identity is touched.
func SweepOpenCodeRunEvidence(state string, maxAge time.Duration, now time.Time) ([]string, error) {
	dir := OpenCodeEvidenceDir(state)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opencode run evidence sweep: %w", err)
	}
	var removed []string
	var errs []error
	for _, e := range entries {
		id := e.Name()
		if !runstate.IsIdentity(id) {
			continue
		}
		path := filepath.Join(dir, id)
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() || now.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("opencode run evidence sweep: %w", err))
			continue
		}
		removed = append(removed, id)
	}
	return removed, errors.Join(errs...)
}
