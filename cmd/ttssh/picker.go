package main

// The key picker and folder browser screens: choose an SSH key from vault
// units and local *.key files, switch the key folder, and download vault
// keys into a folder.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"ttssh/internal/config"
	"ttssh/internal/ui"
	"ttssh/internal/vault"
)

// keyPicker is the key picker's state beyond its list.
type keyPicker struct {
	units   []vault.Unit
	loading bool // vault units are still being fetched
}

// openKeyPicker starts the new-connection flow: pick a key, then the target.
// Local keys show at once; vault units are loaded in the background.
func (m *model) openKeyPicker() tea.Cmd {
	p := &keyPicker{}
	s := &screen{
		kind:    screenKeys,
		list:    newList("Keys", "key", "keys", nil),
		actions: []key.Binding{m.keys.Select, m.keys.Folder, m.keys.Pull},
		details: keyDetails,
	}
	s.onKey = func(m *model, msg tea.KeyMsg) (tea.Cmd, bool) {
		switch {
		case key.Matches(msg, m.keys.Select):
			if it := s.selected(); it.kind != itemNone {
				return m.askTarget(it), true
			}
			return nil, true
		case key.Matches(msg, m.keys.Folder):
			return m.changeKeyDir(s, p), true
		case key.Matches(msg, m.keys.Pull):
			return m.pullVaultKeys(s, p), true
		}
		return nil, false
	}

	cmds := []tea.Cmd{m.push(s), m.reloadKeys(s, p)}
	if m.vlt != nil {
		p.loading = true
		vlt := m.vlt
		cmds = append(cmds, m.startOp("Loading vault keys…", false, func(ctx context.Context) func(*model) tea.Cmd {
			units, err := vlt.ListUnits(ctx)
			return func(m *model) tea.Cmd {
				p.loading = false
				if err != nil {
					return m.setStatus(statusWarn, "vault: "+err.Error())
				}
				p.units = units
				return m.reloadKeys(s, p)
			}
		}))
	}
	return tea.Batch(cmds...)
}

// reloadKeys lists vault units first, then *.key files under m.keyDir.
func (m *model) reloadKeys(s *screen, p *keyPicker) tea.Cmd {
	var items []item
	for _, u := range p.units {
		label := "☁ " + u.UnitID
		if u.Revoked() {
			label += "  (revoked)"
		}
		items = append(items, item{kind: itemVaultKey, label: label, value: u.UnitID, unit: u})
	}
	keys, scanErr := scanKeys(m.keyDir)
	for _, k := range keys {
		label := k
		if rel, err := filepath.Rel(m.keyDir, k); err == nil {
			label = rel
		}
		items = append(items, item{kind: itemLocalKey, label: label, value: k})
	}

	s.list.Title = "Keys · " + m.keyDir
	s.empty = ""
	cmds := []tea.Cmd{s.list.SetItems(toListItems(items))}
	if scanErr != nil {
		s.empty = scanErr.Error() + ".\n\nPress f to choose another folder."
		if len(items) > 0 {
			cmds = append(cmds, m.setStatus(statusInfo, scanErr.Error()))
		}
	}
	return tea.Batch(cmds...)
}

func keyDetails(it item) string {
	if it.kind == itemLocalKey {
		return localFileDetails(it)
	}
	u := it.unit
	state := "active"
	if u.Revoked() {
		state = "revoked"
	}
	return "☁ " + u.UnitID + "\n" + ui.MutedStyle.Render("vault key · decrypted only for this session") + "\n\n" +
		ui.MutedStyle.Render("fingerprint  ") + u.Fingerprint + "\n" +
		ui.MutedStyle.Render("created      ") + u.CreatedAt + "\n" +
		ui.MutedStyle.Render("status       ") + state
}

