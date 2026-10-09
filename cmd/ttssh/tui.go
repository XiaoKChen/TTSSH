package main

// The Bubble Tea dashboard core: a stack of screens (type-to-filter lists or
// embedded huh forms) framed by a header, a status line, and a help footer;
// the "/" command popup; async operations; and handing the terminal to
// ssh/scp.
//
// Keyboard model: on list screens every printable character except "/"
// filters the list, and "/" opens a popup whose next keypress runs a
// command. Forms and y/n confirmations keep their own keys.
//
// Concurrency rule: tea.Cmds only do I/O. Everything they produce is
// applied in Update, which is the only place the model, *config.Config and
// the vault session temp dir are mutated.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"ttssh/internal/config"
	"ttssh/internal/ui"
	"ttssh/internal/vault"
)

const (
	statusLifetime  = 4 * time.Second
	minDetailsWidth = 80 // narrower terminals hide the details pane
	listPanePercent = 55 // share of the width taken by the list pane
	minBodyHeight   = 5  // panel border, title, and at least one row
	commandsKey     = "/"
)

// keyMap holds the keys the model itself reacts to.
type keyMap struct {
	ForceQuit, Back, Cancel, Yes, No        key.Binding
	Enter, Left, Right, Backspace, Commands key.Binding
}

func newKeyMap() keyMap {
	bind := func(help, desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
	}
	return keyMap{
		ForceQuit: bind("ctrl+c", "quit", "ctrl+c"),
		Back:      bind("esc", "back", "esc"),
		Cancel:    bind("esc", "cancel", "esc"),
		Yes:       bind("y", "confirm", "y", "Y"),
		No:        bind("n/esc", "cancel", "n", "N", "esc"),
		Enter:     bind("enter", "select", "enter"),
		Left:      bind("←", "left", "left"),
		Right:     bind("→", "right", "right"),
		Backspace: bind("backspace", "erase", "backspace", "ctrl+h"),
		Commands:  bind(commandsKey, "commands", commandsKey),
	}
}

// hint builds a display-only binding for the footer.
func hint(k, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(k), key.WithHelp(k, desc))
}

// listKeyMap is the bubbles list keymap reduced to arrow navigation: letters
// belong to the type-to-filter query and the "/" popup, and the model handles
// quitting.
func listKeyMap() list.KeyMap {
	km := list.DefaultKeyMap()
	km.CursorUp.SetKeys("up")
	km.CursorDown.SetKeys("down")
	km.PrevPage.SetKeys("pgup")
	km.PrevPage.SetHelp("pgup", "prev page")
	km.NextPage.SetKeys("pgdown")
	km.NextPage.SetHelp("pgdn", "next page")
	km.GoToStart.SetKeys("home")
	km.GoToEnd.SetKeys("end")
	km.Filter.SetKeys()
	km.ClearFilter.SetKeys()
	km.CancelWhileFiltering.SetKeys()
	km.AcceptWhileFiltering.SetKeys()
	km.ShowFullHelp.SetKeys()
	km.CloseFullHelp.SetKeys()
	km.Quit.SetKeys()
	km.ForceQuit.SetKeys()
	return km
}

type itemKind int

const (
	itemNone itemKind = iota // zero value: nothing selected
	itemRecent
	itemFolder
	itemEntry
	itemHeader
	itemNoKey
	itemVaultKey
	itemLocalKey
	itemDir
	itemDrive
	itemFile
)

// item is one row of any list screen.
type item struct {
	kind   itemKind
	label  string
	value  string // path, unit id, or remote path, depending on kind
	recent config.Recent
	unit   vault.Unit
	entry  config.Entry
	path   []string // folder rows: the folder's path; entry rows: the containing folder
}

func (i item) Title() string       { return i.label }
func (i item) Description() string { return "" }
func (i item) FilterValue() string { return i.label }

// id identifies a row across list rebuilds so the selection can be kept.
func (i item) id() string {
	switch i.kind {
	case itemFolder:
		return "d:" + pathKey(i.path)
	case itemEntry:
		return "e:" + pathKey(i.path) + "\x00" + i.entry.User + "@" + i.entry.Host + "\x00" + i.entry.Key
	case itemRecent:
		return "r:" + i.recent.User + "@" + i.recent.Host + "\x00" + i.recent.Key
	}
	return fmt.Sprintf("%d:%s:%s", i.kind, i.value, i.label)
}

