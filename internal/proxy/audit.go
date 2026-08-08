package proxy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type AuditLog struct {
	ID           int64          `json:"id"`
	Actor        string         `json:"actor"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Summary      string         `json:"summary"`
	Metadata     map[string]any `json:"metadata"`
	CreatedAt    time.Time      `json:"created_at"`
}

type AuditFilter struct {
	Action       string
	ResourceType string
	ResourceID   string
	Limit        int
}

type AuditStore struct {
	db *sql.DB
}

func accountAuditMetadata(account *Account) map[string]any {
	if account == nil {
		return map[string]any{}
	}
	return map[string]any{
		"name":     account.Name,
		"base_url": account.BaseURL,
		"weight":   account.Weight,
		"models":   append([]string(nil), account.Models...),
		"enabled":  !account.Disabled,
	}
}

func virtualKeyAuditMetadata(view VirtualKeyView) map[string]any {
	return map[string]any{
		"name":      view.Name,
		"tenant_id": view.TenantID,
		"prefix":    view.Prefix,
		"enabled":   view.Enabled,
		"quota":     view.Quota,
	}
}

func NewAuditStore(db *sql.DB) *AuditStore {
	return &AuditStore{db: db}
}

func (s *AuditStore) Record(action, resourceType, resourceID, summary string, metadata map[string]any, now time.Time) {
	if s == nil || s.db == nil {
		return
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		log.Printf("encode audit metadata %s: %v", action, err)
		return
	}
	_, err = s.db.Exec(`INSERT INTO audit_logs (actor, action, resource_type, resource_id, summary, metadata_json, created_at)
		VALUES ('admin', ?, ?, ?, ?, ?, ?)`, strings.TrimSpace(action), strings.TrimSpace(resourceType), strings.TrimSpace(resourceID),
		strings.TrimSpace(summary), string(encoded), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		log.Printf("persist audit log %s: %v", action, err)
	}
}

func (s *AuditStore) List(filter AuditFilter) ([]AuditLog, error) {
	if s == nil || s.db == nil {
		return []AuditLog{}, nil
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}
	where := []string{"1 = 1"}
	args := make([]any, 0, 4)
	if value := strings.TrimSpace(filter.Action); value != "" {
		where = append(where, "action = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.ResourceType); value != "" {
		where = append(where, "resource_type = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.ResourceID); value != "" {
		where = append(where, "resource_id = ?")
		args = append(args, value)
	}
	args = append(args, filter.Limit)
	rows, err := s.db.Query(`SELECT id, actor, action, resource_type, resource_id, summary, metadata_json, created_at
		FROM audit_logs WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit logs: %w", err)
	}
	defer rows.Close()
	result := make([]AuditLog, 0)
	for rows.Next() {
		var item AuditLog
		var metadata, createdAt string
		if err := rows.Scan(&item.ID, &item.Actor, &item.Action, &item.ResourceType, &item.ResourceID, &item.Summary, &metadata, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		item.Metadata = map[string]any{}
		if strings.TrimSpace(metadata) != "" {
			if err := json.Unmarshal([]byte(metadata), &item.Metadata); err != nil {
				return nil, fmt.Errorf("decode audit metadata: %w", err)
			}
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Server) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/admin/audit-logs" {
		w.Header().Set("Allow", "GET")
		if r.URL.Path != "/admin/audit-logs" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.audit.List(AuditFilter{Action: r.URL.Query().Get("action"), ResourceType: r.URL.Query().Get("resource_type"), ResourceID: r.URL.Query().Get("resource_id"), Limit: limit})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "query audit logs failed"})
		return
	}
	writeJSON(w, http.StatusOK, items)
}
