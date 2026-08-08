package proxy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const encryptedSecretPrefix = "enc:v1:"

type secretKey struct {
	ID  string
	Key []byte
}

type secretKeyFile struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

type secretKeyringFile struct {
	Version  int             `json:"version"`
	Current  secretKeyFile   `json:"current"`
	Previous []secretKeyFile `json:"previous,omitempty"`
}

type SecretCipher struct {
	mu       sync.RWMutex
	filePath string
	current  secretKey
	previous map[string][]byte
}

func DefaultSecretKeyPath(sqlitePath string) string {
	if sqlitePath == "" || sqlitePath == ":memory:" || strings.HasPrefix(sqlitePath, "?") {
		return ""
	}
	ext := filepath.Ext(sqlitePath)
	if ext == "" {
		return sqlitePath + ".key"
	}
	return strings.TrimSuffix(sqlitePath, ext) + ".key"
}

func OpenSecretCipher(db *sql.DB, path string) (*SecretCipher, error) {
	if path == "" {
		key, err := generateSecretKey()
		if err != nil {
			return nil, err
		}
		return newSecretCipher(key, "")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create secret key directory: %w", err)
		}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if encrypted, checkErr := hasEncryptedSecrets(db); checkErr != nil {
			return nil, fmt.Errorf("inspect encrypted secrets: %w", checkErr)
		} else if encrypted {
			return nil, fmt.Errorf("secret key file %q is missing while SQLite contains encrypted credentials", path)
		}
		key, keyErr := generateSecretKey()
		if keyErr != nil {
			return nil, keyErr
		}
		cipher, cipherErr := newSecretCipher(key, path)
		if cipherErr != nil {
			return nil, cipherErr
		}
		if err := cipher.persistKeyring(); err != nil {
			return nil, fmt.Errorf("create secret key file: %w", err)
		}
		return cipher, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secret key file: %w", err)
	}
	cipher, err := parseSecretKeyring(data, path)
	if err != nil {
		return nil, fmt.Errorf("parse secret key file: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return cipher, nil
}

func NewSecretCipherFromBase64(value string) (*SecretCipher, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("secret master key is empty")
	}
	key, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		key, err = base64.StdEncoding.DecodeString(value)
	}
	if err != nil && len(value) == 64 {
		key, err = hex.DecodeString(value)
	}
	if err != nil {
		return nil, fmt.Errorf("decode secret master key: %w", err)
	}
	return newSecretCipher(key, "")
}

func newSecretCipher(key []byte, path string) (*SecretCipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret master key must be 32 bytes")
	}
	keyCopy := append([]byte(nil), key...)
	return &SecretCipher{filePath: path, current: secretKey{ID: secretKeyID(keyCopy), Key: keyCopy}, previous: make(map[string][]byte)}, nil
}

func generateSecretKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate secret master key: %w", err)
	}
	return key, nil
}

func secretKeyID(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

func parseSecretKeyring(data []byte, path string) (*SecretCipher, error) {
	var file secretKeyringFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("unsupported keyring version %d", file.Version)
	}
	current, err := decodeSecretKeyFile(file.Current)
	if err != nil {
		return nil, fmt.Errorf("decode current key: %w", err)
	}
	cipher, err := newSecretCipher(current.Key, path)
	if err != nil {
		return nil, err
	}
	if current.ID != cipher.current.ID {
		return nil, fmt.Errorf("current key id does not match key material")
	}
	for _, previous := range file.Previous {
		decoded, decodeErr := decodeSecretKeyFile(previous)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode previous key: %w", decodeErr)
		}
		cipher.previous[decoded.ID] = decoded.Key
	}
	return cipher, nil
}

func decodeSecretKeyFile(file secretKeyFile) (secretKey, error) {
	key, err := base64.RawStdEncoding.DecodeString(file.Key)
	if err != nil {
		return secretKey{}, err
	}
	if len(key) != 32 || file.ID == "" {
		return secretKey{}, fmt.Errorf("invalid key material")
	}
	if file.ID != secretKeyID(key) {
		return secretKey{}, fmt.Errorf("key id does not match key material")
	}
	return secretKey{ID: file.ID, Key: key}, nil
}

