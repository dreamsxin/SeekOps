package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAlertLifecycleAndPersistence(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAlertStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	store.Raise("account_check:a", "account_check", "account", "a", "warning", "检测失败", "timeout", now)
	items := store.List(now)
	if len(items) != 1 || items[0].Status != alertStatusOpen {
		t.Fatalf("initial alerts = %+v", items)
	}
	acknowledged, err := store.Acknowledge(items[0].ID, now.Add(time.Minute))
	if err != nil || acknowledged.Status != alertStatusAcknowledged {
		t.Fatalf("acknowledge = %+v, %v", acknowledged, err)
	}
	store.Raise("account_check:a", "account_check", "account", "a", "warning", "检测失败", "still failing", now.Add(2*time.Minute))
	if got := store.List(now.Add(2 * time.Minute))[0]; got.Status != alertStatusAcknowledged {
		t.Fatalf("acknowledged alert reopened without escalation: %+v", got)
	}
	store.Raise("account_check:a", "account_check", "account", "a", "critical", "检测失败", "invalid key", now.Add(3*time.Minute))
	if got := store.List(now.Add(3 * time.Minute))[0]; got.Status != alertStatusOpen || got.Severity != "critical" {
		t.Fatalf("critical escalation = %+v", got)
	}
	silenced, err := store.Silence(items[0].ID, 5*time.Minute, now.Add(4*time.Minute))
	if err != nil || silenced.Status != alertStatusSilenced {
		t.Fatalf("silence = %+v, %v", silenced, err)
	}
	if got := store.List(now.Add(10 * time.Minute))[0]; got.Status != alertStatusOpen {
		t.Fatalf("expired silence = %+v", got)
	}
	store.Resolve("account_check:a", now.Add(11*time.Minute))
	if got := store.List(now.Add(11 * time.Minute))[0]; got.Status != alertStatusResolved || got.ResolvedAt.IsZero() {
		t.Fatalf("resolved alert = %+v", got)
	}
	store.Raise("account_check:a", "account_check", "account", "a", "warning", "再次失败", "timeout", now.Add(12*time.Minute))
	reopened := store.List(now.Add(12 * time.Minute))[0]
	if reopened.Status != alertStatusOpen || !reopened.FirstSeenAt.Equal(now.Add(12*time.Minute)) || !reopened.ResolvedAt.IsZero() {
		t.Fatalf("reopened alert = %+v", reopened)
	}
	reloaded, err := NewAlertStore(db)
	if err != nil {
		t.Fatal(err)
	}
	persisted := reloaded.List(now.Add(12 * time.Minute))
	if len(persisted) != 1 || persisted[0].Status != alertStatusOpen || persisted[0].Message != "timeout" {
		t.Fatalf("persisted alerts = %+v", persisted)
	}
}

func TestAlertConditionsRecover(t *testing.T) {
	store, err := NewAlertStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	settings := defaultAlertSettings()
	settings.ErrorRateMinRequests = 4
	settings.ErrorRateThresholdPercent = 50
	settings.ErrorRateWindowMinutes = 1
	if _, err := store.UpdateSettings(settings, now); err != nil {
		t.Fatal(err)
	}

	account := AccountView{ID: "acct-a", Name: "生产账号", Enabled: true, BalanceError: "HTTP 401", BalanceUpdatedAt: now}
	store.EvaluateAccount(account, now)
	assertAlertStatus(t, store, "account_check:acct-a", alertStatusOpen, "critical", now)
	account.BalanceError = ""
	account.BalanceAvailable = true
	account.Balances = []BalanceInfo{{Currency: "CNY", TotalBalance: "5.00"}}
	store.EvaluateAccount(account, now.Add(time.Minute))
	assertAlertStatus(t, store, "account_check:acct-a", alertStatusResolved, "critical", now.Add(time.Minute))
	assertAlertStatus(t, store, "low_balance:acct-a", alertStatusOpen, "warning", now.Add(time.Minute))
	account.Balances[0].TotalBalance = "25.00"
	store.EvaluateAccount(account, now.Add(2*time.Minute))
	assertAlertStatus(t, store, "low_balance:acct-a", alertStatusResolved, "warning", now.Add(2*time.Minute))

	key := VirtualKeyView{ID: "vk-a", Name: "生产租户", Enabled: true, Quota: QuotaPolicy{DailyTokens: 100}, Usage: QuotaUsage{DailyTokens: 80}}
	store.EvaluateQuota(key, now)
	assertAlertStatus(t, store, "quota_tokens:vk-a", alertStatusOpen, "warning", now)
	key.Usage.DailyTokens = 100
	store.EvaluateQuota(key, now.Add(time.Minute))
	assertAlertStatus(t, store, "quota_tokens:vk-a", alertStatusOpen, "critical", now.Add(time.Minute))
	key.Usage.DailyTokens = 20
	store.EvaluateQuota(key, now.Add(2*time.Minute))
	assertAlertStatus(t, store, "quota_tokens:vk-a", alertStatusResolved, "critical", now.Add(2*time.Minute))

	for index, status := range []int{500, 502, 200, 200} {
		store.RecordRequest(RequestStats{Status: status}, now.Add(time.Duration(index)*time.Second))
	}
	assertAlertStatus(t, store, "error_rate:global", alertStatusOpen, "critical", now.Add(4*time.Second))
	store.RecordRequest(RequestStats{Status: 200}, now.Add(5*time.Second))
	assertAlertStatus(t, store, "error_rate:global", alertStatusResolved, "critical", now.Add(5*time.Second))
	store.RecordRequest(RequestStats{Status: 200}, now.Add(2*time.Minute))
	assertAlertStatus(t, store, "error_rate:global", alertStatusResolved, "critical", now.Add(2*time.Minute))
	store.RecordRequest(RequestStats{Status: 500}, now.Add(2*time.Minute+time.Second))
	store.RecordRequest(RequestStats{Status: 502}, now.Add(2*time.Minute+2*time.Second))
	store.RecordRequest(RequestStats{Status: 200}, now.Add(2*time.Minute+3*time.Second))
	assertAlertStatus(t, store, "error_rate:global", alertStatusOpen, "critical", now.Add(2*time.Minute+3*time.Second))
}

