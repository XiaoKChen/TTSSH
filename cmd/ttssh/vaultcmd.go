package main

// Vault user-facing pieces: session temp keys, the download-to-folder flow,
// and the `ttssh vault <list|pull|setup>` subcommands.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"

	"ttssh/internal/config"
	"ttssh/internal/ui"
	"ttssh/internal/vault"
)

// connectVault opens the vault if it is configured. It returns nil, nil when
// no vault is configured; on a misconfigured vault the caller warns and the
// local-keys flow keeps working.
func connectVault(cfg config.Config) (*vault.Client, error) {
	vc := vault.ResolveConfig(cfg)
	if !vc.Configured() {
		return nil, nil
	}
	return vault.Open(vc)
}

// reconnectVault replaces the vault client after the settings changed; a
// failed connect leaves the vault off so stale credentials are not used.
func (m *model) reconnectVault() error {
	vlt, err := connectVault(*m.cfg)
	m.vlt = vlt
	return err
}

// ---- session temp keys ----

// Permissions for the session-only decrypted key material: keyDirPerm on the
// temp directory holding the keys, keyFilePerm on each key file (and on the
// files 'vault pull' saves permanently).
const (
	keyDirPerm  = 0o700
	keyFilePerm = 0o600
)

// Session keys live in ~/.ssh/tmp/ttssh-vault-*, not the system temp dir:
// on Windows chmod does not touch ACLs, so key files inherit their folder's
// ACL, and OpenSSH rejects keys that anyone else can read. %TEMP% often
// grants extra groups access, while ~/.ssh is normally private to the user.
const (
	sessionKeyParent    = "tmp"
	sessionKeyDirPrefix = "ttssh-vault-"
)

// writeSessionKey writes unitID's decrypted key to the session temp
// directory. *tempDir holds that directory's path across calls so the temp
// dir is created at most once per run; the caller (main) owns it and is
// responsible for calling cleanupVaultTemp on exit. The dashboard fetches
// the key in a tea.Cmd and calls this only from Update.
func writeSessionKey(tempDir *string, unitID string, key []byte) (string, error) {
	if *tempDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locating home directory: %w", err)
		}
		parent := filepath.Join(home, ".ssh", sessionKeyParent)
		if err := os.MkdirAll(parent, keyDirPerm); err != nil {
			return "", fmt.Errorf("creating %s: %w", parent, err)
		}
		dir, err := os.MkdirTemp(parent, sessionKeyDirPrefix)
		if err != nil {
			return "", fmt.Errorf("creating session key dir: %w", err)
		}
		if err := os.Chmod(dir, keyDirPerm); err != nil {
			_ = os.RemoveAll(dir) // best-effort cleanup of a dir we're abandoning
			return "", err
		}
		*tempDir = dir
	}
	path := filepath.Join(*tempDir, unitID+".key")
	if err := os.WriteFile(path, key, keyFilePerm); err != nil {
		return "", fmt.Errorf("writing session key file: %w", err)
	}
	return path, nil
}

// cleanupVaultTemp removes the session temp dir, if one was created, and
// clears *tempDir. Safe to call even when no key was ever materialized.
func cleanupVaultTemp(tempDir *string) {
	if *tempDir != "" {
		_ = os.RemoveAll(*tempDir) // best-effort cleanup on exit
		// Best-effort: drops ~/.ssh/tmp only when empty, so another running
		// ttssh's keys (or the user's own files) are left alone.
		_ = os.Remove(filepath.Dir(*tempDir))
		*tempDir = ""
	}
}

func vaultLabel(unitID string) string { return "☁ " + unitID + " (vault)" }

const vaultRecentPrefix = "vault:"

// ---- download to a folder ----

// vaultPullInteractive multi-selects units and saves them as <unit>.key files
// into a folder chosen with the folder browser.
func vaultPullInteractive(ctx context.Context, v *vault.Client, startDir string) error {
	units, err := v.ListUnits(ctx)
	if err != nil {
		return err
	}
	if len(units) == 0 {
		ui.PrintWarn("The vault has no keys.")
		return nil
	}

	var selected []string
	err = huh.NewMultiSelect[string]().
		Title("Download which keys?").
		Description("Space toggles · a toggles all · Enter confirms").
		Options(unitOptions(units)...).
		Value(&selected).
		WithTheme(ui.HuhTheme()).
		Run()
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		ui.PrintNote("Nothing selected.")
		return nil
	}

	ui.PrintNote("Where should the keys be saved?")
	dir, err := browseDir(ctx, startDir)
	if err != nil {
		return err
	}

	saved, err := saveVaultKeys(ctx, v, selected, dir, confirmOverwrite, cliNotifier())
	if saved > 0 {
		ui.PrintSuccess(fmt.Sprintf("Saved %d key file(s) to %s", saved, dir))
	}
	return err
}

