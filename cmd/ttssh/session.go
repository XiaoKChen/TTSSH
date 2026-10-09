package main

// The dashboard screen and the session flows that start from it: resolving
// a connection's key, ssh, and copying files to/from the remote host.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"ttssh/internal/config"
	"ttssh/internal/ui"
	"ttssh/internal/vault"
)

// session is a connection target with its key file resolved.
type session struct {
	Key  string // key file path passed to ssh/scp -i; empty means password login
	User string
	Host string
}

func (s session) target() string { return s.User + "@" + s.Host }

// keyArgs is the -i option for ssh/scp; password sessions have none.
func (s session) keyArgs() []string {
	if s.Key == "" {
		return nil
	}
	return []string{"-i", s.Key}
}

// run shows the dashboard until the user quits.
func run(ctx context.Context, cfg *config.Config, keyDir string, vaultTempDir *string, version string) error {
	vlt, vaultErr := connectVault(*cfg)
	m := newModel(ctx, cfg, vlt, vaultTempDir, keyDir, version)
	startup := []tea.Cmd{m.pushDashboard()}
	if vaultErr != nil {
		startup = append(startup, m.setStatus(statusWarn, "vault: "+vaultErr.Error()))
	}
	m.startup = tea.Batch(startup...)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// ---- connecting ----

// connect resolves the key of r, fetching vault keys into the session temp
// dir, bumps r to the top of the recents, and continues with then. A recent
// whose key is gone is pruned; a saved entry only gets a warning.
func (m *model) connect(r config.Recent, saved bool, then func(session) tea.Cmd) tea.Cmd {
	if r.Key == "" {
		return m.useRecent(r, saved, "", then)
	}
	unitID, isVault := strings.CutPrefix(r.Key, vaultRecentPrefix)
	if !isVault {
		if _, err := os.Stat(r.Key); err != nil {
			if saved {
				return m.setStatus(statusWarn, "Key file "+r.Key+" no longer exists.")
			}
			m.cfg.RemoveRecent(r)
			return tea.Batch(m.refreshDashboard(""),
				m.setStatus(statusWarn, "Key file "+r.Key+" no longer exists — removed this entry."))
		}
		return m.useRecent(r, saved, r.Key, then)
	}
	if m.vlt == nil {
		return m.setStatus(statusWarn, "This entry uses a vault key, but the vault is not configured (ttssh vault setup).")
	}

	vlt := m.vlt
	return m.startOp("Fetching "+unitID+" from the vault…", true, func(ctx context.Context) func(*model) tea.Cmd {
		keyData, err := vlt.FetchKey(ctx, unitID)
		return func(m *model) tea.Cmd {
			if err != nil {
				cmd := m.setStatus(statusWarn, err.Error())
				if !saved && errors.Is(err, vault.ErrUnitNotFound) {
					m.cfg.RemoveRecent(r)
					cmd = tea.Batch(cmd, m.refreshDashboard(""))
				}
				return cmd
			}
			path, err := writeSessionKey(m.tempDir, unitID, keyData)
			if err != nil {
				return m.setStatus(statusError, err.Error())
			}
			return m.useRecent(r, saved, path, then)
		}
	})
}

// useRecent records r as the latest connection. A recent follows its row to
// the top of the recents; a saved entry keeps the selection where it is.
func (m *model) useRecent(r config.Recent, saved bool, keyPath string, then func(session) tea.Cmd) tea.Cmd {
	m.cfg.AddRecent(r)
	selectID := ""
	if !saved {
		selectID = item{kind: itemRecent, recent: r}.id()
	}
	return tea.Batch(m.refreshDashboard(selectID), then(session{Key: keyPath, User: r.User, Host: r.Host}))
}

func (m *model) confirmKey(msg tea.KeyMsg) tea.Cmd {
	c := m.confirming
	switch {
	case key.Matches(msg, m.keys.Yes):
		m.confirming = nil
		return c.onYes(m)
	case key.Matches(msg, m.keys.No):
		m.confirming = nil
	}
	return nil
}

func (m *model) sshInto(sess session) tea.Cmd {
	return m.runTerminal("", "ssh", append(sess.keyArgs(), sess.target())...)
}

// ---- copy TO the remote host ----

// startUpload asks for a local folder, lists its files, asks for the remote
// destination, and scp's the chosen file.
func (m *model) startUpload(sess session) tea.Cmd {
	var dir string
	return m.push(formScreen(inputForm("Local directory to search", ".", &dir, nil), func(m *model) tea.Cmd {
		root := config.ExpandHome(orDefault(dir, "."))
		files, err := listLocalFiles(root)
		if err != nil {
			return m.setStatus(statusError, err.Error())
		}
		if len(files) == 0 {
			return m.setStatus(statusWarn, "No files found under "+root+".")
		}
		items := make([]item, len(files))
		for i, f := range files {
			items[i] = item{kind: itemFile, label: f, value: f}
		}
		return m.push(m.fileList("Upload to "+sess.target(), items, localFileDetails, func(m *model, local string) tea.Cmd {
			var dest string
			form := inputForm("Remote destination path", "~/", &dest, nil)
			return m.push(formScreen(form, func(m *model) tea.Cmd {
				remote := orDefault(dest, "~/")
				m.popTo(1)
				return m.runTerminal("Copied "+filepath.Base(local)+" to "+sess.target()+":"+remote,
					"scp", append(sess.keyArgs(), local, sess.target()+":"+remote)...)
			}))
		}))
	}))
}

// fileList is a filterable list of paths; enter hands the chosen one to choose.
func (m *model) fileList(title string, items []item, details func(item) string, choose func(m *model, path string) tea.Cmd) *screen {
	s := &screen{
		kind:      screenFiles,
		list:      newList(title, "file", "files"),
		details:   details,
		enterDesc: func(item) string { return "select" },
	}
	s.setItems(items)
	s.onKey = func(m *model, msg tea.KeyMsg) (tea.Cmd, bool) {
		if !key.Matches(msg, m.keys.Enter) {
			return nil, false
		}
		if it := s.selected(); it.kind != itemNone {
			return choose(m, it.value), true
		}
		return nil, true
	}
	return s
}

// localFileDetails describes a local file (or key file) for the details pane.
func localFileDetails(it item) string {
	info, err := os.Stat(it.value)
	if err != nil {
		return it.value + "\n\n" + ui.MutedStyle.Render("(unreadable)")
	}
	return it.value + "\n\n" +
		ui.MutedStyle.Render("size      ") + fmt.Sprintf("%d bytes", info.Size()) + "\n" +
		ui.MutedStyle.Render("modified  ") + info.ModTime().Format("2006-01-02 15:04")
}

// ---- copy FROM the remote host ----

// startDownload asks for a remote folder, lists its files over ssh (falling
// back to typing the path), asks for the local destination, and scp's it.
func (m *model) startDownload(sess session) tea.Cmd {
	var dir string
	return m.push(formScreen(inputForm("Remote directory to search", "~", &dir, nil), func(m *model) tea.Cmd {
		remoteDir := orDefault(dir, "~")
		return m.startOp("Listing files on "+sess.target()+"…", true, func(ctx context.Context) func(*model) tea.Cmd {
			files, _ := listRemoteFiles(ctx, sess, remoteDir) // any failure ends in the manual fallback below
			return func(m *model) tea.Cmd {
				if len(files) == 0 {
					return tea.Batch(m.setStatus(statusWarn, listFailure(sess)), m.askRemotePath(sess))
				}
				items := make([]item, len(files))
				for i, f := range files {
					items[i] = item{kind: itemFile, label: f, value: f}
				}
				details := func(it item) string { return sess.target() + ":" + it.value }
				return m.push(m.fileList("Download from "+sess.target(), items, details, func(m *model, remote string) tea.Cmd {
					return m.askLocalDest(sess, remote)
				}))
			}
		})
	}))
}

func (m *model) askRemotePath(sess session) tea.Cmd {
	var path string
	required := func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("required")
		}
		return nil
	}
	return m.push(formScreen(inputForm("Remote file path", "", &path, required), func(m *model) tea.Cmd {
		return m.askLocalDest(sess, strings.TrimSpace(path))
	}))
}

