package proxy

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type UsageSummary struct {
	Start            time.Time        `json:"start"`
	End              time.Time        `json:"end"`
	Requests         int64            `json:"requests"`
	Successes        int64            `json:"successes"`
	Errors           int64            `json:"errors"`
	TotalTokens      int64            `json:"total_tokens"`
	PromptTokens     int64            `json:"prompt_tokens"`
	CompletionTokens int64            `json:"completion_tokens"`
	CacheHitTokens   int64            `json:"cache_hit_tokens"`
	CacheMissTokens  int64            `json:"cache_miss_tokens"`
	EstimatedCostCNY float64          `json:"estimated_cost_cny"`
	UnpricedRequests int64            `json:"unpriced_requests"`
	Daily            []UsageBucket    `json:"daily"`
	ByTenant         []UsageBreakdown `json:"by_tenant"`
	ByVirtualKey     []UsageBreakdown `json:"by_virtual_key"`
	ByModel          []UsageBreakdown `json:"by_model"`
	ByAccount        []UsageBreakdown `json:"by_account"`
}

type UsageBucket struct {
	Date             string  `json:"date"`
	Requests         int64   `json:"requests"`
	Successes        int64   `json:"successes"`
	Errors           int64   `json:"errors"`
	TotalTokens      int64   `json:"total_tokens"`
	EstimatedCostCNY float64 `json:"estimated_cost_cny"`
	UnpricedRequests int64   `json:"unpriced_requests"`
}

type UsageBreakdown struct {
	ID               string  `json:"id"`
	Requests         int64   `json:"requests"`
	Successes        int64   `json:"successes"`
	Errors           int64   `json:"errors"`
	TotalTokens      int64   `json:"total_tokens"`
	EstimatedCostCNY float64 `json:"estimated_cost_cny"`
	UnpricedRequests int64   `json:"unpriced_requests"`
}

func parseUsageDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func utcDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func usageRange(startValue, endValue string, now time.Time) (time.Time, time.Time, error) {
	start, err := parseUsageDate(startValue)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start date")
	}
	end, err := parseUsageDate(endValue)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end date")
	}
	if start.IsZero() {
		start = utcDay(now).AddDate(0, 0, -6)
	}
	if end.IsZero() {
		end = utcDay(now).AddDate(0, 0, 1)
	} else if len(strings.TrimSpace(endValue)) == len("2006-01-02") {
		end = end.AddDate(0, 0, 1)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end date must be after start date")
	}
	if end.Sub(start) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("date range must not exceed 366 days")
	}
	return start, end, nil
}

func usageWhere(filter UsageFilter) (string, []any) {
	where := strings.Builder{}
	where.WriteString(" WHERE 1=1")
	args := make([]any, 0, 6)
	if !filter.StartAt.IsZero() {
		where.WriteString(" AND created_at >= ?")
		args = append(args, filter.StartAt.UTC().Format(time.RFC3339Nano))
	}
	if !filter.EndAt.IsZero() {
		where.WriteString(" AND created_at < ?")
		args = append(args, filter.EndAt.UTC().Format(time.RFC3339Nano))
	}
	if filter.TenantID != "" {
		where.WriteString(" AND tenant_id = ?")
		args = append(args, filter.TenantID)
	}
	if filter.VirtualKeyID != "" {
		where.WriteString(" AND virtual_key_id = ?")
		args = append(args, filter.VirtualKeyID)
	}
	if filter.AccountID != "" {
		where.WriteString(" AND account_id = ?")
		args = append(args, filter.AccountID)
	}
	if filter.Model != "" {
		where.WriteString(" AND model = ?")
		args = append(args, filter.Model)
	}
	return where.String(), args
}

