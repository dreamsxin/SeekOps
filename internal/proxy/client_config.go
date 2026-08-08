package proxy

import (
	"net/http"
	"strings"
)

type ClientConfigView struct {
	BaseURL          string `json:"base_url"`
	AnthropicBaseURL string `json:"anthropic_base_url"`
	APIKey           string `json:"api_key"`
	APIKeyPrefix     string `json:"api_key_prefix"`
}

func (s *Server) handleClientConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	apiKey := s.config.PlatformAPIKey
	writeJSON(w, http.StatusOK, ClientConfigView{
		BaseURL:          s.clientBaseURL(r),
		AnthropicBaseURL: s.clientAnthropicBaseURL(r),
		APIKey:           apiKey,
		APIKeyPrefix:     secretPrefix(apiKey),
	})
}

func (s *Server) clientAnthropicBaseURL(r *http.Request) string {
	baseURL := strings.TrimRight(s.clientBaseURL(r), "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	return baseURL + "/anthropic"
}

func (s *Server) clientBaseURL(r *http.Request) string {
	if configured := strings.TrimRight(strings.TrimSpace(s.config.PublicBaseURL), "/"); configured != "" {
		return configured
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := firstForwardedValue(r.Header.Get("X-Forwarded-Proto")); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	host := r.Host
	if forwarded := firstForwardedValue(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host + "/v1"
}

func firstForwardedValue(value string) string {
	value, _, _ = strings.Cut(value, ",")
	return strings.TrimSpace(value)
}