func (m *model) askLocalDest(sess session, remote string) tea.Cmd {
	var dest string
	return m.push(formScreen(inputForm("Local destination path", ".", &dest, nil), func(m *model) tea.Cmd {
		local := orDefault(dest, ".")
		m.popTo(1)
		return m.runTerminal("Copied "+sess.target()+":"+remote+" to "+local,
			"scp", append(sess.keyArgs(), sess.target()+":"+remote, config.ExpandHome(local))...)
	}))
}

// listRemoteFiles runs find on the remote host. BatchMode stops ssh from
// prompting (password, passphrase, host key) while the TUI owns the
// terminal; such hosts fall back to typing the path.
func listRemoteFiles(ctx context.Context, sess session, remoteDir string) ([]string, error) {
	args := append([]string{"-o", "BatchMode=yes"}, sess.keyArgs()...)
	args = append(args, sess.target(), fmt.Sprintf("find %s -maxdepth 6 -type f 2>/dev/null", remoteDir))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := cmd.Output()
	return splitLines(string(out)), err
}

// listFailure explains why the remote listing fell back to typing a path.
func listFailure(sess session) string {
	if sess.Key == "" {
		return "Can't list files on a password login (ssh can't ask for it here); enter the path manually."
	}
	return "Could not list remote files; enter the path manually."
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

// ---- settings and config.json ----

const (
	visualEnv        = "VISUAL"
	editorEnv        = "EDITOR"
	windowsEditor    = "notepad"
	unixEditor       = "vi"
	configEditedNote = "Opening config.json — save and quit the editor to return."
)

// configEditedMsg reports that the editor launched by "/ C" exited.
type configEditedMsg struct{ err error }

// editorCommand is the editor command line: $VISUAL, else $EDITOR (either may
// carry arguments, like "code --wait"), else a platform default.
func editorCommand() []string {
	for _, env := range []string{visualEnv, editorEnv} {
		if fields := strings.Fields(os.Getenv(env)); len(fields) > 0 {
			return fields
		}
	}
	if runtime.GOOS == "windows" {
		return []string{windowsEditor}
	}
	return []string{unixEditor}
}

// settingsChanged follows up a change of the settings fields: it re-derives
// the key folder, reconnects the vault if its settings changed, and shows
// okMsg (with a warning when the vault cannot be opened).
func (m *model) settingsChanged(old config.Config, okMsg string) tea.Cmd {
	if m.cfg.KeyDir != old.KeyDir {
		m.keyDir = config.ResolveKeyDir("", *m.cfg)
	}
	refresh := m.refreshDashboard("")
	if m.cfg.Vault != old.Vault {
		if err := m.reconnectVault(); err != nil {
			return tea.Batch(refresh, m.setStatus(statusWarn, okMsg+" Vault not connected: "+err.Error()))
		}
	}
	return tea.Batch(refresh, m.setStatus(statusSuccess, okMsg))
}

// editSettings edits the key folder and vault settings. The token and master
// key are masked, like in 'ttssh vault setup'.
func (m *model) editSettings() tea.Cmd {
	old := *m.cfg
	keyDir := old.KeyDir
	vc := old.Vault
	form := newForm(huh.NewGroup(
		huh.NewInput().Title("Key folder").
			Description("Scanned for *.key files; empty means ~/.ssh").
			Validate(validateKeyDir).Value(&keyDir),
		huh.NewInput().Title("Vault database URL").
			Description("Empty turns the vault off").
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return nil
				}
				return validateVaultURL(s)
			}).Value(&vc.URL),
		huh.NewInput().Title("Vault auth token").
			EchoMode(huh.EchoModePassword).Value(&vc.Token),
		huh.NewInput().Title("Vault master key (64 hex chars)").
			EchoMode(huh.EchoModePassword).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" && strings.TrimSpace(vc.URL) == "" {
					return nil
				}
				return validateMasterKeyHex(s)
			}).Value(&vc.MasterKeyHex),
		huh.NewInput().Title("Vault private CA certificate path").
			Description("Optional").Value(&vc.CACert),
	).Title("Settings"))
	return m.push(formScreen(form, func(m *model) tea.Cmd {
		m.cfg.KeyDir = strings.TrimSpace(keyDir)
		m.cfg.Vault = config.VaultConfig{
			URL:          strings.TrimSpace(vc.URL),
			Token:        strings.TrimSpace(vc.Token),
			MasterKeyHex: strings.TrimSpace(vc.MasterKeyHex),
			CACert:       strings.TrimSpace(vc.CACert),
		}
		if err := m.cfg.Save(); err != nil {
			return tea.Batch(m.settingsChanged(old, "Settings changed for this session."),
				m.setStatus(statusError, "Changed for this session, but saving failed: "+err.Error()))
		}
		return m.settingsChanged(old, "Settings saved.")
	}))
}

