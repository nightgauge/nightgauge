package runstate

// The serve registry (#1426) — <home>/.nightgauge/serve as a directory that
// can be READ, not just written into.
//
// It holds two kinds of file, both keyed to one workspace root: the `.json`
// claim record the daemon heartbeats (serve_sidecar.go) and the `.lock` file
// the scheduler lease flocks (serve_lease.go). Between them they are the only
// machine-local record of which workspaces have ever run a daemon, so anything
// that wants to answer "which workspaces on this machine have one" has to read
// them.
//
// Measured on a machine with history, that answer was 95% wrong: 150 records,
// 143 of them naming a workspace root that no longer existed, and 191 lock
// files, 174 of them with no record at all. Three separate mechanisms produced
// that, and this file closes all three.
//
// NOTHING EVER REMOVED ANYTHING. RemoveServeSidecar runs only on a clean
// shutdown, and only while the record still names the exiting pid — so
// anything killed, crashed or reparented left its record behind permanently,
// and lock files were never unlinked by any path at all. PruneServeRegistry is
// the sweep that was missing; startServeSidecar runs it, so a daemon start
// cleans the directory it is about to write into.
//
// THE NAME WAS ONE-WAY. A file was named by the first 16 hex digits of
// sha256(root), which is adequate for a record — that carries workspace_root
// inside it — and fatal for a lock, which carries nothing. An orphaned `.lock`
// therefore named a workspace NOTHING on the machine could recover. The key is
// a reversible encoding of the root now, so every file in the directory names
// its workspace whether or not its sibling survived, and `ls` is legible to
// the operator who has to act on it.
//
// TWO WALKERS DISAGREE. `doctor` walked this directory with its own ReadDir
// and its own suffix filter, and the prune would have been a second one. That
// is the dual-path drift this repo names as a defect class — doctor's own
// comment already warns that a path two packages spell independently is a path
// that eventually disagrees, and a filter spelled twice is the same hazard.
// EachServeRegistryFile is the one enumeration; doctor keeps its own record
// SHAPE (it is a reader of a schema it does not own) but no longer its own
// directory walk.
//
// WHY A DEAD RECORD IS NOT MERELY UNTIDY. A registry that is 95% dead invites
// a feature to be built on it — #1421 wanted exactly that, to find the
// workspace owning a misrouted attention card — and then to be over-broad in a
// way that only shows up on a machine with history. ADR-019 rejected that
// approach BECAUSE the registry could not be trusted.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
)

const (
	serveRecordSuffix = ".json"
	serveLockSuffix   = ".lock"

	// serveKeySeparator stands in for the path separator, so an encoded key
	// reads like the path it came from. One byte, legal in a filename
	// everywhere, and never emitted for anything else — a literal '~' in a
	// root is percent-escaped, so the mapping stays one-to-one.
	serveKeySeparator = '~'

	// serveKeyMaxLen is the longest encoded key this scheme will produce.
	//
	// A hash was fixed-width; a reversible encoding is not, and one path
	// segment has a hard ceiling (NAME_MAX, 255 bytes on every filesystem
	// this runs on). The budget is not just the key plus its 5-byte suffix:
	// AtomicWriteFile writes through "<name>.<random>.tmp", which adds about
	// 15 more. 200 leaves room for all of it and is far past any real
	// workspace root, so serveKeyIsHashed below is the rare path rather than
	// the normal one.
	serveKeyMaxLen = 200

	// serveKeyHashPrefix marks the fallback key for a root too long to encode.
	// It is not legal at the start of an encoded key — the encoder escapes a
	// literal '%' — so the two forms can never be confused.
	serveKeyHashPrefix = "%%"
)

