package proxy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProxyFailoverRetriesRetryableStatus(t *testing.T) {
	var failedCalls, successfulCalls atomic.Int64
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failedCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer failed.Close()
	successful := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successfulCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer fallback-key" {
			t.Fatalf("fallback authorization = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), `"model":"deepseek-chat"`) {
			t.Fatalf("fallback body=%q err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"deepseek-chat","usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	}))
	defer successful.Close()

	server := NewServer(Config{PlatformAPIKey: "client-key", Accounts: []*Account{
		{ID: "fallback", APIKey: "fallback-key", BaseURL: successful.URL},
		{ID: "limited", APIKey: "limited-key", BaseURL: failed.URL},
	}})
	recorder := serveProxyRequest(server, `{"model":"deepseek-chat","messages":[]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if failedCalls.Load() != 1 || successfulCalls.Load() != 1 {
		t.Fatalf("calls failed=%d successful=%d", failedCalls.Load(), successfulCalls.Load())
	}
	if got := recorder.Header().Get("X-Proxy-Attempts"); got != "2" {
		t.Fatalf("attempts header=%q", got)
	}
	stats := server.Recorder().Snapshot()
	if len(stats.LastRequests) != 1 || stats.LastRequests[0].AccountID != "fallback" || stats.LastRequests[0].Attempts != 2 {
		t.Fatalf("last requests=%+v", stats.LastRequests)
	}
}

func TestProxyFailoverRetriesNetworkError(t *testing.T) {
	var successfulCalls atomic.Int64
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	successful := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successfulCalls.Add(1)
		fmt.Fprint(w, `{"usage":{"total_tokens":1}}`)
	}))
	defer successful.Close()

	server := NewServer(Config{PlatformAPIKey: "client-key", Accounts: []*Account{
		{ID: "fallback", APIKey: "fallback-key", BaseURL: successful.URL},
		{ID: "offline", APIKey: "offline-key", BaseURL: closedURL},
	}})
	recorder := serveProxyRequest(server, `{"model":"deepseek-chat","messages":[]}`)
	if recorder.Code != http.StatusOK || successfulCalls.Load() != 1 || recorder.Header().Get("X-Proxy-Attempts") != "2" {
		t.Fatalf("status=%d calls=%d attempts=%q body=%s", recorder.Code, successfulCalls.Load(), recorder.Header().Get("X-Proxy-Attempts"), recorder.Body.String())
	}
}

func TestProxyFailoverReportsAttemptsWhenBothAccountsFail(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	firstURL := first.URL
	first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	secondURL := second.URL
	second.Close()

	server := NewServer(Config{PlatformAPIKey: "client-key", Accounts: []*Account{
		{ID: "second", APIKey: "second-key", BaseURL: secondURL},
		{ID: "first", APIKey: "first-key", BaseURL: firstURL},
	}})
	recorder := serveProxyRequest(server, `{"model":"deepseek-chat","messages":[]}`)
	if recorder.Code != http.StatusBadGateway || recorder.Header().Get("X-Proxy-Attempts") != "2" {
		t.Fatalf("status=%d attempts=%q body=%s", recorder.Code, recorder.Header().Get("X-Proxy-Attempts"), recorder.Body.String())
	}
	stats := server.Recorder().Snapshot()
	if len(stats.LastRequests) != 1 || stats.LastRequests[0].Attempts != 2 || stats.LastRequests[0].Model != "deepseek-chat" {
		t.Fatalf("last requests=%+v", stats.LastRequests)
	}
}

func TestProxyFailoverDoesNotRetryNonRetryableStatus(t *testing.T) {
	var fallbackCalls atomic.Int64
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid key"}}`)
	}))
	defer unauthorized.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer fallback.Close()

	server := NewServer(Config{PlatformAPIKey: "client-key", Accounts: []*Account{
		{ID: "fallback", APIKey: "fallback-key", BaseURL: fallback.URL},
		{ID: "unauthorized", APIKey: "bad-key", BaseURL: unauthorized.URL},
	}})
	recorder := serveProxyRequest(server, `{"model":"deepseek-chat","messages":[]}`)
	if recorder.Code != http.StatusUnauthorized || fallbackCalls.Load() != 0 || recorder.Header().Get("X-Proxy-Attempts") != "1" {
		t.Fatalf("status=%d fallback=%d attempts=%q", recorder.Code, fallbackCalls.Load(), recorder.Header().Get("X-Proxy-Attempts"))
	}
}

func TestAccountAPITestModelsAndChat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Fatalf("accept header=%q", r.Header.Get("Accept"))
		}
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"deepseek-v4-flash"},{"id":"deepseek-chat"}]}`)
		case "/chat/completions":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["model"] != "deepseek-v4-flash" {
				t.Fatalf("chat payload=%+v err=%v", payload, err)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"model":"deepseek-v4-flash","choices":[{"message":{"content":"OK"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	server := NewServer(Config{PlatformAPIKey: "client-key", AdminAPIKey: "admin-key", Accounts: []*Account{{ID: "test", APIKey: "upstream-key", BaseURL: upstream.URL, Models: []string{"deepseek-v4-flash"}}}})

	models := callAccountTest(t, server, "{}")
	if !models.OK || models.Status != http.StatusOK || len(models.Models) != 2 || models.Models[0] != "deepseek-v4-flash" {
		t.Fatalf("models result=%+v", models)
	}
	chat := callAccountTest(t, server, `{"mode":"chat"}`)
	if !chat.OK || chat.Status != http.StatusOK || chat.Model != "deepseek-v4-flash" || chat.Output != "OK" {
		t.Fatalf("chat result=%+v", chat)
	}
}

func TestSQLiteMigratesUsageMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE usage_events (
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
	)`)
	if err == nil {
		_, err = legacy.Exec(`INSERT INTO usage_events
			(request_id, tenant_id, virtual_key_id, account_id, model, path, status, duration_ms, first_byte_ms,
			 prompt_tokens, cache_hit_tokens, cache_miss_tokens, completion_tokens, reasoning_tokens, total_tokens,
			 usage_present, usage_status, estimated_cost_cny, created_at)
			VALUES ('legacy', 'tenant', 'key', 'account', 'model', '/chat/completions', 200, 1, 1,
			 1, 0, 1, 1, 0, 2, 1, 'complete', 0, '2026-08-08T00:00:00Z')`)
	}
	if closeErr := legacy.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attempts int
	var affinityReused, affinityFallback int
	var priceRuleID, priceStatus, routingPolicy string
	if err := db.QueryRow(`SELECT attempts, price_rule_id, price_status, routing_policy, affinity_reused, affinity_fallback FROM usage_events WHERE request_id = 'legacy'`).Scan(&attempts, &priceRuleID, &priceStatus, &routingPolicy, &affinityReused, &affinityFallback); err != nil || attempts != 1 || priceRuleID != "" || priceStatus != "legacy" || routingPolicy != "legacy" || affinityReused != 0 || affinityFallback != 0 {
		t.Fatalf("attempts=%d price_rule_id=%q price_status=%q routing_policy=%q reused=%d fallback=%d err=%v", attempts, priceRuleID, priceStatus, routingPolicy, affinityReused, affinityFallback, err)
	}
}

func serveProxyRequest(server *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer client-key")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	return recorder
}

func callAccountTest(t *testing.T, server *Server, body string) AccountTestResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/test/test", strings.NewReader(body))
	req.Header.Set("X-Admin-Key", "admin-key")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	var result AccountTestResult
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &result) != nil {
		t.Fatalf("test status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return result
}