func toListItems(items []item) []list.Item {
	out := make([]list.Item, len(items))
	for i, it := range items {
		out[i] = it
	}
	return out
}

// filterItems keeps the items whose label contains every space-separated
// term of query, ignoring case.
func filterItems(items []item, query string) []item {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return items
	}
	var out []item
	for _, it := range items {
		label := strings.ToLower(it.label)
		if !slices.ContainsFunc(terms, func(t string) bool { return !strings.Contains(label, t) }) {
			out = append(out, it)
		}
	}
	return out
}

func newList(title, singular, plural string) list.Model {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	d.Styles = ui.ListItemStyles()

	l := list.New(nil, d, 0, 0)
	l.Title = title
	l.Styles = ui.ListStyles()
	l.KeyMap = listKeyMap()
	l.DisableQuitKeybindings()
	l.SetFilteringEnabled(false) // the model filters, see screen.query
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetStatusBarItemName(singular, plural)
	return l
}

type screenKind int

const (
	screenDashboard screenKind = iota
	screenKeys
	screenFolders
	screenFiles
	screenForm
)

// command is one entry of the "/" popup.
type command struct {
	key  string
	desc string
	run  func(m *model) tea.Cmd
}

// popup is the open "/" command popup; the next keypress picks a command.
type popup struct{ cmds []command }

// screen is one step of the TUI: a type-to-filter list with an optional
// details pane, or an embedded huh form. The flow that pushes a screen
// supplies its behavior through the callbacks.
type screen struct {
	kind    screenKind
	list    list.Model
	form    *huh.Form
	details func(item) string // right pane content; nil hides the pane
	empty   string            // hint shown instead of an empty list

	all   []item // every row before filtering
	query string // typed filter text
	// build replaces the plain filter over all, for screens whose rows
	// depend on the query (the dashboard's tree turns flat while filtering).
	build func(query string) []item

	// commands lists what the "/" popup offers right now; the model adds
	// help and quit.
	commands func(m *model) []command
	// enterDesc names what enter does on the item, for the footer.
	enterDesc func(item) string
	hints     []key.Binding // extra footer hints

	// onKey handles a key before it is typed into the query; false lets the
	// model go on.
	onKey func(m *model, msg tea.KeyMsg) (tea.Cmd, bool)
	// onSubmit runs once a form completes, after its screen was popped.
	onSubmit func(m *model) tea.Cmd
	// onBack replaces popping the screen on esc.
	onBack func(m *model) tea.Cmd
}

func (s *screen) selected() item {
	it, _ := s.list.SelectedItem().(item)
	return it
}

// refilter shows the rows matching the query.
func (s *screen) refilter() tea.Cmd {
	var rows []item
	if s.build != nil {
		rows = s.build(s.query)
	} else {
		rows = filterItems(s.all, s.query)
	}
	return s.list.SetItems(toListItems(rows))
}

// setItems replaces every row, keeping the query.
func (s *screen) setItems(items []item) tea.Cmd {
	s.all = items
	return s.refilter()
}

// setQuery changes the filter; the selection moves to the top unless
// keepSelection finds the previously selected row again.
func (s *screen) setQuery(query string, keepSelection bool) tea.Cmd {
	id := s.selected().id()
	s.query = query
	cmd := s.refilter()
	s.list.Select(0)
	if keepSelection {
		s.selectID(id)
	}
	s.skipHeader(1)
	return cmd
}

func (s *screen) selectID(id string) bool {
	for i, li := range s.list.Items() {
		if it, _ := li.(item); it.id() == id {
			s.list.Select(i)
			return true
		}
	}
	return false
}

// skipHeader moves off a section header, which is not a real choice.
func (s *screen) skipHeader(dir int) {
	if s.selected().kind != itemHeader {
		return
	}
	i := s.list.Index() + dir
	if i < 0 || i >= len(s.list.Items()) {
		i = s.list.Index() - dir
	}
	s.list.Select(i)
}

