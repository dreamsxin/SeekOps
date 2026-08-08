package proxy

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type adminKeyCredential struct {
	Salt []byte
	Hash []byte
}

type AdminSetupStatus struct {
	Initialized bool `json:"initialized"`
}

func loadAdminKey(db *sql.DB) (*adminKeyCredential, error) {
	if db == nil {
		return nil, nil
	}
	var saltEncoded, hashEncoded string
	err := db.QueryRow(`SELECT key_salt, key_hash FROM admin_settings WHERE id = 1`).Scan(&saltEncoded, &hashEncoded)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load admin key: %w", err)
	}
	salt, err := base64.RawURLEncoding.DecodeString(saltEncoded)
	if err != nil {
		return nil, fmt.Errorf("decode admin key salt: %w", err)
	}
	hash, err := base64.RawURLEncoding.DecodeString(hashEncoded)
	if err != nil {
		return nil, fmt.Errorf("decode admin key hash: %w", err)
	}
	return &adminKeyCredential{Salt: salt, Hash: hash}, nil
}

func persistAdminKey(db *sql.DB, credential *adminKeyCredential) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`INSERT INTO admin_settings (id, key_salt, key_hash, updated_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET key_salt=excluded.key_salt,
		key_hash=excluded.key_hash, updated_at=excluded.updated_at`,
		base64.RawURLEncoding.EncodeToString(credential.Salt),
		base64.RawURLEncoding.EncodeToString(credential.Hash), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func newAdminKeyCredential(apiKey string) (*adminKeyCredential, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate admin key salt: %w", err)
	}
	return &adminKeyCredential{Salt: salt, Hash: hashAdminKey(salt, apiKey)}, nil
}

func hashAdminKey(salt []byte, apiKey string) []byte {
	data := make([]byte, 0, len(salt)+len(apiKey))
	data = append(data, salt...)
	data = append(data, apiKey...)
	digest := sha256.Sum256(data)
	return digest[:]
}

func (credential *adminKeyCredential) matches(apiKey string) bool {
	if credential == nil || apiKey == "" {
		return false
	}
	hash := hashAdminKey(credential.Salt, apiKey)
	return subtle.ConstantTimeCompare(hash, credential.Hash) == 1
}

func (s *Server) adminSetupStatus() AdminSetupStatus {
	s.adminMu.RLock()
	defer s.adminMu.RUnlock()
	return AdminSetupStatus{Initialized: s.adminKey != nil}
}

func (s *Server) authenticateAdmin(r *http.Request) bool {
	key := r.Header.Get("X-Admin-Key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	s.adminMu.RLock()
	stored := s.adminKey
	s.adminMu.RUnlock()
	if stored != nil {
		return stored.matches(key)
	}
	return key != "" && key == s.config.AdminAPIKey
}

func (s *Server) handleAdminSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.adminSetupStatus())
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid setup payload"})
		return
	}
	input.APIKey = strings.TrimSpace(input.APIKey)
	if input.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "api_key is required"})
		return
	}
	credential, err := newAdminKeyCredential(input.APIKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	if s.adminKey != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "admin api key already initialized"})
		return
	}
	if err := persistAdminKey(s.config.DB, credential); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "persist admin api key failed"})
		return
	}
	s.adminKey = credential
	writeJSON(w, http.StatusCreated, AdminSetupStatus{Initialized: true})
}
