// Package opencodeplugin embeds the Nightgauge OpenCode plugin (#1635): the
// entry module and its gate that give an OpenCode stage the same
// careful-gate every Claude Code stage runs through hooks.json, since
// OpenCode never reads Claude Code hooks and its only extension point is an
// in-process JS plugin (tool.execute.before, blocking a tool by throwing).
//
// Write copies the embedded plugin/ tree into the run's per-run OpenCode
// plugin directory — never installed from npm or Bun — so opencode 1.18.30
// loads it as a local plugin file, named directly in the per-run config's
// `plugin` array (internal/execution/adapters/opencode.go). A startup
// handshake (VerifyLoaded, VerifyNotLate) lets the manager's stream loop
// (internal/execution/manager.go) confirm the plugin actually initialized
// before any tool call could run, and fail the stage closed — never silently
// ungated — when it did not.
package opencodeplugin

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

//go:embed plugin
var pluginFS embed.FS

// pluginRoot is the go:embed directory name, stripped by Files/Write so
// callers see a tree rooted at nightgauge.js itself.
const pluginRoot = "plugin"

// PluginVersion is the handshake's plugin_version. It MUST match
// NIGHTGAUGE_PLUGIN_VERSION in plugin/nightgauge.js byte for byte
// (TestPluginVersionMatchesGo).
const PluginVersion = "1"

// EntryFile is the plugin file the per-run config's `plugin` array names.
const EntryFile = "nightgauge.js"

// HookNames are every hook nightgauge.js registers, in registration order:
// the handshake sentinel's "hooks" field, and what
// TestPluginRegistersGoldenHooks compares plugin/nightgauge.js's own
// NIGHTGAUGE_HOOK_NAMES against.
var HookNames = []string{
	"tool.execute.before",
	"tool.execute.after",
	"command.execute.before",
	"permission.ask",
	"event",
	"experimental.session.compacting",
	"experimental.compaction.autocontinue",
}

// Files returns the embedded plugin tree, rooted at nightgauge.js (i.e.
// Files().Open("nightgauge.js") and Files().Open("nightgauge/gates.js")).
func Files() (fs.FS, error) {
	return fs.Sub(pluginFS, pluginRoot)
}

// Write copies the embedded plugin tree into dir (created 0700; every file
// 0600) so opencode loads it as a local plugin file rather than an npm or
// Bun package. Returns the absolute path to EntryFile, the value the
// per-run config's `plugin` array names.
//
// Write always rewrites every file, so a plugin version upgrade between two
// runs that share a stale PluginDir never leaves an old file mixed in with
// the new one.
func Write(dir string) (string, error) {
	sub, err := Files()
	if err != nil {
		return "", fmt.Errorf("opencodeplugin: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("opencodeplugin: creating %s: %w", dir, err)
	}
	err = fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, readErr := fs.ReadFile(sub, path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		return "", fmt.Errorf("opencodeplugin: writing plugin into %s: %w", dir, err)
	}
	return filepath.Join(dir, EntryFile), nil
}

// NewNonce mints the handshake nonce: 128 random bits, hex-encoded.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("opencodeplugin: minting handshake nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// SentinelPath is the handshake sentinel's path for one run: beside
// outputFile when the dispatch has one (the plugin's own convention,
// ".opencode-plugin-<RUN_ID>.json"), else inside runDir — a dispatch with no
// output file (e.g. the `nightgauge opencode config` SDK-parity path, which
// never actually spawns opencode) still gets a deterministic, writable
// location rather than an empty path.
func SentinelPath(outputFile, runDir, runID string) string {
	name := fmt.Sprintf(".opencode-plugin-%s.json", runID)
	if outputFile != "" {
		return filepath.Join(filepath.Dir(outputFile), name)
	}
	return filepath.Join(runDir, name)
}

// DeleteStaleSentinel removes any sentinel left at path by an earlier spawn
// that shared the same root, so a stale file can never be misread as this
// run's handshake. A missing file is not an error.
func DeleteStaleSentinel(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("opencodeplugin: deleting stale sentinel %s: %w", path, err)
	}
	return nil
}

// Sentinel is the JSON handshake file the plugin's init writes.
type Sentinel struct {
	Nonce         string   `json:"nonce"`
	PluginVersion string   `json:"plugin_version"`
	Hooks         []string `json:"hooks"`
}

// Environment variable names opencode.go (adapters.OpenCodeAdapter.PrepareRunRoot)
// sets on the run and a manager reads back — a single Go-side source for
// both the nonce and the sentinel's path, so the plugin process (which reads
// these two variables verbatim) and the verifier can never disagree on a
// path formula reimplemented in two languages.
const (
	EnvNonce      = "NIGHTGAUGE_OPENCODE_PLUGIN_NONCE"
	EnvSentinel   = "NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL"
	EnvPluginPath = "NIGHTGAUGE_OPENCODE_PLUGIN_PATH"
	// EnvReadMaxLines is the most lines one read returns (#2178): gates.js
	// gives a read that names no limit, or a larger one, this limit.
	EnvReadMaxLines = "NIGHTGAUGE_OPENCODE_READ_MAX_LINES"
	// EnvOperatorInstallRisk names, when set, the operator-owned OpenCode
	// config directory ($HOME/.opencode, or an inherited OPENCODE_CONFIG_DIR)
	// this run's config puts at risk of OpenCode's own @opencode-ai/plugin
	// install waiting on the registry (#1635/A11 round 6, ADR-022 amendment
	// 2026-09-15, narrowed AC1: Nightgauge never seeds or merges into either
	// directory). adapters.operatorInstallRisk sets it (read-only — the
	// directory is never written); manager.go reads it back to bound the
	// wait and classify a stage that never produces output as
	// adapter_incompatible instead of an unclassified hang.
	EnvOperatorInstallRisk = "NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK"
)