// formScreen wraps a form; onSubmit continues the flow once it completes.
func formScreen(form *huh.Form, onSubmit func(m *model) tea.Cmd) *screen {
	return &screen{kind: screenForm, form: form, onSubmit: onSubmit}
}

// newForm applies the shared theme; the footer shows the form's keys.
func newForm(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).WithTheme(ui.HuhTheme()).WithShowHelp(false)
}

// inputForm is a one-line text input whose placeholder is the default.
func inputForm(title, placeholder string, value *string, validate func(string) error) *huh.Form {
	in := huh.NewInput().Title(title).Placeholder(placeholder).Value(value)
	if validate != nil {
		in = in.Validate(validate)
	}
	return newForm(huh.NewGroup(in))
}

// orDefault trims s and returns def when it is empty.
func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s == "" {
		return def
	}
	return s
}

// operation is the single in-flight async job. Its result is applied only
// if it is still current, so results of cancelled jobs are dropped.
type operation struct {
	id       int
	label    string
	blocking bool    // blocking operations swallow keys except esc
	owner    *screen // popping the owner cancels the operation
	cancel   context.CancelFunc
}

// opResultMsg carries an operation's outcome; apply runs inside Update.
type opResultMsg struct {
	id    int
	apply func(m *model) tea.Cmd
}

type statusKind int

const (
	statusInfo statusKind = iota
	statusSuccess
	statusWarn
	statusError
)

type status struct {
	kind statusKind
	text string
	id   int
}

type statusExpiredMsg struct{ id int }

type execDoneMsg struct {
	name    string
	success string
	err     error
}

// confirmation is a y/n question shown in the status line; onYes runs when
// the user confirms.
type confirmation struct {
	prompt string
	onYes  func(m *model) tea.Cmd
}

// model is the dashboard. It is used through a pointer so callbacks can
// mutate it; Bubble Tea only calls Update and View from its event loop.
type model struct {
	// ctx is the root context for async operations and child processes. It
	// is stored because Bubble Tea's Update has no context parameter.
	ctx     context.Context
	cfg     *config.Config // nil when only the folder browser runs
	vlt     *vault.Client  // nil when the vault is off
	tempDir *string        // vault session key dir; main owns and cleans it
	keyDir  string
	version string

	keys    keyMap
	help    help.Model
	spinner spinner.Model
	stack   []*screen
	width   int
	height  int

	collapsed map[string]bool // folders folded on the dashboard, by pathKey; session only

	op         *operation
	lastOpID   int
	status     status
	lastStID   int
	confirming *confirmation // y/n question awaiting an answer
	popup      *popup        // "/" command popup awaiting a key
	quitting   bool
	startup    tea.Cmd
}

func newModel(ctx context.Context, cfg *config.Config, vlt *vault.Client, tempDir *string, keyDir, version string) *model {
	m := &model{
		ctx: ctx, cfg: cfg, vlt: vlt, tempDir: tempDir, keyDir: keyDir, version: version,
		keys:      newKeyMap(),
		help:      help.New(),
		collapsed: map[string]bool{},
		spinner: spinner.New(
			spinner.WithSpinner(spinner.Dot),
			spinner.WithStyle(lipgloss.NewStyle().Foreground(ui.Accent))),
	}
	m.help.Styles = ui.HelpStyles()
	return m
}

func (m *model) Init() tea.Cmd { return m.startup }

func (m *model) top() *screen {
	if len(m.stack) == 0 {
		return nil
	}
	return m.stack[len(m.stack)-1]
}

func (m *model) push(s *screen) tea.Cmd {
	m.stack = append(m.stack, s)
	m.layout()
	if s.form != nil {
		return s.form.Init()
	}
	return nil
}

// popTo truncates the stack to n screens, cancelling an operation owned by
// a removed screen.
func (m *model) popTo(n int) {
	if n < 0 || n > len(m.stack) {
		return
	}
	for _, s := range m.stack[n:] {
		if m.op != nil && m.op.owner == s {
			m.stopOp()
		}
	}
	clear(m.stack[n:])
	m.stack = m.stack[:n]
}

