package vault

// Interop tests: the vectors below are the exact Node.js-generated vectors
// from Key-Upload-TUI/tests/test_crypto_interop.py. If these fail, ttssh
// cannot read what the uploader wrote.

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ttssh/internal/config"
)

var interopMaster, _ = hex.DecodeString(
	"000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")

var interopVectors = []struct {
	unitID, plaintext, derivedKeyHex, ivB64, ciphertextB64, tagB64 string
}{
	{
		unitID:        "unit-001",
		plaintext:     "hello, airform",
		derivedKeyHex: "4c15f4c035f2ae7b667f173e5dd505808e03f28d64e96ba56f53bd19b56fdf67",
		ivB64:         "AQIDBAUGBwgJCgsM",
		ciphertextB64: "gyvtWTI363izLTBRUDI=",
		tagB64:        "/3sGe46TSiu5s0ZoK4OQOQ==",
	},
	{
		unitID: "selftest",
		plaintext: "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
			"fake-for-vectors\n" +
			"-----END OPENSSH PRIVATE KEY-----\n",
		derivedKeyHex: "41ac6511694c098c1bff8425741ce751d1b987c11aa34a91cc7773097c8de0a2",
		ivB64:         "qrvM3e7/ABEiM0RV",
		ciphertextB64: "7N2ADKeknvtxNYVqZPnVQHkT4uXorrXCMvUvTcppcTyhJIqW60kYYUfAvWHtMRfW" +
			"o7xN1UfMWsRjmZv7Hx+iNCD27fJNelaEKIWrgZuV0gaWan0HlyXD",
		tagB64: "bSUCe04Yfw2SU4HvlaT7Xg==",
	},
	{
		unitID:        "unit-with-longer-id-42",
		plaintext:     "x",
		derivedKeyHex: "bb48f6a2e7377cedabdbc169ac341a53c4cf30c0e1f9da893d662f66a34219ee",
		ivB64:         "AAAAAAAAAAAAAAAB",
		ciphertextB64: "pQ==",
		tagB64:        "wbsx79VbDK/Fb0h65vgWNw==",
	},
}

func TestHKDFMatchesReference(t *testing.T) {
	for _, v := range interopVectors {
		key, err := hkdf.Key(sha256.New, interopMaster, nil, "unit-key:"+v.unitID, 32)
		if err != nil {
			t.Fatalf("%s: hkdf: %v", v.unitID, err)
		}
		if got := hex.EncodeToString(key); got != v.derivedKeyHex {
			t.Errorf("%s: derived key = %s, want %s", v.unitID, got, v.derivedKeyHex)
		}
	}
}

func TestDecryptMatchesReference(t *testing.T) {
	for _, v := range interopVectors {
		plain, err := decryptForUnit(interopMaster, v.unitID, v.ciphertextB64, v.ivB64, v.tagB64)
		if err != nil {
			t.Fatalf("%s: decrypt: %v", v.unitID, err)
		}
		if string(plain) != v.plaintext {
			t.Errorf("%s: plaintext mismatch", v.unitID)
		}
	}
}

func TestDecryptRejectsAADMismatch(t *testing.T) {
	v := interopVectors[0]
	if _, err := decryptForUnit(interopMaster, "some-other-unit", v.ciphertextB64, v.ivB64, v.tagB64); err == nil {
		t.Fatal("decrypting with the wrong unit id must fail (AAD mismatch)")
	}
}

func TestDecryptRejectsWrongMaster(t *testing.T) {
	v := interopVectors[0]
	wrong := make([]byte, 32)
	if _, err := decryptForUnit(wrong, v.unitID, v.ciphertextB64, v.ivB64, v.tagB64); err == nil {
		t.Fatal("decrypting with the wrong master key must fail")
	}
}

// pipelineStub serves a canned Turso v2/pipeline response and records the
// request for assertions.
func pipelineStub(t *testing.T, rows [][]map[string]any, cols []string, lastBody *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/pipeline" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("unexpected auth header %q", got)
		}
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		*lastBody = body

		colObjs := make([]map[string]string, len(cols))
		for i, c := range cols {
			colObjs[i] = map[string]string{"name": c}
		}
		resp := map[string]any{
			"results": []any{
				map[string]any{
					"type": "ok",
					"response": map[string]any{
						"result": map[string]any{"cols": colObjs, "rows": rows},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestListUnitsViaPipeline(t *testing.T) {
	var lastBody []byte
	rows := [][]map[string]any{
		{
			{"type": "text", "value": "unit-001"},
			{"type": "text", "value": "SHA256:abc"},
			{"type": "text", "value": "2026-07-01 10:00:00"},
			{"type": "null"},
		},
	}
	srv := pipelineStub(t, rows, []string{"unit_id", "fingerprint", "created_at", "revoked_at"}, &lastBody)
	defer srv.Close()

	v, err := Open(config.VaultConfig{
		URL:          srv.URL,
		Token:        "tok",
		MasterKeyHex: hex.EncodeToString(interopMaster),
	})
	if err != nil {
		t.Fatal(err)
	}
	units, err := v.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].UnitID != "unit-001" || units[0].Revoked() {
		t.Fatalf("unexpected units: %+v", units)
	}
}

func TestFetchKeyViaPipeline(t *testing.T) {
	vec := interopVectors[0]
	var lastBody []byte
	rows := [][]map[string]any{
		{
			{"type": "text", "value": vec.ciphertextB64},
			{"type": "text", "value": vec.ivB64},
			{"type": "text", "value": vec.tagB64},
			{"type": "integer", "value": "1"}, // Turso encodes integers as strings
		},
	}
	srv := pipelineStub(t, rows, []string{"ciphertext", "iv", "auth_tag", "key_version"}, &lastBody)
	defer srv.Close()

	v, err := Open(config.VaultConfig{
		URL:          srv.URL,
		Token:        "tok",
		MasterKeyHex: hex.EncodeToString(interopMaster),
	})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := v.FetchKey(vec.unitID)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != vec.plaintext {
		t.Fatalf("plaintext mismatch: %q", plain)
	}
	// the unit id must have been sent as a parameterized text arg, not inlined
	var req struct {
		Requests []struct {
			Stmt struct {
				SQL  string              `json:"sql"`
				Args []map[string]string `json:"args"`
			} `json:"stmt"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(lastBody, &req); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if len(req.Requests) == 0 || len(req.Requests[0].Stmt.Args) != 1 ||
		req.Requests[0].Stmt.Args[0]["value"] != vec.unitID {
		t.Fatalf("unit id not parameterized: %s", lastBody)
	}
}

func TestOpenVaultValidation(t *testing.T) {
	if _, err := Open(config.VaultConfig{}); err == nil {
		t.Error("empty config must error")
	}
	if _, err := Open(config.VaultConfig{URL: "https://x", MasterKeyHex: "zz"}); err == nil {
		t.Error("bad master key hex must error")
	}
	if _, err := Open(config.VaultConfig{URL: "ftp://x", MasterKeyHex: hex.EncodeToString(interopMaster)}); err == nil {
		t.Error("bad scheme must error")
	}
	v, err := Open(config.VaultConfig{URL: "libsql://db.example.com", MasterKeyHex: hex.EncodeToString(interopMaster)})
	if err != nil {
		t.Fatal(err)
	}
	if v.endpoint != "https://db.example.com/v2/pipeline" {
		t.Errorf("libsql scheme not rewritten: %s", v.endpoint)
	}
}