func usageSummary(db *sql.DB, filter UsageFilter) (UsageSummary, error) {
	result := UsageSummary{Start: filter.StartAt.UTC(), End: filter.EndAt.UTC(), Daily: []UsageBucket{}, ByTenant: []UsageBreakdown{}, ByVirtualKey: []UsageBreakdown{}, ByModel: []UsageBreakdown{}, ByAccount: []UsageBreakdown{}}
	if db == nil {
		return result, nil
	}
	where, args := usageWhere(filter)
	row := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status >= 200 AND status < 400 THEN 1 ELSE 0 END), 0), COALESCE(SUM(CASE WHEN status < 200 OR status >= 400 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0),
		COALESCE(SUM(cache_hit_tokens), 0), COALESCE(SUM(cache_miss_tokens), 0), COALESCE(SUM(estimated_cost_cny), 0),
		COALESCE(SUM(CASE WHEN price_status IN ('missing', 'usage_missing') THEN 1 ELSE 0 END), 0)
		FROM usage_events`+where, args...)
	if err := row.Scan(&result.Requests, &result.Successes, &result.Errors, &result.TotalTokens, &result.PromptTokens, &result.CompletionTokens, &result.CacheHitTokens, &result.CacheMissTokens, &result.EstimatedCostCNY, &result.UnpricedRequests); err != nil {
		return UsageSummary{}, err
	}
	var err error
	if result.Daily, err = queryUsageBuckets(db, where, args); err != nil {
		return UsageSummary{}, err
	}
	for _, item := range []struct {
		column string
		target *[]UsageBreakdown
	}{{"tenant_id", &result.ByTenant}, {"virtual_key_id", &result.ByVirtualKey}, {"model", &result.ByModel}, {"account_id", &result.ByAccount}} {
		if *item.target, err = queryUsageBreakdowns(db, where, args, item.column); err != nil {
			return UsageSummary{}, err
		}
	}
	return result, nil
}

func queryUsageBuckets(db *sql.DB, where string, args []any) ([]UsageBucket, error) {
	rows, err := db.Query(`SELECT substr(created_at, 1, 10), COUNT(*), COALESCE(SUM(CASE WHEN status >= 200 AND status < 400 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status < 200 OR status >= 400 THEN 1 ELSE 0 END), 0), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(estimated_cost_cny), 0),
		COALESCE(SUM(CASE WHEN price_status IN ('missing', 'usage_missing') THEN 1 ELSE 0 END), 0)
		FROM usage_events`+where+` GROUP BY substr(created_at, 1, 10) ORDER BY substr(created_at, 1, 10)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]UsageBucket, 0)
	for rows.Next() {
		var item UsageBucket
		if err := rows.Scan(&item.Date, &item.Requests, &item.Successes, &item.Errors, &item.TotalTokens, &item.EstimatedCostCNY, &item.UnpricedRequests); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func queryUsageBreakdowns(db *sql.DB, where string, args []any, column string) ([]UsageBreakdown, error) {
	allowed := map[string]bool{"tenant_id": true, "virtual_key_id": true, "model": true, "account_id": true}
	if !allowed[column] {
		return nil, fmt.Errorf("invalid breakdown column")
	}
	rows, err := db.Query(`SELECT `+column+`, COUNT(*), COALESCE(SUM(CASE WHEN status >= 200 AND status < 400 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status < 200 OR status >= 400 THEN 1 ELSE 0 END), 0), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(estimated_cost_cny), 0),
		COALESCE(SUM(CASE WHEN price_status IN ('missing', 'usage_missing') THEN 1 ELSE 0 END), 0)
		FROM usage_events`+where+` GROUP BY `+column+` ORDER BY total_tokens DESC, estimated_cost_cny DESC LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]UsageBreakdown, 0)
	for rows.Next() {
		var item UsageBreakdown
		if err := rows.Scan(&item.ID, &item.Requests, &item.Successes, &item.Errors, &item.TotalTokens, &item.EstimatedCostCNY, &item.UnpricedRequests); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func writeUsageCSV(w http.ResponseWriter, events []RequestStats) error {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="seekops-usage.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(w)
	if err := writer.Write([]string{"created_at", "request_id", "tenant_id", "virtual_key_id", "account_id", "attempts", "model", "path", "status", "duration_ms", "first_byte_ms", "prompt_tokens", "cache_hit_tokens", "cache_miss_tokens", "completion_tokens", "reasoning_tokens", "total_tokens", "usage_status", "price_rule_id", "price_status", "estimated_cost_cny"}); err != nil {
		return err
	}
	for _, event := range events {
		values := []string{event.CreatedAt.UTC().Format(time.RFC3339), csvText(event.RequestID), csvText(event.TenantID), csvText(event.VirtualKeyID), csvText(event.AccountID), strconv.Itoa(event.Attempts), csvText(event.Model), csvText(event.Path), strconv.Itoa(event.Status), strconv.FormatInt(event.DurationMS, 10), strconv.FormatInt(event.FirstByteMS, 10), strconv.FormatInt(event.Usage.PromptTokens, 10), strconv.FormatInt(event.Usage.CacheHitTokens, 10), strconv.FormatInt(event.Usage.CacheMissTokens, 10), strconv.FormatInt(event.Usage.CompletionTokens, 10), strconv.FormatInt(event.Usage.ReasoningTokens, 10), strconv.FormatInt(event.Usage.TotalTokens, 10), csvText(event.UsageStatus), csvText(event.PriceRuleID), csvText(event.PriceStatus), strconv.FormatFloat(event.EstimatedCostCNY, 'f', 8, 64)}
		if err := writer.Write(values); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func csvText(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}

func (s *Server) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	start, end, err := usageRange(r.URL.Query().Get("start"), r.URL.Query().Get("end"), time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	filter := UsageFilter{TenantID: r.URL.Query().Get("tenant_id"), VirtualKeyID: r.URL.Query().Get("virtual_key_id"), AccountID: r.URL.Query().Get("account_id"), Model: r.URL.Query().Get("model"), StartAt: start, EndAt: end}
	if r.URL.Path == "/admin/usage/summary" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if s.config.DB == nil {
			writeJSON(w, http.StatusOK, UsageSummary{Start: start, End: end, Daily: []UsageBucket{}, ByTenant: []UsageBreakdown{}, ByVirtualKey: []UsageBreakdown{}, ByModel: []UsageBreakdown{}, ByAccount: []UsageBreakdown{}})
			return
		}
		summary, err := usageSummary(s.config.DB, filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "query usage summary failed"})
			return
		}
		writeJSON(w, http.StatusOK, summary)
		return
	}
	if r.URL.Path == "/admin/usage/export" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if s.config.DB == nil {
			_ = writeUsageCSV(w, nil)
			return
		}
		filter.Limit = 10000
		events, err := queryUsage(s.config.DB, filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "export usage failed"})
			return
		}
		if err := writeUsageCSV(w, events); err != nil {
			return
		}
		return
	}
}
