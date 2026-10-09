package main

// The dashboard's connection tree: saved folders and entries above the
// recents, its "/" commands, and the folder-choosing forms.

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"ttssh/internal/config"
	"ttssh/internal/ui"
)

const (
	topLevelLabel   = "(top level)"
	dontSaveLabel   = "Don't save (recent only)"
	recentHeader    = "── Recent ──"
	passwordKeyName = "password"
	pathLabelSep    = " / "
	indentStep      = "  "
	maxSelectRows   = 12 // tallest folder chooser, in rows
)

// pathLabel renders a folder path for people: "Prod / EU".
func pathLabel(path []string) string {
	if len(path) == 0 {
		return topLevelLabel
	}
	return strings.Join(path, pathLabelSep)
}

// keyName is the short key label: the file name, "☁ unit" for vault keys, or
// "password" for keyless connections.
func keyName(keyRef string) string {
	if keyRef == "" {
		return passwordKeyName
	}
	if id, ok := strings.CutPrefix(keyRef, vaultRecentPrefix); ok {
		return "☁ " + id
	}
	return filepath.Base(keyRef)
}

// ---- rows ----

func (m *model) pushDashboard() tea.Cmd {
	s := &screen{
		kind:    screenDashboard,
		list:    newList("Connections", "row", "rows"),
		empty:   "No connections yet.\nPress / then n to add one.",
		details: m.dashboardDetails,
		enterDesc: func(it item) string {
			if it.kind == itemFolder {
				return "fold"
			}
			return "ssh"
		},
		commands: dashboardCommands,
		onKey:    dashboardKey,
		onBack:   func(*model) tea.Cmd { return nil }, // the root has nowhere to go back to
	}
	s.build = m.dashboardRows
	s.refilter()
	return m.push(s)
}

// dashboardRows is the tree, or a flat list of matches while filtering.
func (m *model) dashboardRows(query string) []item {
	if strings.TrimSpace(query) == "" {
		return m.treeRows()
	}
	return filterItems(m.flatRows(), query)
}

func (m *model) treeRows() []item {
	var rows []item
	var walk func(f config.Folder, path []string, depth int)
	walk = func(f config.Folder, path []string, depth int) {
		indent := strings.Repeat(indentStep, depth)
		for _, sub := range f.Folders {
			subPath := append(slices.Clone(path), sub.Name)
			open := !m.collapsed[pathKey(subPath)]
			marker := "▸"
			if open {
				marker = "▾"
			}
			rows = append(rows, item{
				kind: itemFolder, path: subPath,
				label: fmt.Sprintf("%s%s %s (%d)", indent, marker, sub.Name, sub.Count()),
			})
			if open {
				walk(sub, subPath, depth+1)
			}
		}
		for _, e := range f.Entries {
			rows = append(rows, item{
				kind: itemEntry, entry: e, path: path,
				label: fmt.Sprintf("%s%s%-24s %s", indent, indentStep, e.User+"@"+e.Host, keyName(e.Key)),
			})
		}
	}
	walk(m.cfg.Saved, nil, 0)
	if len(rows) > 0 && len(m.cfg.Recents) > 0 {
		rows = append(rows, item{kind: itemHeader, label: recentHeader})
	}
	return append(rows, m.recentRows()...)
}

// flatRows lists every saved entry (with its folder path) and every recent.
func (m *model) flatRows() []item {
	var rows []item
	var walk func(f config.Folder, path []string)
	walk = func(f config.Folder, path []string) {
		for _, sub := range f.Folders {
			walk(sub, append(slices.Clone(path), sub.Name))
		}
		for _, e := range f.Entries {
			rows = append(rows, item{
				kind: itemEntry, entry: e, path: path,
				label: fmt.Sprintf("%-24s %-18s %s", e.User+"@"+e.Host, keyName(e.Key), pathLabel(path)),
			})
		}
	}
	walk(m.cfg.Saved, nil)
	return append(rows, m.recentRows()...)
}

func (m *model) recentRows() []item {
	rows := make([]item, 0, len(m.cfg.Recents))
	for _, r := range m.cfg.Recents {
		label := fmt.Sprintf("%-24s %-18s %s", r.User+"@"+r.Host, keyName(r.Key), ui.RelTime(r.LastUsed))
		rows = append(rows, item{kind: itemRecent, label: label, recent: r})
	}
	return rows
}

// refreshDashboard rebuilds the rows after the config changed. The row with
// selectID stays selected; without one the old position is kept.
func (m *model) refreshDashboard(selectID string) tea.Cmd {
	s := m.stack[0]
	index := s.list.Index()
	cmd := s.refilter()
	if n := len(s.list.Items()); n > 0 && !(selectID != "" && s.selectID(selectID)) {
		s.list.Select(min(max(index, 0), n-1))
	}
	s.skipHeader(1)
	return cmd
}