// validateKeyDir accepts an empty value (the default) or an existing folder.
func validateKeyDir(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	info, err := os.Stat(config.ExpandHome(s))
	switch {
	case err != nil:
		return errors.New("folder not found")
	case !info.IsDir():
		return errors.New("not a folder")
	}
	return nil
}

// editConfigFile suspends the TUI to edit config.json. The current state is
// saved first so the editor opens an up-to-date file.
func (m *model) editConfigFile() tea.Cmd {
	path, err := config.Path()
	if err != nil {
		return m.setStatus(statusError, "locating config.json: "+err.Error())
	}
	if err := m.cfg.Save(); err != nil {
		return m.setStatus(statusError, "saving config.json before editing: "+err.Error())
	}
	argv := append(editorCommand(), path)
	c := terminalCmd{exec.CommandContext(m.ctx, argv[0], argv[1:]...)}
	return tea.Batch(m.setStatus(statusInfo, configEditedNote),
		tea.Exec(c, func(err error) tea.Msg { return configEditedMsg{err: err} }))
}

// reloadConfig applies config.json after the editor exited. A file that no
// longer parses is left as the user wrote it and the current config is kept.
func (m *model) reloadConfig(editorErr error) tea.Cmd {
	if editorErr != nil {
		return m.setStatus(statusError, "running the editor: "+editorErr.Error()+" (config not reloaded)")
	}
	next, err := config.Read()
	if err != nil {
		return m.setStatus(statusError, "config.json not reloaded, keeping current settings: "+err.Error())
	}
	old := *m.cfg
	*m.cfg = next
	return m.settingsChanged(old, "Reloaded config.json.")
}