// popScreen removes s and every screen above it.
func (m *model) popScreen(s *screen) {
	if i := slices.Index(m.stack, s); i >= 0 {
		m.popTo(i)
	}
}

func (m *model) back() tea.Cmd {
	s := m.top()
	if s.onBack != nil {
		return s.onBack(m)
	}
	m.popTo(len(m.stack) - 1)
	return nil
}

func (m *model) quit() tea.Cmd {
	m.stopOp()
	m.quitting = true
	return tea.Quit
}

// startOp runs work in a tea.Cmd under a cancellable context. work does
// I/O only and returns the closure that Update applies with the outcome.
func (m *model) startOp(label string, blocking bool, work func(ctx context.Context) func(*model) tea.Cmd) tea.Cmd {
	m.stopOp()
	ctx, cancel := context.WithCancel(m.ctx)
	m.lastOpID++
	id := m.lastOpID
	m.op = &operation{id: id, label: label, blocking: blocking, owner: m.top(), cancel: cancel}
	return tea.Batch(
		func() tea.Msg { return opResultMsg{id: id, apply: work(ctx)} },
		m.spinner.Tick,
	)
}

// stopOp cancels the current operation; its late result will be ignored.
func (m *model) stopOp() {
	if m.op != nil {
		m.op.cancel()
		m.op = nil
	}
}

func (m *model) setStatus(kind statusKind, text string) tea.Cmd {
	m.lastStID++
	id := m.lastStID
	m.status = status{kind: kind, text: text, id: id}
	return tea.Tick(statusLifetime, func(time.Time) tea.Msg { return statusExpiredMsg{id: id} })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.layout()
	return m, cmd
}

func (m *model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		if s := m.top(); s != nil && s.form != nil {
			return m.updateForm(s, m.formSize())
		}
		return nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case spinner.TickMsg:
		if m.op == nil {
			return nil // let the tick loop die
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return cmd
	case statusExpiredMsg:
		if msg.id == m.status.id {
			m.status = status{}
		}
		return nil
	case opResultMsg:
		if m.op == nil || m.op.id != msg.id {
			return nil // stale: cancelled or superseded
		}
		m.stopOp()
		return msg.apply(m)
	case execDoneMsg:
		return m.execDone(msg)
	}
	return m.forward(msg)
}

func (m *model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if key.Matches(msg, m.keys.ForceQuit) {
		return m.quit()
	}
	m.help.ShowAll = false // the full help lasts until the next key
	s := m.top()
	switch {
	case s == nil:
		return nil
	case m.confirming != nil:
		return m.confirmKey(msg)
	case m.op != nil && m.op.blocking:
		if key.Matches(msg, m.keys.Cancel) {
			m.stopOp()
			return m.setStatus(statusInfo, "Cancelled.")
		}
		return nil
	case m.popup != nil:
		return m.popupKey(msg)
	case s.form != nil:
		// esc leaves the form unless the focused field uses it, as a
		// multi-select does to end or clear its filter.
		if key.Matches(msg, m.keys.Back) && !key.Matches(msg, s.form.KeyBinds()...) {
			return m.back()
		}
		return m.updateForm(s, msg)
	}
	return m.listKey(s, msg)
}

// listKey routes a key on a list screen: "/" opens the popup, esc clears the
// query or goes back, other printable text edits the query.
func (m *model) listKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Commands):
		m.popup = &popup{cmds: m.popupCommands(s)}
		return nil
	case key.Matches(msg, m.keys.Back):
		if s.query != "" {
			return s.setQuery("", true)
		}
		return m.back()
	case key.Matches(msg, m.keys.Backspace) && s.query != "":
		runes := []rune(s.query)
		return s.setQuery(string(runes[:len(runes)-1]), false)
	}
	if s.onKey != nil {
		if cmd, handled := s.onKey(m, msg); handled {
			return cmd
		}
	}
	if text, ok := typedText(msg); ok {
		return s.setQuery(s.query+text, false)
	}
	cmd := m.forward(msg)
	dir := 1
	if slices.Contains([]string{"up", "pgup", "home"}, msg.String()) {
		dir = -1
	}
	s.skipHeader(dir)
	return cmd
}

