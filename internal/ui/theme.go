package ui

// The palette and every style derived from it. Colors are adaptive so the
// same tokens read well on light and dark terminals.

import (
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// Palette tokens. Everything else in this package is derived from these.
var (
	// Accent marks the app title, focus, and key names.
	Accent = lipgloss.AdaptiveColor{Light: "#5B4BC4", Dark: "#A496FF"}
	// OnAccent is text drawn on an Accent background.
	OnAccent = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#16122B"}
	// Text is regular foreground text.
	Text = lipgloss.AdaptiveColor{Light: "#1F2328", Dark: "#E6E6E6"}
	// Muted is secondary text: labels, hints, descriptions.
	Muted = lipgloss.AdaptiveColor{Light: "#656D76", Dark: "#8B949E"}
	// Subtle is tertiary text such as separators.
	Subtle = lipgloss.AdaptiveColor{Light: "#A0A7AF", Dark: "#5A616A"}
	// Border outlines unfocused panels.
	Border = lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#3D444D"}
	// FocusBorder outlines the focused panel.
	FocusBorder = Accent
	// Success marks completed actions.
	Success = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#3FB950"}
	// Warning marks recoverable problems.
	Warning = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#D29922"}
	// Error marks failures.
	Error = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#F85149"}
	// Highlight is the strongest neutral: black on light terminals, white on dark.
	Highlight = lipgloss.AdaptiveColor{Light: "#000000", Dark: "#FFFFFF"}
	// SelectionBg is the neutral grey background of the selected list row.
	SelectionBg = lipgloss.AdaptiveColor{Light: "#E4E4E7", Dark: "#2A2A2E"}
)

// Styles shared by the CLI output and the dashboard.
var (
	// TitleStyle renders headings and the app name.
	TitleStyle = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	// TextStyle renders regular text.
	TextStyle = lipgloss.NewStyle().Foreground(Text)
	// MutedStyle renders secondary text.
	MutedStyle = lipgloss.NewStyle().Foreground(Muted)
	// KeyStyle renders key names in hints.
	KeyStyle = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	// SuccessStyle renders success messages.
	SuccessStyle = lipgloss.NewStyle().Bold(true).Foreground(Success)
	// WarnStyle renders warnings.
	WarnStyle = lipgloss.NewStyle().Bold(true).Foreground(Warning)
	// ErrorStyle renders errors.
	ErrorStyle = lipgloss.NewStyle().Bold(true).Foreground(Error)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Border).
			Padding(0, 1)
)

// panelChrome is the horizontal and vertical space a panel's border and
// padding take up.
const (
	panelChromeWidth  = 4
	panelChromeHeight = 2
)

// PanelInnerSize returns the content size of a panel of the given outer size.
func PanelInnerSize(width, height int) (int, int) {
	return max(width-panelChromeWidth, 0), max(height-panelChromeHeight, 0)
}

// Panel draws content in a rounded frame of exactly width x height cells,
// wrapping long lines and clipping what does not fit. The focused panel gets
// the accent border.
func Panel(content string, width, height int, focused bool) string {
	innerW, innerH := PanelInnerSize(width, height)
	content = lipgloss.NewStyle().Width(innerW).Render(content)
	if lines := strings.Split(content, "\n"); len(lines) > innerH {
		content = strings.Join(lines[:innerH], "\n")
	}
	style := panelStyle.Width(innerW + 2).Height(innerH)
	if focused {
		style = style.BorderForeground(FocusBorder)
	}
	return style.Render(content)
}

// ListStyles styles a bubbles list (title, filter prompt, status bar).
func ListStyles() list.Styles {
	s := list.DefaultStyles()
	s.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)
	s.Title = TitleStyle
	s.Spinner = lipgloss.NewStyle().Foreground(Accent)
	s.FilterPrompt = lipgloss.NewStyle().Foreground(Accent)
	s.FilterCursor = lipgloss.NewStyle().Foreground(Accent)
	s.StatusBar = lipgloss.NewStyle().Foreground(Muted).Padding(0, 0, 1, 0)
	s.StatusEmpty = MutedStyle
	s.StatusBarActiveFilter = TextStyle
	s.StatusBarFilterCount = lipgloss.NewStyle().Foreground(Subtle)
	s.NoItems = MutedStyle
	s.PaginationStyle = lipgloss.NewStyle()
	s.ArabicPagination = MutedStyle
	s.ActivePaginationDot = lipgloss.NewStyle().Foreground(Accent).SetString("•")
	s.InactivePaginationDot = lipgloss.NewStyle().Foreground(Subtle).SetString("•")
	s.DividerDot = lipgloss.NewStyle().Foreground(Subtle).SetString(" • ")
	return s
}

// ListItemStyles styles list rows. The selected row is monochrome and
// carries a left bar, so selection never depends on color alone.
func ListItemStyles() list.DefaultItemStyles {
	s := list.NewDefaultItemStyles()
	s.NormalTitle = lipgloss.NewStyle().Foreground(Text).Padding(0, 0, 0, 2)
	s.SelectedTitle = lipgloss.NewStyle().
		Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderForeground(Highlight).
		Foreground(Highlight).
		Background(SelectionBg).
		Bold(true).
		Padding(0, 0, 0, 1)
	s.DimmedTitle = lipgloss.NewStyle().Foreground(Muted).Padding(0, 0, 0, 2)
	s.FilterMatch = lipgloss.NewStyle().Underline(true)
	return s
}

// HelpStyles styles the bubbles help footer.
func HelpStyles() help.Styles {
	key := lipgloss.NewStyle().Foreground(Accent)
	desc := lipgloss.NewStyle().Foreground(Muted)
	sep := lipgloss.NewStyle().Foreground(Subtle)
	return help.Styles{
		Ellipsis:       sep,
		ShortKey:       key,
		ShortDesc:      desc,
		ShortSeparator: sep,
		FullKey:        key,
		FullDesc:       desc,
		FullSeparator:  sep,
	}
}

// HuhTheme is the huh form theme built from the palette.
func HuhTheme() *huh.Theme {
	t := huh.ThemeBase()

	t.Focused.Base = t.Focused.Base.BorderForeground(Accent)
	t.Focused.Card = t.Focused.Base
	t.Focused.Title = t.Focused.Title.Foreground(Accent).Bold(true)
	t.Focused.NoteTitle = t.Focused.NoteTitle.Foreground(Accent).Bold(true).MarginBottom(1)
	t.Focused.Directory = t.Focused.Directory.Foreground(Accent)
	t.Focused.Description = t.Focused.Description.Foreground(Muted)
	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(Error)
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(Error)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(Accent)
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(Accent)
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(Accent)
	t.Focused.Option = t.Focused.Option.Foreground(Text)
	t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(Accent)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(Success)
	t.Focused.SelectedPrefix = lipgloss.NewStyle().Foreground(Success).SetString("[✓] ")
	t.Focused.UnselectedPrefix = lipgloss.NewStyle().Foreground(Muted).SetString("[ ] ")
	t.Focused.UnselectedOption = t.Focused.UnselectedOption.Foreground(Text)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(OnAccent).Background(Accent).Bold(true)
	t.Focused.Next = t.Focused.FocusedButton
	t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(Text).Background(SelectionBg)
	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(Accent)
	t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(Subtle)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(Accent)
	t.Focused.TextInput.Text = t.Focused.TextInput.Text.Foreground(Text)

	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.Title = t.Blurred.Title.Foreground(Muted)
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()

	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description
	t.Help = HelpStyles()
	return t
}