// HandshakeConfig is what a manager needs to verify one opencode run's
// plugin handshake.
type HandshakeConfig struct {
	Nonce        string
	SentinelPath string
	PluginPath   string
}

// HandshakeConfigFromEnv reads a HandshakeConfig from a run's environment
// map (adapters.RunRoot.Env, or any map carrying the same keys). ok is false
// when the run carries no handshake at all: no run identity was minted for
// the dispatch, or the run is not an OpenCode dispatch.
func HandshakeConfigFromEnv(env map[string]string) (cfg HandshakeConfig, ok bool) {
	nonce := env[EnvNonce]
	sentinel := env[EnvSentinel]
	if nonce == "" || sentinel == "" {
		return HandshakeConfig{}, false
	}
	return HandshakeConfig{Nonce: nonce, SentinelPath: sentinel, PluginPath: env[EnvPluginPath]}, true
}

// incompatibleKind is adapters.OpenCodeIncompatible's value, duplicated
// (rather than imported, which would cycle: adapters already imports this
// package to write the plugin) so a failed handshake classifies through the
// same terminalkind table entry, which keys on the bare word
// "adapter_incompatible" anywhere in a stage's stderr reason.
const incompatibleKind = "adapter_incompatible"

// IncompatibleError is a failed handshake check: the plugin did not load,
// loaded late, or a stale/foreign sentinel was read.
type IncompatibleError struct {
	Reason     string
	PluginPath string
}

func (e *IncompatibleError) Error() string {
	msg := incompatibleKind + ": " + e.Reason
	if e.PluginPath != "" {
		msg += " (plugin: " + e.PluginPath + ")"
	}
	return msg
}

// Kind reports incompatibleKind, matching adapters.OpenCodeIncompatible.
func (e *IncompatibleError) Kind() string { return incompatibleKind }

// VerifyLoaded is the step_start check: an opencode run never emits
// tool_use before its first step_start event, so calling this the instant
// that event is observed proves the plugin had already run its init (and so
// written the sentinel) before any tool call could exist. The sentinel must
// carry this run's own nonce and the plugin version this binary embeds;
// anything else means the plugin failed to load, loaded late, or a
// stale/foreign sentinel is being read.
func VerifyLoaded(cfg HandshakeConfig) error {
	data, err := os.ReadFile(cfg.SentinelPath)
	if err != nil {
		return &IncompatibleError{
			Reason:     fmt.Sprintf("no plugin handshake sentinel at %s: the Nightgauge OpenCode plugin did not load (%v)", cfg.SentinelPath, err),
			PluginPath: cfg.PluginPath,
		}
	}
	var s Sentinel
	if err := json.Unmarshal(data, &s); err != nil {
		return &IncompatibleError{
			Reason:     fmt.Sprintf("the plugin handshake sentinel at %s did not parse as JSON: %v", cfg.SentinelPath, err),
			PluginPath: cfg.PluginPath,
		}
	}
	if s.Nonce != cfg.Nonce {
		return &IncompatibleError{
			Reason:     fmt.Sprintf("the plugin handshake sentinel at %s carries the wrong nonce: a stale or foreign sentinel was read", cfg.SentinelPath),
			PluginPath: cfg.PluginPath,
		}
	}
	if s.PluginVersion != PluginVersion {
		return &IncompatibleError{
			Reason:     fmt.Sprintf("the plugin handshake sentinel at %s names plugin_version %q, not the %q this binary embeds", cfg.SentinelPath, s.PluginVersion, PluginVersion),
			PluginPath: cfg.PluginPath,
		}
	}
	return nil
}

// VerifyNotLate re-checks the same sentinel once the run has exited: its
// mtime must precede firstToolUse, the moment a caller first observed a
// tool_use event on the run's stream. A late sentinel means some tool ran
// before this exact file was written — VerifyLoaded's step_start check alone
// cannot see that, since it only proves a sentinel existed by the first
// step_start, not that it stayed the one gating every tool after it.
//
// firstToolUse.IsZero() (no tool call was ever observed) always passes:
// there is nothing a late sentinel could have let through.
func VerifyNotLate(cfg HandshakeConfig, firstToolUse time.Time) error {
	if firstToolUse.IsZero() {
		return nil
	}
	info, err := os.Stat(cfg.SentinelPath)
	if err != nil {
		return &IncompatibleError{
			Reason:     fmt.Sprintf("the plugin handshake sentinel at %s is gone at exit: %v", cfg.SentinelPath, err),
			PluginPath: cfg.PluginPath,
		}
	}
	if info.ModTime().After(firstToolUse) {
		return &IncompatibleError{
			Reason:     fmt.Sprintf("the plugin handshake sentinel at %s was written after the run's first tool call started", cfg.SentinelPath),
			PluginPath: cfg.PluginPath,
		}
	}
	return nil
}
