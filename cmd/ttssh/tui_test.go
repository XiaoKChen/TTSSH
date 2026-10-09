package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"ttssh/internal/config"
)

// newTestModel builds a dashboard over an in-memory config. The config dir
// is redirected so AddRecent/RemoveRecent never touch the real config.json.
func newTestModel(t *testing.T, recents []config.Recent) *model {
	t.Helper()
	return newTestModelConfig(t, config.Config{Recents: recents})
}

// newTestModelConfig is newTestModel over a full config, saved tree included.
func newTestModelConfig(t *testing.T, cfg config.Config) *model {
	t.Helper()
	cfgHome := t.TempDir()
	t.Setenv("AppData", cfgHome)         // Windows
	t.Setenv("XDG_CONFIG_HOME", cfgHome) // Linux
	t.Setenv("HOME", cfgHome)            // macOS

	keyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(keyDir, "id.key"), []byte("key"), keyFilePerm); err != nil {
		t.Fatal(err)
	}
	var tempDir string
	m := newModel(context.Background(), &cfg, nil, &tempDir, keyDir, "test")
	m.pushDashboard()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

const pumpWait = 20 * time.Millisecond

// press sends the keys one by one and feeds back the messages the returned
// commands produce at once (huh advances its fields that way). Commands that
// wait, like timers and cursor blinks, are skipped.
func press(m *model, keys ...string) {
	for _, k := range keys {
		_, cmd := m.Update(keyMsg(k))
		pump(m, cmd)
	}
}

func pump(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				pump(m, c)
			}
			return
		}
		if msg != nil {
			_, next := m.Update(msg)
			pump(m, next)
		}
	case <-time.After(pumpWait):
	}
}

func screenKinds(m *model) []screenKind {
	kinds := make([]screenKind, len(m.stack))
	for i, s := range m.stack {
		kinds[i] = s.kind
	}
	return kinds
}

func sampleRecents() []config.Recent {
	now := time.Now()
	return []config.Recent{
		{User: "root", Host: "alpha", Key: "/keys/a.key", LastUsed: now},
		{User: "admin", Host: "beta", Key: "vault:B-002", LastUsed: now},
	}
}

// savedTree is Prod{EU{beta}, alpha} plus a top-level gamma entry.
func savedTree() config.Folder {
	return config.Folder{
		Folders: []config.Folder{{
			Name:    "Prod",
			Entries: []config.Entry{{User: "root", Host: "alpha", Key: "/keys/a.key"}},
			Folders: []config.Folder{{Name: "EU", Entries: []config.Entry{{User: "pw", Host: "beta"}}}},
		}},
		Entries: []config.Entry{{User: "top", Host: "gamma"}},
	}
}

func rowLabels(m *model) []string {
	var out []string
	for _, li := range m.top().list.Items() {
		out = append(out, li.(item).label)
	}
	return out
}