// typedText reports the text a key press types; "/" never gets here.
func typedText(msg tea.KeyMsg) (string, bool) {
	switch {
	case msg.Alt:
		return "", false
	case msg.Type == tea.KeySpace:
		return " ", true
	case msg.Type == tea.KeyRunes:
		return string(msg.Runes), true
	}
	return "", false
}

// popupCommands is what the screen offers plus help and quit.
func (m *model) popupCommands(s *screen) []command {
	var cmds []command
	if s.commands != nil {
		cmds = s.commands(m)
	}
	return append(cmds,
		command{"?", "full help", func(m *model) tea.Cmd { m.help.ShowAll = true; return nil }},
		command{"q", "quit", func(m *model) tea.Cmd { return m.quit() }},
	)
}

// popupKey runs the command bound to the key and closes the popup.
func (m *model) popupKey(msg tea.KeyMsg) tea.Cmd {
	p := m.popup
	m.popup = nil
	k := msg.String()
	if k == "esc" || k == commandsKey {
		return nil
	}
	for _, c := range p.cmds {
		if c.key == k {
			return c.run(m)
		}
	}
	return m.setStatus(statusInfo, fmt.Sprintf("no shortcut %q", k))
}

// forward hands msg to the top screen's list or form.
func (m *model) forward(msg tea.Msg) tea.Cmd {
	s := m.top()
	if s == nil {
		return nil
	}
	if s.form != nil {
		return m.updateForm(s, msg)
	}
	var cmd tea.Cmd
	s.list, cmd = s.list.Update(msg)
	return cmd
}

func (m *model) updateForm(s *screen, msg tea.Msg) tea.Cmd {
	next, cmd := s.form.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.form = f
	}
	if s.form.State != huh.StateCompleted {
		return cmd
	}
	m.popScreen(s)
	if s.onSubmit == nil {
		return cmd
	}
	return tea.Batch(cmd, s.onSubmit(m))
}

// runTerminal suspends the TUI and runs name with the real terminal; the
// TUI resumes when it exits. success is shown when it exits cleanly.
func (m *model) runTerminal(success, name string, args ...string) tea.Cmd {
	c := terminalCmd{exec.CommandContext(m.ctx, name, args...)}
	return tea.Exec(c, func(err error) tea.Msg {
		return execDoneMsg{name: name, success: success, err: err}
	})
}

func (m *model) execDone(msg execDoneMsg) tea.Cmd {
	var exitErr *exec.ExitError
	switch {
	case msg.err == nil && msg.success == "":
		return nil
	case msg.err == nil:
		return m.setStatus(statusSuccess, msg.success)
	case errors.As(msg.err, &exitErr):
		return m.setStatus(statusWarn, fmt.Sprintf("%s exited with code %d", msg.name, exitErr.ExitCode()))
	default:
		return m.setStatus(statusError, fmt.Sprintf("running %s: %v (is it installed and on PATH?)", msg.name, msg.err))
	}
}

// terminalCmd echoes the command line before running it, so the plain
// terminal shows what ttssh launched while the TUI is suspended.
type terminalCmd struct{ *exec.Cmd }

func (c terminalCmd) SetStdin(r io.Reader)  { c.Stdin = r }
func (c terminalCmd) SetStdout(w io.Writer) { c.Stdout = w }
func (c terminalCmd) SetStderr(w io.Writer) { c.Stderr = w }

func (c terminalCmd) Run() error {
	ui.PrintCommand(c.Args[0], c.Args[1:])
	return c.Cmd.Run()
}

// ---- layout and view ----

// bodySize is the space between the header and the status line.
func (m *model) bodySize() (int, int) {
	return m.width, m.height - 2 - lipgloss.Height(m.footerView())
}

// paneWidths splits the body between the list and the details pane; the
// details pane is dropped on narrow terminals.
func (m *model) paneWidths(s *screen) (int, int) {
	if s.details == nil || m.width < minDetailsWidth {
		return m.width, 0
	}
	left := m.width * listPanePercent / 100
	return left, m.width - left
}

