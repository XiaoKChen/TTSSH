package main

// The dashboard screen and the session flows that start from it: resolving
// a recent connection's key, ssh, and copying files to/from the remote host.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"ttssh/internal/config"
	"ttssh/internal/ui"
	"ttssh/internal/vault"
)

// session is a connection target with its key file resolved.
type session struct {
	Key  string // key file path passed to ssh/scp -i
	User string
	Host string
}

func (s session) target() string { return s.User + "@" + s.Host }

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

// ---- dashboard ----

func (m *model) pushDashboard() tea.Cmd {
	s := &screen{
		kind:    screenDashboard,
		list:    newList("Connections", "entry", "entries", m.connectionItems()),
		actions: []key.Binding{m.keys.Connect, m.keys.Upload, m.keys.Download, m.keys.New, m.keys.Remove},
		details: m.connectionDetails,
		onKey:   dashboardKey,
		onBack:  func(*model) tea.Cmd { return nil }, // the root has nowhere to go back to
	}
	return m.push(s)
}

func (m *model) connectionItems() []item {
	items := make([]item, 0, len(m.cfg.Recents)+1)
	for _, r := range m.cfg.Recents {
		label := fmt.Sprintf("%-24s %-18s %s", r.User+"@"+r.Host, recentKeyName(r), ui.RelTime(r.LastUsed))
		items = append(items, item{kind: itemRecent, label: label, recent: r})
	}
	return append(items, item{kind: itemNewConnection, label: "＋ New connection"})
}

// recentKeyName is the short key label: the file name, or ☁ unit for vault keys.
func recentKeyName(r config.Recent) string {
	if id, ok := strings.CutPrefix(r.Key, vaultRecentPrefix); ok {
		return "☁ " + id
	}
	return filepath.Base(r.Key)
}

// refreshDashboard rebuilds the connections list after cfg.Recents changed
// and selects the row at index (clamped).
func (m *model) refreshDashboard(index int) tea.Cmd {
	s := m.stack[0]
	items := m.connectionItems()
	s.list.ResetFilter()
	cmd := s.list.SetItems(toListItems(items))
	s.list.Select(min(max(index, 0), len(items)-1))
	return cmd
}

func (m *model) connectionDetails(it item) string {
	row := func(label, value string) string {
		return ui.MutedStyle.Render(fmt.Sprintf("%-10s ", label)) + value + "\n"
	}
	hint := func(k, desc string) string {
		return ui.KeyStyle.Render(fmt.Sprintf("%-6s", k)) + " " + desc + "\n"
	}
	if it.kind == itemNewConnection {
		text := "Pick a key, then enter the user and host.\n\n" + hint("enter", "set up a new connection")
		if len(m.cfg.Recents) == 0 {
			text = ui.MutedStyle.Render("No recent connections yet.") + "\n\n" + text
		}
		return text
	}
	r := it.recent
	keyDesc := r.Key
	if id, ok := strings.CutPrefix(r.Key, vaultRecentPrefix); ok {
		keyDesc = vaultLabel(id)
	}
	return row("Target", r.User+"@"+r.Host) +
		row("Key", keyDesc) +
		row("Last used", ui.RelTime(r.LastUsed)+" ("+r.LastUsed.Format("2006-01-02 15:04")+")") +
		"\n" +
		hint("enter", "ssh into the host") +
		hint("u", "upload a file to the host") +
		hint("d", "download a file from the host") +
		hint("x", "remove from recents")
}

func dashboardKey(m *model, msg tea.KeyMsg) (tea.Cmd, bool) {
	it := m.top().selected()
	switch {
	case key.Matches(msg, m.keys.New):
		return m.openKeyPicker(), true
	case key.Matches(msg, m.keys.Connect) && it.kind == itemNewConnection:
		return m.openKeyPicker(), true
	case !key.Matches(msg, m.keys.Connect, m.keys.Upload, m.keys.Download, m.keys.Remove):
		return nil, false
	case it.kind != itemRecent:
		return m.setStatus(statusInfo, "Select a connection first."), true
	case key.Matches(msg, m.keys.Connect):
		return m.connect(it.recent, m.sshInto), true
	case key.Matches(msg, m.keys.Upload):
		return m.connect(it.recent, m.startUpload), true
	case key.Matches(msg, m.keys.Download):
		return m.connect(it.recent, m.startDownload), true
	default: // remove
		r := it.recent
		m.removing = &r
		return nil, true
	}
}

