package okf

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StampInput holds the provenance fields to merge into an entry's frontmatter.
// A zero-value field means "leave whatever is there alone" — the stamp never
// clears a value it was not asked to set.
type StampInput struct {
	// GeneratedBy replaces `generated` with this actor stamped at now.
	GeneratedBy string
	// Sources are appended to `sources`, skipping resources already present.
	Sources []Source
	// VerifiedBy appends a `verified` event, unless this actor already has one.
	VerifiedBy string
	// StaleAfter sets `stale_after` (RFC3339).
	StaleAfter string
	// Status sets `status` (draft|stable|deprecated).
	Status string
}

// Empty reports whether the input would change nothing.
func (in StampInput) Empty() bool {
	return in.GeneratedBy == "" && len(in.Sources) == 0 && in.VerifiedBy == "" &&
		in.StaleAfter == "" && in.Status == ""
}

// Validate checks every field before any file is touched, so a rejected stamp
// writes nothing at all.
func (in StampInput) Validate() error {
	if in.GeneratedBy != "" {
		if err := ValidateActor(in.GeneratedBy); err != nil {
			return fmt.Errorf("--generated-by: %w", err)
		}
	}
	if in.VerifiedBy != "" {
		if err := ValidateActor(in.VerifiedBy); err != nil {
			return fmt.Errorf("--verified-by: %w", err)
		}
	}
	if in.Status != "" {
		switch in.Status {
		case StatusDraft, StatusStable, StatusDeprecated:
		default:
			return fmt.Errorf("--status: %q is not one of %s, %s, %s",
				in.Status, StatusDraft, StatusStable, StatusDeprecated)
		}
	}
	if in.StaleAfter != "" {
		if _, err := time.Parse(time.RFC3339, in.StaleAfter); err != nil {
			return fmt.Errorf("--stale-after: %q is not an RFC3339 timestamp", in.StaleAfter)
		}
	}
	for i, s := range in.Sources {
		if strings.TrimSpace(s.Resource) == "" {
			return fmt.Errorf("--source[%d]: empty resource", i)
		}
	}
	return nil
}

// Stamp merges in's provenance fields into the frontmatter of the entry at
// path and rewrites the file. It is the only writer of provenance fields;
// skills and stages call it rather than editing frontmatter themselves.
//
// The body is never inspected or modified: the file is split, only the block
// is rebuilt, and the two are re-joined. Merge rules, which decide
// idempotency:
//
//   - generated is REPLACED. It is a single object naming the last producer,
//     not a log.
//   - verified appends only when no existing event has the same actor.
//     De-duplication is on the actor ALONE: `at` differs on every run, so a
//     by+at key would append forever and make retro non-idempotent.
//   - sources appends only when no existing entry has the same resource.
//   - status and stale_after are set only when non-empty.
//
// Returns the merged block and whether anything actually changed. When
// nothing changed the file is left untouched, byte for byte.
func Stamp(path string, in StampInput) (*FrontmatterBlock, bool, error) {
	if err := in.Validate(); err != nil {
		return nil, false, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, false, fmt.Errorf("stamp %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, false, fmt.Errorf("stamp %s: is a directory", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("stamp %s: %w", path, err)
	}
	content := string(data)

	block, err := ParseFrontmatter(content)
	if err != nil {
		return nil, false, fmt.Errorf("stamp %s: %w", path, err)
	}
	if block == nil {
		block = &FrontmatterBlock{}
	}
	_, body := SplitFrontmatter(content)

	changed := false

	if in.GeneratedBy != "" {
		next := Provenance{By: in.GeneratedBy, At: NowStamp()}
		if block.Generated != nil {
			// Keep the previous producer's unknown keys; only the contract
			// fields are being replaced.
			next.Extra = block.Generated.Extra
		}
		block.Generated = &next
		changed = true
	}

	if in.VerifiedBy != "" {
		seen := false
		for _, v := range block.Verified {
			if v.By == in.VerifiedBy {
				seen = true
				break
			}
		}
		if !seen {
			block.Verified = append(block.Verified, Provenance{By: in.VerifiedBy, At: NowStamp()})
			changed = true
		}
	}

	for _, src := range in.Sources {
		seen := false
		for _, existing := range block.Sources {
			if existing.Resource == src.Resource {
				seen = true
				break
			}
		}
		if !seen {
			block.Sources = append(block.Sources, src)
			changed = true
		}
	}

	if in.Status != "" && block.Status != in.Status {
		block.Status = in.Status
		changed = true
	}
	if in.StaleAfter != "" && block.StaleAfter != in.StaleAfter {
		block.StaleAfter = in.StaleAfter
		changed = true
	}

	if !changed {
		return block, false, nil
	}

	out, err := WithFrontmatter(block, body)
	if err != nil {
		return nil, false, fmt.Errorf("stamp %s: %w", path, err)
	}
	if err := writeAtomic(path, []byte(out), info.Mode().Perm()); err != nil {
		return nil, false, fmt.Errorf("stamp %s: %w", path, err)
	}
	return block, true, nil
}

// writeAtomic replaces path via a temp file in the same directory so a crash
// mid-write cannot leave an entry with half a frontmatter block.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".stamp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
