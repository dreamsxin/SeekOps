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

func TestAlertWebhookNotifiesOnTransitions(t *testing.T) {
	store, err := NewAlertStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	store.notify = func(notification webhookNotification) {
		events = append(events, notification.Event+":"+notification.Alert.Severity)
	}
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	store.Raise("account_check:a", "account_check", "account", "a", "warning", "检测失败", "timeout", now)
	if len(events) != 0 {
		t.Fatalf("no webhook configured, events=%v", events)
	}
	settings := store.Settings()
	settings.WebhookURL = "https://example.invalid/hook"
	if _, err := store.UpdateSettings(settings, now); err != nil {
		t.Fatal(err)
	}

	store.Raise("account_check:b", "account_check", "account", "b", "warning", "检测失败", "timeout", now)
	store.Raise("account_check:b", "account_check", "account", "b", "warning", "检测失败", "still failing", now.Add(time.Minute))
	store.Raise("account_check:b", "account_check", "account", "b", "critical", "检测失败", "invalid key", now.Add(2*time.Minute))
	store.Resolve("account_check:b", now.Add(3*time.Minute))
	store.Raise("account_check:b", "account_check", "account", "b", "warning", "检测失败", "failing again", now.Add(4*time.Minute))
	want := []string{"raised:warning", "escalated:critical", "resolved:critical", "raised:warning"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events=%v want=%v", events, want)
	}

	events = nil
	settings = store.Settings()
	settings.WebhookMinSeverity = "critical"
	if _, err := store.UpdateSettings(settings, now); err != nil {
		t.Fatal(err)
	}
	store.Raise("account_check:c", "account_check", "account", "c", "warning", "检测失败", "timeout", now)
	if len(events) != 0 {
		t.Fatalf("warning must be filtered by webhook_min_severity, events=%v", events)
	}
	store.Raise("account_check:c", "account_check", "account", "c", "critical", "检测失败", "invalid key", now.Add(time.Minute))
	if len(events) != 1 || events[0] != "escalated:critical" {
		t.Fatalf("events=%v", events)
	}
}

func TestAlertWebhookPayloadFormats(t *testing.T) {
	var contentType string
	var payloads []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode webhook payload: %v", err)
		}
		payloads = append(payloads, payload)
	}))
	defer upstream.Close()
	alert := Alert{ID: "alert-1", Severity: "critical", Title: "余额不足", Message: "CNY 余额 1.00",
		ScopeType: "account", ScopeID: "acct-a", LastSeenAt: time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)}
	for _, format := range []string{webhookFormatFeishu, webhookFormatWeCom, webhookFormatGeneric} {
		notification := webhookNotification{Alert: alert, Event: alertEventRaised,
			Settings: AlertSettings{WebhookURL: upstream.URL, WebhookFormat: format}}
		if err := postAlertWebhook(notification); err != nil {
			t.Fatalf("%s delivery: %v", format, err)
		}
	}
	if contentType != "application/json" {
		t.Fatalf("content type=%q", contentType)
	}
	if len(payloads) != 3 {
		t.Fatalf("payloads=%+v", payloads)
	}
	feishu, _ := payloads[0]["content"].(map[string]any)
	if payloads[0]["msg_type"] != "text" || !strings.Contains(feishu["text"].(string), "余额不足") {
		t.Fatalf("feishu payload=%+v", payloads[0])
	}
	wecom, _ := payloads[1]["text"].(map[string]any)
	if payloads[1]["msgtype"] != "text" || !strings.Contains(wecom["content"].(string), "acct-a") {
		t.Fatalf("wecom payload=%+v", payloads[1])
	}
	generic, _ := payloads[2]["alert"].(map[string]any)
	if payloads[2]["event"] != alertEventRaised || generic["id"] != "alert-1" {
		t.Fatalf("generic payload=%+v", payloads[2])
	}
}

func TestAlertWebhookTestEndpointReportsDelivery(t *testing.T) {
	var received int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received++
		if received > 1 {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer upstream.Close()
	server := NewServer(Config{PlatformAPIKey: "client-key", AdminAPIKey: "admin-key"})
	settings := server.alerts.Settings()
	settings.WebhookURL = upstream.URL
	settings.WebhookFormat = webhookFormatWeCom
	if _, err := server.alerts.UpdateSettings(settings, time.Now()); err != nil {
		t.Fatal(err)
	}
	call := func() map[string]any {
		request := httptest.NewRequest(http.MethodPost, "/admin/alerts/settings/test", nil)
		request.Header.Set("X-Admin-Key", "admin-key")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	if payload := call(); payload["delivered"] != true {
		t.Fatalf("first delivery=%+v", payload)
	}
	if got := server.alerts.Settings(); got.WebhookLastError != "" || got.WebhookLastDeliveryAt.IsZero() {
		t.Fatalf("settings after success=%+v", got)
	}
	failed := call()
	if failed["delivered"] != false || !strings.Contains(failed["error"].(string), "500") {
		t.Fatalf("second delivery=%+v", failed)
	}
	if got := server.alerts.Settings(); !strings.Contains(got.WebhookLastError, "500") {
		t.Fatalf("settings after failure=%+v", got)
	}
}

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