// unitOptions lists vault units for a multi-select, marking revoked ones.
func unitOptions(units []vault.Unit) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(units))
	for _, u := range units {
		label := fmt.Sprintf("%-24s  %s", u.UnitID, u.Fingerprint)
		if u.Revoked() {
			label += "  (revoked)"
		}
		opts = append(opts, huh.NewOption(label, u.UnitID))
	}
	return opts
}

// overwriteConfirm asks before replacing the existing file dest.
func overwriteConfirm(dest string, ok *bool) *huh.Confirm {
	return huh.NewConfirm().
		Title(filepath.Base(dest) + " already exists — overwrite?").
		Description(dest).
		Affirmative("Overwrite").
		Negative("Skip").
		Value(ok)
}

// confirmOverwrite asks before replacing an existing file (interactive flow).
func confirmOverwrite(dest string) (bool, error) {
	ok := false
	err := overwriteConfirm(dest, &ok).WithTheme(ui.HuhTheme()).Run()
	return ok, err
}

// saveNotifier receives saveVaultKeys' per-file messages: the CLI prints
// them, the dashboard collects them for its status line.
type saveNotifier struct {
	saved, skipped, failed func(msg string)
}

func cliNotifier() saveNotifier {
	return saveNotifier{saved: ui.PrintSuccess, skipped: ui.PrintNote, failed: ui.PrintWarn}
}

// saveVaultKeys fetches, decrypts, and writes each unit to dir as
// <unit>.key (0600). onConflict decides what happens to existing files;
// nil means "refuse". Returns how many files were written.
func saveVaultKeys(ctx context.Context, v *vault.Client, unitIDs []string, dir string, onConflict func(string) (bool, error), notify saveNotifier) (int, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return 0, fmt.Errorf("destination folder %s does not exist", dir)
	}
	saved := 0
	for _, id := range unitIDs {
		dest := filepath.Join(dir, id+".key")
		if _, err := os.Stat(dest); err == nil {
			if onConflict == nil {
				notify.failed(dest + " already exists — skipping (use -force to overwrite)")
				continue
			}
			ok, err := onConflict(dest)
			if err != nil {
				return saved, err
			}
			if !ok {
				notify.skipped("Skipped " + id + ".")
				continue
			}
		}
		key, err := v.FetchKey(ctx, id)
		if err != nil {
			notify.failed(err.Error())
			continue
		}
		if err := os.WriteFile(dest, key, keyFilePerm); err != nil {
			return saved, fmt.Errorf("writing %s: %w", dest, err)
		}
		notify.saved("Saved " + dest)
		saved++
	}
	return saved, nil
}

// ---- ttssh vault subcommands ----

func vaultUsage() {
	fmt.Fprint(os.Stderr, `usage:
  ttssh vault list                      list keys stored in the vault
  ttssh vault pull [-out DIR] [-force] [UNIT ...]
                                        download keys as <unit>.key files
                                        (no units: interactive multi-select;
                                         no -out: interactive folder browser)
  ttssh vault setup                     store vault credentials in config.json

Credentials are read from DB_URL / DB_TOKEN / MASTER_KEY_V1_HEX / DB_CA_CERT
(environment or a .env in the current directory), falling back to the values
saved by 'ttssh vault setup'.
`)
}