func TestScreenTransitions(t *testing.T) {
	tests := []struct {
		name    string
		recents []config.Recent
		keys    []string
		want    []screenKind
	}{
		{"/ n opens the key picker", sampleRecents(), []string{"/", "n"}, []screenKind{screenDashboard, screenKeys}},
		{"a bare n does not", sampleRecents(), []string{"n"}, []screenKind{screenDashboard}},
		{"esc goes back to the dashboard", sampleRecents(), []string{"/", "n", "esc"}, []screenKind{screenDashboard}},
		{"enter with no connections stays on the dashboard", nil, []string{"enter"}, []screenKind{screenDashboard}},
		{"/ f in the key picker opens the folder browser", nil, []string{"/", "n", "/", "f"}, []screenKind{screenDashboard, screenKeys, screenFolders}},
		{"enter on a key asks for the target", nil, []string{"/", "n", "enter"}, []screenKind{screenDashboard, screenKeys, screenForm}},
		{"esc backs out of the target form", nil, []string{"/", "n", "enter", "esc"}, []screenKind{screenDashboard, screenKeys}},
		{"esc on the dashboard stays put", nil, []string{"esc"}, []screenKind{screenDashboard}},
		{"/ then a second / closes the popup", nil, []string{"/", "/", "n"}, []screenKind{screenDashboard}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, tc.recents)
			press(m, tc.keys...)
			if got := screenKinds(m); !slices.Equal(got, tc.want) {
				t.Errorf("screens = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestQuitKey(t *testing.T) {
	tests := []struct {
		name     string
		keys     []string
		wantQuit bool
		typed    func(m *model) string // where the q should have landed
	}{
		{"/ q quits", []string{"/", "q"}, true, nil},
		{"a bare q types into the filter", []string{"q"}, false,
			func(m *model) string { return m.top().query }},
		{"q in the target form types into the field", []string{"/", "n", "enter", "q"}, false,
			func(m *model) string { v, _ := m.top().form.GetFocusedField().GetValue().(string); return v }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, sampleRecents())
			press(m, tc.keys...)
			if m.quitting != tc.wantQuit {
				t.Fatalf("quitting = %v, want %v", m.quitting, tc.wantQuit)
			}
			if tc.typed != nil {
				if got := tc.typed(m); got != "q" {
					t.Errorf("typed text = %q, want %q", got, "q")
				}
			}
		})
	}
}

func TestRemoveRecents(t *testing.T) {
	tests := []struct {
		name      string
		keys      []string
		wantHosts []string
	}{
		{"x then y removes the selected recent", []string{"/", "x", "y"}, []string{"beta"}},
		{"x then n keeps it", []string{"/", "x", "n"}, []string{"alpha", "beta"}},
		{"x then esc keeps it", []string{"/", "x", "esc"}, []string{"alpha", "beta"}},
		{"X then y clears every recent", []string{"/", "X", "y"}, nil},
		{"X then n keeps them all", []string{"/", "X", "n"}, []string{"alpha", "beta"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, sampleRecents())
			press(m, tc.keys...)
			var hosts []string
			for _, r := range m.cfg.Recents {
				hosts = append(hosts, r.Host)
			}
			if !slices.Equal(hosts, tc.wantHosts) {
				t.Errorf("recents = %v, want %v", hosts, tc.wantHosts)
			}
			if got := len(m.top().list.Items()); got != len(tc.wantHosts) {
				t.Errorf("list shows %d rows, want %d", got, len(tc.wantHosts))
			}
			if m.confirming != nil {
				t.Error("confirmation still pending")
			}
		})
	}
}

func TestTypingFiltersAndNeverActs(t *testing.T) {
	tests := []struct {
		name      string
		keys      []string
		wantQuery string
		wantRows  int
	}{
		{"letters narrow the list", []string{"a", "l", "p"}, "alp", 1},
		{"action letters are plain text", []string{"x", "X", "n", "u", "d", "q", "?"}, "xXnudq?", 0},
		{"backspace edits the query", []string{"a", "l", "p", "backspace", "backspace"}, "a", 2},
		{"esc clears the query before going back", []string{"a", "l", "p", "esc"}, "", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, sampleRecents())
			press(m, tc.keys...)
			if got := m.top().query; got != tc.wantQuery {
				t.Errorf("query = %q, want %q", got, tc.wantQuery)
			}
			if got := len(m.top().list.Items()); got != tc.wantRows {
				t.Errorf("rows = %d, want %d", got, tc.wantRows)
			}
			if m.confirming != nil || m.quitting || len(m.stack) != 1 || len(m.cfg.Recents) != 2 {
				t.Errorf("a typed letter acted: confirming=%v quitting=%v screens=%d recents=%d",
					m.confirming != nil, m.quitting, len(m.stack), len(m.cfg.Recents))
			}
		})
	}
}

