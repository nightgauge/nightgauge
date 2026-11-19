package main

import (
	"context"
	"log"
	"sync"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// actionCenterBridge attaches the Action Center platform bridge (attention
// sync plus the platform agent) at most once per daemon (#1883).
//
// Two paths reach it: serve's startup, when a license key resolved, and
// platform.reloadLicense, when the editor obtains a device key after the
// daemon started. Both go through Attach, so a reload racing startup cannot
// attach a second uploader or register the agent twice.
type actionCenterBridge struct {
	mu       sync.Mutex
	attach   func(pc *platform.Client) bool
	attached bool
}

// Bind sets the attach step. serve binds it once the scheduler and the IPC
// server exist; until then Attach reports false and attaches nothing.
func (b *actionCenterBridge) Bind(attach func(pc *platform.Client) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attach = attach
}

// Attach runs the bound attach step for pc unless the bridge is already
// attached, and reports whether the bridge is attached afterwards. A step
// that declines (no scheduler, no attention store) leaves the bridge
// detached, so a later call may try again.
func (b *actionCenterBridge) Attach(pc *platform.Client) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.attached {
		return true
	}
	if b.attach == nil || pc == nil {
		return false
	}
	b.attached = b.attach(pc)
	return b.attached
}

// Attached reports whether the bridge is attached.
func (b *actionCenterBridge) Attached() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attached
}

// newLicenseReloader builds the platform.reloadLicense callback (#1883).
//
// resolve re-runs serve's own platform resolution (resolvePlatformConfig with
// the same flag/env values and the stored-key lookup), so a key reaches the
// daemon by exactly the precedence startup applies. The resolved key is
// adopted when the client holds none. A held key is replaced only by a key
// resolved from the OS keychain, where the editor stores an issued device key:
// that is how a key re-issued after the platform revoked or expired the old
// one reaches a running daemon. Resolution applies startup's precedence, so a
// flag or environment key resolves first and is never replaced. Once the client holds a key, the Action Center bridge is
// attached.
func newLicenseReloader(resolve func() resolvedPlatformConfig, bridge *actionCenterBridge) ipc.LicenseReloader {
	return func(_ context.Context, pc *platform.Client) (*ipc.PlatformReloadLicenseResult, error) {
		resolved := resolve()
		adopted := false
		if resolved.LicenseKey != "" {
			adopted = pc.AdoptLicenseKey(resolved.LicenseKey)
			if !adopted && resolved.Source == platformSourceKeychain {
				adopted = pc.ReplaceLicenseKey(resolved.LicenseKey)
			}
		}
		present := pc.LicenseKey() != ""
		source := string(resolved.Source)
		if adopted {
			log.Printf("serve: platform license reloaded (license=present, source=%s)", source)
		}
		attached := false
		if present {
			attached = bridge.Attach(pc)
		}
		return &ipc.PlatformReloadLicenseResult{
			LicensePresent: present,
			Adopted:        adopted,
			Source:         source,
			BridgeAttached: attached,
		}, nil
	}
}

// reloadServeConfig re-reads the workspace configuration so a reload sees a
// platform.enabled the user turned on after serve started. When the read
// fails, the configuration serve started with stands.
func reloadServeConfig(workspaceRoot string, started *config.Config) *config.Config {
	if workspaceRoot == "" {
		return started
	}
	cfg, err := config.Load(workspaceRoot)
	if err != nil {
		log.Printf("serve: license reload: config re-read failed in %s (%v); using the configuration serve started with", workspaceRoot, err)
		return started
	}
	if cfg == nil {
		return started
	}
	return cfg
}
