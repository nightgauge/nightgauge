package doctor

// Resolving a machine-id conflict (#2308, NGD045).
//
// When ~/.nightgauge/machine-id and STATE/machine-id both exist and differ,
// the machine-state migration moves nothing (it never overwrites a file) and
// the running binary uses the STATE copy. Which id is right is the operator's
// call: each is a device the platform may already know, and a new id is a new
// device, counted against the account's machine limit. So there is no
// default. `nightgauge doctor resolve machine-id --keep state|legacy` carries
// out the choice:
//
//   - the chosen id ends at STATE/machine-id, byte for byte, mode 0600;
//   - the other id is kept, never deleted, at
//     STATE/machine-id.replaced-<UTC timestamp> (mode 0600). No migration row
//     or lookup reads that name, so it is never taken for legacy state;
//   - the legacy path is cleared, so `doctor --fix` moves the rest.
//
// It runs under the machine-state migration lock and acts only on the
// conflict the scan reports, so it cannot race the migration or act on a
// symlinked or out-of-root path. Nothing is minted.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nightgauge/nightgauge/internal/layout"
)

const (
	machineIDName = "machine-id"
	// machineIDBackupPrefix names the backup of the id a resolution replaced.
	machineIDBackupPrefix = machineIDName + ".replaced-"
	// MachineIDKeepState keeps the id in the machine-state directory, the one
	// the running binary uses.
	MachineIDKeepState = "state"
	// MachineIDKeepLegacy keeps the id an older build left in ~/.nightgauge.
	MachineIDKeepLegacy = "legacy"
)

// ErrNoMachineIDConflict reports that there is no machine-id conflict to
// resolve; nothing was changed.
var ErrNoMachineIDConflict = errors.New("no machine-id conflict to resolve")

// machineIDResolveCommand is the command that keeps side.
func machineIDResolveCommand(side string) string {
	return "nightgauge doctor resolve machine-id --keep " + side
}

// MachineIDResolution is what a resolution did.
type MachineIDResolution struct {
	Kept     string `json:"kept"`      // MachineIDKeepState or MachineIDKeepLegacy
	Path     string `json:"path"`      // STATE/machine-id, which now holds ID
	ID       string `json:"id"`        // the id kept
	Backup   string `json:"backup"`    // where the other id was saved
	BackupID string `json:"backup_id"` // the other id
	Legacy   string `json:"legacy"`    // the legacy path, now cleared
}

// ResolveMachineIDConflict resolves this user's machine-id conflict by
// keeping side (MachineIDKeepState or MachineIDKeepLegacy). Anything else, or
// no conflict, changes nothing.
func ResolveMachineIDConflict(side string) (MachineIDResolution, error) {
	return resolveMachineID(newMachineStateMigrator(), side, time.Now())
}

