// Package keychain stores Nightgauge credentials in the operating system's
// credential store — macOS Keychain, Windows Credential Manager, or the
// Secret Service on Linux — and falls back to the 0600 machine-tier config
// file on a host that has none (a headless CI runner, a container, an SSH
// session with a locked login keychain).
//
// # The entry contract
//
// Every entry lives under the fixed service name [Service]. The account name
// is the credential's dotted machine-tier config path, so the fallback file
// and the keychain address a credential by the same string. The VS Code
// extension does not open the keychain itself: it pipes the key to
// `nightgauge auth license set`, so this package is the only writer and
// reader of the entry. The strings are pinned by tests on both sides; change
// them in both places or not at all.
//
// On macOS the stored value carries go-keyring's `go-keyring-base64:` prefix
// (the library encodes every value so non-ASCII survives the `security`
// tool). Read the entry through this package, not through `security`.
//
// # License-key resolution
//
// [Store.ResolveLicenseKey] is the one resolution the CLI and the daemon use,
// highest precedence first:
//
//  1. the NIGHTGAUGE_LICENSE_KEY environment variable ([SourceEnv]);
//  2. the keychain entry ([SourceKeychain]);
//  3. platform.license_key in the machine-tier config file
//     ([SourceMachineFile]).
//
// No value is ever logged; a [Result] carries the value for the caller and a
// source label for everything else.
package keychain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/zalando/go-keyring"
)

const (
	// Service is the keychain service name every Nightgauge entry uses.
	Service = "nightgauge"
	// AccountLicenseKey is the account name of the platform license key. It is
	// also the key's dotted path in the machine-tier config file.
	AccountLicenseKey = "platform.license_key"
	// EnvLicenseKey is the environment variable that overrides every stored
	// license key.
	EnvLicenseKey = "NIGHTGAUGE_LICENSE_KEY"
)

// Source says where a credential came from.
type Source string

const (
	SourceEnv         Source = "env"
	SourceKeychain    Source = "keychain"
	SourceMachineFile Source = "machine-file"
	SourceNone        Source = "none"
)

// defaultTimeout bounds each keychain call. On Linux the Secret Service is
// reached over D-Bus, and a host without a session bus can stall rather than
// fail; gh bounds the same calls the same way.
const defaultTimeout = 3 * time.Second

// backend is the subset of go-keyring the store needs.
type backend interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
	Delete(service, account string) error
}

// libraryBackend delegates to go-keyring's package-level provider, which
// keyring.MockInit replaces with an in-memory store in tests.
type libraryBackend struct{}

func (libraryBackend) Get(s, a string) (string, error) { return keyring.Get(s, a) }
func (libraryBackend) Set(s, a, v string) error        { return keyring.Set(s, a, v) }
func (libraryBackend) Delete(s, a string) error        { return keyring.Delete(s, a) }

// Store reads and writes Nightgauge credentials.
type Store struct {
	backend backend
	timeout time.Duration
	getenv  func(string) string
}

// New returns a store backed by the OS keychain.
func New() *Store {
	return &Store{backend: libraryBackend{}, timeout: defaultTimeout, getenv: os.Getenv}
}

// Result is the outcome of a read or write. Value is set only by reads.
type Result struct {
	Value  string
	Source Source
	// Path is the machine-tier file when Source is SourceMachineFile, and the
	// file a plaintext copy was removed from when RemovedFileCopy is true.
	Path string
	// KeychainErr is non-nil when the keychain could not be used and the
	// machine-tier file was used instead; it says why. It never contains the
	// credential.
	KeychainErr error
	// RemovedFileCopy reports that a keychain write also removed a plaintext
	// copy from the machine-tier file; FileCleanupErr says why it could not.
	RemovedFileCopy bool
	FileCleanupErr  error
}

// ErrTimeout reports a keychain call that did not return in time. A write
// that times out may still complete (a macOS unlock prompt can be answered
// later), so it is never followed by a plaintext fallback.
var ErrTimeout = errors.New("the OS keychain did not respond")

// Fingerprint is a non-reversible identifier of a key: the first 12 hex
// characters of its SHA-256. It lets two holders of a key compare copies
// without either printing the key.
func Fingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// call runs fn with the store's timeout. A call that overruns is abandoned:
// its goroutine finishes (or not) on its own, and the caller falls back.
func (s *Store) call(fn func() (string, error)) (string, error) {
	type outcome struct {
		v   string
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		v, err := fn()
		ch <- outcome{v, err}
	}()
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case o := <-ch:
		return o.v, o.err
	case <-timer.C:
		return "", fmt.Errorf("%w within %s", ErrTimeout, s.timeout)
	}
}

