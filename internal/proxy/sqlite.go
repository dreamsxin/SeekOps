package proxy

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func OpenSQLite(path string) (*sql.DB, error) {
	if path == "" {
		return nil, nil
	}
	if path != ":memory:" && len(path) > 0 && path[0] != '?' {
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create sqlite directory: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite %s: %w", pragma, err)
		}
	}
	if err := migrateSQLite(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrateSQLite(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS upstream_accounts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			api_key TEXT NOT NULL,
			base_url TEXT NOT NULL,
			weight INTEGER NOT NULL DEFAULT 1,
			models_json TEXT NOT NULL DEFAULT '[]',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS admin_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			key_salt TEXT NOT NULL,
			key_hash TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS virtual_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			prefix TEXT NOT NULL,
			secret TEXT NOT NULL DEFAULT '',
			secret_hash TEXT NOT NULL UNIQUE,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			quota_rpm INTEGER NOT NULL DEFAULT 0,
			quota_concurrent INTEGER NOT NULL DEFAULT 0,
			quota_daily_tokens INTEGER NOT NULL DEFAULT 0,
			quota_daily_cost_cny REAL NOT NULL DEFAULT 0,
			usage_date TEXT NOT NULL DEFAULT '',
			daily_tokens INTEGER NOT NULL DEFAULT 0,
			daily_cost_cny REAL NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS usage_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id TEXT NOT NULL UNIQUE,
			tenant_id TEXT NOT NULL,
			virtual_key_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			path TEXT NOT NULL,
			status INTEGER NOT NULL,
			duration_ms INTEGER NOT NULL,
			first_byte_ms INTEGER NOT NULL,
			prompt_tokens INTEGER NOT NULL,
			cache_hit_tokens INTEGER NOT NULL,
			cache_miss_tokens INTEGER NOT NULL,
			completion_tokens INTEGER NOT NULL,
			reasoning_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			usage_present INTEGER NOT NULL,
			usage_status TEXT NOT NULL,
			estimated_cost_cny REAL NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_created_at ON usage_events(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_tenant ON usage_events(tenant_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS balance_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id TEXT NOT NULL,
			currency TEXT NOT NULL,
			total_balance TEXT NOT NULL,
			granted_balance TEXT NOT NULL,
			topped_up_balance TEXT NOT NULL,
			is_available INTEGER NOT NULL,
			observed_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_balance_snapshots_account ON balance_snapshots(account_id, observed_at)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("migrate sqlite: %w", err)
		}
	}
	if err := ensureVirtualKeySecretColumn(db); err != nil {
		return fmt.Errorf("migrate sqlite virtual key secret: %w", err)
	}
	return nil
}

func ensureVirtualKeySecretColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(virtual_keys)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == "secret" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE virtual_keys ADD COLUMN secret TEXT NOT NULL DEFAULT ''`)
	return err
}

func (s *KeyStore) loadSQLite(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, name, tenant_id, prefix, secret, secret_hash, enabled, created_at,
		quota_rpm, quota_concurrent, quota_daily_tokens, quota_daily_cost_cny,
		usage_date, daily_tokens, daily_cost_cny FROM virtual_keys`)
	if err != nil {
		return fmt.Errorf("load virtual keys from sqlite: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key virtualKey
		var enabled int
		var createdAt string
		if err := rows.Scan(&key.ID, &key.Name, &key.TenantID, &key.Prefix, &key.Secret, &key.Hash, &enabled, &createdAt,
			&key.Quota.RequestsPerMinute, &key.Quota.ConcurrentRequests, &key.Quota.DailyTokens, &key.Quota.DailyCostCNY,
			&key.usageDate, &key.dailyTokens, &key.dailyCostCNY); err != nil {
			return fmt.Errorf("scan virtual key from sqlite: %w", err)
		}
		key.Secret, err = s.secrets.Decrypt("virtual:"+key.ID, key.Secret)
		if err != nil {
			return fmt.Errorf("decrypt virtual key %s secret: %w", key.ID, err)
		}
		key.Enabled = enabled != 0
		key.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		s.byHash[key.Hash] = &key
		s.byID[key.ID] = &key
	}
	return rows.Err()
}