// askTarget collects the user and host for the chosen key, records the new
// connection in the recents, and selects it on the dashboard.
func (m *model) askTarget(keyItem item) tea.Cmd {
	keyRef, keyLabel := keyItem.value, keyItem.value
	if keyItem.kind == itemVaultKey {
		keyRef, keyLabel = vaultRecentPrefix+keyItem.value, vaultLabel(keyItem.value)
	}
	var user, host string
	form := newForm(huh.NewGroup(
		huh.NewInput().Title("Username").Placeholder("root").
			Validate(noSpaces("username")).Value(&user),
		huh.NewInput().Title("Host (IP or hostname)").Placeholder("192.168.1.10").
			Validate(noSpaces("host")).Value(&host),
	).Title("New connection").Description("Key: " + keyLabel))
	return m.push(formScreen(form, func(m *model) tea.Cmd {
		r := config.Recent{User: strings.TrimSpace(user), Host: strings.TrimSpace(host), Key: keyRef}
		m.popTo(1)
		m.cfg.AddRecent(r)
		return tea.Batch(m.refreshDashboard(0),
			m.setStatus(statusSuccess, "Added "+r.User+"@"+r.Host+" — press enter to connect."))
	}))
}

func noSpaces(what string) func(string) error {
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

// changeKeyDir browses to another key folder and offers to save it as the
// default; backing out of that question keeps it for this session only.
func (m *model) changeKeyDir(s *screen, p *keyPicker) tea.Cmd {
	return m.push(m.folderBrowser(m.keyDir, func(m *model, dir string) tea.Cmd {
		if dir == m.keyDir {
			return nil
		}
		m.keyDir = dir
		save := false
		confirm := newForm(huh.NewGroup(huh.NewConfirm().
			Title("Save as default key folder?").
			Description(dir).
			Affirmative("Save").
			Negative("Just this session").
			Value(&save)))
		return tea.Batch(m.reloadKeys(s, p), m.push(formScreen(confirm, func(m *model) tea.Cmd {
			if !save {
				return m.setStatus(statusInfo, "Using "+dir+" for this session.")
			}
			m.cfg.KeyDir = dir
			if err := m.cfg.Save(); err != nil {
				return m.setStatus(statusWarn, "Could not save config: "+err.Error())
			}
			return m.setStatus(statusSuccess, "Default key folder saved.")
		})))
	}))
}

// pullVaultKeys multi-selects vault units, browses to a folder, confirms
// overwrites one by one, then downloads the keys in the background.
func (m *model) pullVaultKeys(s *screen, p *keyPicker) tea.Cmd {
	switch {
	case p.loading:
		return m.setStatus(statusInfo, "Vault keys are still loading…")
	case len(p.units) == 0:
		return m.setStatus(statusWarn, "The vault has no keys.")
	}
	var selected []string
	form := newForm(huh.NewGroup(huh.NewMultiSelect[string]().
		Title("Download which keys?").
		Description("space toggles · ctrl+a toggles all · enter confirms").
		Options(unitOptions(p.units)...).
		Value(&selected)))
	return m.push(formScreen(form, func(m *model) tea.Cmd {
		if len(selected) == 0 {
			return m.setStatus(statusInfo, "Nothing selected.")
		}
		return m.push(m.folderBrowser(m.keyDir, func(m *model, dir string) tea.Cmd {
			return m.confirmOverwrites(dir, selected, func(m *model, ids []string) tea.Cmd {
				return m.saveKeys(s, p, dir, ids)
			})
		}))
	}))
}

// confirmOverwrites asks about each id whose file already exists in dir,
// then calls done with the ids to write. Esc on a question drops the batch.
func (m *model) confirmOverwrites(dir string, ids []string, done func(m *model, ids []string) tea.Cmd) tea.Cmd {
	var ask func(m *model, next int, keep []string) tea.Cmd
	ask = func(m *model, next int, keep []string) tea.Cmd {
		for ; next < len(ids); next++ {
			id, dest := ids[next], filepath.Join(dir, ids[next]+keyExt)
			if _, err := os.Stat(dest); err != nil {
				keep = append(keep, id)
				continue
			}
			overwrite, after := false, next+1
			return m.push(formScreen(newForm(huh.NewGroup(overwriteConfirm(dest, &overwrite))), func(m *model) tea.Cmd {
				if overwrite {
					return ask(m, after, append(slices.Clone(keep), id))
				}
				return ask(m, after, keep)
			}))
		}
		if len(keep) == 0 {
			return m.setStatus(statusInfo, "Nothing to download.")
		}
		return done(m, keep)
	}
	return ask(m, 0, nil)
}

// saveKeys downloads ids into dir via saveVaultKeys; every conflict was
// already confirmed, so existing files are overwritten.
func (m *model) saveKeys(s *screen, p *keyPicker, dir string, ids []string) tea.Cmd {
	vlt := m.vlt
	label := fmt.Sprintf("Downloading %d key(s) to %s…", len(ids), dir)
	return m.startOp(label, true, func(ctx context.Context) func(*model) tea.Cmd {
		var problems []string
		notify := saveNotifier{
			saved:   func(string) {},
			skipped: func(msg string) { problems = append(problems, msg) },
			failed:  func(msg string) { problems = append(problems, msg) },
		}
		overwrite := func(string) (bool, error) { return true, nil }
		saved, err := saveVaultKeys(ctx, vlt, ids, dir, overwrite, notify)
		return func(m *model) tea.Cmd {
			reload := m.reloadKeys(s, p) // dir may be the key folder
			switch {
			case err != nil:
				return tea.Batch(reload, m.setStatus(statusError, err.Error()))
			case len(problems) > 0:
				return tea.Batch(reload, m.setStatus(statusWarn,
					fmt.Sprintf("Saved %d of %d key file(s) to %s: %s", saved, len(ids), dir, problems[0])))
			default:
				return tea.Batch(reload, m.setStatus(statusSuccess, fmt.Sprintf("Saved %d key file(s) to %s", saved, dir)))
			}
		}
	})
}

// scanKeys collects *.key files under dir.
func scanKeys(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("key folder %s does not exist", dir)
	}

	var keys []string
	// Error discarded: per-entry errors are already skipped in the callback
	// below; an unreadable root just yields an empty key list, which the
	// caller already handles via the "no *.key files found" error.
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), keyExt) {
			keys = append(keys, path)
		}
		return nil
	})
	if len(keys) == 0 {
		return nil, fmt.Errorf("no *%s files found in %s", keyExt, dir)
	}
	return keys, nil
}