// layout sizes every list to its pane.
func (m *model) layout() {
	_, bodyH := m.bodySize()
	for _, s := range m.stack {
		if s.form != nil {
			continue
		}
		listW, _ := m.paneWidths(s)
		w, h := ui.PanelInnerSize(listW, bodyH)
		s.list.SetSize(w, h)
	}
}

// formSize is the WindowSizeMsg a form sees: the inside of the body panel.
func (m *model) formSize() tea.WindowSizeMsg {
	w, h := ui.PanelInnerSize(m.bodySize())
	return tea.WindowSizeMsg{Width: w, Height: h}
}

func (m *model) View() string {
	s := m.top()
	if m.quitting || s == nil || m.width == 0 {
		return ""
	}
	_, bodyH := m.bodySize()
	body := m.bodyView(s, bodyH)
	if m.popup != nil {
		body = overlayCentered(body, m.popupView(), m.width)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.headerView(), body, m.statusView(), m.footerView())
}

// popupView renders the command popup: aligned key and description rows.
func (m *model) popupView() string {
	keyWidth := 0
	for _, c := range m.popup.cmds {
		keyWidth = max(keyWidth, lipgloss.Width(c.key))
	}
	lines := []string{ui.TitleStyle.Render("Commands") + ui.MutedStyle.Render("  esc closes")}
	for _, c := range m.popup.cmds {
		pad := strings.Repeat(" ", keyWidth-lipgloss.Width(c.key))
		lines = append(lines, ui.KeyStyle.Render(c.key)+pad+"  "+c.desc)
	}
	return ui.Popup(strings.Join(lines, "\n"))
}

// overlayCentered draws box over the middle of base, leaving the rest of base
// visible around it.
func overlayCentered(base, box string, width int) string {
	baseLines := strings.Split(base, "\n")
	boxLines := strings.Split(box, "\n")
	boxWidth := lipgloss.Width(box)
	top := max((len(baseLines)-len(boxLines))/2, 0)
	left := max((width-boxWidth)/2, 0)
	for i, boxLine := range boxLines {
		y := top + i
		if y >= len(baseLines) {
			break
		}
		line := baseLines[y]
		prefix := ansi.Truncate(line, left, "")
		prefix += strings.Repeat(" ", max(left-lipgloss.Width(prefix), 0))
		baseLines[y] = prefix + "\x1b[0m" + boxLine + "\x1b[0m" + ansi.TruncateLeft(line, left+boxWidth, "")
	}
	return strings.Join(baseLines, "\n")
}

func (m *model) headerView() string {
	width := m.width - 2
	left := ui.TitleStyle.Render("TTSSH")
	if m.version != "" {
		left += " " + ui.MutedStyle.Render(m.version)
	}
	var right string
	if m.cfg != nil {
		vaultState := ui.MutedStyle.Render("vault off")
		if m.vlt != nil {
			vaultState = ui.SuccessStyle.Render("☁ vault")
		}
		right = vaultState + ui.MutedStyle.Render("  ·  keys "+m.keyDir)
	}
	room := width - lipgloss.Width(left) - 1
	right = lipgloss.NewStyle().MaxWidth(max(room, 0)).Render(right)
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return lipgloss.NewStyle().Padding(0, 1).Render(left + strings.Repeat(" ", gap) + right)
}

func (m *model) bodyView(s *screen, height int) string {
	if height < minBodyHeight || m.width < minBodyHeight*2 {
		return lipgloss.NewStyle().Height(max(height, 0)).Render(
			ui.MutedStyle.Render(" Window too small, enlarge it or press ctrl+c to quit."))
	}
	if s.form != nil {
		return ui.Panel(s.form.View(), m.width, height, true)
	}
	listW, detailsW := m.paneWidths(s)
	innerW, _ := ui.PanelInnerSize(listW, height)
	l := s.list // a copy, so the title is shortened for this frame only
	if s.query != "" {
		l.Title = ellipsizeLeft(l.Title, max(innerW-lipgloss.Width(s.query)-4, 1)) + "  › " + s.query + "▏"
	} else {
		l.Title = ellipsizeLeft(l.Title, innerW)
	}
	content := l.View()
	if len(l.Items()) == 0 {
		hintText := s.empty
		if s.query != "" {
			hintText = "Nothing matches.\n\nesc clears the filter."
		}
		content = ui.TitleStyle.Render(l.Title) + "\n\n" + ui.MutedStyle.Render(hintText)
	}
	left := ui.Panel(content, listW, height, true)
	if detailsW == 0 {
		return left
	}
	details := ui.TitleStyle.Render("Details") + "\n\n"
	if it := s.selected(); it.kind != itemNone {
		details += s.details(it)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, ui.Panel(details, detailsW, height, false))
}

