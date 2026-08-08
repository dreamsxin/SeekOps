package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditStorePersistsAndFiltersSafeMetadata(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := NewAuditStore(db)
	store.Record("account_created", "account", "acct-one", "创建上游账号", map[string]any{
		"name":   "Primary",
		"models": []string{"deepseek-chat"},
	}, time.Now())
	store.Record("virtual_key_rotated", "virtual_key", "vk-one", "轮换租户密钥", map[string]any{
		"prefix": "sk-proxy-abcd...",
	}, time.Now())

	items, err := store.List(AuditFilter{ResourceType: "account", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ResourceID != "acct-one" || items[0].Metadata["name"] != "Primary" {
		t.Fatalf("unexpected audit rows: %#v", items)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sk-secret") {
		t.Fatalf("audit output contains a plaintext secret: %s", encoded)
	}
}

func TestAuditEndpointRequiresAdminAndReturnsRows(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "audit-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, err := NewServerChecked(Config{PlatformAPIKey: "platform-key", AdminAPIKey: "admin-key", DB: db})
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRequest(http.MethodPost, "/admin/virtual-keys", strings.NewReader(`{"name":"Billing","tenant_id":"team-one","quota":{"daily_tokens":1000}}`))
	create.Header.Set("X-Admin-Key", "admin-key")
	created := httptest.NewRecorder()
	server.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create virtual key = %d %s", created.Code, created.Body.String())
	}
	var createdPayload struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdPayload); err != nil || createdPayload.Secret == "" {
		t.Fatalf("decode created key: %v %s", err, created.Body.String())
	}

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/admin/audit-logs", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/admin/audit-logs?resource_type=virtual_key&limit=5", nil)
	request.Header.Set("X-Admin-Key", "admin-key")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "virtual_key_created") {
		t.Fatalf("audit response = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), createdPayload.Secret) {
		t.Fatalf("audit response contains created secret: %s", response.Body.String())
	}
}