// showRow clears the filter so the row with id is visible, and selects it.
func (m *model) showRow(id string) tea.Cmd {
	m.stack[0].query = ""
	return m.refreshDashboard(id)
}

// reveal unfolds every folder along path.
func (m *model) reveal(path []string) {
	for i := 1; i <= len(path); i++ {
		delete(m.collapsed, pathKey(path[:i]))
	}
}

func (m *model) setFolded(it item, fold bool) tea.Cmd {
	if fold {
		m.collapsed[pathKey(it.path)] = true
	} else {
		delete(m.collapsed, pathKey(it.path))
	}
	return m.refreshDashboard(it.id())
}

// ---- details ----

func (m *model) dashboardDetails(it item) string {
	row := func(label, value string) string {
		return ui.MutedStyle.Render(fmt.Sprintf("%-10s ", label)) + value + "\n"
	}
	cmdHint := func(k, desc string) string {
		return ui.KeyStyle.Render(fmt.Sprintf("%-7s", k)) + " " + desc + "\n"
	}
	keyDesc := func(keyRef string) string {
		if id, ok := strings.CutPrefix(keyRef, vaultRecentPrefix); ok {
			return vaultLabel(id)
		}
		if keyRef == "" {
			return passwordKeyName + " (ssh asks for it; never stored)"
		}
		return keyRef
	}
	switch it.kind {
	case itemFolder:
		f, _ := m.cfg.Saved.Find(it.path)
		return row("Folder", pathLabel(it.path)) +
			row("Contains", fmt.Sprintf("%d connection(s), %d subfolder(s)", len(f.Entries), len(f.Folders))) +
			"\n" +
			cmdHint("enter", "fold or unfold") +
			cmdHint("/ n", "new connection") +
			cmdHint("/ f", "new subfolder") +
			cmdHint("/ r", "rename") +
			cmdHint("/ m", "move") +
			cmdHint("/ x", "delete with contents")
	case itemEntry:
		e := it.entry
		return row("Target", e.User+"@"+e.Host) +
			row("Key", keyDesc(e.Key)) +
			row("Folder", pathLabel(it.path)) +
			"\n" +
			cmdHint("enter", "ssh into the host") +
			cmdHint("/ u", "upload a file") +
			cmdHint("/ d", "download a file") +
			cmdHint("/ m", "move to a folder") +
			cmdHint("/ x", "remove")
	default:
		r := it.recent
		return row("Target", r.User+"@"+r.Host) +
			row("Key", keyDesc(r.Key)) +
			row("Last used", ui.RelTime(r.LastUsed)+" ("+r.LastUsed.Format("2006-01-02 15:04")+")") +
			"\n" +
			cmdHint("enter", "ssh into the host") +
			cmdHint("/ u", "upload a file") +
			cmdHint("/ d", "download a file") +
			cmdHint("/ a", "save to a folder") +
			cmdHint("/ x", "remove from recents") +
			cmdHint("/ X", "clear all recents")
	}
}

// ---- keys and commands ----

// dashboardKey handles the direct keys: enter, and left/right in the tree.
func dashboardKey(m *model, msg tea.KeyMsg) (tea.Cmd, bool) {
	s := m.top()
	it := s.selected()
	treeMode := strings.TrimSpace(s.query) == ""
	switch {
	case key.Matches(msg, m.keys.Enter):
		switch it.kind {
		case itemFolder:
			return m.setFolded(it, !m.collapsed[pathKey(it.path)]), true
		case itemEntry:
			return m.connectEntry(it, m.sshInto), true
		case itemRecent:
			return m.connect(it.recent, false, m.sshInto), true
		}
		return nil, true
	case key.Matches(msg, m.keys.Right) && treeMode:
		if it.kind == itemFolder {
			return m.setFolded(it, false), true
		}
		return nil, true
	case key.Matches(msg, m.keys.Left) && treeMode:
		switch {
		case it.kind == itemFolder:
			return m.setFolded(it, true), true
		case it.kind == itemEntry && len(it.path) > 0:
			s.selectID(item{kind: itemFolder, path: it.path}.id())
		}
		return nil, true
	}
	return nil, false
}

func (m *model) connectEntry(it item, then func(session) tea.Cmd) tea.Cmd {
	e := it.entry
	return m.connect(config.Recent{User: e.User, Host: e.Host, Key: e.Key}, true, then)
}

