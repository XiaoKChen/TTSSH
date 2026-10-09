// Package config handles ttssh's persistent settings: the key directory,
// recent connections, and the optional key-vault credentials.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Config holds persistent settings for ttssh.
type Config struct {
	// KeyDir is the directory scanned for *.key files. Empty means ~/.ssh.
	KeyDir string `json:"key_dir,omitempty"`
	// Recents is the list of recent connections, most recent first.
	Recents []Recent `json:"recents,omitempty"`
	// Saved is the user-organized tree of saved connections; the root folder
	// is unnamed.
	Saved Folder `json:"saved,omitzero"`
	// Vault is the optional Key-Upload-TUI database connection. Environment
	// variables (DB_URL, DB_TOKEN, MASTER_KEY_V1_HEX, DB_CA_CERT) and a .env
	// in the current directory take precedence over these values.
	Vault VaultConfig `json:"vault,omitempty"`
}

// VaultConfig holds the connection settings for the key vault database.
// Stored in config.json (vault section) or supplied via environment.
type VaultConfig struct {
	URL          string `json:"url,omitempty"`
	Token        string `json:"token,omitempty"`
	MasterKeyHex string `json:"master_key_hex,omitempty"`
	CACert       string `json:"ca_cert,omitempty"`
}

// Configured reports whether enough settings are present to open the vault.
func (v VaultConfig) Configured() bool {
	return v.URL != "" && v.MasterKeyHex != ""
}

// Recent is a previously used connection. An empty Key means password login
// (no -i); otherwise Key is a local key path or the "vault:<unit>" marker.
type Recent struct {
	User     string    `json:"user"`
	Host     string    `json:"host"`
	Key      string    `json:"key"`
	LastUsed time.Time `json:"last_used"`
}

const maxRecents = 10

// AddRecent records a connection at the top of the recents list,
// deduplicating and capping the list, then persists the config.
func (c *Config) AddRecent(r Recent) {
	r.LastUsed = time.Now()
	out := []Recent{r}
	for _, existing := range c.Recents {
		if existing.User == r.User && existing.Host == r.Host && existing.Key == r.Key {
			continue
		}
		out = append(out, existing)
		if len(out) == maxRecents {
			break
		}
	}
	c.Recents = out
	_ = c.Save() // recents are best-effort; don't fail the session
}

// RemoveRecent drops a stale entry (e.g. its key file no longer exists).
func (c *Config) RemoveRecent(r Recent) {
	out := c.Recents[:0]
	for _, existing := range c.Recents {
		if existing.User == r.User && existing.Host == r.Host && existing.Key == r.Key {
			continue
		}
		out = append(out, existing)
	}
	c.Recents = out
	_ = c.Save() // recents are best-effort; don't fail the session
}

// Path returns the location of config.json in the OS config directory.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ttssh", "config.json"), nil
}

// Load reads the config, returning a zero Config when missing or unreadable.
func Load() Config {
	var cfg Config
	path, err := Path()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg) // corrupt config falls back to zero-value defaults instead of blocking startup
	return cfg
}

const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// Save persists the config, creating the config directory if needed.
func (c Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, filePerm)
}

// defaultKeyDir returns ~/.ssh.
func defaultKeyDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ssh"
	}
	return filepath.Join(home, ".ssh")
}