func (c *SecretCipher) Encrypt(recordID, value string) (string, error) {
	if c == nil || value == "" {
		return value, nil
	}
	c.mu.RLock()
	key := append([]byte(nil), c.current.Key...)
	keyID := c.current.ID
	c.mu.RUnlock()
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(value), []byte(recordID))
	encoded := base64.RawStdEncoding.EncodeToString(append(nonce, sealed...))
	return encryptedSecretPrefix + keyID + ":" + encoded, nil
}

func (c *SecretCipher) Decrypt(recordID, value string) (string, error) {
	if c == nil || value == "" || !strings.HasPrefix(value, encryptedSecretPrefix) {
		return value, nil
	}
	parts := strings.SplitN(strings.TrimPrefix(value, encryptedSecretPrefix), ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid encrypted secret format")
	}
	c.mu.RLock()
	keyBytes := append([]byte(nil), c.previous[parts[0]]...)
	if parts[0] == c.current.ID {
		keyBytes = append([]byte(nil), c.current.Key...)
	}
	c.mu.RUnlock()
	if len(keyBytes) != 32 {
		return "", fmt.Errorf("secret key %s is not available", parts[0])
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode encrypted secret: %w", err)
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", fmt.Errorf("encrypted secret is truncated")
	}
	plain, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], []byte(recordID))
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plain), nil
}

func (c *SecretCipher) persistKeyring() error {
	if c == nil || c.filePath == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.persistKeyringLocked()
}

func (c *SecretCipher) persistKeyringLocked() error {
	file := secretKeyringFile{Version: 1, Current: secretKeyFile{ID: c.current.ID, Key: base64.RawStdEncoding.EncodeToString(c.current.Key)}}
	for id, key := range c.previous {
		file.Previous = append(file.Previous, secretKeyFile{ID: id, Key: base64.RawStdEncoding.EncodeToString(key)})
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.filePath)
	temporary, err := os.CreateTemp(dir, filepath.Base(c.filePath)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, c.filePath); err != nil {
		return err
	}
	return os.Chmod(c.filePath, 0o600)
}

func (c *SecretCipher) CurrentID() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current.ID
}

func (c *SecretCipher) FilePath() string {
	if c == nil {
		return ""
	}
	return c.filePath
}

func (c *SecretCipher) Rotate(reencrypt func() error) error {
	if c == nil {
		return fmt.Errorf("secret encryption is not configured")
	}
	key, err := generateSecretKey()
	if err != nil {
		return err
	}
	next, err := newSecretCipher(key, c.filePath)
	if err != nil {
		return err
	}
	c.mu.Lock()
	old := c.current
	previous := make(map[string][]byte, len(c.previous)+1)
	for id, key := range c.previous {
		previous[id] = append([]byte(nil), key...)
	}
	previous[old.ID] = append([]byte(nil), old.Key...)
	c.current = next.current
	c.previous = previous
	if err := c.persistKeyringLocked(); err != nil {
		c.current = old
		delete(c.previous, old.ID)
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()
	if err := reencrypt(); err != nil {
		return err
	}
	c.mu.Lock()
	c.previous = make(map[string][]byte)
	err = c.persistKeyringLocked()
	c.mu.Unlock()
	return err
}

func ValidateStoredSecrets(db *sql.DB, c *SecretCipher) error {
	if db == nil {
		return nil
	}
	if c == nil {
		encrypted, err := hasEncryptedSecrets(db)
		if err != nil {
			return err
		}
		if encrypted {
			return fmt.Errorf("SQLite contains encrypted credentials but secret encryption is not configured")
		}
		return nil
	}
	rows, err := db.Query(`SELECT id, api_key, 'upstream:' || id FROM upstream_accounts
		UNION ALL SELECT id, secret, 'virtual:' || id FROM virtual_keys WHERE secret <> ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, value, recordID string
		if err := rows.Scan(&id, &value, &recordID); err != nil {
			return err
		}
		if _, err := c.Decrypt(recordID, value); err != nil {
			return fmt.Errorf("validate stored secret %s: %w", id, err)
		}
	}
	return rows.Err()
}

func hasEncryptedSecrets(db *sql.DB) (bool, error) {
	if db == nil {
		return false, nil
	}
	var count int
	err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM upstream_accounts WHERE api_key LIKE 'enc:v1:%') +
		(SELECT COUNT(*) FROM virtual_keys WHERE secret LIKE 'enc:v1:%')`).Scan(&count)
	return count > 0, err
}