// dashboardCommands lists the "/" commands that make sense for the selection.
func dashboardCommands(m *model) []command {
	it := m.top().selected()
	cmds := []command{{"n", "new connection", func(m *model) tea.Cmd { return m.openKeyPicker() }}}

	if it.kind == itemEntry || it.kind == itemRecent {
		connect := func(then func(session) tea.Cmd) func(m *model) tea.Cmd {
			return func(m *model) tea.Cmd {
				if it.kind == itemEntry {
					return m.connectEntry(it, then)
				}
				return m.connect(it.recent, false, then)
			}
		}
		cmds = append(cmds,
			command{"u", "upload a file", connect(m.startUpload)},
			command{"d", "download a file", connect(m.startDownload)})
	}
	switch it.kind {
	case itemRecent:
		cmds = append(cmds, command{"a", "save to a folder", func(m *model) tea.Cmd { return m.saveRecent(it.recent) }})
	case itemEntry, itemFolder:
		cmds = append(cmds, command{"m", "move to a folder", func(m *model) tea.Cmd { return m.moveRow(it) }})
	}
	cmds = append(cmds, command{"f", "new folder", func(m *model) tea.Cmd { return m.newFolder(it) }})
	if it.kind == itemFolder {
		cmds = append(cmds, command{"r", "rename folder", func(m *model) tea.Cmd { return m.renameFolder(it) }})
	}
	if it.kind != itemNone {
		desc := map[itemKind]string{
			itemEntry: "remove saved connection", itemFolder: "delete folder and contents", itemRecent: "remove from recents",
		}[it.kind]
		cmds = append(cmds, command{"x", desc, func(m *model) tea.Cmd { return m.removeRow(it) }})
	}
	if len(m.cfg.Recents) > 0 {
		cmds = append(cmds, command{"X", "clear all recents", func(m *model) tea.Cmd {
			m.confirming = &confirmation{
				prompt: fmt.Sprintf("Clear all %d recent connections?", len(m.cfg.Recents)),
				onYes:  clearRecents,
			}
			return nil
		}})
	}
	return cmds
}

func clearRecents(m *model) tea.Cmd {
	m.cfg.Recents = nil
	refresh := m.refreshDashboard("")
	if err := m.cfg.Save(); err != nil {
		return tea.Batch(refresh, m.setStatus(statusError, "Cleared for this session, but saving failed: "+err.Error()))
	}
	return tea.Batch(refresh, m.setStatus(statusSuccess, "Cleared all recent connections."))
}

// ---- tree changes ----

// finishTreeChange reports the outcome of a tree mutation: on success it
// saves the config, refreshes the rows and selects selectID (if any).
func (m *model) finishTreeChange(err error, okMsg, selectID string) tea.Cmd {
	if err != nil {
		return m.setStatus(statusError, err.Error())
	}
	var refresh tea.Cmd
	if selectID != "" {
		refresh = m.showRow(selectID)
	} else {
		refresh = m.refreshDashboard("")
	}
	if saveErr := m.cfg.Save(); saveErr != nil {
		return tea.Batch(refresh, m.setStatus(statusError, "Changed for this session, but saving failed: "+saveErr.Error()))
	}
	return tea.Batch(refresh, m.setStatus(statusSuccess, okMsg))
}

// parentFolder is where a new folder goes: the selected folder, the selected
// entry's folder, or the top level.
func parentFolder(it item) []string {
	if it.kind == itemFolder || it.kind == itemEntry {
		return it.path
	}
	return nil
}

func (m *model) newFolder(sel item) tea.Cmd {
	parent := parentFolder(sel)
	var name string
	form := inputForm("New folder in "+pathLabel(parent), "name", &name, config.ValidateFolderName)
	return m.push(formScreen(form, func(m *model) tea.Cmd {
		name = strings.TrimSpace(name)
		err := m.cfg.CreateFolder(parent, name)
		m.reveal(parent)
		created := append(slices.Clone(parent), name)
		return m.finishTreeChange(err, "Created folder "+pathLabel(created)+".", item{kind: itemFolder, path: created}.id())
	}))
}

func (m *model) renameFolder(it item) tea.Cmd {
	name := it.path[len(it.path)-1]
	form := inputForm("Rename "+pathLabel(it.path), "name", &name, config.ValidateFolderName)
	return m.push(formScreen(form, func(m *model) tea.Cmd {
		name = strings.TrimSpace(name)
		err := m.cfg.RenameFolder(it.path, name)
		renamed := append(slices.Clone(it.path[:len(it.path)-1]), name)
		return m.finishTreeChange(err, "Renamed to "+name+".", item{kind: itemFolder, path: renamed}.id())
	}))
}

