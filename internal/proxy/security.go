package proxy

import (
	"fmt"
	"net/http"
)

type SecurityStatus struct {
	EncryptionEnabled bool   `json:"encryption_enabled"`
	KeyID             string `json:"key_id,omitempty"`
	KeyStorage        string `json:"key_storage"`
	KeyFile           string `json:"key_file,omitempty"`
	RotationSupported bool   `json:"rotation_supported"`
}

func (s *Server) securityStatus() SecurityStatus {
	cipher := s.config.SecretCipher
	if cipher == nil {
		return SecurityStatus{KeyStorage: "disabled"}
	}
	status := SecurityStatus{
		EncryptionEnabled: true,
		KeyID:             cipher.CurrentID(),
		KeyStorage:        "external",
	}
	if path := cipher.FilePath(); path != "" {
		status.KeyStorage = "local_file"
		status.KeyFile = path
		status.RotationSupported = s.config.DB != nil
	}
	return status
}

func (s *Server) handleSecurity(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/security" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, s.securityStatus())
		return
	}
	if r.URL.Path != "/admin/security/rotate" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.securityStatus().RotationSupported {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "master key rotation requires SQLite and a local key file"})
		return
	}
	if err := s.config.SecretCipher.Rotate(s.reencryptStoredSecrets); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "rotate master key failed"})
		return
	}
	writeJSON(w, http.StatusOK, s.securityStatus())
}

func (s *Server) reencryptStoredSecrets() error {
	if s.config.DB == nil || s.config.SecretCipher == nil {
		return fmt.Errorf("persistent secret encryption is not configured")
	}
	s.accountsMu.Lock()
	for _, account := range s.accounts {
		if account == nil || !account.Managed {
			continue
		}
		if err := persistManagedAccount(s.config.DB, s.config.SecretCipher, account); err != nil {
			s.accountsMu.Unlock()
			return fmt.Errorf("reencrypt account %s: %w", account.ID, err)
		}
	}
	s.accountsMu.Unlock()

	s.keys.mu.Lock()
	defer s.keys.mu.Unlock()
	for _, key := range s.keys.byID {
		if err := s.keys.persistKeyLocked(s.config.DB, key); err != nil {
			return fmt.Errorf("reencrypt virtual key %s: %w", key.ID, err)
		}
	}
	return nil
}
