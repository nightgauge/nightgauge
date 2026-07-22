package careful

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnableActiveDisable(t *testing.T) {
	root := t.TempDir()
	if Active(root) {
		t.Fatal("careful should be off initially")
	}
	if err := Enable(root, 0, "touching prod"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !Active(root) {
		t.Fatal("careful should be active after Enable")
	}
	lk, active := Read(root)
	if !active || lk == nil || lk.Note != "touching prod" || lk.TTLMinutes != DefaultTTLMinutes {
		t.Fatalf("unexpected lock: %+v active=%v", lk, active)
	}
	if err := Disable(root); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if Active(root) {
		t.Fatal("careful should be off after Disable")
	}
}

func TestDisableMissingIsNoError(t *testing.T) {
	if err := Disable(t.TempDir()); err != nil {
		t.Fatalf("disabling absent lock should not error: %v", err)
	}
}

func TestExpiredLockIsInactive(t *testing.T) {
	root := t.TempDir()
	// Write a lock that started 2h ago with a 60-min TTL → expired.
	stale := Lock{Since: time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339), TTLMinutes: 60}
	data, _ := json.MarshalIndent(stale, "", "  ")
	if err := os.MkdirAll(filepath.Dir(LockPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(LockPath(root), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if Active(root) {
		t.Fatal("expired lock must be inactive")
	}
}

// seg builds a single parsed Command (one pipeline segment) for the tests.
func seg(raw string, argv ...string) Command { return Command{Raw: raw, Argv: argv} }

// pipe builds a one-pipeline input from one or more commands.
func pipe(cmds ...Command) [][]Command { return [][]Command{cmds} }

func TestDestructiveProdReason(t *testing.T) {
	blocked := []struct {
		name  string
		pipes [][]Command
	}{
		{"compose down -v", pipe(seg("docker compose down -v", "docker", "compose", "down", "-v"))},
		{"compose -f down --volumes", pipe(seg("docker compose -f prod.yml down --volumes", "docker", "compose", "-f", "prod.yml", "down", "--volumes"))},
		{"docker-compose down -v", pipe(seg("docker-compose down -v", "docker-compose", "down", "-v"))},
		{"volume rm", pipe(seg("docker volume rm postgres_data", "docker", "volume", "rm", "postgres_data"))},
		{"volume prune", pipe(seg("docker volume prune -f", "docker", "volume", "prune", "-f"))},
		{"kubectl delete", pipe(seg("kubectl delete pod api-0 -n prod", "kubectl", "delete", "pod", "api-0", "-n", "prod"))},
		{"psql -c DROP", pipe(seg("psql -c 'DROP TABLE subscriptions'", "psql", "-c", "DROP TABLE subscriptions"))},
		{"echo TRUNCATE | psql", pipe(seg("echo 'TRUNCATE TABLE events'", "echo", "TRUNCATE TABLE events"), seg("psql", "psql"))},
		{"mysql -e drop database", pipe(seg("mysql -e 'drop database billing'", "mysql", "-e", "drop database billing"))},
	}
	for _, tc := range blocked {
		if DestructiveProdReason(tc.pipes) == "" {
			t.Errorf("expected %q to be blocked", tc.name)
		}
	}

	allowed := []struct {
		name  string
		pipes [][]Command
	}{
		{"compose down (no -v)", pipe(seg("docker compose down", "docker", "compose", "down"))},
		{"compose up", pipe(seg("docker compose up -d", "docker", "compose", "up", "-d"))},
		{"kubectl get", pipe(seg("kubectl get pods", "kubectl", "get", "pods"))},
		{"git status", pipe(seg("git status", "git", "status"))},
		{"npm build", pipe(seg("npm run build", "npm", "run", "build"))},
		{"SELECT read-only", pipe(seg("psql -c 'SELECT * FROM subscriptions'", "psql", "-c", "SELECT * FROM subscriptions"))},
		// #4069: destructive words inside quoted prose must NOT be flagged — the
		// program is echo/gh/git, not a SQL client / docker / kubectl op.
		{"echo prose with DROP TABLE", pipe(seg("echo we should not DROP TABLE users", "echo", "we should not DROP TABLE users"))},
		{"non-destructive migration comment", pipe(seg("gh issue comment 5 --body non-destructive SQLite migration", "gh", "issue", "comment", "5", "--body", "non-destructive SQLite migration"))},
		{"commit message mentioning docker compose down -v", pipe(seg("git commit -m avoid docker compose down -v in prod", "git", "commit", "-m", "avoid docker compose down -v in prod"))},
		{"echo kubectl delete advice", pipe(seg("echo never run kubectl delete on prod", "echo", "never run kubectl delete on prod"))},
		// #4069 finding: a DROP echoed in a SEPARATE command (not piped into the
		// SQL client) must not be flagged — two pipelines, no cross-contamination.
		{"DROP echo then separate psql select", [][]Command{
			{seg("echo DROP TABLE plan", "echo", "DROP TABLE plan")},
			{seg("psql -e 'select count(*)'", "psql", "-e", "select count(*)")},
		}},
	}
	for _, tc := range allowed {
		if r := DestructiveProdReason(tc.pipes); r != "" {
			t.Errorf("expected %q to be allowed, got reason: %s", tc.name, r)
		}
	}
}
