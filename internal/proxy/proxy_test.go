package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestVirtualKeyQuotas(t *testing.T) {
	rpm := NewKeyStore("")
	_, rpmSecret, err := rpm.CreateWithQuota("RPM", "tenant", QuotaPolicy{RequestsPerMinute: 1})
	if err != nil {
		t.Fatal(err)
	}
	principal, rejection := rpm.Acquire(rpmSecret, time.Now())
	if principal == nil || rejection != nil {
		t.Fatalf("first RPM acquire principal=%+v rejection=%+v", principal, rejection)
	}
	rpm.Release(principal.ID)
	if _, rejection = rpm.Acquire(rpmSecret, time.Now()); rejection == nil || rejection.Reason != "requests_per_minute" {
		t.Fatalf("second RPM acquire rejection=%+v", rejection)
	}
	nextMinute := time.Now().Add(time.Minute)
	if principal, rejection = rpm.Acquire(rpmSecret, nextMinute); principal == nil || rejection != nil {
		t.Fatalf("next-minute RPM acquire principal=%+v rejection=%+v", principal, rejection)
	}
	rpm.Release(principal.ID)

	concurrent := NewKeyStore("")
	_, concurrentSecret, err := concurrent.CreateWithQuota("Concurrency", "tenant", QuotaPolicy{ConcurrentRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, rejection := concurrent.Acquire(concurrentSecret, time.Now())
	if first == nil || rejection != nil {
		t.Fatalf("first concurrent acquire principal=%+v rejection=%+v", first, rejection)
	}
	if _, rejection = concurrent.Acquire(concurrentSecret, time.Now()); rejection == nil || rejection.Reason != "concurrent_requests" {
		t.Fatalf("second concurrent acquire rejection=%+v", rejection)
	}
	concurrent.Release(first.ID)

	daily := NewKeyStore("")
	_, dailySecret, err := daily.CreateWithQuota("Daily", "tenant", QuotaPolicy{DailyTokens: 2})
	if err != nil {
		t.Fatal(err)
	}
	first, rejection = daily.Acquire(dailySecret, time.Now())
	if first == nil || rejection != nil {
		t.Fatalf("daily acquire principal=%+v rejection=%+v", first, rejection)
	}
	daily.RecordUsage(first.ID, 2, 0, time.Now())
	daily.Release(first.ID)
	if _, rejection = daily.Acquire(dailySecret, time.Now()); rejection == nil || rejection.Reason != "daily_tokens" {
		t.Fatalf("daily quota rejection=%+v", rejection)
	}
}

func TestVirtualKeyAdminAndTenantStats(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"deepseek-v4-flash","usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	s := NewServer(Config{PlatformAPIKey: "admin-secret", AdminAPIKey: "admin-secret", Accounts: []*Account{{ID: "a", APIKey: "upstream-secret", BaseURL: upstream.URL}}})
	create := httptest.NewRequest(http.MethodPost, "/admin/virtual-keys", strings.NewReader(`{"name":"App A","tenant_id":"tenant-a","quota":{"requests_per_minute":12,"daily_tokens":1000}}`))
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
	if payload.Key.Quota.RequestsPerMinute != 12 || payload.Key.Quota.DailyTokens != 1000 {
		t.Fatalf("quota=%+v", payload.Key.Quota)
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

func TestSQLitePersistenceAcrossServerRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "seekops.db")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(Config{PlatformAPIKey: "admin-secret", DB: db})
	view, secret, err := s.keys.CreateWithQuota("Persistent app", "tenant-p", QuotaPolicy{DailyTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	s.keys.RecordUsage(view.ID, 12, 0.25, time.Now())
	s.Recorder().Record(RequestStats{RequestID: "persisted-request", TenantID: "tenant-p", VirtualKeyID: view.ID, AccountID: "acct-a", Path: "/chat/completions", Status: 200, Usage: Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, UsagePresent: true}, UsageStatus: "complete", EstimatedCostCNY: 0.25, CreatedAt: time.Now()})
	if err := persistBalance(db, "acct-a", true, []BalanceInfo{{Currency: "CNY", TotalBalance: "88.00", GrantedBalance: "8.00", ToppedUpBalance: "80.00"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	account := &Account{ID: "acct-a"}
	restarted := NewServer(Config{PlatformAPIKey: "admin-secret", DB: reopened, Accounts: []*Account{account}})
	if _, ok := restarted.keys.Authenticate(secret); !ok {
		t.Fatal("persisted virtual key did not authenticate after restart")
	}
	var persistedTokens int64
	if err := reopened.QueryRow("SELECT daily_tokens FROM virtual_keys WHERE id = ?", view.ID).Scan(&persistedTokens); err != nil {
		t.Fatal(err)
	}
	if persistedTokens != 12 {
		t.Fatalf("persisted daily tokens=%d", persistedTokens)
	}
	stats := restarted.Recorder().Snapshot()
	if stats.Requests != 1 || stats.TotalTokens != 12 || len(stats.LastRequests) != 1 {
		t.Fatalf("restored stats=%+v", stats)
	}
	available, balances, _, _ := account.balanceSnapshot()
	if !available || len(balances) != 1 || balances[0].TotalBalance != "88.00" {
		t.Fatalf("restored balance available=%v balances=%+v", available, balances)
	}
	usageReq := httptest.NewRequest(http.MethodGet, "/admin/usage?tenant_id=tenant-p&limit=10", nil)
	usageReq.Header.Set("X-Admin-Key", "admin-secret")
	usageRec := httptest.NewRecorder()
	restarted.ServeHTTP(usageRec, usageReq)
	var events []RequestStats
	if usageRec.Code != http.StatusOK || json.Unmarshal(usageRec.Body.Bytes(), &events) != nil || len(events) != 1 || events[0].RequestID != "persisted-request" {
		t.Fatalf("usage query status=%d body=%s", usageRec.Code, usageRec.Body.String())
	}
	balanceReq := httptest.NewRequest(http.MethodGet, "/admin/balance-history?account_id=acct-a", nil)
	balanceReq.Header.Set("X-Admin-Key", "admin-secret")
	balanceRec := httptest.NewRecorder()
	restarted.ServeHTTP(balanceRec, balanceReq)
	var history []BalanceSnapshot
	if balanceRec.Code != http.StatusOK || json.Unmarshal(balanceRec.Body.Bytes(), &history) != nil || len(history) != 1 || history[0].TotalBalance != "88.00" {
		t.Fatalf("balance query status=%d body=%s", balanceRec.Code, balanceRec.Body.String())
	}
}

func TestConsoleAssets(t *testing.T) {
	s := NewServer(Config{PlatformAPIKey: "admin-secret"})
	paths := []string{"/console/"}
	entries, err := ConsoleAssets.ReadDir("web/assets")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".js") {
			paths = append(paths, "/console/assets/"+entry.Name())
			break
		}
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Fatalf("console path=%s status=%d bytes=%d", path, rec.Code, rec.Body.Len())
		}
	}
}
