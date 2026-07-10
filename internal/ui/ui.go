// Package ui holds ttssh's shared terminal styling and prompt helpers.
package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"ttssh/internal/config"
)

var (
	accent = lipgloss.Color("212")
	dim    = lipgloss.Color("241")
	green  = lipgloss.Color("42")
	red    = lipgloss.Color("196")

	bannerStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accent).
			Padding(0, 2)

	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(accent)
	subtitleStyle = lipgloss.NewStyle().Foreground(dim)
	successStyle  = lipgloss.NewStyle().Foreground(green).Bold(true)
	warnStyle     = lipgloss.NewStyle().Foreground(red)
	targetStyle   = lipgloss.NewStyle().Foreground(accent).Bold(true)
	cmdStyle      = lipgloss.NewStyle().Foreground(dim)

	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(dim).
			Padding(0, 1)
)

// PrintBanner prints ttssh's startup banner showing the version, active key
// directory, and vault status.
func PrintBanner(keyDir string, vaultOn bool, version string) {
	fmt.Println(bannerStyle.Render(
		titleStyle.Render("⚡ TTSSH") + " " + subtitleStyle.Render(version) + "  " + subtitleStyle.Render("interactive SSH manager")))
	fmt.Println(subtitleStyle.Render("  📁 keys: " + keyDir))
	if vaultOn {
		fmt.Println(subtitleStyle.Render("  ☁ vault: connected"))
	}
	fmt.Println()
}

// PrintSessionCard shows the active connection above the action menu.
func PrintSessionCard(target, key string) {
	fmt.Println(cardStyle.Render(
		targetStyle.Render("🔗 "+target) + "\n" +
			subtitleStyle.Render("🔑 "+key)))
}

// PrintSuccess prints a success message.
func PrintSuccess(msg string) {
	fmt.Println(successStyle.Render("✓ " + msg))
	fmt.Println()
}

// PrintWarn prints a warning message.
func PrintWarn(msg string) {
	fmt.Println(warnStyle.Render("! " + msg))
	fmt.Println()
}

// PrintNote prints an informational note.
func PrintNote(msg string) {
	fmt.Println(subtitleStyle.Render(msg))
	fmt.Println()
}

// PrintCommand prints the external command about to be run.
func PrintCommand(name string, args []string) {
	fmt.Println(cmdStyle.Render("> " + name + " " + strings.Join(args, " ")))
	fmt.Println()
}

// SelectOne shows a styled single-select menu and returns the chosen value.
func SelectOne[T comparable](title string, options []huh.Option[T]) (T, error) {
	var choice T
	err := huh.NewSelect[T]().
		Title(title).
		Options(options...).
		Value(&choice).
		WithTheme(huh.ThemeCharm()).
		Run()
	return choice, err
}

// InputLine shows a styled text input. If the user submits an empty line,
// def is returned. required fields reject empty input instead.
func InputLine(title, def string, required bool) (string, error) {
	var val string
	field := huh.NewInput().
		Title(title).
		Value(&val).
		Validate(func(s string) error {
			if required && def == "" && strings.TrimSpace(s) == "" {
				return fmt.Errorf("required")
			}
			return nil
		})
	if def != "" {
		field = field.Placeholder(def)
	}
	if err := field.WithTheme(huh.ThemeCharm()).Run(); err != nil {
		return "", err
	}
	val = strings.TrimSpace(val)
	if val == "" {
		return def, nil
	}
	return val, nil
}

// InputDir prompts for a folder path with live validation that it exists.
// Empty input returns def; a leading ~ is expanded.
func InputDir(title, def string) (string, error) {
	var val string
	err := huh.NewInput().
		Title(title).
		Placeholder(def).
		Description("Path to a folder with *.key files · leave empty to keep " + def).
		Validate(func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil
			}
			info, err := os.Stat(config.ExpandHome(s))
			if err != nil || !info.IsDir() {
				return fmt.Errorf("folder not found")
			}
			return nil
		}).
		Value(&val).
		WithTheme(huh.ThemeCharm()).
		Run()
	if err != nil {
		return "", err
	}
	val = strings.TrimSpace(val)
	if val == "" {
		return def, nil
	}
	return config.ExpandHome(val), nil
}

// RelTime renders a compact "how long ago" label for the recents list.
func RelTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
