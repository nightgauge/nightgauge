package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/nightgauge/nightgauge/internal/keychain"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// newLicenseStore is the store every license-key read and write goes through.
var newLicenseStore = keychain.New

// maxLicenseInput bounds what `auth license set` reads from stdin.
const maxLicenseInput = 16 << 10

func authLicenseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "license",
		Short: "Store, inspect or clear the platform license key",
		Long: `Manages the platform license key in the OS keychain (service "nightgauge",
account "platform.license_key") — macOS Keychain, Windows Credential Manager,
or the Secret Service on Linux. On a host with no keychain the key is kept in
the machine-tier config file (mode 0600) instead.

The CLI and the daemon resolve the key in this order:
  1. NIGHTGAUGE_LICENSE_KEY
  2. the OS keychain entry
  3. platform.license_key in the machine-tier config file

The key is never printed.`,
	}
	cmd.AddCommand(authLicenseSetCmd(), authLicenseStatusCmd(), authLicenseClearCmd())
	return cmd
}

// licenseSetResult is the `auth license set --json` shape. It never carries
// the key.
type licenseSetResult struct {
	Source        keychain.Source `json:"source"`
	Path          string          `json:"path,omitempty"`
	KeychainError string          `json:"keychainError,omitempty"`
}

func authLicenseSetCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Store the license key read from stdin",
		Long: `Reads the license key from stdin and stores it in the OS keychain. The key is
never accepted as an argument, because arguments are visible to every process
on the machine. At a terminal the key is read without echo.`,
		Example:      `  printf '%s' "$KEY" | nightgauge auth license set`,
		Args:         noLicenseArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := readLicenseInput(cmd)
			if err != nil {
				return err
			}
			key, err := keychain.NormalizeLicenseKey(raw)
			if err != nil {
				return err
			}
			res, err := newLicenseStore().Set(keychain.AccountLicenseKey, key)
			if err != nil {
				return fmt.Errorf("store license key: %w", err)
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				r := licenseSetResult{Source: res.Source, Path: res.Path}
				if res.KeychainErr != nil {
					r.KeychainError = res.KeychainErr.Error()
				}
				return json.NewEncoder(out).Encode(r)
			}
			switch res.Source {
			case keychain.SourceKeychain:
				fmt.Fprintf(out, "License key stored in the OS keychain (service %q, account %q).\n",
					keychain.Service, keychain.AccountLicenseKey)
			default:
				fmt.Fprintf(cmd.ErrOrStderr(), "No OS keychain is available: %v\n", res.KeychainErr)
				fmt.Fprintf(out, "License key stored in %s (mode 0600) instead.\n", res.Path)
			}
			if os.Getenv(keychain.EnvLicenseKey) != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Note: %s is set in this environment and takes precedence.\n",
					keychain.EnvLicenseKey)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output where the key was stored as JSON")
	return cmd
}

// noLicenseArgs rejects a key passed as an argument without quoting it back.
func noLicenseArgs(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return errors.New("the license key is read from stdin, never from arguments: printf '%s' \"$KEY\" | nightgauge auth license set")
	}
	return nil
}

// readLicenseInput reads the key from stdin: without echo at a terminal,
// otherwise the whole (bounded) stream.
func readLicenseInput(cmd *cobra.Command) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), "Paste the license key, then press Enter: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", fmt.Errorf("read license key: %w", err)
		}
		return string(b), nil
	}
	b, err := io.ReadAll(io.LimitReader(in, maxLicenseInput+1))
	if err != nil {
		return "", fmt.Errorf("read license key: %w", err)
	}
	if len(b) > maxLicenseInput {
		return "", errors.New("stdin is too large to be a license key")
	}
	return string(b), nil
}

// licenseStatus is the `auth license status --json` shape. It never carries
// the key.
type licenseStatus struct {
	Source            keychain.Source `json:"source"`
	Path              string          `json:"path,omitempty"`
	KeychainAvailable bool            `json:"keychainAvailable"`
	KeychainError     string          `json:"keychainError,omitempty"`
}

func authLicenseStatusCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Print where the license key comes from, never the key",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := newLicenseStore().ResolveLicenseKey()
			if err != nil {
				return fmt.Errorf("resolve license key: %w", err)
			}
			st := licenseStatus{Source: res.Source, Path: res.Path, KeychainAvailable: res.KeychainErr == nil}
			if res.KeychainErr != nil {
				st.KeychainError = res.KeychainErr.Error()
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				return json.NewEncoder(out).Encode(st)
			}
			fmt.Fprintf(out, "source: %s\n", st.Source)
			if st.Path != "" {
				fmt.Fprintf(out, "path: %s\n", st.Path)
			}
			if st.KeychainError != "" {
				fmt.Fprintf(out, "keychain: unavailable (%s)\n", st.KeychainError)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func authLicenseClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "clear",
		Short:        "Delete the stored license key from the keychain and the machine-tier file",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			removed, err := newLicenseStore().Delete(keychain.AccountLicenseKey)
			out := cmd.OutOrStdout()
			if removed.Keychain {
				fmt.Fprintln(out, "Removed the license key from the OS keychain.")
			}
			if removed.MachineFile {
				fmt.Fprintf(out, "Removed platform.license_key from %s.\n", removed.Path)
			}
			if err != nil {
				return fmt.Errorf("clear license key: %w", err)
			}
			if removed.KeychainErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: the OS keychain could not be reached (%v); an entry may remain there.\n",
					removed.KeychainErr)
			}
			if !removed.Keychain && !removed.MachineFile && removed.KeychainErr == nil {
				fmt.Fprintln(out, "No stored license key to remove.")
			}
			if os.Getenv(keychain.EnvLicenseKey) != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Note: %s is still set in this environment.\n", keychain.EnvLicenseKey)
			}
			return nil
		},
	}
}
