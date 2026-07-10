// Package config handles ttssh's persistent settings: the key directory,
// recent connections, and the optional key-vault credentials.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Config holds persistent settings for ttssh.
type Config struct {
	// KeyDir is the directory scanned for *.key files. Empty means ~/.ssh.
	KeyDir string `json:"key_dir,omitempty"`
	// Recents is the list of recent connections, most recent first.
	Recents []Recent `json:"recents,omitempty"`
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

// Recent is a previously used connection.
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