func (s *KeyStore) persistKeyLocked(db *sql.DB, key *virtualKey) error {
	storedSecret, err := s.secrets.Encrypt("virtual:"+key.ID, key.Secret)
	if err != nil {
		return fmt.Errorf("encrypt virtual key secret: %w", err)
	}
	_, err = db.Exec(`INSERT INTO virtual_keys
		(id, name, tenant_id, prefix, secret, secret_hash, enabled, created_at, quota_rpm, quota_concurrent,
		 quota_daily_tokens, quota_daily_cost_cny, usage_date, daily_tokens, daily_cost_cny)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, tenant_id=excluded.tenant_id,
		prefix=excluded.prefix, secret=excluded.secret, secret_hash=excluded.secret_hash,
		enabled=excluded.enabled, created_at=excluded.created_at,
		quota_rpm=excluded.quota_rpm,
		quota_concurrent=excluded.quota_concurrent, quota_daily_tokens=excluded.quota_daily_tokens,
		quota_daily_cost_cny=excluded.quota_daily_cost_cny, usage_date=excluded.usage_date,
		daily_tokens=excluded.daily_tokens, daily_cost_cny=excluded.daily_cost_cny`,
		key.ID, key.Name, key.TenantID, key.Prefix, storedSecret, key.Hash, boolInt(key.Enabled), key.CreatedAt.UTC().Format(time.RFC3339Nano),
		key.Quota.RequestsPerMinute, key.Quota.ConcurrentRequests, key.Quota.DailyTokens, key.Quota.DailyCostCNY,
		key.usageDate, key.dailyTokens, key.dailyCostCNY)
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func persistRequest(db *sql.DB, event RequestStats) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO usage_events
		(request_id, tenant_id, virtual_key_id, account_id, model, path, status, duration_ms, first_byte_ms,
		 prompt_tokens, cache_hit_tokens, cache_miss_tokens, completion_tokens, reasoning_tokens, total_tokens,
		 usage_present, usage_status, estimated_cost_cny, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RequestID, event.TenantID, event.VirtualKeyID, event.AccountID, event.Model, event.Path, event.Status,
		event.DurationMS, event.FirstByteMS, event.Usage.PromptTokens, event.Usage.CacheHitTokens,
		event.Usage.CacheMissTokens, event.Usage.CompletionTokens, event.Usage.ReasoningTokens, event.Usage.TotalTokens,
		boolInt(event.Usage.UsagePresent), event.UsageStatus, event.EstimatedCostCNY, event.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (r *Recorder) loadSQLite(db *sql.DB) {
	row := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(prompt_tokens), 0),
		COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(cache_hit_tokens), 0), COALESCE(SUM(cache_miss_tokens), 0),
		COALESCE(SUM(estimated_cost_cny), 0) FROM usage_events`)
	if err := row.Scan(&r.stats.Requests, &r.stats.TotalTokens, &r.stats.PromptTokens, &r.stats.CompletionTokens,
		&r.stats.CacheHitTokens, &r.stats.CacheMissTokens, &r.stats.EstimatedCostCNY); err != nil {
		log.Printf("load usage totals from sqlite: %v", err)
		return
	}
	row = db.QueryRow(`SELECT COUNT(*) FROM usage_events WHERE status >= 200 AND status < 400`)
	_ = row.Scan(&r.stats.Successes)
	r.stats.Errors = r.stats.Requests - r.stats.Successes
	rows, err := db.Query(`SELECT request_id, tenant_id, virtual_key_id, account_id, model, path, status,
		duration_ms, first_byte_ms, prompt_tokens, cache_hit_tokens, cache_miss_tokens, completion_tokens,
		reasoning_tokens, total_tokens, usage_present, usage_status, estimated_cost_cny, created_at
		FROM usage_events ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var event RequestStats
		var present int
		var createdAt string
		if err := rows.Scan(&event.RequestID, &event.TenantID, &event.VirtualKeyID, &event.AccountID, &event.Model, &event.Path,
			&event.Status, &event.DurationMS, &event.FirstByteMS, &event.Usage.PromptTokens, &event.Usage.CacheHitTokens,
			&event.Usage.CacheMissTokens, &event.Usage.CompletionTokens, &event.Usage.ReasoningTokens, &event.Usage.TotalTokens,
			&present, &event.UsageStatus, &event.EstimatedCostCNY, &createdAt); err != nil {
			continue
		}
		event.Usage.UsagePresent = present != 0
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		r.latest = append(r.latest, event)
	}
}

