// Package vault fetches SSH keys uploaded by Key-Upload-TUI from a
// Turso/libSQL database and decrypts them client-side.
//
// v1 crypto contract (MUST stay interoperable with the Python/Node tooling):
//  1. Master key: 32 bytes (MASTER_KEY_V1_HEX, 64 hex chars).
//  2. Per-unit key: HKDF-SHA256(IKM=master, salt=empty, info="unit-key:<unitId>", len=32).
//  3. AES-256-GCM, 12-byte IV, AAD = UTF-8 bytes of <unitId>, 16-byte tag.
//  4. ciphertext / iv / tag stored as standalone base64 strings (tag NOT
//     appended to ciphertext).
//  5. key_version = 1.
//
// Private key material is only ever written to disk at the user's explicit
// request (vault pull) or as a 0600 session temp file that is removed on exit.
// Errors never include key material or the auth token.
package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ttssh/internal/config"
)

const keyVersion = 1

// ErrUnitNotFound distinguishes "this unit was deleted from the vault" from
// transient errors, so callers can prune stale recents safely.
var ErrUnitNotFound = errors.New("not found in the vault")

var unitIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{3,64}$`)

// ValidUnitID reports whether id has the shape of a vault unit id.
func ValidUnitID(id string) bool { return unitIDRe.MatchString(id) }

// ResolveConfig builds the effective vault settings. Precedence per field:
// environment variable > .env in the current directory > config.json.
// The .env support means running ttssh from the Key-Upload-TUI folder picks
// up the exact same credentials the uploader uses.
func ResolveConfig(cfg config.Config) config.VaultConfig {
	dotenv := loadDotEnv(".env")
	pick := func(envKey, fileVal string) string {
		if v := os.Getenv(envKey); v != "" {
			return v
		}
		if v := dotenv[envKey]; v != "" {
			return v
		}
		return fileVal
	}
	return config.VaultConfig{
		URL:          pick("DB_URL", cfg.Vault.URL),
		Token:        pick("DB_TOKEN", cfg.Vault.Token),
		MasterKeyHex: pick("MASTER_KEY_V1_HEX", cfg.Vault.MasterKeyHex),
		CACert:       pick("DB_CA_CERT", cfg.Vault.CACert),
	}
}

// loadDotEnv parses simple KEY=VALUE lines (with optional quotes) from path.
// Missing file or malformed lines are ignored — this is best-effort.
func loadDotEnv(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// Unit is one stored key's metadata (never the key itself).
type Unit struct {
	UnitID      string
	Fingerprint string
	CreatedAt   string
	RevokedAt   string
}

// Revoked reports whether the unit's key has been revoked.
func (u Unit) Revoked() bool { return u.RevokedAt != "" }

// Client is a connected key-vault client.
type Client struct {
	endpoint  string
	token     string
	masterKey []byte
	client    *http.Client
}

// Open validates the settings and returns a ready client.
func Open(vc config.VaultConfig) (*Client, error) {
	if !vc.Configured() {
		return nil, fmt.Errorf("vault is not configured — run 'ttssh vault setup' or set DB_URL / DB_TOKEN / MASTER_KEY_V1_HEX")
	}
	master, err := hex.DecodeString(vc.MasterKeyHex)
	if err != nil || len(master) != 32 {
		return nil, fmt.Errorf("MASTER_KEY_V1_HEX must be exactly 64 hex characters (32 bytes)")
	}

	url := vc.URL
	if strings.HasPrefix(url, "libsql://") {
		url = "https://" + strings.TrimPrefix(url, "libsql://")
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return nil, fmt.Errorf("unsupported vault DB_URL scheme (want libsql:// or https://): %s", vc.URL)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	if vc.CACert != "" {
		pem, err := os.ReadFile(config.ExpandHome(vc.CACert))
		if err != nil {
			return nil, fmt.Errorf("cannot read DB_CA_CERT: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("DB_CA_CERT is not a valid PEM certificate")
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	}

	return &Client{
		endpoint:  strings.TrimRight(url, "/") + "/v2/pipeline",
		token:     vc.Token,
		masterKey: master,
		client:    client,
	}, nil
}

// ---- Turso v2/pipeline HTTP API ----

type pipelineResponse struct {
	Results []struct {
		Type     string                   `json:"type"`
		Error    struct{ Message string } `json:"error"`
		Response struct {
			Result struct {
				Cols []struct {
					Name string `json:"name"`
				} `json:"cols"`
				Rows [][]struct {
					Type  string          `json:"type"`
					Value json.RawMessage `json:"value"`
				} `json:"rows"`
			} `json:"result"`
		} `json:"response"`
	} `json:"results"`
}

// execute runs one parameterized statement and returns rows as column→value
// maps. Only text parameters are needed by this client.
func (v *Client) execute(sql string, params ...string) ([]map[string]string, error) {
	args := make([]map[string]string, len(params))
	for i, p := range params {
		args[i] = map[string]string{"type": "text", "value": p}
	}
	body, err := json.Marshal(map[string]any{
		"requests": []any{
			map[string]any{"type": "execute", "stmt": map[string]any{"sql": sql, "args": args}},
			map[string]any{"type": "close"},
		},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, v.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+v.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "certificate") || strings.Contains(msg, "x509") {
			return nil, fmt.Errorf("vault TLS certificate not trusted — if the server uses a private CA, set DB_CA_CERT to its root certificate (%v)", err)
		}
		return nil, fmt.Errorf("cannot reach vault database: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("vault database rejected the auth token (HTTP %d) — check DB_TOKEN", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault database HTTP error %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("reading vault response: %v", err)
	}
	var payload pipelineResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("unexpected vault response: %v", err)
	}
	if len(payload.Results) == 0 {
		return nil, fmt.Errorf("empty vault response")
	}
	result := payload.Results[0]
	if result.Type == "error" {
		return nil, fmt.Errorf("vault SQL error: %s", result.Error.Message)
	}

	inner := result.Response.Result
	rows := make([]map[string]string, 0, len(inner.Rows))
	for _, row := range inner.Rows {
		m := make(map[string]string, len(inner.Cols))
		for i, cell := range row {
			if i >= len(inner.Cols) {
				break
			}
			m[inner.Cols[i].Name] = decodeCell(cell.Type, cell.Value)
		}
		rows = append(rows, m)
	}
	return rows, nil
}

// decodeCell renders a pipeline cell as a string ("" for null). Values arrive
// as JSON strings for text/integer and numbers for float.
func decodeCell(typ string, raw json.RawMessage) string {
	if typ == "null" || len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return string(raw)
}

// ListUnits returns all stored units, active first, then by unit id.
func (v *Client) ListUnits() ([]Unit, error) {
	rows, err := v.execute(
		"SELECT unit_id, fingerprint, created_at, revoked_at FROM unit_keys " +
			"ORDER BY (revoked_at IS NOT NULL), unit_id")
	if err != nil {
		return nil, err
	}
	units := make([]Unit, 0, len(rows))
	for _, r := range rows {
		units = append(units, Unit{
			UnitID:      r["unit_id"],
			Fingerprint: r["fingerprint"],
			CreatedAt:   r["created_at"],
			RevokedAt:   r["revoked_at"],
		})
	}
	return units, nil
}

// FetchKey downloads and decrypts one unit's private key.
func (v *Client) FetchKey(unitID string) ([]byte, error) {
	if !ValidUnitID(unitID) {
		return nil, fmt.Errorf("invalid unit id %q", unitID)
	}
	rows, err := v.execute(
		"SELECT ciphertext, iv, auth_tag, key_version FROM unit_keys WHERE unit_id = ?", unitID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("unit %q %w", unitID, ErrUnitNotFound)
	}
	r := rows[0]
	if r["key_version"] != strconv.Itoa(keyVersion) {
		return nil, fmt.Errorf("unit %q has unsupported key_version %s", unitID, r["key_version"])
	}
	return decryptForUnit(v.masterKey, unitID, r["ciphertext"], r["iv"], r["auth_tag"])
}

// decryptForUnit implements the v1 contract: HKDF per-unit key, AES-256-GCM
// with the unit id as AAD, tag stored separately from the ciphertext.
func decryptForUnit(masterKey []byte, unitID, ctB64, ivB64, tagB64 string) ([]byte, error) {
	ct, err1 := base64.StdEncoding.DecodeString(ctB64)
	iv, err2 := base64.StdEncoding.DecodeString(ivB64)
	tag, err3 := base64.StdEncoding.DecodeString(tagB64)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, fmt.Errorf("stored row for %q contains invalid base64", unitID)
	}
	key, err := hkdf.Key(sha256.New, masterKey, nil, "unit-key:"+unitID, 32)
	if err != nil {
		return nil, fmt.Errorf("key derivation failed: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(iv) != gcm.NonceSize() {
		return nil, fmt.Errorf("stored row for %q has a bad IV length", unitID)
	}
	plain, err := gcm.Open(nil, iv, append(ct, tag...), []byte(unitID))
	if err != nil {
		return nil, fmt.Errorf("decryption failed for %q: wrong master key, corrupted data, or unitId/AAD mismatch", unitID)
	}
	return plain, nil
}