// handleVaultCommand runs `ttssh vault <sub>` and returns the process exit code.
func handleVaultCommand(ctx context.Context, cfg *config.Config, args []string) int {
	if len(args) == 0 {
		vaultUsage()
		return 2
	}
	sub, rest := args[0], args[1:]

	if sub == "setup" {
		if err := vaultSetup(ctx, cfg); err != nil {
			if isAbort(err) {
				ui.PrintNote("Cancelled.")
				return 0
			}
			fmt.Fprintf(os.Stderr, "ttssh: %v\n", err)
			return 1
		}
		return 0
	}

	v, err := vault.Open(vault.ResolveConfig(*cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ttssh: %v\n", err)
		return 1
	}

	switch sub {
	case "list":
		units, err := v.ListUnits(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ttssh: %v\n", err)
			return 1
		}
		if len(units) == 0 {
			fmt.Println("vault is empty")
			return 0
		}
		fmt.Printf("%-24s  %-50s  %-19s  %s\n", "UNIT", "FINGERPRINT", "CREATED", "STATUS")
		for _, u := range units {
			status := "active"
			if u.Revoked() {
				status = "revoked"
			}
			fmt.Printf("%-24s  %-50s  %-19s  %s\n", u.UnitID, u.Fingerprint, u.CreatedAt, status)
		}
		return 0

	case "pull":
		fs := flag.NewFlagSet("vault pull", flag.ContinueOnError)
		out := fs.String("out", "", "destination folder (interactive browser when omitted)")
		force := fs.Bool("force", false, "overwrite existing files without asking")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		ids := fs.Args()
		for _, id := range ids {
			if !vault.ValidUnitID(id) {
				fmt.Fprintf(os.Stderr, "ttssh: invalid unit id %q\n", id)
				return 2
			}
		}
		if err := vaultPull(ctx, cfg, v, ids, *out, *force); err != nil {
			if isAbort(err) {
				ui.PrintNote("Cancelled.")
				return 0
			}
			fmt.Fprintf(os.Stderr, "ttssh: %v\n", err)
			return 1
		}
		return 0

	default:
		vaultUsage()
		return 2
	}
}

// vaultPull is the command-line download: fully non-interactive when both
// unit ids and -out are given, interactive where information is missing.
func vaultPull(ctx context.Context, cfg *config.Config, v *vault.Client, ids []string, out string, force bool) error {
	if len(ids) == 0 && out == "" {
		return vaultPullInteractive(ctx, v, config.ResolveKeyDir("", *cfg))
	}

	if len(ids) == 0 {
		units, err := v.ListUnits(ctx)
		if err != nil {
			return fmt.Errorf("listing vault keys: %w", err)
		}
		if err := huh.NewMultiSelect[string]().
			Title("Download which keys?").
			Options(unitOptions(units)...).
			Value(&ids).
			WithTheme(ui.HuhTheme()).
			Run(); err != nil {
			return err
		}
		if len(ids) == 0 {
			ui.PrintNote("Nothing selected.")
			return nil
		}
	}

	dir := config.ExpandHome(out)
	if dir == "" {
		var err error
		if dir, err = browseDir(ctx, config.ResolveKeyDir("", *cfg)); err != nil {
			return err
		}
	}

	onConflict := func(string) (bool, error) { return true, nil } // -force
	if !force {
		onConflict = nil // refuse, with a hint
	}
	saved, err := saveVaultKeys(ctx, v, ids, dir, onConflict, cliNotifier())
	if err != nil {
		return err
	}
	if saved == 0 {
		return errors.New("no files were written")
	}
	fmt.Printf("saved %d key file(s) to %s\n", saved, dir)
	return nil
}

const masterKeyHexLen = 64

func validateVaultURL(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("required")
	}
	if !strings.HasPrefix(s, "libsql://") && !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return errors.New("must start with libsql:// or https://")
	}
	return nil
}

func validateMasterKeyHex(s string) error {
	s = strings.TrimSpace(s)
	if len(s) != masterKeyHexLen {
		return fmt.Errorf("must be exactly %d hex characters", masterKeyHexLen)
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return errors.New("must be hex")
		}
	}
	return nil
}

// vaultSetup interactively collects and persists vault credentials. Secrets
// land in config.json in plain text (same trust level as the uploader's
// .env); prefer environment variables if that is a concern.
func vaultSetup(ctx context.Context, cfg *config.Config) error {
	vc := vault.ResolveConfig(*cfg)
	url, token, masterHex, caCert := vc.URL, vc.Token, vc.MasterKeyHex, vc.CACert

	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Database URL").
			Description("libsql://… or https://… (the uploader's DB_URL)").
			Placeholder("libsql://your-db.turso.io").
			Validate(validateVaultURL).Value(&url),
		huh.NewInput().Title("Auth token (DB_TOKEN)").
			EchoMode(huh.EchoModePassword).
			Value(&token),
		huh.NewInput().Title("Master key (MASTER_KEY_V1_HEX, 64 hex chars)").
			EchoMode(huh.EchoModePassword).
			Validate(validateMasterKeyHex).Value(&masterHex),
		huh.NewInput().Title("Private CA certificate path (optional)").
			Description("Leave empty unless the server uses a private CA").
			Value(&caCert),
	)).WithTheme(ui.HuhTheme())
	if err := form.Run(); err != nil {
		return err
	}

	candidate := config.VaultConfig{
		URL:          strings.TrimSpace(url),
		Token:        strings.TrimSpace(token),
		MasterKeyHex: strings.TrimSpace(masterHex),
		CACert:       strings.TrimSpace(caCert),
	}

	ui.PrintNote("Testing the connection…")
	v, err := vault.Open(candidate)
	if err != nil {
		return fmt.Errorf("connecting to vault: %w", err)
	}
	units, err := v.ListUnits(ctx)
	if err != nil {
		return fmt.Errorf("listing vault keys: %w", err)
	}
	ui.PrintSuccess(fmt.Sprintf("Connected — %d key(s) in the vault.", len(units)))

	cfg.Vault = candidate
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	path, err := config.Path()
	if err != nil {
		path = "(unknown)"
	}
	ui.PrintSuccess("Vault settings saved to " + path)
	ui.PrintWarn("The token and master key are stored there in plain text — protect that file, or use environment variables instead.")
	return nil
}