func (m *model) removeRow(it item) tea.Cmd {
	switch it.kind {
	case itemEntry:
		e := it.entry
		m.confirming = &confirmation{
			prompt: "Remove " + e.User + "@" + e.Host + " from " + pathLabel(it.path) + "?",
			onYes: func(m *model) tea.Cmd {
				return m.finishTreeChange(m.cfg.RemoveEntry(it.path, e), "Removed "+e.User+"@"+e.Host+".", "")
			},
		}
	case itemFolder:
		f, _ := m.cfg.Saved.Find(it.path)
		m.confirming = &confirmation{
			prompt: fmt.Sprintf("Delete folder %s and everything in it (%d connection(s))?", pathLabel(it.path), f.Count()),
			onYes: func(m *model) tea.Cmd {
				return m.finishTreeChange(m.cfg.DeleteFolder(it.path), "Deleted folder "+pathLabel(it.path)+".", "")
			},
		}
	case itemRecent:
		r := it.recent
		m.confirming = &confirmation{
			prompt: "Remove " + r.User + "@" + r.Host + " from recents?",
			onYes: func(m *model) tea.Cmd {
				m.cfg.RemoveRecent(r)
				return tea.Batch(m.refreshDashboard(""),
					m.setStatus(statusSuccess, "Removed "+r.User+"@"+r.Host+" from recents."))
			},
		}
	}
	return nil
}

func (m *model) saveRecent(r config.Recent) tea.Cmd {
	e := config.Entry{User: r.User, Host: r.Host, Key: r.Key}
	return m.chooseFolder("Save "+e.User+"@"+e.Host+" to", m.folderChoices(nil), func(m *model, dest []string) tea.Cmd {
		err := m.cfg.AddEntry(dest, e)
		m.reveal(dest)
		return m.finishTreeChange(err, "Saved "+e.User+"@"+e.Host+" to "+pathLabel(dest)+".",
			item{kind: itemEntry, entry: e, path: dest}.id())
	})
}

func (m *model) moveRow(it item) tea.Cmd {
	if it.kind == itemFolder {
		name := it.path[len(it.path)-1]
		return m.chooseFolder("Move folder "+pathLabel(it.path)+" to", m.folderChoices(it.path), func(m *model, dest []string) tea.Cmd {
			err := m.cfg.MoveFolder(it.path, dest)
			m.reveal(dest)
			moved := append(slices.Clone(dest), name)
			return m.finishTreeChange(err, "Moved "+name+" to "+pathLabel(dest)+".", item{kind: itemFolder, path: moved}.id())
		})
	}
	e := it.entry
	return m.chooseFolder("Move "+e.User+"@"+e.Host+" to", m.folderChoices(nil), func(m *model, dest []string) tea.Cmd {
		err := m.cfg.MoveEntry(it.path, e, dest)
		m.reveal(dest)
		return m.finishTreeChange(err, "Moved "+e.User+"@"+e.Host+" to "+pathLabel(dest)+".",
			item{kind: itemEntry, entry: e, path: dest}.id())
	})
}

// ---- folder choosers ----

// folderChoice is one option of a folder chooser; none marks "don't save".
type folderChoice struct {
	label string
	path  []string
	none  bool
}

// folderChoices lists the top level and every folder, leaving out exclude
// and everything below it.
func (m *model) folderChoices(exclude []string) []folderChoice {
	choices := []folderChoice{{label: topLevelLabel}}
	for _, p := range m.cfg.Saved.Paths() {
		if len(exclude) > 0 && len(p) >= len(exclude) && slices.Equal(p[:len(exclude)], exclude) {
			continue
		}
		choices = append(choices, folderChoice{label: pathLabel(p), path: p})
	}
	return choices
}

// folderSelect is a single-choice field over choices; idx receives the index.
func folderSelect(title string, choices []folderChoice, idx *int) *huh.Select[int] {
	opts := make([]huh.Option[int], len(choices))
	for i, c := range choices {
		opts[i] = huh.NewOption(c.label, i)
	}
	return huh.NewSelect[int]().Title(title).Options(opts...).
		Height(min(len(choices)+2, maxSelectRows)).Value(idx)
}

// chooseFolder asks for a destination folder and passes its path to pick.
func (m *model) chooseFolder(title string, choices []folderChoice, pick func(m *model, dest []string) tea.Cmd) tea.Cmd {
	var idx int
	form := newForm(huh.NewGroup(folderSelect(title, choices, &idx)))
	return m.push(formScreen(form, func(m *model) tea.Cmd { return pick(m, choices[idx].path) }))
}
