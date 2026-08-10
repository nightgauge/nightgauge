package runstate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewRunID returns a UUID v7 — time-ordered, 48 bits of millisecond timestamp
// followed by version (4) + variant (2) + random bits.
//
// We synthesize the layout by hand instead of pulling github.com/google/uuid
// straight in because the dependency's NewV7 is fine but rolling our own
// keeps the runstate package self-contained and easy to stub in tests.
func NewRunID() (string, error) {
	now := time.Now().UnixMilli()
	if now < 0 {
		return "", fmt.Errorf("clock returned negative timestamp")
	}
	var b [16]byte
	// 48-bit big-endian timestamp
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	// Version 7 — high nibble of byte 6
	b[6] = (b[6] & 0x0f) | 0x70
	// Variant 10 — high two bits of byte 8
	b[8] = (b[8] & 0x3f) | 0x80

	hexStr := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:32]), nil
}

// UUIDv7Millis decodes the 48-bit big-endian Unix-millisecond prefix NewRunID
// writes above. It is the ONLY decoder in the tree and it has exactly one
// caller: ADR-017 Decision 9's pause-restore claim token, whose age decides
// whether the reconciler releases a claim (7.4, C17). Run identities are never
// decoded for a correctness rule — identity.go states that carve-out.
//
// The version nibble is validated (through IsIdentity, whose pattern pins it to
// 7): a non-v7 UUID carries random bits at this offset, and reading them as a
// time yields a confident wrong answer — which is the shape of the mtime rule
// this replaces (F34). An error is the caller's signal to treat the claim as
// FRESH and never release it.
func UUIDv7Millis(id string) (int64, error) {
	if !IsIdentity(id) {
		return 0, fmt.Errorf("uuidv7: %q is not a canonical v7 identity", id)
	}
	// Offsets are fixed by the 8-4-4-4-12 layout: the first six bytes are the
	// timestamp, i.e. the first two hyphen-separated groups.
	raw, err := hex.DecodeString(id[0:8] + id[9:13])
	if err != nil {
		return 0, fmt.Errorf("uuidv7: decode timestamp prefix of %q: %w", id, err)
	}
	var ms int64
	for _, b := range raw {
		ms = ms<<8 | int64(b)
	}
	return ms, nil
}