// Get returns the credential stored for account: the keychain entry when
// there is one, otherwise the machine-tier file's value. Source is SourceNone
// when neither holds it. An error means the machine-tier file could not be
// read; an unusable keychain is reported in Result.KeychainErr instead.
func (s *Store) Get(account string) (Result, error) {
	v, err := s.call(func() (string, error) { return s.backend.Get(Service, account) })
	if err == nil && v != "" {
		return Result{Value: v, Source: SourceKeychain}, nil
	}
	var res Result
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		res.KeychainErr = err
	}
	value, path, ferr := config.ReadMachineString(account)
	if ferr != nil {
		res.Source = SourceNone
		return res, ferr
	}
	if value == "" {
		res.Source = SourceNone
		return res, nil
	}
	res.Value, res.Source, res.Path = value, SourceMachineFile, path
	return res, nil
}

// Set stores value for account in the keychain and removes any plaintext copy
// from the machine-tier file, so a rotated key never leaves the old one on
// disk. When the keychain answers that it cannot be used (no Secret Service,
// no D-Bus, a locked keychain that refuses interaction) it writes the
// machine-tier file instead (mode 0600, never through a symlink) and returns
// Source SourceMachineFile with the reason in KeychainErr. A keychain that
// does not answer in time is an error, not a fallback: the abandoned write can
// still land, and a second copy on disk is what the keychain exists to avoid.
func (s *Store) Set(account, value string) (Result, error) {
	if value == "" {
		return Result{}, errors.New("refusing to store an empty value")
	}
	_, err := s.call(func() (string, error) { return "", s.backend.Set(Service, account, value) })
	if err == nil {
		res := Result{Source: SourceKeychain}
		old, path, rerr := config.ReadMachineString(account)
		switch {
		case rerr != nil:
			res.FileCleanupErr = rerr
		case old != "":
			if _, werr := config.WriteMachinePrivateValue(account, ""); werr != nil {
				res.FileCleanupErr = werr
			} else {
				res.RemovedFileCopy, res.Path = true, path
			}
		}
		return res, nil
	}
	if errors.Is(err, ErrTimeout) {
		return Result{KeychainErr: err}, fmt.Errorf("%w; the key was not written anywhere else. "+
			"If the keychain asked to be unlocked, unlock it and run the command again", err)
	}
	if errors.Is(err, keyring.ErrSetDataTooBig) {
		return Result{}, err
	}
	path, werr := config.WriteMachinePrivateValue(account, value)
	if werr != nil {
		return Result{KeychainErr: err}, fmt.Errorf("keychain unavailable (%v) and the machine-tier fallback failed: %w", err, werr)
	}
	return Result{Source: SourceMachineFile, Path: path, KeychainErr: err}, nil
}

// Removed reports what Delete removed.
type Removed struct {
	Keychain    bool
	MachineFile bool
	Path        string
	// KeychainErr is non-nil when the keychain could not be used.
	KeychainErr error
	// NoKeychain reports that KeychainErr means this host has no keychain
	// service at all (no backend, no D-Bus session, no Secret Service), so
	// no entry can exist. Otherwise (a timeout or an unexpected error) an
	// entry may remain.
	NoKeychain bool
}

// noKeychainMarkers are the error texts of a host with no keychain service:
// no D-Bus session bus, no Secret Service on it, or no backend binary.
var noKeychainMarkers = []string{
	"couldn't determine address of session bus",
	"org.freedesktop.DBus.Error.ServiceUnknown",
	"org.freedesktop.secrets was not provided",
	"dbus-launch",
}

// IsNoKeychain reports whether err says the host has no keychain service, as
// opposed to a keychain that timed out or failed in a way that may leave an
// entry behind.
func IsNoKeychain(err error) bool {
	if err == nil || errors.Is(err, ErrTimeout) {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	msg := err.Error()
	for _, m := range noKeychainMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// Delete removes account from the keychain and from the machine-tier file, so
// no stored copy survives. Deleting what is not there is not an error.
func (s *Store) Delete(account string) (Removed, error) {
	var out Removed
	_, err := s.call(func() (string, error) { return "", s.backend.Delete(Service, account) })
	switch {
	case err == nil:
		out.Keychain = true
	case !errors.Is(err, keyring.ErrNotFound):
		out.KeychainErr = err
		out.NoKeychain = IsNoKeychain(err)
	}
	value, path, ferr := config.ReadMachineString(account)
	out.Path = path
	if ferr != nil {
		return out, ferr
	}
	if value != "" {
		if _, werr := config.WriteMachinePrivateValue(account, ""); werr != nil {
			return out, werr
		}
		out.MachineFile = true
	}
	return out, nil
}

// ResolveLicenseKey resolves the platform license key: the environment
// variable, then the keychain entry, then the machine-tier file.
func (s *Store) ResolveLicenseKey() (Result, error) {
	if v := strings.TrimSpace(s.getenv(EnvLicenseKey)); v != "" {
		return Result{Value: v, Source: SourceEnv}, nil
	}
	return s.Get(AccountLicenseKey)
}

// NormalizeLicenseKey trims surrounding whitespace and rejects a value that is
// empty or is not a single printable token. The error never quotes the input.
func NormalizeLicenseKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", errors.New("no license key on stdin")
	}
	for _, r := range key {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return "", errors.New("the license key must be a single token with no spaces or control characters")
		}
	}
	return key, nil
}
