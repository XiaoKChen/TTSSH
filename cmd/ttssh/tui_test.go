package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"ttssh/internal/config"
)

// newTestModel builds a dashboard over an in-memory config. The config dir
// is redirected so AddRecent/RemoveRecent never touch the real config.json.
func newTestModel(t *testing.T, recents []config.Recent) *model {
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
	m := newModel(context.Background(), &config.Config{Recents: recents}, nil, &tempDir, keyDir, "test")
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
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		m.Update(keyMsg(k))
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

func TestScreenTransitions(t *testing.T) {
	tests := []struct {
		name    string
		recents []config.Recent
		keys    []string
		want    []screenKind
	}{
		{"n opens the key picker", sampleRecents(), []string{"n"}, []screenKind{screenDashboard, screenKeys}},
		{"esc goes back to the dashboard", sampleRecents(), []string{"n", "esc"}, []screenKind{screenDashboard}},
		{"enter with no connections stays on the dashboard", nil, []string{"enter"}, []screenKind{screenDashboard}},
		{"f opens the folder browser", nil, []string{"n", "f"}, []screenKind{screenDashboard, screenKeys, screenFolders}},
		{"enter on a key asks for the target", nil, []string{"n", "enter"}, []screenKind{screenDashboard, screenKeys, screenForm}},
		{"esc backs out of the target form", nil, []string{"n", "enter", "esc"}, []screenKind{screenDashboard, screenKeys}},
		{"esc on the dashboard stays put", nil, []string{"esc"}, []screenKind{screenDashboard}},
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
		{"q on the dashboard quits", []string{"q"}, true, nil},
		{"q while filtering types into the filter", []string{"/", "q"}, false,
			func(m *model) string { return m.top().list.FilterValue() }},
		{"q in the target form types into the field", []string{"n", "enter", "q"}, false,
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
		{"x then y removes the selected recent", []string{"x", "y"}, []string{"beta"}},
		{"x then n keeps it", []string{"x", "n"}, []string{"alpha", "beta"}},
		{"x then esc keeps it", []string{"x", "esc"}, []string{"alpha", "beta"}},
		{"X then y clears every recent", []string{"X", "y"}, nil},
		{"X then n keeps them all", []string{"X", "n"}, []string{"alpha", "beta"}},
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
			press(m, "n") // the operation belongs to the key picker
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
