// Package careful implements the opt-in destructive-operation guardrail behind
// the /nightgauge-careful skill. Because skill-frontmatter session hooks
// are not wired into this codebase, "careful mode" is a sentinel lock file
// (.nightgauge/careful.lock) that the always-registered PreToolUse(Bash)
// careful-gate hook consults: when the lock is present (and unexpired) the gate
// blocks the documented production-destructive commands. A TTL bounds a forgotten
// lock so it cannot block forever.
package careful

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const lockRelPath = ".nightgauge/careful.lock"

// DefaultTTLMinutes bounds a forgotten lock (12h) so careful mode can't outlive
// a working session indefinitely.
const DefaultTTLMinutes = 720

// Lock is the on-disk careful-mode sentinel.
type Lock struct {
	Since      string `json:"since"`                 // RFC3339 UTC
	TTLMinutes int    `json:"ttl_minutes,omitempty"` // 0 → DefaultTTLMinutes
	Note       string `json:"note,omitempty"`
}

// LockPath returns the absolute lock path for the given project root.
func LockPath(root string) string {
	return filepath.Join(root, lockRelPath)
}

// Enable writes (or refreshes) the careful lock.
func Enable(root string, ttlMinutes int, note string) error {
	if ttlMinutes <= 0 {
		ttlMinutes = DefaultTTLMinutes
	}
	path := LockPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create careful dir: %w", err)
	}
	data, err := json.MarshalIndent(Lock{
		Since:      time.Now().UTC().Format(time.RFC3339),
		TTLMinutes: ttlMinutes,
		Note:       note,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lock: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write careful lock: %w", err)
	}
	return nil
}

// Disable removes the careful lock. A missing lock is not an error.
func Disable(root string) error {
	err := os.Remove(LockPath(root))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove careful lock: %w", err)
	}
	return nil
}

// Read returns the lock and whether careful mode is currently active (present and
// unexpired). A missing/expired/unparseable lock is inactive.
func Read(root string) (*Lock, bool) {
	data, err := os.ReadFile(LockPath(root))
	if err != nil {
		return nil, false
	}
	var lk Lock
	if err := json.Unmarshal(data, &lk); err != nil {
		return nil, false
	}
	return &lk, !lk.expired()
}

// Active reports whether careful mode is on for the given root.
func Active(root string) bool {
	_, active := Read(root)
	return active
}

func (l Lock) expired() bool {
	since, err := time.Parse(time.RFC3339, l.Since)
	if err != nil {
		return true // unparseable timestamp → treat as expired (inactive)
	}
	ttl := l.TTLMinutes
	if ttl <= 0 {
		ttl = DefaultTTLMinutes
	}
	return time.Now().UTC().After(since.Add(time.Duration(ttl) * time.Minute))
}

// dropTruncateRe matches destructive SQL DDL/DML regardless of spacing/case.
var dropTruncateRe = regexp.MustCompile(`(?i)\b(drop\s+(table|database|schema)|truncate\s+table|truncate\b)\b`)

// sqlClients are programs whose arguments may carry executable SQL. The SQL
// DROP/TRUNCATE check only fires when the invoked program is one of these, so a
// `DROP TABLE` mentioned in an echo, commit message, or issue comment is never
// flagged (it isn't being executed against a database).
var sqlClients = map[string]bool{
	"psql": true, "mysql": true, "mariadb": true, "mysqladmin": true,
	"sqlite3": true, "cockroach": true, "pgcli": true, "mycli": true,
	"sqlcmd": true, "usql": true, "clickhouse-client": true,
}

// Command is a single parsed shell command: Argv is the program and its
// arguments (leading environment assignments already stripped), and Raw is the
// original segment text (used for the SQL-client content check). The caller
// (the hooks careful gate) tokenizes with the shared cmdparse tokenizer and
// hands the result here, so the careful package needs no parser of its own and
// matches on the real program/verb rather than substrings of arbitrary text.
type Command struct {
	Argv []string
	Raw  string
}

// DestructiveProdReason returns a non-empty reason (with the safe alternative) if
// any command is one of the production-destructive operations careful mode
// blocks, or "" if all are allowed. Input is grouped into pipelines (commands
// connected by `|`). It inspects the actual program and verb tokens — never
// substrings of quoted prose — so a destructive word inside an echo, commit
// message, or PR body is not flagged (issue #4069). The SQL check is scoped to a
// single pipeline that actually runs a SQL client, so `echo 'DROP TABLE' | psql`
// is blocked while `echo "DROP TABLE plan"; psql -e 'select 1'` (separate
// commands, no DROP reaching the client) is not. These complement — they do not
// duplicate — the always-on workflow gate (main-push, force-push, reset --hard,
// clean -f, secret read/write).
func DestructiveProdReason(pipelines [][]Command) string {
	for _, pipe := range pipelines {
		sqlClientPresent := false
		var pipeText strings.Builder
		for _, c := range pipe {
			pipeText.WriteString(c.Raw)
			pipeText.WriteByte('\n')
			if len(c.Argv) > 0 && sqlClients[progBase(c.Argv[0])] {
				sqlClientPresent = true
			}
			if reason := dockerKubectlReason(c); reason != "" {
				return reason
			}
		}
		// Destructive SQL — only when an actual SQL client runs in THIS pipeline.
		if sqlClientPresent && dropTruncateRe.MatchString(pipeText.String()) {
			return "destructive SQL (DROP/TRUNCATE) can irreversibly delete data. Take a backup and use a reversible migration."
		}
	}
	return ""
}

// dockerKubectlReason flags a single command if it is a destructive
// docker/kubectl operation, matching on the real program and verb tokens.
func dockerKubectlReason(c Command) string {
	if len(c.Argv) == 0 {
		return ""
	}
	prog := progBase(c.Argv[0])
	args := c.Argv[1:]
	has := func(tok string) bool {
		for _, f := range args {
			if f == tok {
				return true
			}
		}
		return false
	}

	switch prog {
	// docker compose down -v / docker-compose down --volumes → destroys volumes.
	case "docker-compose":
		if has("down") && (has("-v") || has("--volumes")) {
			return composeReason
		}
	case "docker":
		if len(args) >= 1 && args[0] == "compose" && has("down") && (has("-v") || has("--volumes")) {
			return composeReason
		}
		if len(args) >= 2 && args[0] == "volume" && (args[1] == "rm" || args[1] == "prune") {
			return "removing Docker volumes can destroy persistent data (e.g. `postgres_data`). Confirm the volume is disposable first."
		}
	case "kubectl":
		if has("delete") {
			return "`kubectl delete` can remove live cluster resources. Verify the context/namespace and prefer declarative `kubectl apply`."
		}
	}
	return ""
}

const composeReason = "`docker compose down -v` destroys the `postgres_data` volume on a production stack. Fix credential mismatches with `ALTER USER`, never a volume wipe."

// progBase returns the final path component of a program word so `/usr/bin/psql`
// is recognised as `psql`.
func progBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
