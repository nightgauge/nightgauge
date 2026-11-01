// Package gateledger records every full local gate a clone runs (#2784).
//
// One append-only file, gate-runs.jsonl in the clone's pipeline state directory,
// answers two questions from the same data: "has this exact tree already
// passed?" and "how many gates did this stage run, and for how long?". A killed
// gate leaves a start with no finish, which is the case retros most need, so
// starts are recorded as well as finishes.
//
// Reuse fails closed: anything the ledger cannot prove means run the gate. CI
// stays the authority.
package gateledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nightgauge/nightgauge/internal/history"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// FileName is the ledger's name in the pipeline state directory.
const FileName = "gate-runs.jsonl"

// Gate kinds. A local-gate pass runs the repository's complete gate, so it
// satisfies a parity query too. A parity pass runs only the commands discovered
// from CI workflows, which omit the repository's full test suite, so it never
// satisfies a local-gate query.
const (
	KindLocalGate = "local-gate"
	KindParity    = "parity"
)

// Event names.
const (
	EventStart  = "start"
	EventFinish = "finish"
	EventReused = "reused"
)

// Verdicts of a finish event. Infra means a check could not run, so the run
// asserted nothing about the tree, and the answer is to run the gate again.
const (
	VerdictPass  = "pass"
	VerdictFail  = "fail"
	VerdictInfra = "infra"
)

// Event is one line of the ledger.
type Event struct {
	RunID string `json:"run_id"`
	Event string `json:"event"`
	TS    string `json:"ts"`
	Kind  string `json:"kind"`
	// Tree is the working tree's fingerprint when the gate started.
	Tree    string `json:"tree,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	// Full is true when every step ran: no change scoping and no skipped step.
	Full bool `json:"full"`
	// TreeMoved is true when the tree at the finish differed from Tree, or could
	// not be fingerprinted. Such a pass proves nothing about any one tree.
	TreeMoved bool   `json:"tree_moved,omitempty"`
	Seconds   int    `json:"seconds,omitempty"`
	Issue     int    `json:"issue,omitempty"`
	Stage     string `json:"stage,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

// Path returns the ledger path for the clone holding root.
func Path(root string) (string, error) {
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		return "", fmt.Errorf("gateledger: %w", err)
	}
	return filepath.Join(dir, FileName), nil
}

// Append adds one event, stamping TS when it is empty.
func Append(root string, ev Event) error {
	path, err := Path(root)
	if err != nil {
		return err
	}
	if ev.TS == "" {
		ev.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return history.AppendJSONL(path, ev)
}

// entry is one ledger line. A line that does not parse keeps its place, because
// Latest must not read past an unknown verdict.
type entry struct {
	ev Event
	ok bool
}

func read(root string) ([]entry, error) {
	path, err := Path(root)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("gateledger: %w", err)
	}
	defer f.Close()
	var out []entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var ev Event
		err := json.Unmarshal(scanner.Bytes(), &ev)
		out = append(out, entry{ev: ev, ok: err == nil})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("gateledger: read %s: %w", path, err)
	}
	return out, nil
}

// Answer is what Latest found for a tree.
type Answer struct {
	Reusable bool
	// Reason says why, either way, for the line the caller prints.
	Reason string
	// Pass is the finish that proved the tree, when Reusable.
	Pass Event
}

// Latest answers whether tree already passed a gate that satisfies kind. It
// walks newest-first: a failure or an infrastructure finish on the tree stops
// the walk (a later verdict supersedes an earlier one, in both directions, #2746),
// a pass that proves nothing about the query is passed over, and an unreadable
// line stops it, since that line could be the failure.
func Latest(root, tree, kind string) (Answer, error) {
	if tree == "" {
		return Answer{Reason: "the tree could not be fingerprinted"}, nil
	}
	entries, err := read(root)
	if err != nil {
		return Answer{Reason: "the gate ledger could not be read"}, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if !e.ok {
			return Answer{Reason: "the gate ledger has an unreadable line"}, nil
		}
		if e.ev.Event != EventFinish || e.ev.Tree != tree {
			continue
		}
		if e.ev.Verdict != VerdictPass {
			return Answer{Reason: fmt.Sprintf("the latest %s gate on tree %s ended %s",
				e.ev.Kind, tree, e.ev.Verdict)}, nil
		}
		if e.ev.Full && !e.ev.TreeMoved && satisfies(e.ev.Kind, kind) {
			return Answer{
				Reusable: true,
				Pass:     e.ev,
				Reason:   fmt.Sprintf("%s passed tree %s", e.ev.Kind, tree),
			}, nil
		}
	}
	return Answer{Reason: fmt.Sprintf("no full %s pass is recorded for tree %s", kind, tree)}, nil
}

func satisfies(passed, wanted string) bool {
	return passed == KindLocalGate || passed == wanted
}

// Usage is one stage's gate spend.
type Usage struct {
	// Runs counts full gates that finished in the window.
	Runs int
	// Seconds is their total duration.
	Seconds int
	// Reused counts gates skipped because the tree had already passed.
	Reused int
	// Unfinished counts full gates that started in the window with no finish
	// anywhere in the ledger: killed, so their duration is unknown.
	Unfinished int
}

// UsageIn totals the full gates of an issue's stage whose events fall in
// [from, to]. An event with no stage (run outside a stage's environment)
// counts toward whichever stage of the issue was running then.
func UsageIn(root string, issue int, stage string, from, to time.Time) (Usage, error) {
	entries, err := read(root)
	if err != nil {
		return Usage{}, err
	}
	finished := map[string]bool{}
	for _, e := range entries {
		if e.ok && e.ev.Event == EventFinish {
			finished[e.ev.RunID] = true
		}
	}
	var u Usage
	for _, e := range entries {
		if !e.ok || e.ev.Issue != issue || (e.ev.Stage != "" && e.ev.Stage != stage) {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, e.ev.TS)
		if err != nil || ts.Before(from) || ts.After(to) {
			continue
		}
		switch e.ev.Event {
		case EventFinish:
			if e.ev.Full {
				u.Runs++
				u.Seconds += e.ev.Seconds
			}
		case EventReused:
			u.Reused++
		case EventStart:
			if e.ev.Full && !finished[e.ev.RunID] {
				u.Unfinished++
			}
		}
	}
	return u, nil
}