func TestAlertSettingsValidation(t *testing.T) {
	store, _ := NewAlertStore(nil)
	settings := defaultAlertSettings()
	settings.QuotaWarningPercent = 100
	if _, err := store.UpdateSettings(settings, time.Now()); err == nil {
		t.Fatal("expected invalid quota warning threshold")
	}
	settings = defaultAlertSettings()
	settings.ErrorRateWindowMinutes = 0
	if _, err := store.UpdateSettings(settings, time.Now()); err == nil {
		t.Fatal("expected invalid error rate window")
	}
}

func TestAlertAdminAPI(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewServer(Config{DB: db, AdminAPIKey: "admin", PlatformAPIKey: "client"})
	now := time.Now()
	server.alerts.Raise("account_check:a", "account_check", "account", "a", "warning", "检测失败", "timeout", now)

	listRequest := httptest.NewRequest(http.MethodGet, "/admin/alerts", nil)
	listRequest.Header.Set("X-Admin-Key", "admin")
	listResponse := httptest.NewRecorder()
	server.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	var alerts []Alert
	if err := json.Unmarshal(listResponse.Body.Bytes(), &alerts); err != nil || len(alerts) != 1 {
		t.Fatalf("list alerts=%+v err=%v", alerts, err)
	}

	ackRequest := httptest.NewRequest(http.MethodPost, "/admin/alerts/"+alerts[0].ID+"/acknowledge", nil)
	ackRequest.Header.Set("X-Admin-Key", "admin")
	ackResponse := httptest.NewRecorder()
	server.ServeHTTP(ackResponse, ackRequest)
	if ackResponse.Code != http.StatusOK || !strings.Contains(ackResponse.Body.String(), `"status":"acknowledged"`) {
		t.Fatalf("ack status=%d body=%s", ackResponse.Code, ackResponse.Body.String())
	}

	settings := defaultAlertSettings()
	settings.BalanceThresholdCNY = 25
	payload, _ := json.Marshal(settings)
	settingsRequest := httptest.NewRequest(http.MethodPut, "/admin/alerts/settings", strings.NewReader(string(payload)))
	settingsRequest.Header.Set("X-Admin-Key", "admin")
	settingsResponse := httptest.NewRecorder()
	server.ServeHTTP(settingsResponse, settingsRequest)
	if settingsResponse.Code != http.StatusOK || !strings.Contains(settingsResponse.Body.String(), `"balance_threshold_cny":25`) {
		t.Fatalf("settings status=%d body=%s", settingsResponse.Code, settingsResponse.Body.String())
	}
}

func TestBalancePollRaisesAndRecoversAccountAlert(t *testing.T) {
	var healthy atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"100.00","granted_balance":"0.00","topped_up_balance":"100.00"}]}`))
	}))
	defer upstream.Close()
	server := NewServer(Config{Accounts: []*Account{{ID: "acct-a", Name: "生产账号", APIKey: "bad-first", BaseURL: upstream.URL}}})

	server.PollBalancesOnce(context.Background())
	assertAlertStatus(t, server.alerts, "account_check:acct-a", alertStatusOpen, "critical", time.Now())
	healthy.Store(true)
	server.PollBalancesOnce(context.Background())
	assertAlertStatus(t, server.alerts, "account_check:acct-a", alertStatusResolved, "critical", time.Now())
}

func assertAlertStatus(t *testing.T, store *AlertStore, source, status, severity string, now time.Time) {
	t.Helper()
	for _, item := range store.List(now) {
		if item.SourceKey == source {
			if item.Status != status || item.Severity != severity {
				t.Fatalf("alert %s = status %s severity %s, want %s %s", source, item.Status, item.Severity, status, severity)
			}
			return
		}
	}
	t.Fatalf("alert %s not found", source)
}