func resolveMachineID(m *machineStateMigrator, side string, now time.Time) (MachineIDResolution, error) {
	res := MachineIDResolution{Kept: side}
	if side != MachineIDKeepState && side != MachineIDKeepLegacy {
		return res, fmt.Errorf("choose which machine-id to keep: --keep %s (the one this build uses) or --keep %s; there is no default",
			MachineIDKeepState, MachineIDKeepLegacy)
	}
	if m == nil || m.legacyRoot == "" {
		return res, fmt.Errorf("%w: there is no home directory, so there is no legacy machine-id", ErrNoMachineIDConflict)
	}
	statePath, err := m.statePath()
	if err != nil {
		return res, fmt.Errorf("resolve the machine-state directory: %w", err)
	}
	// Only a conflict the scan reports is resolved, and only to a non-empty
	// id; neither check creates anything.
	pre, err := findMachineIDConflict(m, statePath)
	if err != nil {
		return res, err
	}
	chosen := pre.Target
	if side == MachineIDKeepLegacy {
		chosen = pre.Legacy
	}
	if data, err := os.ReadFile(chosen); err != nil {
		return res, err
	} else if strings.TrimSpace(string(data)) == "" {
		return res, fmt.Errorf("the %s machine-id %s is empty, so it cannot be kept; nothing was changed", side, chosen)
	}
	state, err := m.stateHome()
	if err != nil {
		return res, fmt.Errorf("resolve the machine-state directory: %w", err)
	}
	release, err := acquireLayoutLock(state)
	if err != nil {
		return res, err
	}
	defer release()
	// Again under the lock: the migration or another resolution may have run.
	c, err := findMachineIDConflict(m, state)
	if err != nil {
		return res, err
	}
	res.Path, res.Legacy = c.Target, c.Legacy

	legacyBytes, err := os.ReadFile(c.Legacy)
	if err != nil {
		return res, err
	}
	stateBytes, err := os.ReadFile(c.Target)
	if err != nil {
		return res, err
	}
	keep, other := stateBytes, legacyBytes
	if side == MachineIDKeepLegacy {
		keep, other = legacyBytes, stateBytes
	}
	if strings.TrimSpace(string(keep)) == "" {
		return res, fmt.Errorf("the %s machine-id is empty, so it cannot be kept; nothing was changed", side)
	}
	res.ID, res.BackupID = strings.TrimSpace(string(keep)), strings.TrimSpace(string(other))

	// 1. The id being replaced is saved first, so no step below can lose it.
	if res.Backup, err = writeMachineIDBackup(state, other, now); err != nil {
		return res, err
	}
	// 2. The chosen id at STATE/machine-id, byte for byte, mode 0600.
	if side == MachineIDKeepLegacy {
		if err := installTemp(c.Target, 0o600, time.Time{}, true, func(w io.Writer) error {
			_, err := w.Write(keep)
			return err
		}); err != nil {
			return res, fmt.Errorf("install the legacy machine-id at %s: %w (the replaced id is saved at %s)", c.Target, err, res.Backup)
		}
		syncDir(filepath.Dir(c.Target))
	} else if err := os.Chmod(c.Target, 0o600); err != nil {
		return res, err
	}
	if got, err := os.ReadFile(c.Target); err != nil || !bytes.Equal(got, keep) {
		return res, fmt.Errorf("%s does not hold the chosen id after the install (%v); the legacy file is left in place", c.Target, err)
	}
	// 3. The legacy path is cleared; its bytes are now at one of the two
	// paths above.
	if err := os.Remove(c.Legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, fmt.Errorf("remove %s: %w", c.Legacy, err)
	}
	syncDir(filepath.Dir(c.Legacy))
	// The automatic migration's retry record would hold the rest of the
	// machine state back for up to an hour; the conflict it recorded is gone.
	_ = os.Remove(filepath.Join(state, layoutAttemptName))
	return res, nil
}

// findMachineIDConflict returns the machine-id conflict the machine-state
// scan of state reports, or ErrNoMachineIDConflict (or the row's refusal).
func findMachineIDConflict(m *machineStateMigrator, state string) (layoutConflict, error) {
	plan, err := m.scan(state)
	if err != nil {
		return layoutConflict{}, err
	}
	for _, it := range plan.Items {
		if it.Entry.Name != machineIDName {
			continue
		}
		if it.Refused != "" {
			return layoutConflict{}, fmt.Errorf("the machine-id cannot be resolved safely: %s; nothing was changed", it.Refused)
		}
		if len(it.Conflicts) == 1 {
			return it.Conflicts[0], nil
		}
	}
	return layoutConflict{}, fmt.Errorf("%w: the two machine-id files do not both exist with different ids; nothing was changed", ErrNoMachineIDConflict)
}

// writeMachineIDBackup saves data at STATE/machine-id.replaced-<UTC time>,
// mode 0600, never replacing an existing file: a second backup in the same
// second gains a suffix.
func writeMachineIDBackup(state string, data []byte, now time.Time) (string, error) {
	base := filepath.Join(state, machineIDBackupPrefix+now.UTC().Format("20060102T150405Z"))
	for i := 0; i < 100; i++ {
		path := base
		if i > 0 {
			path += "-" + strconv.Itoa(i)
		}
		_, won, err := layout.WriteStateFileExclusive(path, data, 0o600)
		if err != nil {
			return "", fmt.Errorf("save the replaced machine-id at %s: %w; nothing was changed", path, err)
		}
		if won {
			return path, nil
		}
	}
	return "", fmt.Errorf("save the replaced machine-id: %s-* are all taken; nothing was changed", base)
}

