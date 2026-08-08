package proxy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	alertStatusOpen         = "open"
	alertStatusAcknowledged = "acknowledged"
	alertStatusSilenced     = "silenced"
	alertStatusResolved     = "resolved"
)

type AlertSettings struct {
	BalanceThresholdCNY       float64 `json:"balance_threshold_cny"`
	QuotaWarningPercent       float64 `json:"quota_warning_percent"`
	ErrorRateThresholdPercent float64 `json:"error_rate_threshold_percent"`
	ErrorRateMinRequests      int     `json:"error_rate_min_requests"`
	ErrorRateWindowMinutes    int     `json:"error_rate_window_minutes"`
	SilenceMinutes            int     `json:"silence_minutes"`
}

type Alert struct {
	ID             string    `json:"id"`
	SourceKey      string    `json:"source_key"`
	Type           string    `json:"type"`
	ScopeType      string    `json:"scope_type"`
	ScopeID        string    `json:"scope_id"`
	Severity       string    `json:"severity"`
	Title          string    `json:"title"`
	Message        string    `json:"message"`
	Status         string    `json:"status"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	AcknowledgedAt time.Time `json:"acknowledged_at,omitempty"`
	SilencedUntil  time.Time `json:"silenced_until,omitempty"`
	ResolvedAt     time.Time `json:"resolved_at,omitempty"`
}

type recentOutcome struct {
	at    time.Time
	error bool
}

type AlertStore struct {
	mu            sync.Mutex
	db            *sql.DB
	settings      AlertSettings
	byID          map[string]*Alert
	bySource      map[string]*Alert
	outcomes      []recentOutcome
	outcomeStart  int
	outcomeErrors int
}

func defaultAlertSettings() AlertSettings {
	return AlertSettings{BalanceThresholdCNY: 10, QuotaWarningPercent: 80, ErrorRateThresholdPercent: 20, ErrorRateMinRequests: 10, ErrorRateWindowMinutes: 15, SilenceMinutes: 60}
}

func NewAlertStore(db *sql.DB) (*AlertStore, error) {
	store := &AlertStore{db: db, settings: defaultAlertSettings(), byID: make(map[string]*Alert), bySource: make(map[string]*Alert)}
	if db == nil {
		return store, nil
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *AlertStore) load() error {
	row := s.db.QueryRow(`SELECT balance_threshold_cny, quota_warning_percent, error_rate_threshold_percent,
		error_rate_min_requests, error_rate_window_minutes, silence_minutes FROM alert_settings WHERE id = 1`)
	if err := row.Scan(&s.settings.BalanceThresholdCNY, &s.settings.QuotaWarningPercent, &s.settings.ErrorRateThresholdPercent,
		&s.settings.ErrorRateMinRequests, &s.settings.ErrorRateWindowMinutes, &s.settings.SilenceMinutes); err != nil {
		if err != sql.ErrNoRows {
			return fmt.Errorf("load alert settings: %w", err)
		}
		if err := s.persistSettingsLocked(); err != nil {
			return fmt.Errorf("create alert settings: %w", err)
		}
	}
	rows, err := s.db.Query(`SELECT id, source_key, type, scope_type, scope_id, severity, title, message, status,
		first_seen_at, last_seen_at, acknowledged_at, silenced_until, resolved_at FROM alerts`)
	if err != nil {
		return fmt.Errorf("load alerts: %w", err)
	}
	for rows.Next() {
		var item Alert
		var first, last, acknowledged, silenced, resolved string
		if err := rows.Scan(&item.ID, &item.SourceKey, &item.Type, &item.ScopeType, &item.ScopeID, &item.Severity,
			&item.Title, &item.Message, &item.Status, &first, &last, &acknowledged, &silenced, &resolved); err != nil {
			rows.Close()
			return err
		}
		item.FirstSeenAt = parseAlertTime(first)
		item.LastSeenAt = parseAlertTime(last)
		item.AcknowledgedAt = parseAlertTime(acknowledged)
		item.SilencedUntil = parseAlertTime(silenced)
		item.ResolvedAt = parseAlertTime(resolved)
		s.byID[item.ID] = &item
		s.bySource[item.SourceKey] = &item
	}
	if err := rows.Close(); err != nil {
		return err
	}
	cutoff := time.Now().Add(-time.Duration(s.settings.ErrorRateWindowMinutes) * time.Minute).UTC().Format(time.RFC3339Nano)
	rows, err = s.db.Query(`SELECT status, created_at FROM usage_events WHERE created_at >= ? ORDER BY created_at`, cutoff)
	if err != nil {
		return fmt.Errorf("load recent alert outcomes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status int
		var created string
		if err := rows.Scan(&status, &created); err != nil {
			return err
		}
		failed := status < 200 || status >= 400
		s.outcomes = append(s.outcomes, recentOutcome{at: parseAlertTime(created), error: failed})
		if failed {
			s.outcomeErrors++
		}
	}
	return rows.Err()
}

func parseAlertTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func alertTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (s *AlertStore) Settings() AlertSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *AlertStore) UpdateSettings(settings AlertSettings, now time.Time) (AlertSettings, error) {
	if settings.BalanceThresholdCNY < 0 {
		return AlertSettings{}, fmt.Errorf("balance_threshold_cny must not be negative")
	}
	if settings.QuotaWarningPercent <= 0 || settings.QuotaWarningPercent >= 100 {
		return AlertSettings{}, fmt.Errorf("quota_warning_percent must be between 0 and 100")
	}
	if settings.ErrorRateThresholdPercent <= 0 || settings.ErrorRateThresholdPercent > 100 {
		return AlertSettings{}, fmt.Errorf("error_rate_threshold_percent must be between 0 and 100")
	}
	if settings.ErrorRateMinRequests < 1 || settings.ErrorRateMinRequests > 10000 {
		return AlertSettings{}, fmt.Errorf("error_rate_min_requests must be between 1 and 10000")
	}
	if settings.ErrorRateWindowMinutes < 1 || settings.ErrorRateWindowMinutes > 1440 {
		return AlertSettings{}, fmt.Errorf("error_rate_window_minutes must be between 1 and 1440")
	}
	if settings.SilenceMinutes < 1 || settings.SilenceMinutes > 10080 {
		return AlertSettings{}, fmt.Errorf("silence_minutes must be between 1 and 10080")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.settings
	s.settings = settings
	if err := s.persistSettingsLocked(); err != nil {
		s.settings = previous
		return AlertSettings{}, err
	}
	s.evaluateErrorRateLocked(now)
	return s.settings, nil
}

func (s *AlertStore) persistSettingsLocked() error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO alert_settings (id, balance_threshold_cny, quota_warning_percent,
		error_rate_threshold_percent, error_rate_min_requests, error_rate_window_minutes, silence_minutes, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET
		balance_threshold_cny=excluded.balance_threshold_cny, quota_warning_percent=excluded.quota_warning_percent,
		error_rate_threshold_percent=excluded.error_rate_threshold_percent, error_rate_min_requests=excluded.error_rate_min_requests,
		error_rate_window_minutes=excluded.error_rate_window_minutes, silence_minutes=excluded.silence_minutes,
		updated_at=excluded.updated_at`, s.settings.BalanceThresholdCNY, s.settings.QuotaWarningPercent,
		s.settings.ErrorRateThresholdPercent, s.settings.ErrorRateMinRequests, s.settings.ErrorRateWindowMinutes,
		s.settings.SilenceMinutes, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *AlertStore) List(now time.Time) []Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireSilencesLocked(now)
	s.evaluateErrorRateLocked(now)
	result := make([]Alert, 0, len(s.byID))
	for _, item := range s.byID {
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := alertStatusRank(result[i].Status), alertStatusRank(result[j].Status)
		if left != right {
			return left < right
		}
		return result[i].LastSeenAt.After(result[j].LastSeenAt)
	})
	return result
}

