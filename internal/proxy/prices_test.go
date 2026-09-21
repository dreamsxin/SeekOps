package proxy

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPriceRuleRatesFollowPeakWindow(t *testing.T) {
	peakHit, peakMiss, peakOutput := 0.04, 2.0, 8.0
	rule := PriceRule{CacheHitCNYPerMillion: 0.02, CacheMissCNYPerMillion: 1, OutputCNYPerMillion: 4,
		PeakCacheHitCNYPerMillion: &peakHit, PeakCacheMissCNYPerMillion: &peakMiss, PeakOutputCNYPerMillion: &peakOutput}
	cases := []struct {
		name       string
		at         time.Time
		wantTier   string
		wantOutput float64
	}{
		{"monday morning peak", time.Date(2026, 9, 21, 10, 0, 0, 0, beijing), pricingTierPeak, 8},
		{"monday lunch break", time.Date(2026, 9, 21, 13, 0, 0, 0, beijing), pricingTierOffPeak, 4},
		{"monday afternoon peak", time.Date(2026, 9, 21, 17, 59, 0, 0, beijing), pricingTierPeak, 8},
		{"monday 18:00 off peak", time.Date(2026, 9, 21, 18, 0, 0, 0, beijing), pricingTierOffPeak, 4},
		{"monday night", time.Date(2026, 9, 21, 22, 0, 0, 0, beijing), pricingTierOffPeak, 4},
		{"saturday noon", time.Date(2026, 9, 19, 10, 0, 0, 0, beijing), pricingTierOffPeak, 4},
		{"utc instant inside peak", time.Date(2026, 9, 21, 2, 0, 0, 0, time.UTC), pricingTierPeak, 8},
	}
	for _, testCase := range cases {
		hit, miss, output, tier := rule.RatesAt(testCase.at)
		if tier != testCase.wantTier || output != testCase.wantOutput {
			t.Fatalf("%s: tier=%s output=%.2f, want tier=%s output=%.2f", testCase.name, tier, output, testCase.wantTier, testCase.wantOutput)
		}
		if tier == pricingTierPeak && (hit != peakHit || miss != peakMiss) {
			t.Fatalf("%s: peak input rates hit=%.4f miss=%.4f", testCase.name, hit, miss)
		}
		if tier == pricingTierOffPeak && (hit != 0.02 || miss != 1) {
			t.Fatalf("%s: off-peak input rates hit=%.4f miss=%.4f", testCase.name, hit, miss)
		}
	}
}

func TestPriceRuleWithoutPeakRatesKeepsBaseCost(t *testing.T) {
	rule := PriceRule{CacheHitCNYPerMillion: 2, CacheMissCNYPerMillion: 4, OutputCNYPerMillion: 8}
	hit, miss, output, tier := rule.RatesAt(time.Date(2026, 9, 21, 10, 0, 0, 0, beijing))
	if hit != 2 || miss != 4 || output != 8 || tier != pricingTierPeak {
		t.Fatalf("rates hit=%.2f miss=%.2f output=%.2f tier=%s", hit, miss, output, tier)
	}
}

func TestSQLiteMigratesPriceRulePeakColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-prices.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE price_rules (
		id TEXT PRIMARY KEY,
		model TEXT NOT NULL,
		cache_hit_cny_per_million REAL NOT NULL,
		cache_miss_cny_per_million REAL NOT NULL,
		output_cny_per_million REAL NOT NULL,
		effective_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`)
	if err == nil {
		_, err = legacy.Exec(`INSERT INTO price_rules VALUES ('legacy-rule', '*', 0.02, 1, 2,
			'2026-08-08T00:00:00Z', '2026-08-08T00:00:00Z')`)
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
	store, err := NewPriceStore(db, PriceDefaults{CacheHit: 0.02, CacheMiss: 1, Output: 4, PeakOutput: 8})
	if err != nil {
		t.Fatal(err)
	}
	rule, ok := store.Resolve("deepseek-flash", time.Now())
	if !ok || rule.ID != "legacy-rule" || rule.PeakOutputCNYPerMillion != nil {
		t.Fatalf("migrated rule = %+v, ok=%v", rule, ok)
	}
	if _, _, output, _ := rule.RatesAt(time.Date(2026, 9, 21, 10, 0, 0, 0, beijing)); output != 2 {
		t.Fatalf("legacy rule cost changed: output=%.2f", output)
	}
}

func TestUsagePricingTierRoundTrip(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	event := RequestStats{RequestID: "peak-request", Model: "deepseek-flash", Path: "/chat/completions", Status: 200,
		Usage: Usage{TotalTokens: 10, UsagePresent: true}, UsageStatus: "complete", PriceRuleID: "price-default",
		PriceStatus: "estimated", PricingTier: pricingTierPeak, EstimatedCostCNY: 0.1, CreatedAt: now}
	if err := persistRequest(db, event); err != nil {
		t.Fatal(err)
	}
	events, err := queryUsage(db, UsageFilter{StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].PricingTier != pricingTierPeak {
		t.Fatalf("persisted events = %+v", events)
	}
}

func TestPriceStoreResolvesVersionAndPersists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "seekops.db")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPriceStore(db, PriceDefaults{CacheHit: 0.02, CacheMiss: 1, Output: 4, PeakCacheHit: 0.04, PeakCacheMiss: 2, PeakOutput: 8})
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
	restored, err := NewPriceStore(reopened, PriceDefaults{CacheHit: 9, CacheMiss: 9, Output: 9})
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