func (m *model) statusView() string {
	var line string
	switch {
	case m.confirming != nil:
		line = ui.WarnStyle.Render("? "+m.confirming.prompt) +
			ui.MutedStyle.Render("  y / n")
	case m.op != nil:
		line = m.spinner.View() + " " + m.op.label
		if m.op.blocking {
			line += ui.MutedStyle.Render("  esc to cancel")
		}
	case m.status.text != "":
		line = statusLine(m.status)
	}
	return lipgloss.NewStyle().Padding(0, 1).MaxWidth(m.width).Render(line)
}

// ellipsizeLeft shortens s to width cells by dropping its start, keeping
// the informative end of long paths.
func ellipsizeLeft(s string, width int) string {
	if lipgloss.Width(s) <= width || width < 2 {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[1:]
	}
	return "…" + string(runes)
}

// statusLine pairs every message with an icon so state never relies on color.
func statusLine(st status) string {
	switch st.kind {
	case statusSuccess:
		return ui.SuccessStyle.Render("✓ " + st.text)
	case statusWarn:
		return ui.WarnStyle.Render("! " + st.text)
	case statusError:
		return ui.ErrorStyle.Render("✗ " + st.text)
	default:
		return ui.MutedStyle.Render("· " + st.text)
	}
}

func (m *model) footerView() string {
	h := m.help
	h.Width = max(m.width-2, 0)
	short, full := m.helpKeys()
	view := h.ShortHelpView(short)
	if h.ShowAll {
		view = h.FullHelpView(full)
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(view)
}

// helpKeys returns the bindings that match what the current screen accepts:
// a minimal row, and the full columns for "/ ?".
func (m *model) helpKeys() ([]key.Binding, [][]key.Binding) {
	s := m.top()
	var only []key.Binding
	switch {
	case s == nil:
		return nil, nil
	case m.confirming != nil:
		only = []key.Binding{m.keys.Yes, m.keys.No}
	case m.op != nil && m.op.blocking:
		only = []key.Binding{m.keys.Cancel, m.keys.ForceQuit}
	case m.popup != nil:
		only = []key.Binding{hint("key", "run command"), hint("esc", "close")}
	case s.form != nil:
		only = append(slices.Clone(s.form.KeyBinds()), m.keys.Back, m.keys.ForceQuit)
	}
	if only != nil {
		return only, [][]key.Binding{only}
	}

	short := []key.Binding{hint("↑/↓", "move")}
	if s.enterDesc != nil {
		if it := s.selected(); it.kind != itemNone {
			short = append(short, hint("enter", s.enterDesc(it)))
		}
	}
	short = append(short, s.hints...)
	short = append(short, hint("type", "to filter"), hint(commandsKey, "commands"))
	switch {
	case s.query != "":
		short = append(short, hint("esc", "clear filter"))
	case len(m.stack) > 1:
		short = append(short, m.keys.Back)
	}

	nav := []key.Binding{hint("↑/↓", "move"), hint("pgup/pgdn", "page")}
	nav = append(nav, s.hints...)
	nav = append(nav, hint("type", "to filter"), m.keys.Back, hint(commandsKey, "commands"))
	var cmds []key.Binding
	for _, c := range m.popupCommands(s) {
		cmds = append(cmds, hint(commandsKey+" "+c.key, c.desc))
	}
	return short, [][]key.Binding{nav, cmds}
}

// pathKey joins a folder path into a map key and display string.
func pathKey(path []string) string { return strings.Join(path, "/") }
