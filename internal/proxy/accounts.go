package proxy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AccountView struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	APIKeyPrefix     string        `json:"api_key_prefix"`
	BaseURL          string        `json:"base_url"`
	Weight           int           `json:"weight"`
	Models           []string      `json:"models"`
	Enabled          bool          `json:"enabled"`
	Managed          bool          `json:"managed"`
	Active           int64         `json:"active"`
	Healthy          bool          `json:"healthy"`
	CheckStatus      string        `json:"check_status"`
	Failures         int64         `json:"failures"`
	BalanceAvailable bool          `json:"balance_available"`
	Balances         []BalanceInfo `json:"balances"`
	BalanceUpdatedAt time.Time     `json:"balance_updated_at,omitempty"`
	BalanceError     string        `json:"balance_error,omitempty"`
}

type AccountTestResult struct {
	AccountID string    `json:"account_id"`
	Mode      string    `json:"mode"`
	OK        bool      `json:"ok"`
	Status    int       `json:"status"`
	LatencyMS int64     `json:"latency_ms"`
	Models    []string  `json:"models,omitempty"`
	Model     string    `json:"model,omitempty"`
	Output    string    `json:"output,omitempty"`
	Error     string    `json:"error,omitempty"`
	TestedAt  time.Time `json:"tested_at"`
}

type accountInput struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	APIKey  string   `json:"api_key"`
	BaseURL string   `json:"base_url"`
	Weight  int      `json:"weight"`
	Models  []string `json:"models"`
	Enabled *bool    `json:"enabled"`
}

