package main

// The interactive session flow: choosing a connection (recents or new),
// and the SSH / copy-to / copy-from action loop against it.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/ktr0731/go-fuzzyfinder"

	"ttssh/internal/config"
	"ttssh/internal/ui"
	"ttssh/internal/vault"
)

// session is an active connection target.
type session struct {
	Key     string // key file path passed to ssh/scp -i
	Label   string // what to show for the key (path, or ☁ unit for vault keys)
	VaultID string // vault unit id when the key came from the vault
	User    string
	Host    string
}

func (s session) target() string { return s.User + "@" + s.Host }

// recentKey is what goes into the recents list: vault keys are stored as a
// "vault:<unit>" marker (re-fetched on use), local keys as their path.
func (s session) recentKey() string {
	if s.VaultID != "" {
		return vaultRecentPrefix + s.VaultID
	}
	return s.Key
}

// chooseSession offers recent connections (if any) or builds a new one.
// keyDir may be updated if the user switches folders in the key picker.
// vaultTempDir is threaded through to materializeVaultKey (see vaultcmd.go).
func chooseSession(ctx context.Context, cfg *config.Config, keyDir *string, vlt *vault.Client, vaultTempDir *string) (session, error) {
	for {
		if len(cfg.Recents) == 0 {
			return newSession(ctx, cfg, keyDir, vlt, vaultTempDir)
		}

		opts := make([]huh.Option[int], 0, len(cfg.Recents)+1)
		for i, r := range cfg.Recents {
			keyName := filepath.Base(r.Key)
			if id, ok := strings.CutPrefix(r.Key, vaultRecentPrefix); ok {
				keyName = "☁ " + id
			}
			label := fmt.Sprintf("%-26s  %-20s %s",
				r.User+"@"+r.Host, keyName, ui.RelTime(r.LastUsed))
			opts = append(opts, huh.NewOption(label, i))
		}
		opts = append(opts, huh.NewOption("＋ New connection", -1))

		idx, err := ui.SelectOne("Connect to", opts)
		if err != nil {
			return session{}, err
		}
		if idx == -1 {
			return newSession(ctx, cfg, keyDir, vlt, vaultTempDir)
		}

		r := cfg.Recents[idx]
		if unitID, ok := strings.CutPrefix(r.Key, vaultRecentPrefix); ok {
			if vlt == nil {
				ui.PrintWarn("This entry uses a vault key, but the vault is not configured (ttssh vault setup).")
				continue
			}
			path, err := materializeVaultKey(ctx, vlt, unitID, vaultTempDir)
			if err != nil {
				ui.PrintWarn(err.Error())
				if errors.Is(err, vault.ErrUnitNotFound) {
					cfg.RemoveRecent(r)
				}
				continue
			}
			return session{Key: path, Label: vaultLabel(unitID), VaultID: unitID, User: r.User, Host: r.Host}, nil
		}
		if _, err := os.Stat(r.Key); err != nil {
			ui.PrintWarn("Key file " + r.Key + " no longer exists — removing this entry.")
			cfg.RemoveRecent(r)
			continue
		}
		return session{Key: r.Key, Label: r.Key, User: r.User, Host: r.Host}, nil
	}
}

// newSession fuzzy-picks a key then asks for username and host.
func newSession(ctx context.Context, cfg *config.Config, keyDir *string, vlt *vault.Client, vaultTempDir *string) (session, error) {
	key, err := pickKey(ctx, cfg, keyDir, vlt, vaultTempDir)
	if err != nil {
		return session{}, err
	}

	var user, host string
	noSpaces := func(what string) func(string) error {
		return func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return fmt.Errorf("%s is required", what)
			}
			if strings.ContainsAny(s, " \t") {
				return fmt.Errorf("%s must not contain spaces", what)
			}
			return nil
		}
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Username").Placeholder("root").
			Validate(noSpaces("username")).Value(&user),
		huh.NewInput().Title("Host (IP or hostname)").Placeholder("192.168.1.10").
			Validate(noSpaces("host")).Value(&host),
	)).WithTheme(ui.HuhTheme())
	if err := form.Run(); err != nil {
		return session{}, err
	}

	return session{
		Key: key.Path, Label: key.Label, VaultID: key.VaultID,
		User: strings.TrimSpace(user), Host: strings.TrimSpace(host),
	}, nil
}

type action int

const (
	actionSSH action = iota
	actionCopyTo
	actionCopyFrom
	actionSwitch
	actionQuit
)