// machineIDShown is a machine-id file's id for a finding, and its
// modification time. A file that is not a plausible id is described, not
// printed.
func machineIDShown(path string) (id string, mtime time.Time) {
	info, err := os.Lstat(path)
	if err != nil {
		return "(unreadable: " + err.Error() + ")", time.Time{}
	}
	mtime = info.ModTime()
	if !info.Mode().IsRegular() {
		return "(not a regular file)", mtime
	}
	f, err := os.Open(path)
	if err != nil {
		return "(unreadable: " + err.Error() + ")", mtime
	}
	defer f.Close()
	buf, _ := io.ReadAll(io.LimitReader(f, 129))
	id = strings.TrimSpace(string(buf))
	switch {
	case id == "":
		return "(empty)", mtime
	case len(buf) > 128 || strings.IndexFunc(id, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) }) >= 0:
		return fmt.Sprintf("(not an id: %d bytes)", info.Size()), mtime
	}
	return id, mtime
}

// machineIDConflictFinding is the NGD045 finding for two differing machine-id
// files (#2308): both ids, each file's modification time, the one the running
// binary uses, and the exact command that keeps each.
func machineIDConflictFinding(c layoutConflict, why string, ev func(map[string]string) map[string]string,
	id func(...string) []string, migrate func(summary, preview string) Remedy) Finding {
	legacyID, legacyAt := machineIDShown(c.Legacy)
	stateID, stateAt := machineIDShown(c.Target)
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return "unknown"
		}
		return t.UTC().Format(time.RFC3339)
	}
	keepState, keepLegacy := machineIDResolveCommand(MachineIDKeepState), machineIDResolveCommand(MachineIDKeepLegacy)
	inUse := "this build uses " + stateID + " (" + c.Target + ")"
	if v := strings.TrimSpace(os.Getenv("NIGHTGAUGE_AGENT_ID")); v != "" {
		inUse = "this process uses NIGHTGAUGE_AGENT_ID=" + v + ", which overrides both files; without it, this build uses " +
			stateID + " (" + c.Target + ")"
	}
	cause := fmt.Sprintf("two different machine ids: %s holds %s (modified %s) and %s holds %s (modified %s); %s. "+
		"Keep one with `%s` or `%s`. The other id is saved beside it as %s<time>, never deleted. "+
		"The platform knows this device by its id, and a new id is a new device, so neither is regenerated and "+
		"nothing is chosen for you. %s",
		c.Target, stateID, stamp(stateAt), c.Legacy, legacyID, stamp(legacyAt), inUse,
		keepState, keepLegacy, machineIDBackupPrefix, sentence(why))
	return newFinding(checkLayout, codeLayoutConflict, SeverityHousekeeping,
		fmt.Sprintf("layout-conflict: a machine-id file exists at both %s and %s", c.Legacy, c.Target),
		cause,
		ev(map[string]string{
			"legacy": c.Legacy, "target": c.Target,
			"legacy_id": legacyID, "legacy_modified": stamp(legacyAt),
			"target_id": stateID, "target_modified": stamp(stateAt),
			"in_use":      inUse,
			"keep_state":  keepState,
			"keep_legacy": keepLegacy,
		}),
		id(c.Target),
		// The migrate remedy comes first so `doctor --fix` reaches the verb,
		// whose precondition refuses with ErrRemedyConflict (exit 3).
		migrate(fmt.Sprintf("Choose the id to keep (`%s` or `%s`), then `nightgauge doctor --fix` migrates the rest", keepState, keepLegacy),
			"refused: "+c.Legacy+" and "+c.Target+" hold different ids; nothing is overwritten"),
		manualRemedy("keep-state", "Keep the id this build uses", checkLayout,
			fmt.Sprintf("Run `%s` to keep %s", keepState, stateID),
			fmt.Sprintf("The legacy id %s is saved as %s<time> and %s is cleared", legacyID, filepath.Join(filepath.Dir(c.Target), machineIDBackupPrefix), c.Legacy),
			"Re-run `nightgauge doctor --fix`"),
		manualRemedy("keep-legacy", "Keep the id an older build used", checkLayout,
			fmt.Sprintf("Run `%s` to keep %s", keepLegacy, legacyID),
			fmt.Sprintf("The id this build used, %s, is saved as %s<time> and %s is cleared", stateID, filepath.Join(filepath.Dir(c.Target), machineIDBackupPrefix), c.Legacy),
			"Re-run `nightgauge doctor --fix`"))
}

// sentence capitalises s's first letter, for a clause that starts a sentence.
func sentence(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
