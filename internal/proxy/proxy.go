package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ConsoleAssets is populated by the frontend build at internal/proxy/web.
//
//go:embed web/* web/assets/*
var ConsoleAssets embed.FS

type Account struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	APIKey    string    `json:"-"`
	BaseURL   string    `json:"base_url"`
	Weight    int       `json:"weight"`
	Models    []string  `json:"models,omitempty"`
	Disabled  bool      `json:"-"`
	Managed   bool      `json:"-"`
	CreatedAt time.Time `json:"-"`

	active  atomic.Int64
	fails   atomic.Int64
	blocked atomic.Int64

	balanceMu        sync.RWMutex
	Balances         []BalanceInfo `json:"-"`
	BalanceAvailable bool          `json:"balance_available"`
	BalanceUpdatedAt time.Time     `json:"balance_updated_at,omitempty"`
	BalanceError     string        `json:"balance_error,omitempty"`
}

type BalanceInfo struct {
	Currency        string `json:"currency"`
	TotalBalance    string `json:"total_balance"`
	GrantedBalance  string `json:"granted_balance"`
	ToppedUpBalance string `json:"topped_up_balance"`
}

func (a *Account) setBalance(available bool, balances []BalanceInfo, err error) {
	a.balanceMu.Lock()
	defer a.balanceMu.Unlock()
	a.BalanceAvailable = available
	a.Balances = append([]BalanceInfo(nil), balances...)
	a.BalanceUpdatedAt = time.Now()
	a.BalanceError = ""
	if err != nil {
		a.BalanceError = err.Error()
	}
}
func (a *Account) balanceSnapshot() (bool, []BalanceInfo, time.Time, string) {
	a.balanceMu.RLock()
	defer a.balanceMu.RUnlock()
	return a.BalanceAvailable, append([]BalanceInfo(nil), a.Balances...), a.BalanceUpdatedAt, a.BalanceError
}

func (a *Account) Active() int64 { return a.active.Load() }
func (a *Account) Healthy(now time.Time) bool {
	return a.blocked.Load() <= now.Unix()
}
func (a *Account) SupportsModel(model string) bool {
	if model == "" || len(a.Models) == 0 {
		return true
	}
	for _, candidate := range a.Models {
		if candidate == model {
			return true
		}
	}
	return false
}
func (a *Account) markStatus(status int) {
	switch {
	case status == http.StatusUnauthorized:
		a.blocked.Store(time.Now().Add(10 * time.Minute).Unix())
	case status == http.StatusPaymentRequired:
		a.blocked.Store(time.Now().Add(30 * time.Minute).Unix())
	case status == http.StatusTooManyRequests:
		a.blocked.Store(time.Now().Add(5 * time.Second).Unix())
	case status >= 500:
		a.fails.Add(1)
		a.blocked.Store(time.Now().Add(3 * time.Second).Unix())
	default:
		a.fails.Store(0)
		a.blocked.Store(0)
	}
}

func (a *Account) markTransportFailure() {
	a.fails.Add(1)
	a.blocked.Store(time.Now().Add(3 * time.Second).Unix())
}