// actionLoop runs actions against sess until the user quits or switches
// connection. Returns true when the user wants to pick another connection.
func actionLoop(ctx context.Context, sess session) (bool, error) {
	for {
		fmt.Println()
		ui.PrintSessionCard(sess.target(), sess.Label)
		choice, err := ui.SelectOne(
			"What next?",
			[]huh.Option[action]{
				huh.NewOption("🖥  SSH into the remote host", actionSSH),
				huh.NewOption("📤 Copy a file TO the remote host", actionCopyTo),
				huh.NewOption("📥 Copy a file FROM the remote host", actionCopyFrom),
				huh.NewOption("🔁 Switch connection", actionSwitch),
				huh.NewOption("👋 Quit", actionQuit),
			})
		if err != nil {
			if isAbort(err) {
				return false, nil
			}
			return false, err
		}

		switch choice {
		case actionSwitch:
			return true, nil
		case actionQuit:
			return false, nil
		case actionSSH:
			err = runInteractive(ctx, "ssh", "-i", sess.Key, sess.target())
		case actionCopyTo:
			err = copyToRemote(ctx, sess)
		case actionCopyFrom:
			err = copyFromRemote(ctx, sess)
		}

		switch {
		case err == nil:
			// menu comes back around
		case isAbort(err):
			ui.PrintNote("Cancelled.")
		default:
			return false, err
		}
	}
}

// copyToRemote fuzzy-selects a local file and scp's it to the remote host.
func copyToRemote(ctx context.Context, sess session) error {
	startDir, err := ui.InputLine("Local directory to search", ".", false)
	if err != nil {
		return err
	}
	files, err := listLocalFiles(config.ExpandHome(startDir))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		ui.PrintWarn("No files found under " + startDir + ".")
		return nil
	}

	idx, err := fuzzyfinder.Find(files, func(i int) string {
		return files[i]
	}, fuzzyfinder.WithHeader("Select local file to copy to "+sess.target()))
	if err != nil {
		return err
	}
	local := files[idx]

	dest, err := ui.InputLine("Remote destination path", "~/", false)
	if err != nil {
		return err
	}
	if err := runInteractive(ctx, "scp", "-i", sess.Key, local, sess.target()+":"+dest); err != nil {
		return err
	}
	ui.PrintSuccess("Copied " + filepath.Base(local) + " to " + sess.target() + ":" + dest)
	return nil
}

// copyFromRemote lists remote files over ssh, fuzzy-selects one, and scp's it back.
func copyFromRemote(ctx context.Context, sess session) error {
	remoteDir, err := ui.InputLine("Remote directory to search", "~", false)
	if err != nil {
		return err
	}

	remote, err := pickRemoteFile(ctx, sess, remoteDir)
	if err != nil {
		return err
	}

	dest, err := ui.InputLine("Local destination path", ".", false)
	if err != nil {
		return err
	}
	if err := runInteractive(ctx, "scp", "-i", sess.Key, sess.target()+":"+remote, config.ExpandHome(dest)); err != nil {
		return err
	}
	ui.PrintSuccess("Copied " + sess.target() + ":" + remote + " to " + dest)
	return nil
}

// pickRemoteFile runs find on the remote host and fuzzy-selects from the result.
// If remote listing fails (e.g. no find command), it falls back to manual entry.
func pickRemoteFile(ctx context.Context, sess session, remoteDir string) (string, error) {
	ui.PrintNote("Listing remote files...")
	cmd := exec.CommandContext(ctx, "ssh", "-i", sess.Key, sess.target(),
		fmt.Sprintf("find %s -maxdepth 6 -type f 2>/dev/null", remoteDir))
	out, err := cmd.Output()
	files := splitLines(string(out))
	if err != nil || len(files) == 0 {
		ui.PrintWarn("Could not list remote files; enter the path manually.")
		return ui.InputLine("Remote file path", "", true)
	}

	idx, err := fuzzyfinder.Find(files, func(i int) string {
		return files[i]
	}, fuzzyfinder.WithHeader("Select remote file on "+sess.target()))
	if err != nil {
		return "", err
	}
	return files[idx], nil
}

// listLocalFiles walks dir collecting files, skipping dot-directories and
// common heavyweight directories so the list stays responsive.
func listLocalFiles(dir string) ([]string, error) {
	skip := map[string]bool{"node_modules": true, "vendor": true, "target": true}
	var files []string
	const limit = 50000

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && (strings.HasPrefix(name, ".") || skip[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, path)
		if len(files) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	return files, err
}

// runInteractive runs a command wired to the user's terminal (needed for
// interactive ssh sessions and scp progress output). A non-zero exit from
// the command is reported but not treated as a ttssh error, so the action
// menu comes back afterwards.
func runInteractive(ctx context.Context, name string, args ...string) error {
	ui.PrintCommand(name, args)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			ui.PrintWarn(fmt.Sprintf("%s exited with code %d", name, exitErr.ExitCode()))
			return nil
		}
		return fmt.Errorf("running %s: %w (is it installed and on PATH?)", name, err)
	}
	return nil
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
