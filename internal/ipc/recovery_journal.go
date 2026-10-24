package ipc

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A recovery journal is append-only, hash-chained and HMAC'd with a per-user
// key that lives under the user's config directory, never in the workspace: the
// workspace is exactly what an attacker or a confused process can write, so
// bytes from it must not be able to authorise a replay.

const (
	journalBegin  = "begin"
	journalDone   = "done"
	journalFailed = "failed"
	journalClosed = "closed"
)

type journalEntry struct {
	Seq      int    `json:"seq"`
	Step     string `json:"step"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Action   string `json:"action,omitempty"`
	Snapshot string `json:"snapshot,omitempty"`
	Time     string `json:"time"`
	PrevHash string `json:"prevHash"`
	Hash     string `json:"hash"`
	MAC      string `json:"mac"`
}

func (e journalEntry) digest() string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		e.Seq, e.Step, e.Status, e.Detail, e.Action, e.Snapshot, e.Time, e.PrevHash)
	return hex.EncodeToString(h.Sum(nil))
}

// recoveryJournal is the verified, in-memory view of one run's journal.
type recoveryJournal struct {
	path    string
	key     []byte
	entries []journalEntry
}

// loadRecoveryKey reads the per-user HMAC key, creating it (0600) on first use.
func loadRecoveryKey(dir string) ([]byte, error) {
	if dir == "" {
		return nil, fmt.Errorf("no user state directory for the recovery journal key")
	}
	keyPath := filepath.Join(dir, "recovery.key")
	if b, err := os.ReadFile(keyPath); err == nil {
		if len(b) < 32 {
			return nil, fmt.Errorf("%s: recovery key is truncated", codeRecoveryTampered)
		}
		return b, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// Lost a creation race: use the winner's key.
		if b, rerr := os.ReadFile(keyPath); rerr == nil && len(b) >= 32 {
			return b, nil
		}
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(key); err != nil {
		return nil, err
	}
	return key, nil
}

func (j *recoveryJournal) mac(hash string) string {
	m := hmac.New(sha256.New, j.key)
	m.Write([]byte(hash))
	return hex.EncodeToString(m.Sum(nil))
}

// openRecoveryJournal loads and VERIFIES the chain. Any broken link, bad
// sequence number or bad MAC refuses the whole journal.
func openRecoveryJournal(dir, runID string) (*recoveryJournal, error) {
	key, err := loadRecoveryKey(dir)
	if err != nil {
		return nil, err
	}
	jdir := filepath.Join(dir, "recovery-journal")
	if err := os.MkdirAll(jdir, 0o700); err != nil {
		return nil, err
	}
	j := &recoveryJournal{path: filepath.Join(jdir, runID+".jsonl"), key: key}
	f, err := os.Open(j.path)
	if os.IsNotExist(err) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	prev := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e journalEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s: unreadable entry: %v", codeRecoveryTampered, err)
		}
		switch {
		case e.Seq != len(j.entries)+1:
			return nil, fmt.Errorf("%s: sequence gap at %d", codeRecoveryTampered, e.Seq)
		case e.PrevHash != prev:
			return nil, fmt.Errorf("%s: broken chain at %d", codeRecoveryTampered, e.Seq)
		case e.Hash != e.digest():
			return nil, fmt.Errorf("%s: altered entry %d", codeRecoveryTampered, e.Seq)
		case !hmac.Equal([]byte(e.MAC), []byte(j.mac(e.Hash))):
			return nil, fmt.Errorf("%s: bad MAC at %d", codeRecoveryTampered, e.Seq)
		}
		prev = e.Hash
		j.entries = append(j.entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *recoveryJournal) append(e journalEntry) error {
	prev := ""
	if n := len(j.entries); n > 0 {
		prev = j.entries[n-1].Hash
	}
	e.Seq = len(j.entries) + 1
	e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	e.PrevHash = prev
	e.Hash = e.digest()
	e.MAC = j.mac(e.Hash)
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	j.entries = append(j.entries, e)
	return nil
}

// open reports the open transaction: its begin entry, if the last begin has no
// later "closed" entry.
func (j *recoveryJournal) open() (begin journalEntry, ok bool) {
	for _, e := range j.entries {
		switch e.Status {
		case journalBegin:
			begin, ok = e, true
		case journalClosed:
			ok = false
		}
	}
	return begin, ok
}

// stepDone reports whether step completed within the open transaction.
func (j *recoveryJournal) stepDone(step string) bool {
	done := false
	for _, e := range j.entries {
		switch {
		case e.Status == journalBegin:
			done = false
		case e.Step == step && e.Status == journalDone:
			done = true
		}
	}
	return done
}