type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CacheHitTokens   int64 `json:"cache_hit_tokens"`
	CacheMissTokens  int64 `json:"cache_miss_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	UsagePresent     bool  `json:"usage_present"`
}

type RequestStats struct {
	RequestID        string    `json:"request_id"`
	TenantID         string    `json:"tenant_id"`
	VirtualKeyID     string    `json:"virtual_key_id"`
	AccountID        string    `json:"account_id"`
	Attempts         int       `json:"attempts"`
	Model            string    `json:"model,omitempty"`
	Path             string    `json:"path"`
	Status           int       `json:"status"`
	DurationMS       int64     `json:"duration_ms"`
	FirstByteMS      int64     `json:"first_byte_ms"`
	Usage            Usage     `json:"usage"`
	UsageStatus      string    `json:"usage_status"`
	PriceRuleID      string    `json:"price_rule_id,omitempty"`
	PriceStatus      string    `json:"price_status"`
	EstimatedCostCNY float64   `json:"estimated_cost_cny"`
	CreatedAt        time.Time `json:"created_at"`
}

type Principal struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	TenantID string `json:"tenant_id"`
}

type QuotaPolicy struct {
	RequestsPerMinute  int     `json:"requests_per_minute,omitempty"`
	ConcurrentRequests int     `json:"concurrent_requests,omitempty"`
	DailyTokens        int64   `json:"daily_tokens,omitempty"`
	DailyCostCNY       float64 `json:"daily_cost_cny,omitempty"`
}

type QuotaUsage struct {
	Date               string  `json:"date"`
	RequestsThisMinute int     `json:"requests_this_minute"`
	ActiveRequests     int     `json:"active_requests"`
	DailyTokens        int64   `json:"daily_tokens"`
	DailyCostCNY       float64 `json:"daily_cost_cny"`
}

type VirtualKeyView struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	TenantID        string      `json:"tenant_id"`
	Prefix          string      `json:"prefix"`
	Secret          string      `json:"secret"`
	SecretAvailable bool        `json:"secret_available"`
	Enabled         bool        `json:"enabled"`
	CreatedAt       time.Time   `json:"created_at"`
	Quota           QuotaPolicy `json:"quota"`
	Usage           QuotaUsage  `json:"usage"`
}

type virtualKey struct {
	VirtualKeyView
	Hash           string
	minute         int64
	minuteRequests int
	active         int
	usageDate      string
	dailyTokens    int64
	dailyCostCNY   float64
}

type KeyStore struct {
	mu      sync.RWMutex
	byHash  map[string]*virtualKey
	byID    map[string]*virtualKey
	db      *sql.DB
	secrets *SecretCipher
}

func NewKeyStore(defaultSecret string) *KeyStore {
	return NewKeyStoreWithDB(defaultSecret, nil)
}
func NewKeyStoreWithDB(defaultSecret string, db *sql.DB) *KeyStore {
	store, err := NewKeyStoreWithDBAndCipher(defaultSecret, db, nil)
	if err != nil {
		panic(err)
	}
	return store
}
func NewKeyStoreWithDBAndCipher(defaultSecret string, db *sql.DB, secrets *SecretCipher) (*KeyStore, error) {
	store := &KeyStore{byHash: make(map[string]*virtualKey), byID: make(map[string]*virtualKey), db: db, secrets: secrets}
	if db != nil {
		store.mu.Lock()
		if err := store.loadSQLite(db); err != nil {
			store.mu.Unlock()
			return nil, err
		}
		store.mu.Unlock()
	}
	if defaultSecret != "" {
		store.mu.Lock()
		hash := hashSecret(defaultSecret)
		key, exists := store.byID["vk-default"]
		if exists {
			delete(store.byHash, key.Hash)
			key.Hash = hash
			key.Secret = defaultSecret
			key.Prefix = secretPrefix(defaultSecret)
			key.Enabled = true
			store.byHash[hash] = key
		} else {
			key = store.add("vk-default", "Default platform key", "default", defaultSecret)
		}
		if db != nil {
			if err := store.persistKeyLocked(db, key); err != nil {
				store.mu.Unlock()
				return nil, fmt.Errorf("persist default virtual key: %w", err)
			}
		}
		store.mu.Unlock()
	}
	if db != nil && secrets != nil {
		store.mu.Lock()
		for _, key := range store.byID {
			if err := store.persistKeyLocked(db, key); err != nil {
				store.mu.Unlock()
				return nil, fmt.Errorf("migrate virtual key %s secret: %w", key.ID, err)
			}
		}
		store.mu.Unlock()
	}
	return store, nil
}
func (s *KeyStore) add(id, name, tenant, secret string) *virtualKey {
	hash := hashSecret(secret)
	key := &virtualKey{VirtualKeyView: VirtualKeyView{ID: id, Name: name, TenantID: tenant, Prefix: secretPrefix(secret), Secret: secret, Enabled: true, CreatedAt: time.Now()}, Hash: hash}
	s.byHash[hash] = key
	s.byID[id] = key
	return key
}
func (s *KeyStore) Authenticate(secret string) (*Principal, bool) {
	hash := hashSecret(secret)
	s.mu.RLock()
	key, ok := s.byHash[hash]
	if ok && !key.Enabled {
		ok = false
	}
	var principal *Principal
	if ok {
		principal = &Principal{ID: key.ID, Name: key.Name, TenantID: key.TenantID}
	}
	s.mu.RUnlock()
	return principal, ok
}
func (s *KeyStore) Create(name, tenant string) (VirtualKeyView, string, error) {
	return s.CreateWithQuota(name, tenant, QuotaPolicy{})
}
func (s *KeyStore) CreateWithQuota(name, tenant string, quota QuotaPolicy) (VirtualKeyView, string, error) {
	name = strings.TrimSpace(name)
	tenant = strings.TrimSpace(tenant)
	if name == "" || tenant == "" {
		return VirtualKeyView{}, "", fmt.Errorf("name and tenant_id are required")
	}
	if quota.RequestsPerMinute < 0 || quota.ConcurrentRequests < 0 || quota.DailyTokens < 0 || quota.DailyCostCNY < 0 {
		return VirtualKeyView{}, "", fmt.Errorf("quota values must not be negative")
	}
	secret := "sk-proxy-" + newID()
	id := "vk-" + newID()[:16]
	s.mu.Lock()
	key := s.add(id, name, tenant, secret)
	key.Quota = quota
	if s.db != nil {
		if err := s.persistKeyLocked(s.db, key); err != nil {
			delete(s.byHash, key.Hash)
			delete(s.byID, key.ID)
			s.mu.Unlock()
			return VirtualKeyView{}, "", fmt.Errorf("persist virtual key: %w", err)
		}
	}
	s.mu.Unlock()
	return key.view(), secret, nil
}
func (s *KeyStore) List() []VirtualKeyView {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	result := make([]VirtualKeyView, 0, len(s.byID))
	for _, key := range s.byID {
		key.resetUsage(now)
		minute := now.Unix() / 60
		if key.minute != minute {
			key.minute = minute
			key.minuteRequests = 0
		}
		result = append(result, key.view())
	}
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].ID < result[i].ID {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result
}

type QuotaRejection struct {
	Reason     string
	RetryAfter int
}

func (s *KeyStore) Acquire(secret string, now time.Time) (*Principal, *QuotaRejection) {
	hash := hashSecret(secret)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.byHash[hash]
	if !ok || !key.Enabled {
		return nil, nil
	}
	key.resetUsage(now)
	minute := now.Unix() / 60
	if key.minute != minute {
		key.minute = minute
		key.minuteRequests = 0
	}
	if key.Quota.RequestsPerMinute > 0 && key.minuteRequests >= key.Quota.RequestsPerMinute {
		return key.principal(), &QuotaRejection{Reason: "requests_per_minute", RetryAfter: int(60 - now.Unix()%60)}
	}
	if key.Quota.ConcurrentRequests > 0 && key.active >= key.Quota.ConcurrentRequests {
		return key.principal(), &QuotaRejection{Reason: "concurrent_requests", RetryAfter: 1}
	}
	if key.Quota.DailyTokens > 0 && key.dailyTokens >= key.Quota.DailyTokens {
		return key.principal(), &QuotaRejection{Reason: "daily_tokens", RetryAfter: 60}
	}
	if key.Quota.DailyCostCNY > 0 && key.dailyCostCNY >= key.Quota.DailyCostCNY {
		return key.principal(), &QuotaRejection{Reason: "daily_cost_cny", RetryAfter: 60}
	}
	key.minuteRequests++
	key.active++
	return key.principal(), nil
}
func (s *KeyStore) Release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key, ok := s.byID[id]; ok && key.active > 0 {
		key.active--
	}
}
func (s *KeyStore) RecordUsage(id string, tokens int64, cost float64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.byID[id]
	if !ok {
		return
	}
	key.resetUsage(now)
	key.dailyTokens += tokens
	key.dailyCostCNY += cost
	if s.db != nil {
		if err := s.persistKeyLocked(s.db, key); err != nil {
			log.Printf("persist virtual key usage: %v", err)
		}
	}
}
func (key *virtualKey) principal() *Principal {
	return &Principal{ID: key.ID, Name: key.Name, TenantID: key.TenantID}
}
func (key *virtualKey) resetUsage(now time.Time) {
	date := now.Format("2006-01-02")
	if key.usageDate != date {
		key.usageDate = date
		key.dailyTokens = 0
		key.dailyCostCNY = 0
	}
}
func (key *virtualKey) view() VirtualKeyView {
	view := key.VirtualKeyView
	view.SecretAvailable = view.Secret != ""
	view.Usage = QuotaUsage{Date: key.usageDate, RequestsThisMinute: key.minuteRequests, ActiveRequests: key.active, DailyTokens: key.dailyTokens, DailyCostCNY: key.dailyCostCNY}
	return view
}

func (s *KeyStore) Update(id, name, tenant string, quota QuotaPolicy, enabled bool) (VirtualKeyView, error) {
	name = strings.TrimSpace(name)
	tenant = strings.TrimSpace(tenant)
	if name == "" || tenant == "" {
		return VirtualKeyView{}, fmt.Errorf("name and tenant_id are required")
	}
	if quota.RequestsPerMinute < 0 || quota.ConcurrentRequests < 0 || quota.DailyTokens < 0 || quota.DailyCostCNY < 0 {
		return VirtualKeyView{}, fmt.Errorf("quota values must not be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.byID[id]
	if !ok {
		return VirtualKeyView{}, fmt.Errorf("virtual key not found")
	}
	if id == "vk-default" {
		enabled = true
	}
	previous := key.VirtualKeyView
	key.Name = name
	key.TenantID = tenant
	key.Quota = quota
	key.Enabled = enabled
	if s.db != nil {
		if err := s.persistKeyLocked(s.db, key); err != nil {
			key.VirtualKeyView = previous
			return VirtualKeyView{}, err
		}
	}
	return key.view(), nil
}

func (s *KeyStore) Rotate(id string) (VirtualKeyView, string, error) {
	if id == "vk-default" {
		return VirtualKeyView{}, "", fmt.Errorf("default platform key is configured by PLATFORM_API_KEY")
	}
	secret := "sk-proxy-" + newID()
	hash := hashSecret(secret)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.byID[id]
	if !ok {
		return VirtualKeyView{}, "", fmt.Errorf("virtual key not found")
	}
	oldHash, oldPrefix, oldSecret, oldEnabled := key.Hash, key.Prefix, key.Secret, key.Enabled
	delete(s.byHash, oldHash)
	key.Hash = hash
	key.Prefix = secretPrefix(secret)
	key.Secret = secret
	key.Enabled = true
	s.byHash[hash] = key
	if s.db != nil {
		if err := s.persistKeyLocked(s.db, key); err != nil {
			delete(s.byHash, hash)
			key.Hash, key.Prefix, key.Secret, key.Enabled = oldHash, oldPrefix, oldSecret, oldEnabled
			s.byHash[oldHash] = key
			return VirtualKeyView{}, "", err
		}
	}
	return key.view(), secret, nil
}
func (s *KeyStore) Revoke(id string) bool {
	if id == "vk-default" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.byID[id]
	if !ok {
		return false
	}
	key.Enabled = false
	if s.db != nil {
		if err := s.persistKeyLocked(s.db, key); err != nil {
			key.Enabled = true
			return false
		}
	}
	return true
}
func hashSecret(secret string) string {
	hash := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(hash[:])
}
func secretPrefix(secret string) string {
	if strings.HasPrefix(secret, "sk-proxy-") && len(secret) > 17 {
		return secret[:17] + "..."
	}
	if len(secret) <= 8 {
		return secret
	}
	return secret[:8] + "..."
}

type Stats struct {
	Requests         int64          `json:"requests"`
	Successes        int64          `json:"successes"`
	Errors           int64          `json:"errors"`
	TotalTokens      int64          `json:"total_tokens"`
	PromptTokens     int64          `json:"prompt_tokens"`
	CompletionTokens int64          `json:"completion_tokens"`
	CacheHitTokens   int64          `json:"cache_hit_tokens"`
	CacheMissTokens  int64          `json:"cache_miss_tokens"`
	EstimatedCostCNY float64        `json:"estimated_cost_cny"`
	UnpricedRequests int64          `json:"unpriced_requests"`
	LastRequests     []RequestStats `json:"last_requests"`
}

type Recorder struct {
	mu     sync.Mutex
	stats  Stats
	latest []RequestStats
	db     *sql.DB
}

func NewRecorder() *Recorder { return &Recorder{latest: make([]RequestStats, 0, 50)} }
func NewRecorderWithDB(db *sql.DB) *Recorder {
	recorder := &Recorder{latest: make([]RequestStats, 0, 50), db: db}
	recorder.loadSQLite(db)
	return recorder
}
func (r *Recorder) Record(event RequestStats) {
	if event.Attempts <= 0 {
		event.Attempts = 1
	}
	r.mu.Lock()
	r.stats.Requests++
	if event.Status >= 200 && event.Status < 400 {
		r.stats.Successes++
	} else {
		r.stats.Errors++
	}
	r.stats.TotalTokens += event.Usage.TotalTokens
	r.stats.PromptTokens += event.Usage.PromptTokens
	r.stats.CompletionTokens += event.Usage.CompletionTokens
	r.stats.CacheHitTokens += event.Usage.CacheHitTokens
	r.stats.CacheMissTokens += event.Usage.CacheMissTokens
	r.stats.EstimatedCostCNY += event.EstimatedCostCNY
	if event.PriceStatus == "missing" || event.PriceStatus == "usage_missing" {
		r.stats.UnpricedRequests++
	}
	r.latest = append([]RequestStats{event}, r.latest...)
	if len(r.latest) > 50 {
		r.latest = r.latest[:50]
	}
	r.mu.Unlock()
	if r.db != nil {
		if err := persistRequest(r.db, event); err != nil {
			log.Printf("persist usage event: %v", err)
		}
	}
}
func (r *Recorder) Snapshot() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := r.stats
	snapshot.LastRequests = append([]RequestStats(nil), r.latest...)
	return snapshot
}

type Config struct {
	ListenAddr     string
	PublicBaseURL  string
	PlatformAPIKey string
	AdminAPIKey    string
	RequestTimeout time.Duration
	Accounts       []*Account
	VirtualKeys    *KeyStore
	DB             *sql.DB
	SecretCipher   *SecretCipher
	PriceInputHit  float64
	PriceInputMiss float64
	PriceOutput    float64
}

type Server struct {
	config     Config
	recorder   *Recorder
	keys       *KeyStore
	prices     *PriceStore
	proxy      *httputil.ReverseProxy
	transport  http.RoundTripper
	server     *http.Server
	sequence   atomic.Uint64
	adminMu    sync.RWMutex
	adminKey   *adminKeyCredential
	accountsMu sync.RWMutex
	accounts   []*Account
}

type contextKey string

const (
	requestMetaKey contextKey = "request-meta"
)

type requestMeta struct {
	requestID string
	principal *Principal
	account   *Account
	model     string
	keys      *KeyStore
	path      string
	started   time.Time
	recorder  *Recorder
	prices    *PriceStore
	firstByte atomic.Int64
	status    atomic.Int64
	attempts  atomic.Int64
	usage     usageCollector
	recorded  atomic.Bool
}

type failoverTransport struct {
	server *Server
	base   http.RoundTripper
}

func (t *failoverTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	meta, _ := r.Context().Value(requestMetaKey).(*requestMeta)
	if meta == nil || meta.account == nil {
		return t.base.RoundTrip(r)
	}
	resp, err := t.base.RoundTrip(r)
	if !retryableUpstreamResult(resp, err) || !requestCanRetry(r) {
		return resp, err
	}
	next := t.server.selectAccount(meta.model, meta.account.ID)
	if next == nil {
		return resp, err
	}
	retry, cloneErr := cloneRequestForRetry(r)
	if cloneErr != nil {
		return resp, err
	}
	previous := meta.account
	if resp != nil {
		previous.markStatus(resp.StatusCode)
		_ = resp.Body.Close()
	} else {
		previous.markTransportFailure()
	}
	previous.active.Add(-1)
	next.active.Add(1)
	meta.account = next
	meta.attempts.Add(1)
	t.server.retargetRequest(retry, next, meta.path, meta.requestID)
	return t.base.RoundTrip(retry)
}

func retryableUpstreamResult(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func requestCanRetry(r *http.Request) bool {
	return r.Body == nil || r.Body == http.NoBody || r.GetBody != nil
}

func cloneRequestForRetry(r *http.Request) (*http.Request, error) {
	retry := r.Clone(r.Context())
	retry.Header = r.Header.Clone()
	if r.URL != nil {
		clonedURL := *r.URL
		retry.URL = &clonedURL
	}
	if r.GetBody != nil {
		body, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		retry.Body = body
	} else {
		retry.Body = http.NoBody
		retry.ContentLength = 0
	}
	return retry, nil
}

type usageCollector struct {
	mu    sync.Mutex
	usage Usage
	model string
}

func (u *usageCollector) merge(v Usage, model string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if v.UsagePresent {
		if v.PromptTokens > 0 || u.usage.PromptTokens == 0 {
			u.usage.PromptTokens = v.PromptTokens
			u.usage.CacheHitTokens = v.CacheHitTokens
			u.usage.CacheMissTokens = v.CacheMissTokens
		}
		if v.CompletionTokens > 0 || u.usage.CompletionTokens == 0 {
			u.usage.CompletionTokens = v.CompletionTokens
			u.usage.ReasoningTokens = v.ReasoningTokens
		}
		u.usage.TotalTokens = maxInt64(v.TotalTokens, u.usage.PromptTokens+u.usage.CompletionTokens)
		u.usage.UsagePresent = true
	}
	if model != "" {
		u.model = model
	}
}
func (u *usageCollector) snapshot() (Usage, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.usage, u.model
}

type trackingBody struct {
	body io.ReadCloser
	meta *requestMeta
	buf  []byte
}

func (b *trackingBody) Read(p []byte) (int, error) {
	if b.meta.firstByte.Load() == 0 {
		b.meta.firstByte.CompareAndSwap(0, time.Since(b.meta.started).Milliseconds())
	}
	n, err := b.body.Read(p)
	if n > 0 {
		b.buf = append(b.buf, p[:n]...)
		b.parse()
	}
	return n, err
}
func (b *trackingBody) Close() error {
	err := b.body.Close()
	b.parse()
	recordMeta(b.meta)
	return err
}
func (b *trackingBody) parse() {
	if len(b.buf) == 0 {
		return
	}
	if len(b.buf) > 2*1024*1024 {
		b.buf = b.buf[len(b.buf)-2*1024*1024:]
	}
	scanUsage(bytesReader(b.buf), b.meta)
}

type byteReader struct {
	data   []byte
	offset int
}

func bytesReader(data []byte) *byteReader { return &byteReader{data: data} }
func (r *byteReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func scanUsage(reader io.Reader, meta *requestMeta) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if line == "" || line == "[DONE]" || !strings.HasPrefix(line, "{") {
			continue
		}
		var payload map[string]any
		if json.Unmarshal([]byte(line), &payload) != nil {
			continue
		}
		if value, ok := payload["usage"].(map[string]any); ok {
			meta.usage.merge(parseCompatibleUsage(value), stringValue(payload["model"]))
		}
		if response, ok := payload["response"].(map[string]any); ok {
			if value, ok := response["usage"].(map[string]any); ok {
				meta.usage.merge(parseResponsesUsage(value), stringValue(response["model"]))
			}
		}
		if message, ok := payload["message"].(map[string]any); ok {
			if value, ok := message["usage"].(map[string]any); ok {
				meta.usage.merge(parseAnthropicUsage(value), stringValue(message["model"]))
			}
		}
	}
}

func parseCompatibleUsage(v map[string]any) Usage {
	if _, ok := v["prompt_tokens"]; ok {
		return parseUsage(v)
	}
	if _, ok := v["completion_tokens"]; ok {
		return parseUsage(v)
	}
	if _, ok := v["cache_read_input_tokens"]; ok {
		return parseAnthropicUsage(v)
	}
	if _, ok := v["cache_creation_input_tokens"]; ok {
		return parseAnthropicUsage(v)
	}
	return parseResponsesUsage(v)
}

func parseUsage(v map[string]any) Usage {
	prompt := intValue(v["prompt_tokens"])
	hit := intValue(v["prompt_cache_hit_tokens"])
	miss := intValue(v["prompt_cache_miss_tokens"])
	if miss == 0 && prompt > hit {
		miss = prompt - hit
	}
	completion := intValue(v["completion_tokens"])
	total := intValue(v["total_tokens"])
	if total == 0 {
		total = prompt + completion
	}
	return Usage{PromptTokens: prompt, CacheHitTokens: hit, CacheMissTokens: miss, CompletionTokens: completion, ReasoningTokens: intValue(nested(v, "completion_tokens_details", "reasoning_tokens")), TotalTokens: total, UsagePresent: true}
}
func parseResponsesUsage(v map[string]any) Usage {
	prompt := intValue(v["input_tokens"])
	hit := intValue(nested(v, "input_tokens_details", "cached_tokens"))
	completion := intValue(v["output_tokens"])
	total := intValue(v["total_tokens"])
	if total == 0 {
		total = prompt + completion
	}
	return Usage{PromptTokens: prompt, CacheHitTokens: hit, CacheMissTokens: prompt - hit, CompletionTokens: completion, ReasoningTokens: intValue(nested(v, "output_tokens_details", "reasoning_tokens")), TotalTokens: total, UsagePresent: true}
}

func parseAnthropicUsage(v map[string]any) Usage {
	input := intValue(v["input_tokens"])
	hit := intValue(v["cache_read_input_tokens"])
	creation := intValue(v["cache_creation_input_tokens"])
	prompt := input + hit + creation
	completion := intValue(v["output_tokens"])
	return Usage{PromptTokens: prompt, CacheHitTokens: hit, CacheMissTokens: input + creation, CompletionTokens: completion, TotalTokens: prompt + completion, UsagePresent: true}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func nested(m map[string]any, first, second string) any {
	child, ok := m[first].(map[string]any)
	if !ok {
		return nil
	}
	return child[second]
}
func intValue(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}
func stringValue(v any) string { s, _ := v.(string); return s }

func NewServer(cfg Config) *Server {
	server, err := NewServerChecked(cfg)
	if err != nil {
		panic(err)
	}
	return server
}

func NewServerChecked(cfg Config) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.PlatformAPIKey == "" {
		cfg.PlatformAPIKey = "proxy-demo-key"
	}
	if cfg.AdminAPIKey == "" {
		cfg.AdminAPIKey = cfg.PlatformAPIKey
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Minute
	}
	if cfg.PriceInputHit == 0 {
		cfg.PriceInputHit = 0.02
	}
	if cfg.PriceInputMiss == 0 {
		cfg.PriceInputMiss = 1
	}
	if cfg.PriceOutput == 0 {
		cfg.PriceOutput = 2
	}
	if err := ValidateStoredSecrets(cfg.DB, cfg.SecretCipher); err != nil {
		return nil, err
	}
	keys := cfg.VirtualKeys
	if keys == nil {
		var err error
		keys, err = NewKeyStoreWithDBAndCipher(cfg.PlatformAPIKey, cfg.DB, cfg.SecretCipher)
		if err != nil {
			return nil, fmt.Errorf("load virtual keys: %w", err)
		}
	}
	prices, err := NewPriceStore(cfg.DB, cfg.PriceInputHit, cfg.PriceInputMiss, cfg.PriceOutput)
	if err != nil {
		return nil, fmt.Errorf("load price rules: %w", err)
	}
	recorder := NewRecorder()
	accounts := mergeAccounts(cfg.Accounts, nil)
	if cfg.DB != nil {
		recorder = NewRecorderWithDB(cfg.DB)
		managedAccounts, err := loadManagedAccounts(cfg.DB, cfg.SecretCipher)
		if err != nil {
			return nil, fmt.Errorf("load managed accounts: %w", err)
		}
		if cfg.SecretCipher != nil {
			for _, account := range managedAccounts {
				if err := persistManagedAccount(cfg.DB, cfg.SecretCipher, account); err != nil {
					return nil, fmt.Errorf("migrate account %s api key: %w", account.ID, err)
				}
			}
		}
		accounts = mergeAccounts(cfg.Accounts, managedAccounts)
		loadLatestBalances(cfg.DB, accounts)
	}
	adminKey, err := loadAdminKey(cfg.DB)
	if err != nil {
		return nil, fmt.Errorf("load admin key: %w", err)
	}
	s := &Server{config: cfg, keys: keys, prices: prices, recorder: recorder, transport: http.DefaultTransport, adminKey: adminKey, accounts: accounts}
	s.proxy = &httputil.ReverseProxy{Director: s.director, Transport: &failoverTransport{server: s, base: s.transport}, ModifyResponse: s.modifyResponse, ErrorHandler: s.errorHandler, FlushInterval: -1}
	s.server = &http.Server{Addr: cfg.ListenAddr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}
func (s *Server) Recorder() *Recorder   { return s.recorder }
func (s *Server) Handler() http.Handler { return s }
func (s *Server) AccountCount() int     { return len(s.accountsSnapshot()) }
func (s *Server) ListenAndServe() error {
	log.Printf("deepseek proxy listening on %s", s.config.ListenAddr)
	return s.server.ListenAndServe()
}
func (s *Server) Shutdown(ctx context.Context) error { return s.server.Shutdown(ctx) }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/console") {
		s.console(w, r)
		return
	}
	if r.URL.Path == "/healthz" {
		writeJSON(w, 200, map[string]any{"status": "ok"})
		return
	}
	if r.URL.Path == "/readyz" {
		if !s.hasEnabledAccount() {
			writeJSON(w, 503, map[string]any{"status": "not_ready"})
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ready"})
		return
	}
	if r.URL.Path == "/metrics" {
		s.writeMetrics(w)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/") {
		s.admin(w, r)
		return
	}
	if !isProxyPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	principal, ok := s.authorize(w, r)
	if !ok {
		return
	}
	defer s.keys.Release(principal.ID)
	model, status, err := s.prepareRequest(r)
	if err != nil {
		writeProxyError(w, r, status, "invalid_request_error", err.Error(), nil)
		return
	}
	account := s.selectAccount(model, "")
	if account == nil {
		writeProxyError(w, r, http.StatusServiceUnavailable, "proxy_error", "no healthy upstream account available", nil)
		return
	}
	requestID := newID()
	meta := &requestMeta{requestID: requestID, principal: principal, account: account, model: model, keys: s.keys, prices: s.prices, path: r.URL.Path, started: time.Now(), recorder: s.recorder}
	meta.attempts.Store(1)
	account.active.Add(1)
	defer func() { meta.account.active.Add(-1) }()
	ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
	defer cancel()
	ctx = context.WithValue(ctx, requestMetaKey, meta)
	r = r.WithContext(ctx)
	w.Header().Set("X-Proxy-Request-ID", requestID)
	s.proxy.ServeHTTP(w, r)
}

func (s *Server) console(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/console")
	if path == "" || path == "/" {
		path = "index.html"
	} else {
		path = strings.TrimPrefix(path, "/")
	}
	if _, err := ConsoleAssets.ReadFile("web/" + path); err != nil {
		path = "index.html"
	}
	data, err := ConsoleAssets.ReadFile("web/" + path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contentType := "text/plain; charset=utf-8"
	switch {
	case strings.HasSuffix(path, ".html"):
		contentType = "text/html; charset=utf-8"
	case strings.HasSuffix(path, ".js"):
		contentType = "text/javascript; charset=utf-8"
	case strings.HasSuffix(path, ".css"):
		contentType = "text/css; charset=utf-8"
	case strings.HasSuffix(path, ".svg"):
		contentType = "image/svg+xml"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func isProxyPath(path string) bool {
	if isAnthropicPath(path) {
		return true
	}
	for _, suffix := range []string{"/chat/completions", "/responses", "/models"} {
		if path == suffix || path == "/v1"+suffix {
			return true
		}
	}
	return false
}
func isAnthropicPath(path string) bool { return path == "/anthropic/v1/messages" }
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) (*Principal, bool) {
	token := r.Header.Get("Authorization")
	if strings.HasPrefix(token, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	}
	if token == "" {
		token = r.Header.Get("X-Proxy-API-Key")
	}
	if token == "" {
		token = r.Header.Get("X-Api-Key")
	}
	principal, rejection := s.keys.Acquire(token, time.Now())
	if principal == nil {
		writeProxyError(w, r, http.StatusUnauthorized, "authentication_error", "invalid proxy API key", nil)
		return nil, false
	}
	if rejection != nil {
		if rejection.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(rejection.RetryAfter))
		}
		writeProxyError(w, r, http.StatusTooManyRequests, "quota_error", "virtual key quota exceeded", map[string]any{"reason": rejection.Reason})
		return nil, false
	}
	return principal, true
}
func (s *Server) selectAccount(model, excludedID string) *Account {
	now := time.Now()
	accounts := s.accountsSnapshot()
	var selected *Account
	var selectedScore float64
	start := int(s.sequence.Add(1) % uint64(max(1, len(accounts))))
	for offset := 0; offset < len(accounts); offset++ {
		a := accounts[(start+offset)%len(accounts)]
		if a == nil || a.ID == excludedID || a.Disabled || a.APIKey == "" || !a.Healthy(now) || !a.SupportsModel(model) {
			continue
		}
		weight := a.Weight
		if weight <= 0 {
			weight = 1
		}
		score := float64(a.Active()+1) / float64(weight)
		if selected == nil || score < selectedScore {
			selected, selectedScore = a, score
		}
	}
	return selected
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func (s *Server) prepareRequest(r *http.Request) (string, int, error) {
	isJSON := strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json")
	isSupportedBody := strings.HasSuffix(r.URL.Path, "/chat/completions") || strings.HasSuffix(r.URL.Path, "/responses") || isAnthropicPath(r.URL.Path)
	if r.Body == nil || !isJSON || !isSupportedBody {
		return r.Header.Get("X-Proxy-Model"), 0, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32*1024*1024+1))
	if err != nil {
		return "", http.StatusBadRequest, fmt.Errorf("read request body: %w", err)
	}
	if len(body) > 32*1024*1024 {
		return "", http.StatusRequestEntityTooLarge, fmt.Errorf("request body exceeds the 32 MiB MVP limit")
	}
	r.Body.Close()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", http.StatusBadRequest, fmt.Errorf("invalid JSON request body: %w", err)
	}
	model := r.Header.Get("X-Proxy-Model")
	if model == "" {
		model = stringValue(payload["model"])
	}
	if strings.HasSuffix(r.URL.Path, "/chat/completions") {
		if stream, ok := payload["stream"].(bool); ok && stream {
			options, _ := payload["stream_options"].(map[string]any)
			if options == nil {
				options = map[string]any{}
				payload["stream_options"] = options
			}
			options["include_usage"] = true
			body, err = json.Marshal(payload)
			if err != nil {
				return model, http.StatusBadRequest, fmt.Errorf("encode streaming request: %w", err)
			}
		}
	}
	setReplayableBody(r, body)
	return model, 0, nil
}

func setReplayableBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

func (s *Server) director(r *http.Request) {
	meta, _ := r.Context().Value(requestMetaKey).(*requestMeta)
	if meta == nil || meta.account == nil {
		return
	}
	s.retargetRequest(r, meta.account, meta.path, meta.requestID)
}

func (s *Server) retargetRequest(r *http.Request, account *Account, originalPath, requestID string) {
	target, err := url.Parse(account.BaseURL)
	if err != nil {
		return
	}
	path := originalPath
	if strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	if isAnthropicPath(path) && strings.HasSuffix(strings.TrimRight(target.Path, "/"), "/anthropic") {
		path = strings.TrimPrefix(path, "/anthropic")
	}
	r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
	r.URL.Path = joinPath(target.Path, path)
	r.URL.RawPath = ""
	r.Host = target.Host
	if isAnthropicPath(originalPath) {
		r.Header.Del("Authorization")
		r.Header.Set("X-Api-Key", account.APIKey)
	} else {
		r.Header.Set("Authorization", "Bearer "+account.APIKey)
		r.Header.Del("X-Api-Key")
	}
	r.Header.Del("X-Proxy-API-Key")
	r.Header.Del("X-Admin-Key")
	r.Header.Del("X-Proxy-Model")
	r.Header.Set("X-Proxy-Request-ID", requestID)
}
func joinPath(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func (s *Server) modifyResponse(resp *http.Response) error {
	if resp.Request == nil {
		return nil
	}
	meta, _ := resp.Request.Context().Value(requestMetaKey).(*requestMeta)
	if meta == nil {
		return nil
	}
	meta.status.Store(int64(resp.StatusCode))
	meta.account.markStatus(resp.StatusCode)
	resp.Body = &trackingBody{body: resp.Body, meta: meta}
	resp.Header.Set("X-Proxy-Request-ID", meta.requestID)
	resp.Header.Set("X-Proxy-Attempts", strconv.FormatInt(meta.attempts.Load(), 10))
	return nil
}
func (s *Server) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	meta, _ := r.Context().Value(requestMetaKey).(*requestMeta)
	if meta != nil && meta.account != nil {
		meta.account.markTransportFailure()
		meta.status.Store(502)
		w.Header().Set("X-Proxy-Attempts", strconv.FormatInt(meta.attempts.Load(), 10))
		recordMeta(meta)
	}
	log.Printf("proxy request failed: %v", err)
	writeProxyError(w, r, http.StatusBadGateway, "proxy_error", "upstream request failed", nil)
}

func writeProxyError(w http.ResponseWriter, r *http.Request, status int, errorType, message string, extra map[string]any) {
	errorBody := map[string]any{"message": message, "type": errorType}
	for key, value := range extra {
		errorBody[key] = value
	}
	if isAnthropicPath(r.URL.Path) {
		writeJSON(w, status, map[string]any{"type": "error", "error": errorBody})
		return
	}
	writeJSON(w, status, map[string]any{"error": errorBody})
}
func recordMeta(meta *requestMeta) {
	if meta == nil || !meta.recorded.CompareAndSwap(false, true) {
		return
	}
	usage, model := meta.usage.snapshot()
	if model == "" {
		model = meta.model
	}
	status := int(meta.status.Load())
	if status == 0 {
		status = 502
	}
	usageStatus := "missing"
	if usage.UsagePresent {
		usageStatus = "complete"
	}
	cost := float64(0)
	priceRuleID := ""
	priceStatus := "usage_missing"
	if usage.UsagePresent && meta.prices != nil {
		if rule, ok := meta.prices.Resolve(model, meta.started); ok {
			priceRuleID = rule.ID
			priceStatus = "estimated"
			cost = (float64(usage.CacheHitTokens)/1e6)*rule.CacheHitCNYPerMillion +
				(float64(usage.CacheMissTokens)/1e6)*rule.CacheMissCNYPerMillion +
				(float64(usage.CompletionTokens)/1e6)*rule.OutputCNYPerMillion
		} else {
			priceStatus = "missing"
		}
	}
	first := meta.firstByte.Load()
	if first == 0 {
		first = time.Since(meta.started).Milliseconds()
	}
	var tenantID, virtualKeyID string
	if meta.principal != nil {
		tenantID = meta.principal.TenantID
		virtualKeyID = meta.principal.ID
	}
	meta.recorder.Record(RequestStats{RequestID: meta.requestID, TenantID: tenantID, VirtualKeyID: virtualKeyID, AccountID: meta.account.ID, Attempts: int(meta.attempts.Load()), Model: model, Path: meta.path, Status: status, DurationMS: time.Since(meta.started).Milliseconds(), FirstByteMS: first, Usage: usage, UsageStatus: usageStatus, PriceRuleID: priceRuleID, PriceStatus: priceStatus, EstimatedCostCNY: cost, CreatedAt: meta.started})
	if meta.keys != nil && meta.principal != nil {
		meta.keys.RecordUsage(meta.principal.ID, usage.TotalTokens, cost, time.Now())
	}
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/setup" {
		s.handleAdminSetup(w, r)
		return
	}
	if !s.authenticateAdmin(r) {
		writeJSON(w, 401, map[string]any{"error": "admin authentication required"})
		return
	}
	if r.URL.Path == "/admin/admin-key" {
		s.handleAdminKeyRotation(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/security") {
		s.handleSecurity(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/prices") {
		s.handlePrices(w, r)
		return
	}
	if r.URL.Path == "/admin/client-config" {
		s.handleClientConfig(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/accounts") {
		s.handleAdminAccounts(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/usage/") {
		s.handleUsageSummary(w, r)
		return
	}
	if r.URL.Path == "/admin/usage" && r.Method == http.MethodGet {
		if s.config.DB == nil {
			writeJSON(w, http.StatusOK, s.recorder.Snapshot().LastRequests)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		start, end, rangeErr := usageRange(r.URL.Query().Get("start"), r.URL.Query().Get("end"), time.Now())
		if rangeErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": rangeErr.Error()})
			return
		}
		events, err := queryUsage(s.config.DB, UsageFilter{TenantID: r.URL.Query().Get("tenant_id"), VirtualKeyID: r.URL.Query().Get("virtual_key_id"), AccountID: r.URL.Query().Get("account_id"), Model: r.URL.Query().Get("model"), Limit: limit, StartAt: start, EndAt: end})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "query usage failed"})
			return
		}
		writeJSON(w, http.StatusOK, events)
		return
	}
	if r.URL.Path == "/admin/balance-history" && r.Method == http.MethodGet {
		if s.config.DB == nil {
			writeJSON(w, http.StatusOK, []BalanceSnapshot{})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		history, err := queryBalanceHistory(s.config.DB, r.URL.Query().Get("account_id"), limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "query balance history failed"})
			return
		}
		writeJSON(w, http.StatusOK, history)
		return
	}
	if r.URL.Path == "/admin/virtual-keys" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.keys.List())
		return
	}
	if r.URL.Path == "/admin/virtual-keys" && r.Method == http.MethodPost {
		var input struct {
			Name     string      `json:"name"`
			TenantID string      `json:"tenant_id"`
			Quota    QuotaPolicy `json:"quota"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		view, secret, err := s.keys.CreateWithQuota(input.Name, input.TenantID, input.Quota)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"key": view, "secret": secret})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/virtual-keys/") && r.Method == http.MethodPut {
		id := strings.TrimPrefix(r.URL.Path, "/admin/virtual-keys/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		var input struct {
			Name     string      `json:"name"`
			TenantID string      `json:"tenant_id"`
			Quota    QuotaPolicy `json:"quota"`
			Enabled  bool        `json:"enabled"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		view, err := s.keys.Update(id, input.Name, input.TenantID, input.Quota, input.Enabled)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/virtual-keys/") && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rotate") {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/virtual-keys/"), "/rotate")
		view, secret, err := s.keys.Rotate(id)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"key": view, "secret": secret})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/virtual-keys/") && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/revoke") {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/virtual-keys/"), "/revoke")
		if !s.keys.Revoke(id) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "virtual key not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "revoked": true})
		return
	}
	switch r.URL.Path {
	case "/admin/stats":
		writeJSON(w, 200, s.recorder.Snapshot())
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) StartBalancePoller(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	s.PollBalancesOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.PollBalancesOnce(ctx)
		}
	}
}

func (s *Server) PollBalancesOnce(ctx context.Context) {
	for _, account := range s.accountsSnapshot() {
		if account == nil || account.Disabled || account.APIKey == "" {
			continue
		}
		s.pollAccountBalance(ctx, account)
	}
}

func (s *Server) pollAccountBalance(ctx context.Context, account *Account) {
	target, err := url.Parse(account.BaseURL)
	if err != nil {
		account.setBalance(false, nil, fmt.Errorf("invalid base_url: %w", err))
		return
	}
	target.Path = joinPath(target.Path, "/user/balance")
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		account.setBalance(false, nil, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+account.APIKey)
	resp, err := (&http.Client{Transport: s.transport}).Do(req)
	if err != nil {
		account.setBalance(false, nil, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		account.markStatus(resp.StatusCode)
		account.setBalance(false, nil, fmt.Errorf("balance endpoint returned HTTP %d", resp.StatusCode))
		return
	}
	var payload struct {
		IsAvailable  bool          `json:"is_available"`
		BalanceInfos []BalanceInfo `json:"balance_infos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		account.setBalance(false, nil, fmt.Errorf("decode balance response: %w", err))
		return
	}
	account.markStatus(resp.StatusCode)
	account.setBalance(payload.IsAvailable, payload.BalanceInfos, nil)
	if s.config.DB != nil {
		if err := persistBalance(s.config.DB, account.ID, payload.IsAvailable, payload.BalanceInfos, time.Now()); err != nil {
			log.Printf("persist balance snapshot: %v", err)
		}
	}
}

func (s *Server) writeMetrics(w http.ResponseWriter) {
	snapshot := s.recorder.Snapshot()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "deepseek_proxy_requests_total %d\n", snapshot.Requests)
	fmt.Fprintf(w, "deepseek_proxy_successes_total %d\n", snapshot.Successes)
	fmt.Fprintf(w, "deepseek_proxy_errors_total %d\n", snapshot.Errors)
	fmt.Fprintf(w, "deepseek_proxy_tokens_total %d\n", snapshot.TotalTokens)
	fmt.Fprintf(w, "deepseek_proxy_estimated_cost_cny_total %.8f\n", snapshot.EstimatedCostCNY)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b[:])
}