func (m *model) confirmRemoveKey(msg tea.KeyMsg) tea.Cmd {
	r := *m.removing
	switch {
	case key.Matches(msg, m.keys.Yes):
		m.removing = nil
		index := m.stack[0].list.GlobalIndex()
		m.cfg.RemoveRecent(r)
		return tea.Batch(m.refreshDashboard(index),
			m.setStatus(statusSuccess, "Removed "+r.User+"@"+r.Host+" from recents."))
	case key.Matches(msg, m.keys.No):
		m.removing = nil
	}
	return nil
}

// connect resolves r's key file, fetching vault keys into the session temp
// dir, bumps r to the top of the recents, and continues with then.
func (m *model) connect(r config.Recent, then func(session) tea.Cmd) tea.Cmd {
	unitID, isVault := strings.CutPrefix(r.Key, vaultRecentPrefix)
	if !isVault {
		if _, err := os.Stat(r.Key); err != nil {
			index := m.stack[0].list.GlobalIndex()
			m.cfg.RemoveRecent(r)
			return tea.Batch(m.refreshDashboard(index),
				m.setStatus(statusWarn, "Key file "+r.Key+" no longer exists — removed this entry."))
		}
		return m.useRecent(r, r.Key, then)
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
				if errors.Is(err, vault.ErrUnitNotFound) {
					index := m.stack[0].list.GlobalIndex()
					m.cfg.RemoveRecent(r)
					cmd = tea.Batch(cmd, m.refreshDashboard(index))
				}
				return cmd
			}
			path, err := writeSessionKey(m.tempDir, unitID, keyData)
			if err != nil {
				return m.setStatus(statusError, err.Error())
			}
			return m.useRecent(r, path, then)
		}
	})
}

func (m *model) useRecent(r config.Recent, keyPath string, then func(session) tea.Cmd) tea.Cmd {
	m.cfg.AddRecent(r)
	return tea.Batch(m.refreshDashboard(0), then(session{Key: keyPath, User: r.User, Host: r.Host}))
}

func (m *model) sshInto(sess session) tea.Cmd {
	return m.runTerminal("", "ssh", "-i", sess.Key, sess.target())
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
					"scp", "-i", sess.Key, local, sess.target()+":"+remote)
			}))
		}))
	}))
}

// fileList is a filterable list of paths; enter hands the chosen one to choose.
func (m *model) fileList(title string, items []item, details func(item) string, choose func(m *model, path string) tea.Cmd) *screen {
	s := &screen{
		kind:    screenFiles,
		list:    newList(title, "file", "files", items),
		actions: []key.Binding{m.keys.Select},
		details: details,
	}
	s.onKey = func(m *model, msg tea.KeyMsg) (tea.Cmd, bool) {
		if !key.Matches(msg, m.keys.Select) {
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
					return tea.Batch(m.setStatus(statusWarn, "Could not list remote files; enter the path manually."),
						m.askRemotePath(sess))
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
			"scp", "-i", sess.Key, sess.target()+":"+remote, config.ExpandHome(local))
	}))
}

// listRemoteFiles runs find on the remote host. BatchMode stops ssh from
// prompting (passphrase, host key) while the TUI owns the terminal; such
// hosts fall back to typing the path.
func listRemoteFiles(ctx context.Context, sess session, remoteDir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-i", sess.Key, sess.target(),
		fmt.Sprintf("find %s -maxdepth 6 -type f 2>/dev/null", remoteDir))
	out, err := cmd.Output()
	return splitLines(string(out)), err
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