// ---- folder browser ----

// errBrowseCancelled reports that the folder browser was left without
// choosing a folder.
var errBrowseCancelled = errors.New("folder selection cancelled")

// browseDir runs the folder browser as its own small program (for the vault
// pull subcommand) and returns the accepted folder.
func browseDir(ctx context.Context, start string) (string, error) {
	m := newModel(ctx, nil, nil, nil, "", "")
	var chosen string
	s := m.folderBrowser(start, func(m *model, dir string) tea.Cmd {
		chosen = dir
		return m.quit()
	})
	s.onBack = func(m *model) tea.Cmd { return m.quit() }
	s.actions = append(s.actions, m.keys.Cancel)
	m.startup = m.push(s)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		return "", fmt.Errorf("folder browser: %w", err)
	}
	if chosen == "" {
		return "", errBrowseCancelled
	}
	return chosen, nil
}

// folderBrowser lists the subfolders of the current folder. choose runs
// after the browser left the stack.
func (m *model) folderBrowser(start string, choose func(m *model, dir string) tea.Cmd) *screen {
	dir, atDrives := start, false
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = "."
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		}
	}

	s := &screen{
		kind:    screenFolders,
		list:    newList("", "folder", "folders", nil),
		actions: []key.Binding{m.keys.Open, m.keys.Parent, m.keys.UseDir, m.keys.TypePath},
		details: func(it item) string { return dirPreview(it.value) },
	}
	// show lists dir's subfolders, selecting the one named selectName.
	show := func(selectName string) tea.Cmd {
		atDrives = false
		s.list.Title = "Folders · " + dir
		s.empty = "No subfolders here.\n\nPress space to use this folder, or ← to go up."
		subs := listSubdirs(dir)
		items := make([]item, len(subs))
		for i, name := range subs {
			items[i] = item{kind: itemDir, label: name + "/", value: filepath.Join(dir, name)}
		}
		s.list.ResetFilter()
		cmd := s.list.SetItems(toListItems(items))
		s.list.Select(max(slices.Index(subs, selectName), 0))
		return cmd
	}
	finish := func(m *model, chosen string) tea.Cmd {
		m.popScreen(s)
		return choose(m, chosen)
	}

	s.onKey = func(m *model, msg tea.KeyMsg) (tea.Cmd, bool) {
		it := s.selected()
		switch {
		case key.Matches(msg, m.keys.Open):
			if it.kind == itemNone {
				return nil, true
			}
			dir = it.value
			return show(""), true
		case key.Matches(msg, m.keys.Parent):
			if atDrives {
				return nil, true
			}
			if parent := filepath.Dir(dir); parent != dir {
				child := filepath.Base(dir)
				dir = parent
				return show(child), true
			}
			drives := listDrives() // at a root: offer switching drives (Windows)
			if len(drives) == 0 {
				return nil, true
			}
			atDrives = true
			s.list.Title = "Drives"
			s.empty = ""
			items := make([]item, len(drives))
			for i, d := range drives {
				items[i] = item{kind: itemDrive, label: d, value: d}
			}
			s.list.ResetFilter()
			return s.list.SetItems(toListItems(items)), true
		case key.Matches(msg, m.keys.UseDir):
			if atDrives {
				if it.kind == itemNone {
					return nil, true
				}
				return finish(m, it.value), true
			}
			return finish(m, dir), true
		case key.Matches(msg, m.keys.TypePath):
			var typed string
			current := dir
			return m.push(formScreen(pathForm(current, &typed), func(m *model) tea.Cmd {
				return finish(m, config.ExpandHome(orDefault(typed, current)))
			})), true
		}
		return nil, false
	}
	show("")
	return s
}