// ServeRegistryFile is one file in the machine-global serve registry, as the
// single enumeration below sees it.
//
// Data is the record's bytes, left UNPARSED on purpose: `doctor` is a reader
// of a schema it does not own and keeps its own minimal struct, so handing it a
// decoded ServeSidecar would make this package's schema answerable to doctor's
// reader. What the two share is the directory, the suffix filter and the read
// — the parts that must not be spelled twice.
type ServeRegistryFile struct {
	// Name is the file name, e.g. "~srv~acme~repo.json".
	Name string
	// Path is the absolute path to it.
	Path string
	// Lock reports a `.lock` file rather than a `.json` record.
	Lock bool
	// WorkspaceRoot is the root recovered from the NAME, or "" when the name
	// does not decode — a file this package did not write, or one whose root
	// was too long to encode. "Unrecoverable" is reported as unrecoverable
	// rather than guessed at.
	WorkspaceRoot string
	// Data is the record's bytes. Always nil for a lock, and nil for a record
	// that could not be read.
	Data []byte
}

// EachServeRegistryFile visits every record and lock in the registry.
//
// Unconditional, and keyed to nothing about the invoking workspace: the claim
// store is machine-global because the questions asked of it are machine-wide
// (see serve_sidecar.go). A missing directory — no daemon has ever run here —
// is simply no visits, and an unreadable record is visited with nil Data
// rather than skipped, because the caller decides what an unreadable file
// means and for the prune that means something quite different than it does
// for doctor.
func EachServeRegistryFile(visit func(ServeRegistryFile)) {
	dir, err := ServeSidecarDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		isLock := strings.HasSuffix(name, serveLockSuffix)
		isRecord := strings.HasSuffix(name, serveRecordSuffix)
		if !isLock && !isRecord {
			// AtomicWriteFile's in-flight temp files land here as
			// "<name>.<random>.tmp"; they are another writer's half-written
			// state and are never this walker's business.
			continue
		}
		f := ServeRegistryFile{Name: name, Path: filepath.Join(dir, name), Lock: isLock}
		if root, ok := ServeRegistryWorkspaceRoot(name); ok {
			f.WorkspaceRoot = root
		}
		if isRecord {
			if data, err := os.ReadFile(f.Path); err == nil {
				f.Data = data
			}
		}
		visit(f)
	}
}

// ServeRegistryWorkspaceRoot recovers the workspace root a registry file name
// stands for, and whether it could be recovered at all.
//
// The second return is the whole point. A name that does not decode has to say
// so — an orphaned lock whose root is unrecoverable is a fact the operator
// needs — and inventing a plausible-looking path for it would be worse than
// the hash this replaced. A hash-fallback key (a root past serveKeyMaxLen) and
// a name this package never wrote both land here, and both are reported the
// same way: unknown, not wrong.
func ServeRegistryWorkspaceRoot(name string) (string, bool) {
	var key string
	switch {
	case strings.HasSuffix(name, serveRecordSuffix):
		key = strings.TrimSuffix(name, serveRecordSuffix)
	case strings.HasSuffix(name, serveLockSuffix):
		key = strings.TrimSuffix(name, serveLockSuffix)
	default:
		return "", false
	}
	root, ok := decodeServeRegistryKey(key)
	if !ok {
		return "", false
	}
	// Only a CLEAN ABSOLUTE path can have been produced by the encoder, so
	// anything else decoded from bytes this package did not write.
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", false
	}
	return root, true
}

