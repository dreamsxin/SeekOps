package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyCapturesNonStreamingUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Fatalf("upstream auth = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"deepseek-v4-flash","usage":{"prompt_tokens":10,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":6,"completion_tokens":3,"total_tokens":13}}`)
	}))
	defer upstream.Close()
	s := NewServer(Config{PlatformAPIKey: "client-secret", Accounts: []*Account{{ID: "a", APIKey: "upstream-secret", BaseURL: upstream.URL}}})
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","messages":[]}`))
	req.Header.Set("Authorization", "Bearer client-secret")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := s.Recorder().Snapshot()
	if got.TotalTokens < 13 || got.CacheHitTokens < 4 {
		t.Fatalf("stats = %+v", got)
	}
}

func TestProxyCapturesStreamingUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"include_usage":true`) {
			t.Fatalf("stream_options.include_usage was not injected: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"x\",\"model\":\"deepseek-v4-flash\",\"choices\":[{}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"model\":\"deepseek-v4-flash\",\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":7,\"total_tokens\":12}}\n\ndata: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()
	s := NewServer(Config{PlatformAPIKey: "client-secret", Accounts: []*Account{{ID: "a", APIKey: "upstream-secret", BaseURL: upstream.URL}}})
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","stream":true}`))
	req.Header.Set("Authorization", "Bearer client-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("stream body = %s", rec.Body.String())
	}
	got := s.Recorder().Snapshot()
	if got.TotalTokens != 12 {
		t.Fatalf("stats = %+v", got)
	}
}

func TestAuthAndReadiness(t *testing.T) {
	s := NewServer(Config{PlatformAPIKey: "client-secret"})
	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("auth status=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("ready status=%d", rec.Code)
	}
}

func TestAccountCooldown(t *testing.T) {
	a := &Account{APIKey: "k", BaseURL: "http://example.com"}
	a.markStatus(http.StatusTooManyRequests)
	if a.Healthy(time.Now()) {
		t.Fatal("account should be cooling down")
	}
}

func TestVirtualKeyLifecycle(t *testing.T) {
	store := NewKeyStore("admin-secret")
	view, secret, err := store.Create("Billing app", "tenant-a")
	if err != nil || secret == "" || view.ID == "" {
		t.Fatalf("create key: view=%+v secret=%q err=%v", view, secret, err)
	}
	principal, ok := store.Authenticate(secret)
	if !ok || principal.TenantID != "tenant-a" || principal.ID != view.ID {
		t.Fatalf("principal=%+v ok=%v", principal, ok)
	}
	if !store.Revoke(view.ID) {
		t.Fatal("revoke failed")
	}
	if _, ok := store.Authenticate(secret); ok {
		t.Fatal("revoked key authenticated")
	}
}

func TestVirtualKeyAdminAndTenantStats(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"deepseek-v4-flash","usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	s := NewServer(Config{PlatformAPIKey: "admin-secret", AdminAPIKey: "admin-secret", Accounts: []*Account{{ID: "a", APIKey: "upstream-secret", BaseURL: upstream.URL}}})
	create := httptest.NewRequest(http.MethodPost, "/admin/virtual-keys", strings.NewReader(`{"name":"App A","tenant_id":"tenant-a"}`))
	create.Header.Set("X-Admin-Key", "admin-secret")
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	s.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var payload struct {
		Key    VirtualKeyView `json:"key"`
		Secret string         `json:"secret"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+payload.Secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status=%d body=%s", rec.Code, rec.Body.String())
	}
	stats := s.Recorder().Snapshot()
	if len(stats.LastRequests) != 1 || stats.LastRequests[0].TenantID != "tenant-a" || stats.LastRequests[0].VirtualKeyID != payload.Key.ID {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestBalancePolling(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/balance" {
			t.Fatalf("balance path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Fatalf("balance auth=%s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"10.00","granted_balance":"1.00","topped_up_balance":"9.00"}]}`)
	}))
	defer upstream.Close()
	account := &Account{ID: "a", APIKey: "upstream-secret", BaseURL: upstream.URL}
	s := NewServer(Config{PlatformAPIKey: "client-secret", Accounts: []*Account{account}})
	s.PollBalancesOnce(context.Background())
	available, balances, updatedAt, balanceError := account.balanceSnapshot()
	if !available || len(balances) != 1 || balances[0].TotalBalance != "10.00" || updatedAt.IsZero() || balanceError != "" {
		t.Fatalf("balance=%v balances=%+v updated=%v error=%q", available, balances, updatedAt, balanceError)
	}
}