func TestCommandPopup(t *testing.T) {
	tests := []struct {
		name       string
		keys       []string
		wantPopup  bool
		wantStatus string
		wantHelp   bool
	}{
		{"/ opens it", []string{"/"}, true, "", false},
		{"esc closes it", []string{"/", "esc"}, false, "", false},
		{"a second / closes it", []string{"/", "/"}, false, "", false},
		{"an unknown key closes it with a message", []string{"/", "z"}, false, `no shortcut "z"`, false},
		{"? shows the full help", []string{"/", "?"}, false, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, sampleRecents())
			press(m, tc.keys...)
			if (m.popup != nil) != tc.wantPopup {
				t.Errorf("popup open = %v, want %v", m.popup != nil, tc.wantPopup)
			}
			if m.status.text != tc.wantStatus {
				t.Errorf("status = %q, want %q", m.status.text, tc.wantStatus)
			}
			if m.help.ShowAll != tc.wantHelp {
				t.Errorf("full help = %v, want %v", m.help.ShowAll, tc.wantHelp)
			}
		})
	}
}

func TestSavedTree(t *testing.T) {
	// The expanded tree is: Prod, EU, beta, alpha, gamma, the Recent header,
	// and two recents.
	const expanded = 8
	tests := []struct {
		name       string
		keys       []string
		wantRows   int
		wantFolder []string // folder names at the top level afterwards
		wantEntry  int      // entries in the whole tree afterwards
	}{
		{"folders start expanded", nil, expanded, []string{"Prod"}, 3},
		{"enter on a folder collapses it", []string{"enter"}, 1 + 1 + 1 + 2, []string{"Prod"}, 3},
		{"enter twice expands it again", []string{"enter", "enter"}, expanded, []string{"Prod"}, 3},
		{"left collapses and right expands", []string{"left", "right"}, expanded, []string{"Prod"}, 3},
		{"/ x y on a folder deletes it with its contents", []string{"/", "x", "y"}, 1 + 1 + 2, nil, 1},
		{"/ x n keeps the folder", []string{"/", "x", "n"}, expanded, []string{"Prod"}, 3},
		{"/ x y on an entry removes it", []string{"down", "down", "down", "/", "x", "y"}, expanded - 1, []string{"Prod"}, 2},
		{"/ f, a name and enter creates a folder inside the selected one", []string{"/", "f", "Dev", "enter"}, expanded + 1, []string{"Prod"}, 3},
		{"/ f with a recent selected creates a top-level folder", []string{"down", "down", "down", "down", "down", "down", "/", "f", "Dev", "enter"}, expanded + 1, []string{"Prod", "Dev"}, 3},
		{"/ r renames the folder", []string{"/", "r", "backspace", "backspace", "backspace", "backspace", "Stage", "enter"}, expanded, []string{"Stage"}, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModelConfig(t, config.Config{Saved: savedTree(), Recents: sampleRecents()})
			press(m, tc.keys...)
			if got := len(m.top().list.Items()); got != tc.wantRows {
				t.Errorf("rows = %d, want %d: %q", got, tc.wantRows, rowLabels(m))
			}
			var names []string
			for _, f := range m.cfg.Saved.Folders {
				names = append(names, f.Name)
			}
			if !slices.Equal(names, tc.wantFolder) {
				t.Errorf("top-level folders = %v, want %v", names, tc.wantFolder)
			}
			if got := m.cfg.Saved.Count(); got != tc.wantEntry {
				t.Errorf("entries = %d, want %d", got, tc.wantEntry)
			}
		})
	}
}

func TestFilteringTheTreeShowsFlatMatches(t *testing.T) {
	m := newTestModelConfig(t, config.Config{Saved: savedTree(), Recents: sampleRecents()})
	press(m, "b", "e", "t", "a")
	rows := rowLabels(m)
	if len(rows) != 2 {
		t.Fatalf("rows = %q, want the beta entry and the beta recent", rows)
	}
	if !strings.Contains(rows[0], "Prod / EU") {
		t.Errorf("flat entry row %q does not show its folder path", rows[0])
	}
}