func persistBalance(db *sql.DB, accountID string, available bool, balances []BalanceInfo, observedAt time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, balance := range balances {
		if _, err := tx.Exec(`INSERT INTO balance_snapshots
			(account_id, currency, total_balance, granted_balance, topped_up_balance, is_available, observed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, accountID, balance.Currency, balance.TotalBalance, balance.GrantedBalance,
			balance.ToppedUpBalance, boolInt(available), observedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func loadLatestBalances(db *sql.DB, accounts []*Account) {
	for _, account := range accounts {
		if account == nil {
			continue
		}
		var observedAt string
		if err := db.QueryRow(`SELECT observed_at FROM balance_snapshots
			WHERE account_id = ? ORDER BY id DESC LIMIT 1`, account.ID).Scan(&observedAt); err != nil {
			continue
		}
		rows, err := db.Query(`SELECT currency, total_balance, granted_balance, topped_up_balance, is_available
			FROM balance_snapshots WHERE account_id = ? AND observed_at = ? ORDER BY id`, account.ID, observedAt)
		if err != nil {
			continue
		}
		var balances []BalanceInfo
		available := false
		for rows.Next() {
			var balance BalanceInfo
			var isAvailable int
			if err := rows.Scan(&balance.Currency, &balance.TotalBalance, &balance.GrantedBalance, &balance.ToppedUpBalance, &isAvailable); err == nil {
				balances = append(balances, balance)
				available = isAvailable != 0
			}
		}
		rows.Close()
		updatedAt, _ := time.Parse(time.RFC3339Nano, observedAt)
		account.balanceMu.Lock()
		account.BalanceAvailable = available
		account.Balances = balances
		account.BalanceUpdatedAt = updatedAt
		account.BalanceError = ""
		account.balanceMu.Unlock()
	}
}

type UsageFilter struct {
	TenantID     string
	VirtualKeyID string
	AccountID    string
	Model        string
	Limit        int
	StartAt      time.Time
	EndAt        time.Time
}

func queryUsage(db *sql.DB, filter UsageFilter) ([]RequestStats, error) {
	query := strings.Builder{}
	query.WriteString(`SELECT request_id, tenant_id, virtual_key_id, account_id, model, path, status,
		duration_ms, first_byte_ms, prompt_tokens, cache_hit_tokens, cache_miss_tokens, completion_tokens,
		reasoning_tokens, total_tokens, usage_present, usage_status, estimated_cost_cny, created_at
		FROM usage_events`)
	where, args := usageWhere(filter)
	query.WriteString(where)
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 10000 {
		limit = 10000
	}
	query.WriteString(" ORDER BY id DESC LIMIT ?")
	args = append(args, limit)
	rows, err := db.Query(query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RequestStats, 0, limit)
	for rows.Next() {
		var event RequestStats
		var present int
		var createdAt string
		if err := rows.Scan(&event.RequestID, &event.TenantID, &event.VirtualKeyID, &event.AccountID, &event.Model, &event.Path,
			&event.Status, &event.DurationMS, &event.FirstByteMS, &event.Usage.PromptTokens, &event.Usage.CacheHitTokens,
			&event.Usage.CacheMissTokens, &event.Usage.CompletionTokens, &event.Usage.ReasoningTokens, &event.Usage.TotalTokens,
			&present, &event.UsageStatus, &event.EstimatedCostCNY, &createdAt); err != nil {
			return nil, err
		}
		event.Usage.UsagePresent = present != 0
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		result = append(result, event)
	}
	return result, rows.Err()
}

type BalanceSnapshot struct {
	AccountID       string    `json:"account_id"`
	Currency        string    `json:"currency"`
	TotalBalance    string    `json:"total_balance"`
	GrantedBalance  string    `json:"granted_balance"`
	ToppedUpBalance string    `json:"topped_up_balance"`
	Available       bool      `json:"available"`
	ObservedAt      time.Time `json:"observed_at"`
}

func queryBalanceHistory(db *sql.DB, accountID string, limit int) ([]BalanceSnapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	query := `SELECT account_id, currency, total_balance, granted_balance, topped_up_balance, is_available, observed_at
		FROM balance_snapshots`
	args := make([]any, 0, 2)
	if accountID != "" {
		query += " WHERE account_id = ?"
		args = append(args, accountID)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]BalanceSnapshot, 0, limit)
	for rows.Next() {
		var item BalanceSnapshot
		var available int
		var observedAt string
		if err := rows.Scan(&item.AccountID, &item.Currency, &item.TotalBalance, &item.GrantedBalance,
			&item.ToppedUpBalance, &available, &observedAt); err != nil {
			return nil, err
		}
		item.Available = available != 0
		item.ObservedAt, _ = time.Parse(time.RFC3339Nano, observedAt)
		result = append(result, item)
	}
	return result, rows.Err()
}
