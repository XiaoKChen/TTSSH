// Command ttssh is an interactive SSH connection manager for the terminal.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"ttssh/internal/config"
	"ttssh/internal/ui"
	"ttssh/internal/vault"
)

const keyExt = ".key"

// version is set at build time via -ldflags "-X main.version=<value>".
var version = "dev"

// resolveVersion falls back to the main module version for `go install`
// builds, which don't go through the ldflags-injecting build scripts.
func resolveVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func main() {
	keyDirFlag := flag.String("key-dir", "", "directory to search for *.key files (overrides config, default ~/.ssh)")
	flag.Usage = usage
	flag.Parse()

	cfg := config.Load()
	ver := resolveVersion()

	// A background root context: the Bubble Tea UI and huh prompts read
	// Ctrl+C as a key (the terminal is in raw mode), and while ssh/scp run
	// via tea.Exec Bubble Tea ignores SIGINT, so an interactive session
	// receives Ctrl+C itself rather than have it kill the child process via
	// context cancellation. No signal.NotifyContext here, intentionally.
	ctx := context.Background()
	var vaultTempDir string

	switch flag.Arg(0) {
	case "set-key-dir":
		if flag.Arg(1) == "" {
			fatal(&vaultTempDir, "usage: ttssh set-key-dir <path>")
		}
		cfg.KeyDir = flag.Arg(1)
		if err := cfg.Save(); err != nil {
			fatal(&vaultTempDir, "saving config: %v", err)
		}
		fmt.Printf("Key directory set to %s\n", config.ExpandHome(cfg.KeyDir))
		return
	case "clear-recents":
		cfg.Recents = nil
		if err := cfg.Save(); err != nil {
			fatal(&vaultTempDir, "saving config: %v", err)
		}
		fmt.Println("Recent connections cleared.")
		return
	case "config":
		fmt.Printf("Version:            %s\n", ver)
		fmt.Printf("Key directory:      %s\n", config.ResolveKeyDir(*keyDirFlag, cfg))
		fmt.Printf("Recent connections: %d\n", len(cfg.Recents))
		vc := vault.ResolveConfig(cfg)
		if vc.Configured() {
			fmt.Printf("Vault:              %s\n", vc.URL)
		} else {
			fmt.Printf("Vault:              not configured (ttssh vault setup)\n")
		}
		return
	case "vault":
		os.Exit(handleVaultCommand(ctx, &cfg, flag.Args()[1:]))
	case "":
		// fall through to interactive flow
	default:
		fatal(&vaultTempDir, "unknown command %q\n\nrun 'ttssh -h' for usage", flag.Arg(0))
	}

	defer cleanupVaultTemp(&vaultTempDir)
	if err := run(ctx, &cfg, config.ResolveKeyDir(*keyDirFlag, cfg), &vaultTempDir, ver); err != nil && !isAbort(err) {
		fatal(&vaultTempDir, "%v", err)
	}
	ui.PrintNote("Bye!")
}

func usage() {
	fmt.Fprintf(os.Stderr, `ttssh %s - interactive SSH manager

Usage:
  ttssh                      start the interactive session
  ttssh vault list           list keys stored in the key vault
  ttssh vault pull           download vault keys to a folder ('ttssh vault' for details)
  ttssh vault setup          store vault credentials in config.json
  ttssh set-key-dir <path>   persist a custom key directory
  ttssh clear-recents        forget all recent connections
  ttssh config               show the effective configuration

Flags:
`, resolveVersion())
	flag.PrintDefaults()
}

// isAbort reports whether the user backed out (Esc / Ctrl+C) of a prompt or
// the folder browser, or the UI was interrupted by SIGINT.
func isAbort(err error) bool {
	return errors.Is(err, huh.ErrUserAborted) || errors.Is(err, errBrowseCancelled) ||
		errors.Is(err, tea.ErrInterrupted)
}

func fatal(vaultTempDir *string, format string, args ...any) {
	cleanupVaultTemp(vaultTempDir) // os.Exit skips defers; don't leave decrypted keys behind
	fmt.Fprintf(os.Stderr, "ttssh: "+format+"\n", args...)
	os.Exit(1)
}