func TestNewConnection(t *testing.T) {
	keys := []string{"/", "n", "enter", "root", "enter", "host1", "enter", "enter"}
	tests := []struct {
		name       string
		saved      config.Folder
		wantFolder []string // where the saved entry went; nil when not saved
	}{
		{"a keyless connection stores an empty key", config.Folder{}, nil},
		{"the selected folder is the default save target", savedTree(), []string{"Prod"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModelConfig(t, config.Config{Saved: tc.saved})
			before := m.cfg.Saved.Count()
			press(m, keys...)
			if len(m.cfg.Recents) != 1 {
				t.Fatalf("recents = %v, want one", m.cfg.Recents)
			}
			if r := m.cfg.Recents[0]; r.Key != "" || r.User != "root" || r.Host != "host1" {
				t.Errorf("recent = %+v, want a keyless root@host1", r)
			}
			want := before
			if tc.wantFolder != nil {
				want++
				f, _ := m.cfg.Saved.Find(tc.wantFolder)
				if !slices.Contains(f.Entries, config.Entry{User: "root", Host: "host1"}) {
					t.Errorf("%v entries = %v, want the new keyless entry", tc.wantFolder, f.Entries)
				}
			}
			if got := m.cfg.Saved.Count(); got != want {
				t.Errorf("saved entries = %d, want %d", got, want)
			}
		})
	}
}

func TestSlashIsLiteralInForms(t *testing.T) {
	m := newTestModel(t, nil)
	press(m, "/", "n", "enter", "/", "a")
	if got, _ := m.top().form.GetFocusedField().GetValue().(string); got != "/a" {
		t.Errorf("username field = %q, want %q", got, "/a")
	}
	if m.popup != nil {
		t.Error("the popup opened inside a form")
	}
}

func TestMissingKeys(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.Config
		wantRecents int
		wantSaved   int
	}{
		{"a recent with a missing key file is pruned",
			config.Config{Recents: []config.Recent{{User: "u", Host: "h", Key: "/gone.key"}}}, 0, 0},
		{"a saved entry with a missing key file is kept",
			config.Config{Saved: config.Folder{Entries: []config.Entry{{User: "u", Host: "h", Key: "/gone.key"}}}}, 0, 1},
		{"a keyless recent is never pruned",
			config.Config{Recents: []config.Recent{{User: "u", Host: "h"}}}, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModelConfig(t, tc.cfg)
			m.Update(keyMsg("enter")) // connect; the returned ssh command is not run
			if len(m.cfg.Recents) != tc.wantRecents || m.cfg.Saved.Count() != tc.wantSaved {
				t.Errorf("recents = %d, saved = %d; want %d, %d",
					len(m.cfg.Recents), m.cfg.Saved.Count(), tc.wantRecents, tc.wantSaved)
			}
		})
	}
}

// opResult runs the operation part of a startOp command (not the spinner)
// and returns its message.
func opResult(t *testing.T, cmd tea.Cmd) opResultMsg {
	t.Helper()
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatal("startOp did not return a batch")
	}
	for _, c := range batch {
		if msg, ok := c().(opResultMsg); ok {
			return msg
		}
	}
	t.Fatal("no operation result in batch")
	return opResultMsg{}
}

func TestAsyncResults(t *testing.T) {
	tests := []struct {
		name        string
		interrupt   func(m *model)
		wantApplied bool
	}{
		{"current result is applied", func(*model) {}, true},
		{"result after esc is ignored", func(m *model) { press(m, "esc") }, false},
		{"result of a superseded operation is ignored", func(m *model) {
			m.startOp("second", false, func(context.Context) func(*model) tea.Cmd { return nil })
		}, false},
		{"result after its screen closed is ignored", func(m *model) { m.popTo(1) }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, sampleRecents())
			press(m, "/", "n") // the operation belongs to the key picker
			applied := false
			cmd := m.startOp("first", true, func(context.Context) func(*model) tea.Cmd {
				return func(*model) tea.Cmd { applied = true; return nil }
			})
			msg := opResult(t, cmd)
			tc.interrupt(m)
			m.Update(msg)
			if applied != tc.wantApplied {
				t.Errorf("applied = %v, want %v", applied, tc.wantApplied)
			}
		})
	}
}
