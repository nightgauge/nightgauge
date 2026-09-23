// Package configpath resolves the machine-tier config file's location.
//
// It is a leaf package so that packages internal/config imports (for example
// internal/github) can name the same file in their messages without an import
// cycle. internal/config's loader and every message that tells an operator
// where a machine-owned value belongs resolve the path here, so the two cannot
// disagree.
package configpath

import (
	"os"
	"path/filepath"
	"runtime"
)

// MachineConfigPath returns the absolute path of the machine-tier config file
// for the running platform. See ForGOOS for the resolution order.
func MachineConfigPath() (string, error) {
	return ForGOOS(runtime.GOOS)
}

// ForGOOS resolves the machine-tier config path as it would be on goos.
//
// Env-override parity with the TS globalConfigResolver
// (packages/nightgauge-vscode/src/utils/globalConfigResolver.ts):
// NIGHTGAUGE_CONFIG_HOME wins, then XDG_CONFIG_HOME/nightgauge, then the
// platform default (~/.config/nightgauge on Linux, %APPDATA%\nightgauge on
// Windows, ~/.nightgauge elsewhere). The overrides also let tests point the
// machine tier at a fixture directory instead of the developer's real file.
func ForGOOS(goos string) (string, error) {
	if dir := os.Getenv("NIGHTGAUGE_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "config.yaml"), nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "nightgauge", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch goos {
	case "linux":
		return filepath.Join(home, ".config", "nightgauge", "config.yaml"), nil
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "nightgauge", "config.yaml"), nil
	default:
		return filepath.Join(home, ".nightgauge", "config.yaml"), nil
	}
}