func alertStatusRank(status string) int {
	switch status {
	case alertStatusOpen:
		return 0
	case alertStatusAcknowledged:
		return 1
	case alertStatusSilenced:
		return 2
	default:
		return 3
	}
}

func (s *AlertStore) Raise(sourceKey, alertType, scopeType, scopeID, severity, title, message string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raiseLocked(sourceKey, alertType, scopeType, scopeID, severity, title, message, now)
}

func (s *AlertStore) raiseLocked(sourceKey, alertType, scopeType, scopeID, severity, title, message string, now time.Time) {
	now = now.UTC()
	item, exists := s.bySource[sourceKey]
	if !exists {
		item = &Alert{ID: "alert-" + newID(), SourceKey: sourceKey, Type: alertType, ScopeType: scopeType, ScopeID: scopeID,
			Severity: severity, Title: title, Message: message, Status: alertStatusOpen, FirstSeenAt: now, LastSeenAt: now}
		s.byID[item.ID] = item
		s.bySource[sourceKey] = item
		s.persistAlertBestEffortLocked(item)
		return
	}
	previousLastSeen := item.LastSeenAt
	shouldPersist := item.Status == alertStatusResolved || (item.Severity != "critical" && severity == "critical")
	if item.Status == alertStatusResolved {
		item.Status = alertStatusOpen
		item.FirstSeenAt = now
		item.AcknowledgedAt = time.Time{}
		item.SilencedUntil = time.Time{}
		item.ResolvedAt = time.Time{}
	}
	if item.Status == alertStatusSilenced && !item.SilencedUntil.After(now) {
		item.Status = alertStatusOpen
		item.SilencedUntil = time.Time{}
		shouldPersist = true
	}
	if item.Severity != "critical" && severity == "critical" {
		item.Status = alertStatusOpen
		item.AcknowledgedAt = time.Time{}
		item.SilencedUntil = time.Time{}
	}
	item.Type, item.ScopeType, item.ScopeID = alertType, scopeType, scopeID
	item.Severity, item.Title, item.Message, item.LastSeenAt = severity, title, message, now
	if shouldPersist || now.Sub(previousLastSeen) >= time.Minute {
		s.persistAlertBestEffortLocked(item)
	}
}