func loadManagedAccounts(db *sql.DB, secrets *SecretCipher) ([]*Account, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(`SELECT id, name, api_key, base_url, weight, models_json, enabled, created_at
		FROM upstream_accounts ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []*Account
	for rows.Next() {
		var account Account
		var modelsJSON, createdAt string
		var enabled int
		if err := rows.Scan(&account.ID, &account.Name, &account.APIKey, &account.BaseURL, &account.Weight, &modelsJSON, &enabled, &createdAt); err != nil {
			return nil, err
		}
		account.APIKey, err = secrets.Decrypt("upstream:"+account.ID, account.APIKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt account %s api key: %w", account.ID, err)
		}
		if err := json.Unmarshal([]byte(modelsJSON), &account.Models); err != nil {
			return nil, fmt.Errorf("decode account %s models: %w", account.ID, err)
		}
		account.Disabled = enabled == 0
		account.Managed = true
		account.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		accounts = append(accounts, &account)
	}
	return accounts, rows.Err()
}

func persistManagedAccount(db *sql.DB, secrets *SecretCipher, account *Account) error {
	if db == nil {
		return nil
	}
	modelsJSON, err := json.Marshal(account.Models)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	createdAt := account.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	storedAPIKey, err := secrets.Encrypt("upstream:"+account.ID, account.APIKey)
	if err != nil {
		return fmt.Errorf("encrypt account api key: %w", err)
	}
	_, err = db.Exec(`INSERT INTO upstream_accounts
		(id, name, api_key, base_url, weight, models_json, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, api_key=excluded.api_key,
		base_url=excluded.base_url, weight=excluded.weight, models_json=excluded.models_json,
		enabled=excluded.enabled, updated_at=excluded.updated_at`, account.ID, account.Name,
		storedAPIKey, account.BaseURL, account.Weight, string(modelsJSON), boolInt(!account.Disabled),
		createdAt.UTC().Format(time.RFC3339Nano), now)
	return err
}

func deleteManagedAccount(db *sql.DB, id string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`DELETE FROM upstream_accounts WHERE id = ?`, id)
	return err
}

func mergeAccounts(configured, managed []*Account) []*Account {
	result := make([]*Account, 0, len(configured)+len(managed))
	ids := make(map[string]struct{}, len(configured)+len(managed))
	for _, account := range configured {
		if account == nil || account.ID == "" {
			continue
		}
		account.Managed = false
		account.Disabled = false
		result = append(result, account)
		ids[account.ID] = struct{}{}
	}
	for _, account := range managed {
		if account == nil {
			continue
		}
		if _, exists := ids[account.ID]; exists {
			continue
		}
		result = append(result, account)
		ids[account.ID] = struct{}{}
	}
	return result
}

func (s *Server) accountsSnapshot() []*Account {
	s.accountsMu.RLock()
	defer s.accountsMu.RUnlock()
	return append([]*Account(nil), s.accounts...)
}

func (s *Server) hasEnabledAccount() bool {
	for _, account := range s.accountsSnapshot() {
		if account != nil && !account.Disabled && account.APIKey != "" {
			return true
		}
	}
	return false
}

func accountView(account *Account) AccountView {
	available, balances, updatedAt, balanceError := account.balanceSnapshot()
	prefix := account.APIKey
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	checkStatus := "unchecked"
	healthy := false
	switch {
	case account.Disabled:
		checkStatus = "disabled"
	case updatedAt.IsZero():
		checkStatus = "unchecked"
	case balanceError != "":
		checkStatus = "error"
	case !account.Healthy(time.Now()):
		checkStatus = "cooldown"
	case !available:
		checkStatus = "unavailable"
	default:
		checkStatus = "healthy"
		healthy = true
	}
	return AccountView{ID: account.ID, Name: account.Name, APIKeyPrefix: prefix, BaseURL: account.BaseURL,
		Weight: account.Weight, Models: append([]string{}, account.Models...), Enabled: !account.Disabled,
		Managed: account.Managed, Active: account.Active(), Healthy: healthy, CheckStatus: checkStatus,
		Failures: account.fails.Load(), BalanceAvailable: available, Balances: balances,
		BalanceUpdatedAt: updatedAt, BalanceError: balanceError}
}

func normalizeAccountInput(input accountInput, existing *Account) (*Account, error) {
	id := strings.TrimSpace(input.ID)
	if existing != nil {
		id = existing.ID
	}
	if id == "" {
		id = "acct-" + newID()
	}
	if len(id) > 80 || !validAccountID(id) {
		return nil, fmt.Errorf("account id may contain only letters, numbers, '.', '_' and '-'")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = id
	}
	apiKey := strings.TrimSpace(input.APIKey)
	if apiKey == "" && existing != nil {
		apiKey = existing.APIKey
	}
	if apiKey == "" {
		return nil, fmt.Errorf("api_key is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("base_url must be an absolute http or https URL")
	}
	weight := input.Weight
	if weight <= 0 {
		weight = 1
	}
	if weight > 1000 {
		return nil, fmt.Errorf("weight must not exceed 1000")
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	} else if existing != nil {
		enabled = !existing.Disabled
	}
	models := normalizeModels(input.Models)
	createdAt := time.Now()
	if existing != nil {
		createdAt = existing.CreatedAt
	}
	return &Account{ID: id, Name: name, APIKey: apiKey, BaseURL: baseURL, Weight: weight,
		Models: models, Disabled: !enabled, Managed: true, CreatedAt: createdAt}, nil
}

func validAccountID(id string) bool {
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return id != ""
}

func normalizeModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	return result
}

func (s *Server) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/accounts" {
		switch r.Method {
		case http.MethodGet:
			accounts := s.accountsSnapshot()
			result := make([]AccountView, 0, len(accounts))
			for _, account := range accounts {
				result = append(result, accountView(account))
			}
			writeJSON(w, http.StatusOK, result)
		case http.MethodPost:
			s.createManagedAccount(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/admin/accounts/")
	if strings.HasSuffix(id, "/check") {
		id = strings.TrimSuffix(id, "/check")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.checkAccount(w, r, id)
		return
	}
	if strings.HasSuffix(id, "/test") {
		id = strings.TrimSuffix(id, "/test")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.testAccountAPI(w, r, id)
		return
	}
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.updateManagedAccount(w, r, id)
	case http.MethodDelete:
		s.removeManagedAccount(w, r, id)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func decodeAccountInput(w http.ResponseWriter, r *http.Request) (accountInput, bool) {
	var input accountInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid account payload"})
		return input, false
	}
	return input, true
}

func (s *Server) createManagedAccount(w http.ResponseWriter, r *http.Request) {
	input, ok := decodeAccountInput(w, r)
	if !ok {
		return
	}
	account, err := normalizeAccountInput(input, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.accountsMu.Lock()
	for _, current := range s.accounts {
		if current.ID == account.ID {
			s.accountsMu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{"error": "account id already exists"})
			return
		}
	}
	if err := persistManagedAccount(s.config.DB, s.config.SecretCipher, account); err != nil {
		s.accountsMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "persist account failed"})
		return
	}
	s.accounts = append(s.accounts, account)
	s.accountsMu.Unlock()
	if !account.Disabled {
		s.pollAccountBalance(r.Context(), account)
	}
	writeJSON(w, http.StatusCreated, accountView(account))
}

func (s *Server) updateManagedAccount(w http.ResponseWriter, r *http.Request, id string) {
	input, ok := decodeAccountInput(w, r)
	if !ok {
		return
	}
	s.accountsMu.Lock()
	for index, current := range s.accounts {
		if current.ID != id {
			continue
		}
		if !current.Managed {
			s.accountsMu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{"error": "environment account is read-only"})
			return
		}
		account, err := normalizeAccountInput(input, current)
		if err != nil {
			s.accountsMu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := persistManagedAccount(s.config.DB, s.config.SecretCipher, account); err != nil {
			s.accountsMu.Unlock()
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "persist account failed"})
			return
		}
		s.accounts[index] = account
		s.accountsMu.Unlock()
		if !account.Disabled {
			s.pollAccountBalance(r.Context(), account)
		} else if s.alerts != nil {
			s.alerts.EvaluateAccount(accountView(account), time.Now())
		}
		writeJSON(w, http.StatusOK, accountView(account))
		return
	}
	s.accountsMu.Unlock()
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "account not found"})
}

func (s *Server) checkAccount(w http.ResponseWriter, r *http.Request, id string) {
	account := s.findAccount(id)
	if account == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "account not found"})
		return
	}
	if account.Disabled {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "account is disabled"})
		return
	}
	s.pollAccountBalance(r.Context(), account)
	writeJSON(w, http.StatusOK, accountView(account))
}

func (s *Server) findAccount(id string) *Account {
	for _, account := range s.accountsSnapshot() {
		if account != nil && account.ID == id {
			return account
		}
	}
	return nil
}

func (s *Server) testAccountAPI(w http.ResponseWriter, r *http.Request, id string) {
	account := s.findAccount(id)
	if account == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "account not found"})
		return
	}
	if account.Disabled {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "account is disabled"})
		return
	}
	var input struct {
		Mode  string `json:"mode"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid test payload"})
		return
	}
	input.Mode = strings.TrimSpace(input.Mode)
	if input.Mode == "" {
		input.Mode = "models"
	}
	if input.Mode != "models" && input.Mode != "chat" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mode must be models or chat"})
		return
	}
	input.Model = strings.TrimSpace(input.Model)
	if input.Mode == "chat" && input.Model == "" {
		if len(account.Models) > 0 {
			input.Model = account.Models[0]
		} else {
			input.Model = "deepseek-chat"
		}
	}

	method, path := http.MethodGet, "/models"
	var payload []byte
	if input.Mode == "chat" {
		method, path = http.MethodPost, "/chat/completions"
		payload, _ = json.Marshal(map[string]any{
			"model":       input.Model,
			"messages":    []map[string]string{{"role": "user", "content": "Reply with exactly OK."}},
			"max_tokens":  8,
			"stream":      false,
			"temperature": 0,
		})
	}
	target, err := url.Parse(account.BaseURL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "account base_url is invalid"})
		return
	}
	target.Path = joinPath(target.Path, path)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(payload))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "create upstream test request failed"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+account.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Proxy-Request-ID", "test-"+newID())
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	started := time.Now()
	resp, requestErr := (&http.Client{Transport: s.transport}).Do(req)
	result := AccountTestResult{AccountID: account.ID, Mode: input.Mode, Model: input.Model, LatencyMS: time.Since(started).Milliseconds(), TestedAt: time.Now()}
	if requestErr != nil {
		account.markTransportFailure()
		result.Error = "upstream request failed: " + requestErr.Error()
		writeJSON(w, http.StatusOK, result)
		return
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if readErr != nil {
		account.markTransportFailure()
		result.Error = "read upstream response failed"
		writeJSON(w, http.StatusOK, result)
		return
	}
	account.markStatus(resp.StatusCode)
	if len(body) > 1<<20 {
		result.Error = "upstream response exceeds 1 MiB"
		writeJSON(w, http.StatusOK, result)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = upstreamErrorMessage(body, resp.StatusCode)
		writeJSON(w, http.StatusOK, result)
		return
	}
	if input.Mode == "models" {
		var response struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			result.Error = "upstream returned invalid models JSON"
			writeJSON(w, http.StatusOK, result)
			return
		}
		result.Models = make([]string, 0, len(response.Data))
		for _, model := range response.Data {
			if model.ID != "" {
				result.Models = append(result.Models, model.ID)
			}
		}
	} else {
		var response struct {
			Model   string `json:"model"`
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			result.Error = "upstream returned invalid chat JSON"
			writeJSON(w, http.StatusOK, result)
			return
		}
		if response.Model != "" {
			result.Model = response.Model
		}
		if len(response.Choices) > 0 {
			result.Output = truncateText(response.Choices[0].Message.Content, 200)
		}
	}
	result.OK = true
	writeJSON(w, http.StatusOK, result)
}

func upstreamErrorMessage(body []byte, status int) string {
	var response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) == nil && response.Error.Message != "" {
		return truncateText(response.Error.Message, 300)
	}
	return fmt.Sprintf("upstream returned HTTP %d", status)
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func (s *Server) removeManagedAccount(w http.ResponseWriter, _ *http.Request, id string) {
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	for index, current := range s.accounts {
		if current.ID != id {
			continue
		}
		if !current.Managed {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "environment account is read-only"})
			return
		}
		if err := deleteManagedAccount(s.config.DB, id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "delete account failed"})
			return
		}
		s.accounts = append(s.accounts[:index], s.accounts[index+1:]...)
		if s.alerts != nil {
			s.alerts.ResolveScope("account", id, time.Now())
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "account not found"})
}
