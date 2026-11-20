package attention

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// migratedSources records the legacy store directories this process has
// already emptied into a workspace store, so New pays the migration's
// directory reads once per checkout per process rather than on every call.
var migratedSources sync.Map

// migrateLockWait bounds how long the migration waits for the workspace store's
// lock. It is the producer wait; tests shorten it to exercise the busy path.
var migrateLockWait = defaultProducerLockWait

// migrateCheckoutStore moves the cards an older build kept in the checkout's
// CHECKOUT/attention into the workspace store dst (#3453). Before the move each
// checkout had its own store and the daemon synced only its own checkout's, so
// a card raised in any other checkout never reached the platform.
//
// The pre-ADR-024 working-tree .nightgauge/attention is deliberately not a
// source: it is repository content, so importing it would let any repository
// checked out on the machine plant cards (and default actions) in the shared
// store.
//
// It runs under dst's store lock, taken with the same bounded acquire every
// producer uses (#2528, #2750), and runs no verb inside it. For each card:
//
//   - a file that does not parse, or whose id does not match its file name, is
//     dropped: the old store's readers skipped it too, so it was never shown;
//   - a card whose id is already in dst is dropped (the same card, already
//     moved by an interrupted run);
//   - an open card whose idempotency key already has an open card in dst is
//     dropped: dst's card is the same condition, and keeping both would show
//     the operator one condition twice;
//   - every other card, terminal ones included, is copied into dst and its
//     source file removed.
//
// The source journal's well-formed entries are appended to dst's, the streak
// counts merge by key (the higher count wins), and the emptied source
// directory is removed. A failure leaves the remaining source files in place
// and is returned, so the next New retries what is left; files already moved
// are not moved twice.
func migrateCheckoutStore(rootDir, dst string) error {
	src, err := layout.CheckoutPath(rootDir, layout.LegacyCheckoutAttention)
	if err != nil {
		return fmt.Errorf("resolve the store directory: %w", err)
	}
	if _, done := migratedSources.Load(src); done {
		return nil
	}
	if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		migratedSources.Store(src, struct{}{})
		return nil
	} else if err != nil {
		return fmt.Errorf("migrate %s: %w", src, err)
	}
	if err := migrateOneStore(src, dst); err != nil {
		return fmt.Errorf("migrate the cards in %s into %s: %w", src, dst, err)
	}
	migratedSources.Store(src, struct{}{})
	return nil
}

// migrateOneStore empties one legacy store directory into dst under dst's
// store lock. A source another opener already emptied is done, not an error:
// the daemon and a CLI command can both open the store first after an upgrade.
func migrateOneStore(src, dst string) error {
	ctx, cancel := context.WithTimeout(context.Background(), migrateLockWait)
	defer cancel()
	release, err := acquireDirCtx(ctx, dst)
	if err != nil {
		return err
	}
	defer release()

	info, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symlink; remove it", ErrUnsafeStoreDir, src)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory; remove it", ErrUnsafeStoreDir, src)
	}

	stored, err := (&Store{dir: dst}).scanLocked()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if err := migrateCard(src, dst, e.Name(), &stored); err != nil {
			return err
		}
	}
	if err := migrateJournal(src, dst); err != nil {
		return err
	}
	if err := migrateStreaks(src, dst); err != nil {
		return err
	}
	// The lock file is the source store's own; nothing else holds meaning.
	_ = os.Remove(filepath.Join(src, lockFileName))
	_ = os.Remove(filepath.Join(src, streakSubdir))
	if err := os.Remove(src); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Something unrecognised is left (a stray temp file). Every card is
		// already moved, so the store is complete; say what was left behind.
		fmt.Fprintf(os.Stderr, "attention: migrated the cards out of %s but could not remove it: %v\n", src, err)
	}
	return nil
}

// migrateCard moves one card file from src into dst, or drops it.
func migrateCard(src, dst, name string, stored *[]storedRequest) error {
	from := filepath.Join(src, name)
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	var req DecisionRequest
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Fprintf(os.Stderr, "attention: dropped %s while migrating: it does not parse: %v\n", from, err)
		return os.Remove(from)
	}
	to, err := (&Store{dir: dst}).pathFor(req.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "attention: dropped %s while migrating: its id %q is invalid: %v\n", from, req.ID, err)
		return os.Remove(from)
	}
	if filepath.Base(to) != name {
		fmt.Fprintf(os.Stderr, "attention: dropped %s while migrating: its id %q does not name the file\n", from, req.ID)
		return os.Remove(from)
	}
	if _, err := os.Lstat(to); err == nil {
		return os.Remove(from)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !req.Lifecycle.State.IsTerminal() && req.IdempotencyKey != "" {
		if existing, _, ok := findOpenByKey(*stored, req.IdempotencyKey); ok {
			fmt.Fprintf(os.Stderr,
				"attention: dropped %s from %s while migrating: open card %s in the workspace store has the same key %q\n",
				req.ID, src, existing.ID, req.IdempotencyKey)
			return os.Remove(from)
		}
	}
	if err := writeFileAtomic(to, data); err != nil {
		return err
	}
	*stored = append(*stored, storedRequest{path: to, req: &req})
	return os.Remove(from)
}

// migrateJournal appends src's well-formed journal entries to dst's and
// removes src's journal. A line that is not a journal entry is dropped.
func migrateJournal(src, dst string) error {
	from := filepath.Join(src, journalFile)
	data, err := os.ReadFile(from)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// The whole file is in memory, so split it directly: a line of any length
	// is judged on whether it parses, never failed for its size.
	var keep bytes.Buffer
	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		line := bytes.TrimSpace(raw)
		var entry JournalEntry
		if len(line) == 0 || json.Unmarshal(line, &entry) != nil || entry.ID == "" {
			continue
		}
		keep.Write(line)
		keep.WriteByte('\n')
	}
	if keep.Len() > 0 {
		f, err := os.OpenFile(filepath.Join(dst, journalFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		_, werr := f.Write(keep.Bytes())
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		if cerr != nil {
			return cerr
		}
	}
	return os.Remove(from)
}

// migrateStreaks merges src's streak counts into dst's, keeping the higher
// count for a key both hold, and removes src's file. A source file that cannot
// be read is read as empty, exactly as the old store's own reader treated it
// (loadStreaksLocked), so there are no counts in it to lose.
func migrateStreaks(src, dst string) error {
	srcStore, dstStore := &Store{dir: src}, &Store{dir: dst}
	if _, err := os.Stat(srcStore.streakPath()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	from := srcStore.loadStreaksLocked()
	if len(from.Counts) > 0 {
		into := dstStore.loadStreaksLocked()
		for k, n := range from.Counts {
			if n > into.Counts[k] {
				into.Counts[k] = n
			}
		}
		if err := dstStore.saveStreaksLocked(into); err != nil {
			return err
		}
	}
	return os.Remove(srcStore.streakPath())
}

// writeFileAtomic publishes data at path through a per-writer temp file in
// the same directory, so a reader never sees a partial card.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := materializedTempPath(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