func (s *AlertStore) Resolve(sourceKey string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolveLocked(sourceKey, now)
}

func (s *AlertStore) resolveLocked(sourceKey string, now time.Time) {
	item, ok := s.bySource[sourceKey]
	if !ok || item.Status == alertStatusResolved {
		return
	}
	item.Status = alertStatusResolved
	item.ResolvedAt = now.UTC()
	item.SilencedUntil = time.Time{}
	s.persistAlertBestEffortLocked(item)
}

func (s *AlertStore) ResolveScope(scopeType, scopeID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.byID {
		if item.ScopeType == scopeType && item.ScopeID == scopeID {
			s.resolveLocked(item.SourceKey, now)
		}
	}
}

func (s *AlertStore) Acknowledge(id string, now time.Time) (Alert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.byID[id]
	if !ok {
		return Alert{}, fmt.Errorf("alert not found")
	}
	if item.Status == alertStatusResolved {
		return Alert{}, fmt.Errorf("resolved alert cannot be acknowledged")
	}
	previous := *item
	item.Status = alertStatusAcknowledged
	item.AcknowledgedAt = now.UTC()
	item.SilencedUntil = time.Time{}
	if err := s.persistAlertLocked(item); err != nil {
		*item = previous
		return Alert{}, err
	}
	return *item, nil
}

func (s *AlertStore) Silence(id string, duration time.Duration, now time.Time) (Alert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.byID[id]
	if !ok {
		return Alert{}, fmt.Errorf("alert not found")
	}
	if item.Status == alertStatusResolved {
		return Alert{}, fmt.Errorf("resolved alert cannot be silenced")
	}
	if duration <= 0 {
		duration = time.Duration(s.settings.SilenceMinutes) * time.Minute
	}
	if duration > 7*24*time.Hour {
		return Alert{}, fmt.Errorf("silence duration must not exceed 7 days")
	}
	previous := *item
	item.Status = alertStatusSilenced
	item.SilencedUntil = now.UTC().Add(duration)
	if err := s.persistAlertLocked(item); err != nil {
		*item = previous
		return Alert{}, err
	}
	return *item, nil
}

