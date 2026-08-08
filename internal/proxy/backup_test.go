package proxy

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupIncludesRestorableSQLiteAndLocalMasterKey(t *testing.T) {
	originalDir := t.TempDir()
	databasePath := filepath.Join(originalDir, "seekops.db")
	keyPath := filepath.Join(originalDir, "seekops.key")
	db, err := OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, err := OpenSecretCipher(db, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServerChecked(Config{PlatformAPIKey: "platform-secret", AdminAPIKey: "admin-key", DB: db, SecretCipher: cipher})
	if err != nil {
		t.Fatal(err)
	}
	account := &Account{ID: "acct-backup", Name: "Backup", APIKey: "upstream-secret", BaseURL: "https://api.deepseek.com", Weight: 1, Managed: true}
	if err := persistManagedAccount(db, cipher, account); err != nil {
		t.Fatal(err)
	}

	check := server.backupCheck()
	if !check.OK || !check.SQLite.OK || !check.Secrets.OK {
		t.Fatalf("backup check failed: %#v", check)
	}
	archivePath, err := server.createBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archivePath)

	restoreDir := t.TempDir()
	manifest := extractBackupArchive(t, archivePath, restoreDir)
	if manifest["key_storage"] != "local_file" || manifest["database"] != "seekops.db" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	restoredDB, err := OpenSQLite(filepath.Join(restoreDir, "seekops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	restoredCipher, err := OpenSecretCipher(restoredDB, filepath.Join(restoreDir, "seekops.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateStoredSecrets(restoredDB, restoredCipher); err != nil {
		t.Fatalf("restored credentials do not match bundled key: %v", err)
	}

	originalKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	otherKeyPath := filepath.Join(t.TempDir(), "other.key")
	if _, err := OpenSecretCipher(nil, otherKeyPath); err != nil {
		t.Fatal(err)
	}
	otherKey, err := os.ReadFile(otherKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, otherKey, 0o600); err != nil {
		t.Fatal(err)
	}
	mismatched := server.backupCheck()
	if mismatched.OK || mismatched.Secrets.OK {
		t.Fatalf("mismatched local key was not detected: %#v", mismatched)
	}
	if err := os.WriteFile(keyPath, originalKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	failed := server.backupCheck()
	if failed.OK || failed.Secrets.OK {
		t.Fatalf("missing local key was not detected: %#v", failed)
	}
}

func extractBackupArchive(t *testing.T, archivePath, targetDir string) map[string]any {
	t.Helper()
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	found := map[string]bool{}
	var manifest map[string]any
	for _, entry := range reader.File {
		if entry.Name != "seekops.db" && entry.Name != "seekops.key" && entry.Name != "manifest.json" {
			t.Fatalf("unexpected archive entry %q", entry.Name)
		}
		input, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(input)
		input.Close()
		if err != nil {
			t.Fatal(err)
		}
		found[entry.Name] = true
		if entry.Name == "manifest.json" {
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(targetDir, entry.Name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"seekops.db", "seekops.key", "manifest.json"} {
		if !found[name] {
			t.Fatalf("archive is missing %s", name)
		}
	}
	return manifest
}