// pathForm asks for a folder path, validating that it exists.
func pathForm(current string, value *string) *huh.Form {
	return inputForm("Folder path", current, value, func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		info, err := os.Stat(config.ExpandHome(s))
		if err != nil || !info.IsDir() {
			return errors.New("folder not found")
		}
		return nil
	})
}

// listSubdirs returns the names of dir's subfolders (hidden ones included,
// since e.g. ~/.ssh is hidden), sorted case-insensitively.
func listSubdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var subs []string
	for _, e := range entries {
		if e.IsDir() {
			subs = append(subs, e.Name())
		}
	}
	sort.Slice(subs, func(i, j int) bool {
		return strings.ToLower(subs[i]) < strings.ToLower(subs[j])
	})
	return subs
}

// dirPreview summarizes a folder for the browser's details pane.
func dirPreview(path string) string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return path + "\n\n(unreadable)"
	}
	var dirs, keys int
	for _, e := range entries {
		switch {
		case e.IsDir():
			dirs++
		case strings.EqualFold(filepath.Ext(e.Name()), keyExt):
			keys++
		}
	}
	return fmt.Sprintf("%s\n\n%d subfolder(s)\n%d *%s file(s)", path, dirs, keys, keyExt)
}

// listDrives returns the available drive roots on Windows (nil elsewhere),
// offered when browsing up from a drive root.
func listDrives() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	var drives []string
	for c := 'A'; c <= 'Z'; c++ {
		p := string(c) + `:\`
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			drives = append(drives, p)
		}
	}
	return drives
}
