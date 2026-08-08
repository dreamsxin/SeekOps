package proxy

import (
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