// ResolveKeyDir picks the key directory: flag > config > ~/.ssh.
func ResolveKeyDir(flagVal string, cfg Config) string {
	if flagVal != "" {
		return ExpandHome(flagVal)
	}
	if cfg.KeyDir != "" {
		return ExpandHome(cfg.KeyDir)
	}
	return defaultKeyDir()
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(path string) string {
	if path == "~" || len(path) >= 2 && path[0] == '~' && (path[1] == '/' || path[1] == '\\') {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// Entry is a saved connection. Key has the same semantics as Recent.Key.
type Entry struct {
	User string `json:"user"`
	Host string `json:"host"`
	Key  string `json:"key"`
}

// Folder is a node of the saved-connections tree.
type Folder struct {
	Name    string   `json:"name,omitempty"`
	Folders []Folder `json:"folders,omitempty"`
	Entries []Entry  `json:"entries,omitempty"`
}

// Errors returned by the saved-tree operations.
var (
	ErrFolderExists   = errors.New("a folder with that name already exists")
	ErrFolderNotFound = errors.New("folder not found")
	ErrInvalidName    = errors.New("invalid folder name")
	ErrMoveIntoSelf   = errors.New("cannot move a folder into itself or its own subfolder")
	ErrEntryExists    = errors.New("that connection is already in the folder")
	ErrEntryNotFound  = errors.New("connection not found in the folder")
	ErrRootFolder     = errors.New("the top level cannot be changed this way")
)

// pathSeparator is forbidden in folder names so paths read unambiguously.
const pathSeparator = "/"

// ValidateFolderName checks a name the way the tree operations do.
func ValidateFolderName(name string) error {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return fmt.Errorf("%w: it is empty", ErrInvalidName)
	case strings.Contains(name, pathSeparator):
		return fmt.Errorf("%w: it contains %q", ErrInvalidName, pathSeparator)
	}
	return nil
}

// Find returns the folder at path below f (nil path is f itself).
func (f *Folder) Find(path []string) (*Folder, bool) {
	cur := f
	for _, name := range path {
		i := slices.IndexFunc(cur.Folders, func(sub Folder) bool { return sub.Name == name })
		if i < 0 {
			return nil, false
		}
		cur = &cur.Folders[i]
	}
	return cur, true
}

// Count is the number of entries in f and all its subfolders.
func (f Folder) Count() int {
	n := len(f.Entries)
	for _, sub := range f.Folders {
		n += sub.Count()
	}
	return n
}

// Paths lists the path of every folder below f, depth first.
func (f Folder) Paths() [][]string {
	var out [][]string
	var walk func(f Folder, prefix []string)
	walk = func(f Folder, prefix []string) {
		for _, sub := range f.Folders {
			p := append(slices.Clone(prefix), sub.Name)
			out = append(out, p)
			walk(sub, p)
		}
	}
	walk(f, nil)
	return out
}

func (f *Folder) hasChild(name string) bool {
	return slices.ContainsFunc(f.Folders, func(sub Folder) bool { return sub.Name == name })
}

// CreateFolder adds an empty folder named name inside the folder at parent.
func (c *Config) CreateFolder(parent []string, name string) error {
	if err := ValidateFolderName(name); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	dst, ok := c.Saved.Find(parent)
	if !ok {
		return ErrFolderNotFound
	}
	if dst.hasChild(name) {
		return ErrFolderExists
	}
	dst.Folders = append(dst.Folders, Folder{Name: name})
	return nil
}

// RenameFolder renames the folder at path.
func (c *Config) RenameFolder(path []string, name string) error {
	if len(path) == 0 {
		return ErrRootFolder
	}
	if err := ValidateFolderName(name); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	parent, ok := c.Saved.Find(path[:len(path)-1])
	if !ok {
		return ErrFolderNotFound
	}
	i := slices.IndexFunc(parent.Folders, func(sub Folder) bool { return sub.Name == path[len(path)-1] })
	if i < 0 {
		return ErrFolderNotFound
	}
	if parent.Folders[i].Name != name && parent.hasChild(name) {
		return ErrFolderExists
	}
	parent.Folders[i].Name = name
	return nil
}

// DeleteFolder removes the folder at path with everything in it.
func (c *Config) DeleteFolder(path []string) error {
	_, err := c.detachFolder(path)
	return err
}

// detachFolder removes the folder at path from its parent and returns it.
func (c *Config) detachFolder(path []string) (Folder, error) {
	if len(path) == 0 {
		return Folder{}, ErrRootFolder
	}
	parent, ok := c.Saved.Find(path[:len(path)-1])
	if !ok {
		return Folder{}, ErrFolderNotFound
	}
	i := slices.IndexFunc(parent.Folders, func(sub Folder) bool { return sub.Name == path[len(path)-1] })
	if i < 0 {
		return Folder{}, ErrFolderNotFound
	}
	removed := parent.Folders[i]
	parent.Folders = slices.Delete(parent.Folders, i, i+1)
	return removed, nil
}

// AddEntry saves e in the folder at path; an identical entry is rejected.
func (c *Config) AddEntry(path []string, e Entry) error {
	dst, ok := c.Saved.Find(path)
	if !ok {
		return ErrFolderNotFound
	}
	if slices.Contains(dst.Entries, e) {
		return ErrEntryExists
	}
	dst.Entries = append(dst.Entries, e)
	return nil
}

// RemoveEntry deletes e from the folder at path.
func (c *Config) RemoveEntry(path []string, e Entry) error {
	src, ok := c.Saved.Find(path)
	if !ok {
		return ErrFolderNotFound
	}
	i := slices.Index(src.Entries, e)
	if i < 0 {
		return ErrEntryNotFound
	}
	src.Entries = slices.Delete(src.Entries, i, i+1)
	return nil
}

// UpdateEntry replaces old with updated in place, keeping its position.
// Nothing changes on error.
func (c *Config) UpdateEntry(folder []string, old, updated Entry) error {
	src, ok := c.Saved.Find(folder)
	if !ok {
		return ErrFolderNotFound
	}
	i := slices.Index(src.Entries, old)
	if i < 0 {
		return ErrEntryNotFound
	}
	if j := slices.Index(src.Entries, updated); j >= 0 && j != i {
		return ErrEntryExists
	}
	src.Entries[i] = updated
	return nil
}

// MoveEntry moves e from one folder to another. Nothing changes on error.
func (c *Config) MoveEntry(from []string, e Entry, to []string) error {
	src, ok := c.Saved.Find(from)
	if !ok {
		return ErrFolderNotFound
	}
	dst, ok := c.Saved.Find(to)
	if !ok {
		return ErrFolderNotFound
	}
	if !slices.Contains(src.Entries, e) {
		return ErrEntryNotFound
	}
	if slices.Equal(from, to) {
		return nil
	}
	if slices.Contains(dst.Entries, e) {
		return ErrEntryExists
	}
	if err := c.RemoveEntry(from, e); err != nil {
		return err
	}
	return c.AddEntry(to, e)
}

// MoveFolder moves the folder at path into the folder at to. Nothing changes
// on error.
func (c *Config) MoveFolder(path, to []string) error {
	if len(path) == 0 {
		return ErrRootFolder
	}
	if len(to) >= len(path) && slices.Equal(to[:len(path)], path) {
		return ErrMoveIntoSelf
	}
	if _, ok := c.Saved.Find(path); !ok {
		return ErrFolderNotFound
	}
	dst, ok := c.Saved.Find(to)
	if !ok {
		return ErrFolderNotFound
	}
	name := path[len(path)-1]
	if slices.Equal(to, path[:len(path)-1]) {
		return nil // already there
	}
	if dst.hasChild(name) {
		return ErrFolderExists
	}
	moved, err := c.detachFolder(path)
	if err != nil {
		return err
	}
	// detaching shifts sibling slices, so look the destination up again
	dst, _ = c.Saved.Find(to)
	dst.Folders = append(dst.Folders, moved)
	return nil
}
