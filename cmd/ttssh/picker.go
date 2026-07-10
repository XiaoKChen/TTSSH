package main

// The key picker and folder browser: fuzzy-select an SSH key from vault
// entries and local *.key files, with folder switching built in.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/ktr0731/go-fuzzyfinder"

	"ttssh/internal/config"
	"ttssh/internal/ui"
	"ttssh/internal/vault"
)

// keyChoice is the outcome of the key picker.
type keyChoice struct {
	Path    string // file passed to ssh -i (session temp file for vault keys)
	Label   string // display label
	VaultID string // vault unit id, empty for local files
}

// pickEntry is one row of the key picker list.
type pickEntry struct {
	display string
	preview string
	kind    int    // pickBrowse | pickDownload | pickVault | pickLocal
	value   string // vault unit id or local file path
}

const (
	pickBrowse = iota
	pickDownload
	pickVault
	pickLocal
)

// pickKey fuzzy-selects an SSH key from the vault (when configured) and from
// *.key files under keyDir. The top entries switch folders or download vault
// keys; keyDir is updated in place when the user switches folders. vaultTempDir
// is threaded through to materializeVaultKey (see vaultcmd.go).
func pickKey(ctx context.Context, cfg *config.Config, keyDir *string, vlt *vault.Client, vaultTempDir *string) (keyChoice, error) {
	for {
		entries := []pickEntry{{
			display: "📁 choose a different folder…",
			kind:    pickBrowse,
			preview: "Search *.key files in another folder,\nwith the option to save it as the default.",
		}}

		if vlt != nil {
			units, err := vlt.ListUnits(ctx)
			if err != nil {
				ui.PrintWarn("vault: " + err.Error())
			} else {
				entries = append(entries, pickEntry{
					display: "⬇ download vault keys to a folder…",
					kind:    pickDownload,
					preview: "Save keys from the vault as <unit>.key files\nin a folder of your choice.",
				})
				for _, u := range units {
					display := "☁ " + u.UnitID
					status := "active"
					if u.Revoked() {
						display += "  (revoked)"
						status = "revoked"
					}
					entries = append(entries, pickEntry{
						display: display,
						kind:    pickVault,
						value:   u.UnitID,
						preview: fmt.Sprintf("vault key · decrypted only for this session\n\nfingerprint  %s\ncreated      %s\nstatus       %s",
							u.Fingerprint, u.CreatedAt, status),
					})
				}
			}
		}

		keys, scanErr := scanKeys(*keyDir)
		for _, k := range keys {
			display := k
			if rel, err := filepath.Rel(*keyDir, k); err == nil {
				display = rel
			}
			entries = append(entries, pickEntry{display: display, kind: pickLocal, value: k})
		}
		if scanErr != nil {
			// Without vault keys this is a dead end — jump to the folder
			// browser like before. With vault keys, just mention it.
			if len(entries) <= 1 {
				ui.PrintWarn(scanErr.Error())
				dir, err := changeKeyDir(cfg, *keyDir)
				if err != nil {
					return keyChoice{}, err
				}
				*keyDir = dir
				continue
			}
			ui.PrintNote("(" + scanErr.Error() + ")")
		}

		idx, err := fuzzyfinder.Find(entries, func(i int) string {
			return entries[i].display
		},
			fuzzyfinder.WithHeader("Select SSH key · "+*keyDir),
			fuzzyfinder.WithPreviewWindow(func(i, w, h int) string {
				if i < 0 {
					return ""
				}
				e := entries[i]
				if e.kind != pickLocal {
					return e.preview
				}
				info, err := os.Stat(e.value)
				if err != nil {
					return e.value
				}
				return fmt.Sprintf("%s\n\nsize      %d bytes\nmodified  %s",
					e.value, info.Size(), info.ModTime().Format("2006-01-02 15:04"))
			}),
		)
		if err != nil {
			return keyChoice{}, err
		}

		switch e := entries[idx]; e.kind {
		case pickBrowse:
			dir, err := changeKeyDir(cfg, *keyDir)
			if err != nil {
				if isAbort(err) {
					continue // back to the key list
				}
				return keyChoice{}, err
			}
			*keyDir = dir
		case pickDownload:
			if err := vaultPullInteractive(ctx, vlt, *keyDir); err != nil && !isAbort(err) {
				ui.PrintWarn(err.Error())
			}
		case pickVault:
			path, err := materializeVaultKey(ctx, vlt, e.value, vaultTempDir)
			if err != nil {
				ui.PrintWarn(err.Error())
				continue
			}
			return keyChoice{Path: path, Label: vaultLabel(e.value), VaultID: e.value}, nil
		case pickLocal:
			return keyChoice{Path: e.value, Label: e.value}, nil
		}
	}
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

// changeKeyDir opens the folder browser and offers to persist the picked
// folder as the default. Backing out of the save prompt keeps the folder for
// this session without saving.
func changeKeyDir(cfg *config.Config, current string) (string, error) {
	dir, err := browseDir(current)
	if err != nil {
		return "", err
	}
	if dir == current {
		return dir, nil
	}

	save := false
	err = huh.NewConfirm().
		Title("Save as default key folder?").
		Description(dir).
		Affirmative("Save").
		Negative("Just this session").
		Value(&save).
		WithTheme(huh.ThemeCharm()).
		Run()
	if err == nil && save {
		cfg.KeyDir = dir
		if err := cfg.Save(); err != nil {
			ui.PrintWarn("Could not save config: " + err.Error())
		} else {
			ui.PrintSuccess("Default key folder saved.")
		}
	}
	return dir, nil
}

// browseDir is a fuzzy-searchable folder browser. Each screen lists the
// current folder's subfolders (type to filter) plus entries to accept the
// current folder, go up, or type a path manually. It returns the accepted
// folder; Esc aborts the browse.
func browseDir(start string) (string, error) {
	dir := start
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		} else {
			dir = "."
		}
	}

	for {
		subs := listSubdirs(dir)
		items := []string{"✔ use this folder", "⬆ go up (..)", "✏ type a path…"}
		items = append(items, subs...)

		idx, err := fuzzyfinder.Find(items, func(i int) string {
			if i < 3 {
				return items[i]
			}
			return "📁 " + items[i]
		},
			fuzzyfinder.WithHeader("Browse folders · "+dir),
			fuzzyfinder.WithPreviewWindow(func(i, w, h int) string {
				switch {
				case i < 0:
					return ""
				case i == 0:
					return dirPreview(dir)
				case i == 1:
					return dirPreview(filepath.Dir(dir))
				case i == 2:
					return "Type a folder path manually (~ is expanded)."
				default:
					return dirPreview(filepath.Join(dir, items[i]))
				}
			}),
		)
		if err != nil {
			return "", err
		}

		switch idx {
		case 0:
			return dir, nil
		case 1:
			parent := filepath.Dir(dir)
			if parent == dir {
				// Already at a root; on Windows offer switching drives.
				if next, ok := pickDrive(); ok {
					dir = next
				}
				continue
			}
			dir = parent
		case 2:
			typed, err := ui.InputDir("Key folder", dir)
			if err != nil {
				if isAbort(err) {
					continue // back to the browser
				}
				return "", err
			}
			return typed, nil
		default:
			dir = filepath.Join(dir, items[idx])
		}
	}
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

// dirPreview summarizes a folder for the browser's preview pane.
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

// pickDrive lets the user switch drives when browsing up from a drive root
// (Windows only). Returns false if unavailable or cancelled.
func pickDrive() (string, bool) {
	if runtime.GOOS != "windows" {
		return "", false
	}
	var drives []string
	for c := 'A'; c <= 'Z'; c++ {
		p := string(c) + `:\`
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			drives = append(drives, p)
		}
	}
	if len(drives) == 0 {
		return "", false
	}
	idx, err := fuzzyfinder.Find(drives, func(i int) string { return "💾 " + drives[i] },
		fuzzyfinder.WithHeader("Switch drive"))
	if err != nil {
		return "", false
	}
	return drives[idx], true
}
