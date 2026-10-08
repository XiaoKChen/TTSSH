// Package ui holds ttssh's palette, shared styles, and CLI print helpers.
package ui

import (
	"fmt"
	"strings"
	"time"
)

// PrintSuccess prints a success message.
func PrintSuccess(msg string) {
	fmt.Println(SuccessStyle.Render("✓ " + msg))
	fmt.Println()
}

// PrintWarn prints a warning message.
func PrintWarn(msg string) {
	fmt.Println(WarnStyle.Render("! " + msg))
	fmt.Println()
}

// PrintNote prints an informational note.
func PrintNote(msg string) {
	fmt.Println(MutedStyle.Render(msg))
	fmt.Println()
}

// PrintCommand prints the external command about to be run.
func PrintCommand(name string, args []string) {
	fmt.Println(MutedStyle.Render("> " + name + " " + strings.Join(args, " ")))
	fmt.Println()
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
