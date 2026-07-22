package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAppendJSONL_CreatesParentDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "nested", "deep", "events.jsonl")

	if err := AppendJSONL(target, map[string]string{"a": "1"}); err != nil {
		t.Fatalf("AppendJSONL: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("expected non-empty file")
	}
}

func TestAppendJSONL_AppendsInsteadOfTruncating(t *testing.T) {
	target := filepath.Join(t.TempDir(), "events.jsonl")

	if err := AppendJSONL(target, map[string]any{"n": 1}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := AppendJSONL(target, map[string]any{"n": 2}); err != nil {
		t.Fatalf("second append: %v", err)
	}

	f, err := os.Open(target)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if got, want := len(lines), 2; got != want {
		t.Fatalf("expected %d lines, got %d", want, got)
	}

	var first, second map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal line 0: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("unmarshal line 1: %v", err)
	}
	if first["n"].(float64) != 1 || second["n"].(float64) != 2 {
		t.Fatalf("expected ordered records, got %+v / %+v", first, second)
	}
}

func TestAppendJSONL_ConcurrentEmitsAreNotInterleaved(t *testing.T) {
	target := filepath.Join(t.TempDir(), "events.jsonl")
	const writers = 16
	const perWriter = 25

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				if err := AppendJSONL(target, map[string]int{"w": id, "j": j}); err != nil {
					t.Errorf("AppendJSONL(%d,%d): %v", id, j, err)
				}
			}
		}(i)
	}
	wg.Wait()

	f, err := os.Open(target)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("line %d not valid JSON: %v (%q)", count, err, scanner.Text())
		}
		count++
	}
	if got, want := count, writers*perWriter; got != want {
		t.Fatalf("expected %d lines, got %d", want, got)
	}
}

func TestAppendJSONL_EmptyPathFails(t *testing.T) {
	if err := AppendJSONL("", map[string]string{"a": "b"}); err == nil {
		t.Fatalf("expected error for empty path")
	}
}

func TestAppendJSONL_UnmarshalableRecordFails(t *testing.T) {
	target := filepath.Join(t.TempDir(), "events.jsonl")
	// channels cannot be JSON-marshaled
	bad := make(chan int)
	if err := AppendJSONL(target, bad); err == nil {
		t.Fatalf("expected marshal error for channel")
	}
}
