package proxy

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecretCipherRoundTripAndAAD(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, err := OpenSecretCipher(db, filepath.Join(t.TempDir(), "seekops.key"))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("upstream:acct-a", "sk-sensitive")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, encryptedSecretPrefix) || strings.Contains(encrypted, "sk-sensitive") {
		t.Fatalf("encrypted value=%q", encrypted)
	}
	plain, err := cipher.Decrypt("upstream:acct-a", encrypted)
	if err != nil || plain != "sk-sensitive" {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
	if _, err := cipher.Decrypt("upstream:acct-b", encrypted); err == nil {
		t.Fatal("encrypted secret decrypted with a different record id")
	}
	prefixedPlain := encryptedSecretPrefix + "not-encrypted"
	prefixedEncrypted, err := cipher.Encrypt("upstream:acct-c", prefixedPlain)
	if err != nil {
		t.Fatal(err)
	}
	prefixedResult, err := cipher.Decrypt("upstream:acct-c", prefixedEncrypted)
	if err != nil || prefixedResult != prefixedPlain || prefixedEncrypted == prefixedPlain {
		t.Fatalf("prefixed plain=%q encrypted=%q result=%q err=%v", prefixedPlain, prefixedEncrypted, prefixedResult, err)
	}
}

func TestSQLiteCredentialEncryptionMigrationAndRotation(t *testing.T) {
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "seekops.db")
	keyPath := filepath.Join(directory, "seekops.key")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	account := &Account{ID: "managed-a", Name: "Managed A", APIKey: "upstream-sensitive", BaseURL: "https://api.deepseek.com", Weight: 1, Managed: true, CreatedAt: time.Now()}
	if err := persistManagedAccount(db, nil, account); err != nil {
		t.Fatal(err)
	}
	legacyStore := NewKeyStoreWithDB("platform-sensitive", db)
	view, tenantSecret, err := legacyStore.Create("Tenant A", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	assertStoredSecret(t, db, "SELECT api_key FROM upstream_accounts WHERE id = ?", "managed-a", "upstream-sensitive", false)
	assertStoredSecret(t, db, "SELECT secret FROM virtual_keys WHERE id = ?", view.ID, tenantSecret, false)

	cipher, err := OpenSecretCipher(db, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServerChecked(Config{PlatformAPIKey: "platform-sensitive", AdminAPIKey: "admin-secret", DB: db, SecretCipher: cipher})
	if err != nil {
		t.Fatal(err)
	}
	assertStoredSecret(t, db, "SELECT api_key FROM upstream_accounts WHERE id = ?", "managed-a", "upstream-sensitive", true)
	assertStoredSecret(t, db, "SELECT secret FROM virtual_keys WHERE id = ?", view.ID, tenantSecret, true)
	if _, ok := server.keys.Authenticate(tenantSecret); !ok {
		t.Fatal("migrated tenant key did not authenticate")
	}
	if len(server.accounts) != 1 || server.accounts[0].APIKey != "upstream-sensitive" {
		t.Fatalf("migrated accounts=%+v", server.accounts)
	}

	wrongKey := make([]byte, 32)
	for i := range wrongKey {
		wrongKey[i] = byte(i + 1)
	}
	wrongCipher, err := NewSecretCipherFromBase64(base64.RawStdEncoding.EncodeToString(wrongKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewServerChecked(Config{PlatformAPIKey: "platform-sensitive", DB: db, SecretCipher: wrongCipher}); err == nil {
		t.Fatal("server initialized with the wrong master key")
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/admin/security", nil)
	statusRequest.Header.Set("X-Admin-Key", "admin-secret")
	statusRecorder := httptest.NewRecorder()
	server.ServeHTTP(statusRecorder, statusRequest)
	var before SecurityStatus
	if statusRecorder.Code != http.StatusOK || json.Unmarshal(statusRecorder.Body.Bytes(), &before) != nil || !before.EncryptionEnabled || !before.RotationSupported || before.KeyFile != keyPath {
		t.Fatalf("security status=%d body=%s", statusRecorder.Code, statusRecorder.Body.String())
	}

	rotateRequest := httptest.NewRequest(http.MethodPost, "/admin/security/rotate", nil)
	rotateRequest.Header.Set("X-Admin-Key", "admin-secret")
	rotateRecorder := httptest.NewRecorder()
	server.ServeHTTP(rotateRecorder, rotateRequest)
	var after SecurityStatus
	if rotateRecorder.Code != http.StatusOK || json.Unmarshal(rotateRecorder.Body.Bytes(), &after) != nil {
		t.Fatalf("rotate status=%d body=%s", rotateRecorder.Code, rotateRecorder.Body.String())
	}
	if after.KeyID == "" || after.KeyID == before.KeyID {
		t.Fatalf("master key id did not change: before=%q after=%q", before.KeyID, after.KeyID)
	}
	assertStoredKeyID(t, db, "SELECT api_key FROM upstream_accounts WHERE id = ?", "managed-a", after.KeyID)
	assertStoredKeyID(t, db, "SELECT secret FROM virtual_keys WHERE id = ?", view.ID, after.KeyID)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	reopenedCipher, err := OpenSecretCipher(reopened, keyPath)
	if err != nil {
		reopened.Close()
		t.Fatal(err)
	}
	restarted, err := NewServerChecked(Config{PlatformAPIKey: "platform-sensitive", DB: reopened, SecretCipher: reopenedCipher})
	if err != nil {
		reopened.Close()
		t.Fatal(err)
	}
	if _, ok := restarted.keys.Authenticate(tenantSecret); !ok || len(restarted.accounts) != 1 || restarted.accounts[0].APIKey != "upstream-sensitive" {
		reopened.Close()
		t.Fatal("rotated credentials were not restored after restart")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	missingKeyDB, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer missingKeyDB.Close()
	if _, err := OpenSecretCipher(missingKeyDB, keyPath); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing key file error=%v", err)
	}
}

func TestSecurityRotationRejectsExternalMasterKey(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(255 - i)
	}
	cipher, err := NewSecretCipherFromBase64(base64.RawStdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{AdminAPIKey: "admin-secret", SecretCipher: cipher})
	request := httptest.NewRequest(http.MethodPost, "/admin/security/rotate", nil)
	request.Header.Set("X-Admin-Key", "admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func assertStoredSecret(t *testing.T, db interface {
	QueryRow(string, ...any) *sql.Row
}, query, id, plain string, encrypted bool) {
	t.Helper()
	var stored string
	if err := db.QueryRow(query, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if encrypted {
		if !strings.HasPrefix(stored, encryptedSecretPrefix) || strings.Contains(stored, plain) {
			t.Fatalf("stored secret=%q", stored)
		}
		return
	}
	if stored != plain {
		t.Fatalf("stored secret=%q want=%q", stored, plain)
	}
}

func assertStoredKeyID(t *testing.T, db interface {
	QueryRow(string, ...any) *sql.Row
}, query, id, keyID string) {
	t.Helper()
	var stored string
	if err := db.QueryRow(query, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, encryptedSecretPrefix+keyID+":") {
		t.Fatalf("stored secret does not use key %s: %q", keyID, stored)
	}
}
