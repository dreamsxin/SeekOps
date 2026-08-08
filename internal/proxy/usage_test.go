package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUsageSummaryFilteringAndCSVExport(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewServer(Config{PlatformAPIKey: "platform", AdminAPIKey: "admin", DB: db})
	events := []RequestStats{
		{RequestID: "req-a", TenantID: "tenant-a", VirtualKeyID: "vk-a", AccountID: "acct-a", Model: "deepseek-chat", Path: "/chat/completions", Status: 200, DurationMS: 120, FirstByteMS: 30, Usage: Usage{PromptTokens: 10, CacheHitTokens: 4, CacheMissTokens: 6, CompletionTokens: 5, TotalTokens: 15, UsagePresent: true}, UsageStatus: "complete", EstimatedCostCNY: 0.01, CreatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)},
		{RequestID: "req-b", TenantID: "tenant-b", VirtualKeyID: "vk-b", AccountID: "acct-a", Model: "=spreadsheet", Path: "/responses", Status: 503, DurationMS: 250, FirstByteMS: 250, Usage: Usage{PromptTokens: 20, CompletionTokens: 0, TotalTokens: 20, UsagePresent: true}, UsageStatus: "complete", EstimatedCostCNY: 0.02, CreatedAt: time.Date(2026, 8, 1, 18, 0, 0, 0, time.UTC)},
		{RequestID: "req-c", TenantID: "tenant-a", VirtualKeyID: "vk-a", AccountID: "acct-b", Model: "deepseek-chat", Path: "/chat/completions", Status: 200, Usage: Usage{PromptTokens: 30, CompletionTokens: 10, TotalTokens: 40, UsagePresent: true}, UsageStatus: "complete", EstimatedCostCNY: 0.03, CreatedAt: time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)},
	}
	for _, event := range events {
		server.Recorder().Record(event)
	}

	summaryRequest := httptest.NewRequest(http.MethodGet, "/admin/usage/summary?start=2026-08-01&end=2026-08-01", nil)
	summaryRequest.Header.Set("X-Admin-Key", "admin")
	summaryRecorder := httptest.NewRecorder()
	server.ServeHTTP(summaryRecorder, summaryRequest)
	var summary UsageSummary
	if summaryRecorder.Code != http.StatusOK || json.Unmarshal(summaryRecorder.Body.Bytes(), &summary) != nil {
		t.Fatalf("summary status=%d body=%s", summaryRecorder.Code, summaryRecorder.Body.String())
	}
	if summary.Requests != 2 || summary.Successes != 1 || summary.Errors != 1 || summary.TotalTokens != 35 || len(summary.Daily) != 1 || len(summary.ByTenant) != 2 || len(summary.ByModel) != 2 {
		t.Fatalf("summary=%+v", summary)
	}
	if got := summary.End.Format("2006-01-02"); got != "2026-08-02" {
		t.Fatalf("exclusive summary end=%s", got)
	}

	filteredRequest := httptest.NewRequest(http.MethodGet, "/admin/usage/summary?start=2026-08-01&end=2026-08-02&tenant_id=tenant-a&model=deepseek-chat", nil)
	filteredRequest.Header.Set("X-Admin-Key", "admin")
	filteredRecorder := httptest.NewRecorder()
	server.ServeHTTP(filteredRecorder, filteredRequest)
	var filtered UsageSummary
	if filteredRecorder.Code != http.StatusOK || json.Unmarshal(filteredRecorder.Body.Bytes(), &filtered) != nil || filtered.Requests != 2 || filtered.TotalTokens != 55 || filtered.Errors != 0 {
		t.Fatalf("filtered status=%d body=%s", filteredRecorder.Code, filteredRecorder.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/admin/usage?start=2026-08-02&end=2026-08-02&limit=10", nil)
	listRequest.Header.Set("X-Admin-Key", "admin")
	listRecorder := httptest.NewRecorder()
	server.ServeHTTP(listRecorder, listRequest)
	var listed []RequestStats
	if listRecorder.Code != http.StatusOK || json.Unmarshal(listRecorder.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].RequestID != "req-c" {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}

	exportRequest := httptest.NewRequest(http.MethodGet, "/admin/usage/export?start=2026-08-01&end=2026-08-01", nil)
	exportRequest.Header.Set("X-Admin-Key", "admin")
	exportRecorder := httptest.NewRecorder()
	server.ServeHTTP(exportRecorder, exportRequest)
	body := exportRecorder.Body.String()
	if exportRecorder.Code != http.StatusOK || !strings.Contains(exportRecorder.Header().Get("Content-Type"), "text/csv") || !strings.Contains(body, "req-a") || !strings.Contains(body, "req-b") || strings.Contains(body, "req-c") || !strings.Contains(body, "'=spreadsheet") {
		t.Fatalf("export status=%d headers=%v body=%q", exportRecorder.Code, exportRecorder.Header(), body)
	}

	invalidRequest := httptest.NewRequest(http.MethodGet, "/admin/usage/summary?start=bad-date", nil)
	invalidRequest.Header.Set("X-Admin-Key", "admin")
	invalidRecorder := httptest.NewRecorder()
	server.ServeHTTP(invalidRecorder, invalidRequest)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid range status=%d body=%s", invalidRecorder.Code, invalidRecorder.Body.String())
	}
}
