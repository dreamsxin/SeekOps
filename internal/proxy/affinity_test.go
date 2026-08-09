package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSessionAffinityReusesHealthyAccount(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		fmt.Fprint(w, `{"usage":{"prompt_tokens":2,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":0,"total_tokens":2}}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		fmt.Fprint(w, `{"usage":{"total_tokens":2}}`)
	}))
	defer second.Close()

	server := NewServer(Config{PlatformAPIKey: "client-key", SessionAffinityPercent: 100, Accounts: []*Account{
		{ID: "first", APIKey: "first-key", BaseURL: first.URL},
		{ID: "second", APIKey: "second-key", BaseURL: second.URL},
	}})
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer client-key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Proxy-Session-ID", "session-1")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i, response.Code, response.Body.String())
		}
		if response.Header().Get("X-Proxy-Routing-Policy") != routingPolicyAffinity {
			t.Fatalf("routing policy=%q", response.Header().Get("X-Proxy-Routing-Policy"))
		}
	}
	if !((firstCalls.Load() == 2 && secondCalls.Load() == 0) || (firstCalls.Load() == 0 && secondCalls.Load() == 2)) {
		t.Fatalf("calls first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
	stats := server.Recorder().Snapshot()
	if len(stats.LastRequests) != 2 || !stats.LastRequests[0].AffinityReused {
		t.Fatalf("request stats=%+v", stats.LastRequests)
	}
}

func TestSessionAffinityFailoverRebindsToFallback(t *testing.T) {
	var preferredCalls, fallbackCalls atomic.Int64
	preferred := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		preferredCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"message":"temporarily unavailable"}}`)
	}))
	defer preferred.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		fmt.Fprint(w, `{"usage":{"total_tokens":1}}`)
	}))
	defer fallback.Close()

	server := NewServer(Config{PlatformAPIKey: "client-key", SessionAffinityPercent: 100, Accounts: []*Account{
		{ID: "preferred", APIKey: "preferred-key", BaseURL: preferred.URL},
		{ID: "fallback", APIKey: "fallback-key", BaseURL: fallback.URL},
	}})
	server.accounts[1].active.Store(10)
	first := affinityRequest(server, "session-failover")
	if first.Code != http.StatusOK || first.Header().Get("X-Proxy-Attempts") != "2" {
		t.Fatalf("first status=%d attempts=%q body=%s", first.Code, first.Header().Get("X-Proxy-Attempts"), first.Body.String())
	}
	second := affinityRequest(server, "session-failover")
	if second.Code != http.StatusOK || second.Header().Get("X-Proxy-Affinity-Reused") != "true" {
		t.Fatalf("second status=%d reused=%q body=%s", second.Code, second.Header().Get("X-Proxy-Affinity-Reused"), second.Body.String())
	}
	if preferredCalls.Load() != 1 || fallbackCalls.Load() != 2 {
		t.Fatalf("calls preferred=%d fallback=%d", preferredCalls.Load(), fallbackCalls.Load())
	}
	stats := server.Recorder().Snapshot()
	if len(stats.LastRequests) != 2 || !stats.LastRequests[0].AffinityReused || !stats.LastRequests[1].AffinityFallback {
		t.Fatalf("request stats=%+v", stats.LastRequests)
	}
}

func affinityRequest(server *Server, sessionID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer client-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Proxy-Session-ID", sessionID)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	return response
}