func (s *AlertStore) ResolveByID(id string, now time.Time) (Alert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.byID[id]
	if !ok {
		return Alert{}, fmt.Errorf("alert not found")
	}
	if item.Status == alertStatusResolved {
		return *item, nil
	}
	previous := *item
	item.Status = alertStatusResolved
	item.ResolvedAt = now.UTC()
	item.SilencedUntil = time.Time{}
	if err := s.persistAlertLocked(item); err != nil {
		*item = previous
		return Alert{}, err
	}
	return *item, nil
}

func (s *AlertStore) expireSilencesLocked(now time.Time) {
	for _, item := range s.byID {
		if item.Status == alertStatusSilenced && !item.SilencedUntil.After(now) {
			item.Status = alertStatusOpen
			item.SilencedUntil = time.Time{}
			s.persistAlertBestEffortLocked(item)
		}
	}
}

func (s *AlertStore) persistAlertLocked(item *Alert) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO alerts (id, source_key, type, scope_type, scope_id, severity, title, message, status,
		first_seen_at, last_seen_at, acknowledged_at, silenced_until, resolved_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_key) DO UPDATE SET type=excluded.type, scope_type=excluded.scope_type, scope_id=excluded.scope_id,
		severity=excluded.severity, title=excluded.title, message=excluded.message, status=excluded.status,
		first_seen_at=excluded.first_seen_at, last_seen_at=excluded.last_seen_at, acknowledged_at=excluded.acknowledged_at,
		silenced_until=excluded.silenced_until, resolved_at=excluded.resolved_at`, item.ID, item.SourceKey, item.Type,
		item.ScopeType, item.ScopeID, item.Severity, item.Title, item.Message, item.Status, alertTime(item.FirstSeenAt),
		alertTime(item.LastSeenAt), alertTime(item.AcknowledgedAt), alertTime(item.SilencedUntil), alertTime(item.ResolvedAt))
	return err
}

func (s *AlertStore) persistAlertBestEffortLocked(item *Alert) {
	if err := s.persistAlertLocked(item); err != nil {
		log.Printf("persist alert %s: %v", item.SourceKey, err)
	}
}

func (s *AlertStore) EvaluateAccount(account AccountView, now time.Time) {
	checkSource := "account_check:" + account.ID
	balanceSource := "low_balance:" + account.ID
	if !account.Enabled {
		s.Resolve(checkSource, now)
		s.Resolve(balanceSource, now)
		return
	}
	if account.BalanceError != "" {
		s.Raise(checkSource, "account_check", "account", account.ID, "critical", account.Name+" 检测失败", account.BalanceError, now)
	} else if !account.BalanceUpdatedAt.IsZero() && !account.BalanceAvailable {
		s.Raise(checkSource, "account_check", "account", account.ID, "warning", account.Name+" 余额不可用", "上游余额接口返回不可用状态", now)
	} else if !account.BalanceUpdatedAt.IsZero() {
		s.Resolve(checkSource, now)
	}
	threshold := s.Settings().BalanceThresholdCNY
	for _, balance := range account.Balances {
		if !strings.EqualFold(balance.Currency, "CNY") {
			continue
		}
		value, err := strconv.ParseFloat(balance.TotalBalance, 64)
		if err != nil {
			break
		}
		if account.BalanceAvailable && value <= threshold {
			severity := "warning"
			if value <= 0 {
				severity = "critical"
			}
			s.Raise(balanceSource, "low_balance", "account", account.ID, severity, account.Name+" 余额不足",
				fmt.Sprintf("CNY 余额 %.2f，已低于 %.2f 的告警阈值", value, threshold), now)
			return
		}
		break
	}
	s.Resolve(balanceSource, now)
}

func (s *AlertStore) EvaluateQuota(key VirtualKeyView, now time.Time) {
	s.evaluateQuotaValue("quota_tokens:"+key.ID, key, "每日 Token", float64(key.Usage.DailyTokens), float64(key.Quota.DailyTokens), now)
	s.evaluateQuotaValue("quota_cost:"+key.ID, key, "每日费用", key.Usage.DailyCostCNY, key.Quota.DailyCostCNY, now)
}

func (s *AlertStore) evaluateQuotaValue(source string, key VirtualKeyView, label string, used, limit float64, now time.Time) {
	settings := s.Settings()
	if !key.Enabled || limit <= 0 {
		s.Resolve(source, now)
		return
	}
	percent := used / limit * 100
	if percent < settings.QuotaWarningPercent {
		s.Resolve(source, now)
		return
	}
	severity := "warning"
	if percent >= 100 {
		severity = "critical"
	}
	s.Raise(source, "quota", "virtual_key", key.ID, severity, key.Name+" 配额接近上限",
		fmt.Sprintf("%s已使用 %.1f%%（%.2f / %.2f）", label, percent, used, limit), now)
}

func (s *AlertStore) RecordRequest(event RequestStats, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	failed := event.Status < 200 || event.Status >= 400
	s.outcomes = append(s.outcomes, recentOutcome{at: now.UTC(), error: failed})
	if failed {
		s.outcomeErrors++
	}
	s.evaluateErrorRateLocked(now)
}

func (s *AlertStore) evaluateErrorRateLocked(now time.Time) {
	cutoff := now.Add(-time.Duration(s.settings.ErrorRateWindowMinutes) * time.Minute)
	for s.outcomeStart < len(s.outcomes) && s.outcomes[s.outcomeStart].at.Before(cutoff) {
		if s.outcomes[s.outcomeStart].error {
			s.outcomeErrors--
		}
		s.outcomeStart++
	}
	if s.outcomeStart >= 1024 && s.outcomeStart*2 >= len(s.outcomes) {
		s.outcomes = append([]recentOutcome(nil), s.outcomes[s.outcomeStart:]...)
		s.outcomeStart = 0
	}
	count := len(s.outcomes) - s.outcomeStart
	source := "error_rate:global"
	if count < s.settings.ErrorRateMinRequests {
		s.resolveLocked(source, now)
		return
	}
	percent := float64(s.outcomeErrors) / float64(count) * 100
	if percent < s.settings.ErrorRateThresholdPercent {
		s.resolveLocked(source, now)
		return
	}
	severity := "warning"
	if percent >= 50 {
		severity = "critical"
	}
	s.raiseLocked(source, "error_rate", "platform", "proxy", severity, "近期请求错误率升高",
		fmt.Sprintf("最近 %d 分钟 %d 次请求中有 %d 次失败，错误率 %.1f%%", s.settings.ErrorRateWindowMinutes, count, s.outcomeErrors, percent), now)
}

func (s *Server) refreshAlerts(now time.Time) {
	if s.alerts == nil {
		return
	}
	for _, account := range s.accountsSnapshot() {
		if account != nil {
			s.alerts.EvaluateAccount(accountView(account), now)
		}
	}
	for _, key := range s.keys.List() {
		s.alerts.EvaluateQuota(key, now)
	}
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/alerts/settings" {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, s.alerts.Settings())
		case http.MethodPut:
			var input AlertSettings
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid alert settings"})
				return
			}
			settings, err := s.alerts.UpdateSettings(input, time.Now())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			s.refreshAlerts(time.Now())
			writeJSON(w, http.StatusOK, settings)
		default:
			w.Header().Set("Allow", "GET, PUT")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if r.URL.Path == "/admin/alerts" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		now := time.Now()
		s.refreshAlerts(now)
		writeJSON(w, http.StatusOK, s.alerts.List(now))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin/alerts/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	var item Alert
	var err error
	switch parts[1] {
	case "acknowledge":
		item, err = s.alerts.Acknowledge(parts[0], time.Now())
	case "silence":
		var input struct {
			Minutes int `json:"minutes"`
		}
		decodeErr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input)
		if decodeErr != nil && decodeErr != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid silence duration"})
			return
		}
		item, err = s.alerts.Silence(parts[0], time.Duration(input.Minutes)*time.Minute, time.Now())
	case "resolve":
		item, err = s.alerts.ResolveByID(parts[0], time.Now())
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, item)
}
