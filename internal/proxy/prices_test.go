package proxy

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPriceStoreResolvesVersionAndPersists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "seekops.db")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPriceStore(db, 0.02, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if rule, ok := store.Resolve("unknown-model", time.Now()); !ok || rule.ID != "price-default" {
		t.Fatalf("default rule = %+v, ok=%v", rule, ok)
	}
	past := time.Now().Add(-time.Hour)
	exact, err := store.Create(PriceRule{Model: "deepseek-test", CacheHitCNYPerMillion: 2, CacheMissCNYPerMillion: 4, OutputCNYPerMillion: 8, EffectiveAt: past})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(PriceRule{Model: "deepseek-test", CacheHitCNYPerMillion: 20, CacheMissCNYPerMillion: 40, OutputCNYPerMillion: 80, EffectiveAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if rule, ok := store.Resolve("deepseek-test", time.Now()); !ok || rule.ID != exact.ID {
		t.Fatalf("resolved rule = %+v, ok=%v", rule, ok)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := NewPriceStore(reopened, 9, 9, 9)
	if err != nil {
		t.Fatal(err)
	}
	if rule, ok := restored.Resolve("deepseek-test", time.Now()); !ok || rule.ID != exact.ID {
		t.Fatalf("restored rule = %+v, ok=%v", rule, ok)
	}
}

func TestRequestBindsPriceVersionAndMarksMissingPrice(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"deepseek-test","usage":{"prompt_tokens":10,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":6,"completion_tokens":3,"total_tokens":13}}`)
	}))
	defer upstream.Close()
	server := NewServer(Config{PlatformAPIKey: "client-key", Accounts: []*Account{{ID: "a", APIKey: "upstream-secret", BaseURL: upstream.URL}}})
	rule, err := server.prices.Create(PriceRule{Model: "deepseek-test", CacheHitCNYPerMillion: 2, CacheMissCNYPerMillion: 4, OutputCNYPerMillion: 8, EffectiveAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	recorder := serveProxyRequest(server, `{"model":"deepseek-test","messages":[]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	event := server.Recorder().Snapshot().LastRequests[0]
	wantCost := (4.0*2 + 6.0*4 + 3.0*8) / 1e6
	if event.PriceRuleID != rule.ID || event.PriceStatus != "estimated" || math.Abs(event.EstimatedCostCNY-wantCost) > 1e-12 {
		t.Fatalf("priced event = %+v, want cost %.12f", event, wantCost)
	}

	missingServer := NewServer(Config{PlatformAPIKey: "client-key", Accounts: []*Account{{ID: "a", APIKey: "upstream-secret", BaseURL: upstream.URL}}})
	if deleted, err := missingServer.prices.Delete("price-default"); err != nil || !deleted {
		t.Fatal("delete default price rule")
	}
	missingRecorder := serveProxyRequest(missingServer, `{"model":"deepseek-test","messages":[]}`)
	if missingRecorder.Code != http.StatusOK {
		t.Fatalf("missing price status=%d body=%s", missingRecorder.Code, missingRecorder.Body.String())
	}
	stats := missingServer.Recorder().Snapshot()
	missing := stats.LastRequests[0]
	if missing.PriceStatus != "missing" || missing.EstimatedCostCNY != 0 || stats.UnpricedRequests != 1 {
		t.Fatalf("missing price event=%+v stats=%+v", missing, stats)
	}
}

func TestPriceAdminCRUD(t *testing.T) {
	server := NewServer(Config{PlatformAPIKey: "client-key", AdminAPIKey: "admin-key"})
	body := `{"model":"deepseek-admin","cache_hit_cny_per_million":1,"cache_miss_cny_per_million":2,"output_cny_per_million":3,"effective_at":"2026-08-08T00:00:00Z"}`
	request := httptest.NewRequest(http.MethodPost, "/admin/prices", strings.NewReader(body))
	request.Header.Set("X-Admin-Key", "admin-key")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || !strings.Contains(recorder.Body.String(), `"model":"deepseek-admin"`) {
		t.Fatalf("create status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/admin/prices", nil)
	listRequest.Header.Set("X-Admin-Key", "admin-key")
	listRecorder := httptest.NewRecorder()
	server.ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), `"model":"deepseek-admin"`) {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
}

func TestUsagePriceMetadataPersistsAndSummarizes(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "seekops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	event := RequestStats{RequestID: "unpriced-request", Model: "new-model", Path: "/chat/completions", Status: 200,
		Usage: Usage{TotalTokens: 10, UsagePresent: true}, UsageStatus: "complete", PriceStatus: "missing", CreatedAt: now}
	if err := persistRequest(db, event); err != nil {
		t.Fatal(err)
	}
	events, err := queryUsage(db, UsageFilter{StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].PriceStatus != "missing" || events[0].PriceRuleID != "" {
		t.Fatalf("persisted events = %+v", events)
	}
	summary, err := usageSummary(db, UsageFilter{StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if summary.UnpricedRequests != 1 || len(summary.ByModel) != 1 || summary.ByModel[0].UnpricedRequests != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}