// encodeServeRegistryKey turns a normalized workspace root into one path
// segment, reversibly.
//
// The scheme is deliberately dull. Bytes that are safe and unambiguous in a
// filename everywhere ([A-Za-z0-9._-]) are kept as themselves; the path
// separator becomes serveKeySeparator so the result reads like the path it
// stands for; every other byte — including a literal '~' or '%', and every
// non-ASCII byte — becomes %XX. Nothing is dropped, so decode is exact.
//
// Base64 would also be reversible and would need no escaping, and was not
// chosen: it expands by a third against a NAME_MAX ceiling, and it turns a
// directory listing back into something an operator cannot read, which is half
// of what the hash cost us.
//
// A root that does not fit gets a hash key instead. That is the one case the
// scheme cannot make reversible — the information does not fit in the name —
// and it is reported as undecodable rather than approximated. The record still
// carries workspace_root; only an ORPHANED LOCK for such a root is anonymous,
// and the prune reclaims those without needing to name them.
func encodeServeRegistryKey(root string) string {
	var b strings.Builder
	b.Grow(len(root) + 8)
	for i := 0; i < len(root); i++ {
		c := root[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
			b.WriteByte(c)
		case c == filepath.Separator:
			b.WriteByte(serveKeySeparator)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	if b.Len() > serveKeyMaxLen {
		sum := sha256.Sum256([]byte(root))
		return serveKeyHashPrefix + hex.EncodeToString(sum[:])[:16]
	}
	return b.String()
}

// decodeServeRegistryKey reverses encodeServeRegistryKey exactly, or reports
// that the key was not produced by it.
func decodeServeRegistryKey(key string) (string, bool) {
	if strings.HasPrefix(key, serveKeyHashPrefix) {
		return "", false // the hash fallback: nothing to recover, and it says so
	}
	var b strings.Builder
	b.Grow(len(key))
	for i := 0; i < len(key); i++ {
		switch c := key[i]; c {
		case serveKeySeparator:
			b.WriteByte(filepath.Separator)
		case '%':
			if i+2 >= len(key) {
				return "", false
			}
			hi, hiOK := unhexServeKey(key[i+1])
			lo, loOK := unhexServeKey(key[i+2])
			if !hiOK || !loOK {
				return "", false
			}
			b.WriteByte(hi<<4 | lo)
			i += 2
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// unhexServeKey decodes one hex digit of a %XX escape.
//
// Lowercase is deliberately rejected: the encoder only ever emits uppercase, so
// accepting both would let two different names decode to one root — and two
// names for one workspace is two claims for one workspace, which is the state
// the lease exists to prevent.
func unhexServeKey(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

// ServeRegistryPrune counts what one sweep did.
type ServeRegistryPrune struct {
	// Records is dead claim records removed.
	Records int
	// Locks is lock files removed — orphans, and the locks of records that
	// were just removed. Only ever a file this process could take the lock
	// on, which is the kernel's proof that nobody holds it.
	Locks int
	// Kept is every file left in place, for whatever reason.
	Kept int
}

// Removed reports how many files the sweep unlinked.
func (p ServeRegistryPrune) Removed() int { return p.Records + p.Locks }

// PruneServeRegistry removes the dead half of the registry and reports what it
// did. Never fatal and never returns an error: this is bookkeeping, and a
// sweep that cannot remove a file leaves it for the next one.
//
// WHAT COUNTS AS DEAD is deliberately narrow, because every rule here can fail
// in the direction of deleting the evidence `doctor` reports on:
//
//   - A LIVE daemon's record is never touched. Live means a pid that exists
//     AND a heartbeat inside ServeLeaseStaleAfter — the same pair the no-flock
//     lease fallback requires, and for the same reason: either alone is
//     satisfied by a recycled pid or by a suspended laptop.
//   - A WEDGED daemon's record is never touched. An alive pid with a cold
//     heartbeat is exactly what doctor's serve-lease arm reports, and that
//     report needs the record to name the holder. Pruning it would erase the
//     finding.
//   - A record whose WORKSPACE ROOT NO LONGER EXISTS is removed. That is 143
//     of the 150 records measured in #1426 — finished t.TempDir()s and
//     reclaimed worktrees — and no daemon can be serving a directory that is
//     not there.
//   - A record naming a DEAD PID whose heartbeat has ALSO gone cold is
//     removed. Both, not either: a pid that died a minute ago still has a
//     fresh heartbeat, and doctor may still be attributing a live process to
//     it through a recycled pid.
//   - A record that does not PARSE is removed. Writes are atomic (temp →
//     fsync → rename), so an unparsable `.json` here is not a torn write; it
//     is a file nothing can read, which can never claim a pid or name a
//     workspace, and which every reader already skips.
//
// A LOCK IS REMOVED ONLY WHEN THIS PROCESS CAN TAKE IT, and only when no
// surviving record explains it. The flock is the authority on whether a lease
// is held — the kernel releases it however the holder dies, which no pid check
// can match — so "I got the lock" is the only safe proof that a lock file is
// litter. On a platform with no advisory lock nothing can be proved, and no
// lock file is removed.
func PruneServeRegistry(now time.Time) ServeRegistryPrune {
	// Grouped by key so a record and its lock are decided together: a lock is
	// only litter once the record that would explain it is gone.
	type group struct {
		record *ServeRegistryFile
		lock   *ServeRegistryFile
	}
	groups := map[string]*group{}
	var order []string
	EachServeRegistryFile(func(f ServeRegistryFile) {
		key := strings.TrimSuffix(strings.TrimSuffix(f.Name, serveLockSuffix), serveRecordSuffix)
		g, ok := groups[key]
		if !ok {
			g = &group{}
			groups[key] = g
			order = append(order, key)
		}
		file := f
		if f.Lock {
			g.lock = &file
		} else {
			g.record = &file
		}
	})

	var res ServeRegistryPrune
	for _, key := range order {
		g := groups[key]
		recordSurvives := false
		if g.record != nil {
			switch {
			case !serveRecordIsDead(*g.record, now):
				recordSurvives = true
				res.Kept++
			case os.Remove(g.record.Path) == nil:
				res.Records++
			default:
				// Still on disk, so it still explains its lock.
				recordSurvives = true
				res.Kept++
			}
		}
		if g.lock == nil {
			continue
		}
		if recordSurvives {
			res.Kept++
			continue
		}
		if removeUnheldServeLock(g.lock.Path) {
			res.Locks++
		} else {
			res.Kept++
		}
	}
	return res
}

// serveRecordIsDead applies the rules documented on PruneServeRegistry.
func serveRecordIsDead(f ServeRegistryFile, now time.Time) bool {
	var sc ServeSidecar
	if len(f.Data) == 0 || json.Unmarshal(f.Data, &sc) != nil {
		return true
	}
	if sc.PID > 0 && ProcessAlive(sc.PID) {
		// Healthy or wedged — either way a live process, and neither is the
		// sweep's business.
		return false
	}
	// The record's own workspace_root is the authority; the file NAME is a
	// second, independent spelling of the same fact, used only when the record
	// does not carry one. WriteServeSidecar always stamps it, but a reader
	// that DEPENDS on that is a reader that breaks silently the day it stops
	// being true.
	root := sc.WorkspaceRoot
	if root == "" {
		root = f.WorkspaceRoot
	}
	if root != "" && !servePathExists(root) {
		return true
	}
	return serveHeartbeatStale(sc, now)
}

// servePathExists reports whether root is still on disk, resolving "cannot
// tell" to "it is there".
//
// A permission error or an unreachable network mount must not read as a
// deleted workspace: the cost of being wrong in that direction is deleting a
// live daemon's claim, while the cost of being wrong the other way is one dead
// record surviving until the next sweep.
func servePathExists(root string) bool {
	if _, err := os.Stat(root); err != nil {
		return !os.IsNotExist(err)
	}
	return true
}

// removeUnheldServeLock takes path's advisory lock and, while still holding it,
// unlinks the file — reporting whether it did.
//
// The unlink happens UNDER the lock on purpose. Unlocking first opens a window
// in which another process takes the lock on the inode this call is about to
// remove: it would then hold a lease on an unlinked file while a third process
// creates a fresh one and locks that — the two-schedulers state the lease
// exists to prevent. A process racing this call instead finds the lock held,
// refuses, and succeeds on its next attempt against the new file.
//
// Failure to take the lock is never an error to report: a held lock is the
// expected answer for a live daemon, and ErrUnsupported means this platform
// can prove nothing about lock files, so it leaves them alone.
func removeUnheldServeLock(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := flock.Exclusive(f, 0); err != nil {
		return false
	}
	defer func() { _ = flock.Unlock(f) }()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false
	}
	return true
}
