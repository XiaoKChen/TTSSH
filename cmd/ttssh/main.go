package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/ktr0731/go-fuzzyfinder"

	"ttssh/internal/config"
	"ttssh/internal/ui"
	"ttssh/internal/vault"
)

const keyExt = ".key"

func main() {
	keyDirFlag := flag.String("key-dir", "", "directory to search for *.key files (overrides config, default ~/.ssh)")
	flag.Usage = usage
	flag.Parse()

	cfg := config.Load()

	switch flag.Arg(0) {
	case "set-key-dir":
		if flag.Arg(1) == "" {
			fatal("usage: ttssh set-key-dir <path>")
		}
		cfg.KeyDir = flag.Arg(1)
		if err := cfg.Save(); err != nil {
			fatal("saving config: %v", err)
		}
		fmt.Printf("Key directory set to %s\n", config.ExpandHome(cfg.KeyDir))
		return
	case "clear-recents":
		cfg.Recents = nil
		if err := cfg.Save(); err != nil {
			fatal("saving config: %v", err)
		}
		fmt.Println("Recent connections cleared.")
		return
	case "config":
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
		os.Exit(handleVaultCommand(&cfg, flag.Args()[1:]))
	case "":
		// fall through to interactive flow
	default:
		fatal("unknown command %q\n\nrun 'ttssh -h' for usage", flag.Arg(0))
	}

	defer cleanupVaultTemp()
	if err := run(&cfg, config.ResolveKeyDir(*keyDirFlag, cfg)); err != nil {
		if isAbort(err) {
			ui.PrintNote("Bye!")
			return
		}
		fatal("%v", err)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `ttssh - interactive SSH manager

Usage:
  ttssh                      start the interactive session
  ttssh vault list           list keys stored in the key vault
  ttssh vault pull           download vault keys to a folder ('ttssh vault' for details)
  ttssh vault setup          store vault credentials in config.json
  ttssh set-key-dir <path>   persist a custom key directory
  ttssh clear-recents        forget all recent connections
  ttssh config               show the effective configuration

Flags:
`)
	flag.PrintDefaults()
}

// isAbort reports whether the user backed out (Esc / Ctrl+C) of a prompt.
func isAbort(err error) bool {
	return errors.Is(err, huh.ErrUserAborted) || errors.Is(err, fuzzyfinder.ErrAbort)
}

func run(cfg *config.Config, keyDir string) error {
	vlt := connectVault(*cfg)
	ui.PrintBanner(keyDir, vlt != nil)

	for {
		sess, err := chooseSession(cfg, &keyDir, vlt)
		if err != nil {
			return err
		}
		cfg.AddRecent(config.Recent{User: sess.User, Host: sess.Host, Key: sess.recentKey()})

		switchConn, err := actionLoop(sess)
		if err != nil {
			return err
		}
		if !switchConn {
			ui.PrintNote("Bye!")
			return nil
		}
	}
}

func fatal(format string, args ...any) {
	cleanupVaultTemp() // os.Exit skips defers; don't leave decrypted keys behind
	fmt.Fprintf(os.Stderr, "ttssh: "+format+"\n", args...)
	os.Exit(1)
}
