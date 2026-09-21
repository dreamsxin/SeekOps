package proxy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	pricingTierPeak    = "peak"
	pricingTierOffPeak = "off_peak"
)

// beijing is the fixed UTC+8 offset DeepSeek uses to define its peak window.
// Mainland China has observed no DST since 1991, so a fixed zone avoids relying
// on the host tzdata.
var beijing = time.FixedZone("UTC+8", 8*60*60)

// isPeakPricing reports whether an instant falls inside the DeepSeek peak
// window: Monday to Friday 09:00-12:00 and 14:00-18:00 Beijing time. Everything
// else is off-peak and billed at half the peak rate.
func isPeakPricing(at time.Time) bool {
	local := at.In(beijing)
	if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
		return false
	}
	minutes := local.Hour()*60 + local.Minute()
	return (minutes >= 9*60 && minutes < 12*60) || (minutes >= 14*60 && minutes < 18*60)
}

// PriceRule carries the off-peak rates in its base fields; the peak rates are
// optional so rules written before peak pricing existed keep their cost.
type PriceRule struct {
	ID                         string    `json:"id"`
	Model                      string    `json:"model"`
	CacheHitCNYPerMillion      float64   `json:"cache_hit_cny_per_million"`
	CacheMissCNYPerMillion     float64   `json:"cache_miss_cny_per_million"`
	OutputCNYPerMillion        float64   `json:"output_cny_per_million"`
	PeakCacheHitCNYPerMillion  *float64  `json:"peak_cache_hit_cny_per_million,omitempty"`
	PeakCacheMissCNYPerMillion *float64  `json:"peak_cache_miss_cny_per_million,omitempty"`
	PeakOutputCNYPerMillion    *float64  `json:"peak_output_cny_per_million,omitempty"`
	EffectiveAt                time.Time `json:"effective_at"`
	CreatedAt                  time.Time `json:"created_at"`
}

// RatesAt resolves the rates that apply at the given instant and reports which
// tier was used, so a ledger entry can be reconciled against the invoice.
func (r PriceRule) RatesAt(at time.Time) (hit, miss, output float64, tier string) {
	hit, miss, output = r.CacheHitCNYPerMillion, r.CacheMissCNYPerMillion, r.OutputCNYPerMillion
	if !isPeakPricing(at) {
		return hit, miss, output, pricingTierOffPeak
	}
	if r.PeakCacheHitCNYPerMillion != nil {
		hit = *r.PeakCacheHitCNYPerMillion
	}
	if r.PeakCacheMissCNYPerMillion != nil {
		miss = *r.PeakCacheMissCNYPerMillion
	}
	if r.PeakOutputCNYPerMillion != nil {
		output = *r.PeakOutputCNYPerMillion
	}
	return hit, miss, output, pricingTierPeak
}

// PriceDefaults seeds the first price rule when no rule exists yet.
type PriceDefaults struct {
	CacheHit      float64
	CacheMiss     float64
	Output        float64
	PeakCacheHit  float64
	PeakCacheMiss float64
	PeakOutput    float64
}

type PriceStore struct {
	mu    sync.RWMutex
	rules []PriceRule
	db    *sql.DB
}

func NewPriceStore(db *sql.DB, defaults PriceDefaults) (*PriceStore, error) {
	store := &PriceStore{db: db, rules: []PriceRule{}}
	if db != nil {
		rows, err := db.Query(`SELECT id, model, cache_hit_cny_per_million, cache_miss_cny_per_million,
			output_cny_per_million, peak_cache_hit_cny_per_million, peak_cache_miss_cny_per_million,
			peak_output_cny_per_million, effective_at, created_at FROM price_rules ORDER BY effective_at DESC, created_at DESC`)
		if err != nil {
			return nil, fmt.Errorf("load price rules: %w", err)
		}
		for rows.Next() {
			var rule PriceRule
			var effectiveAt, createdAt string
			var peakHit, peakMiss, peakOutput sql.NullFloat64
			if err := rows.Scan(&rule.ID, &rule.Model, &rule.CacheHitCNYPerMillion, &rule.CacheMissCNYPerMillion,
				&rule.OutputCNYPerMillion, &peakHit, &peakMiss, &peakOutput, &effectiveAt, &createdAt); err != nil {
				return nil, fmt.Errorf("scan price rule: %w", err)
			}
			rule.PeakCacheHitCNYPerMillion = nullableFloat(peakHit)
			rule.PeakCacheMissCNYPerMillion = nullableFloat(peakMiss)
			rule.PeakOutputCNYPerMillion = nullableFloat(peakOutput)
			rule.EffectiveAt, _ = time.Parse(time.RFC3339Nano, effectiveAt)
			rule.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
			store.rules = append(store.rules, rule)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("load price rules: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("load price rules: %w", err)
		}
	}
	if len(store.rules) == 0 {
		seed := PriceRule{
			ID: "price-default", Model: "*", CacheHitCNYPerMillion: defaults.CacheHit,
			CacheMissCNYPerMillion: defaults.CacheMiss, OutputCNYPerMillion: defaults.Output,
			PeakCacheHitCNYPerMillion:  positiveFloat(defaults.PeakCacheHit),
			PeakCacheMissCNYPerMillion: positiveFloat(defaults.PeakCacheMiss),
			PeakOutputCNYPerMillion:    positiveFloat(defaults.PeakOutput),
			EffectiveAt:                time.Unix(0, 0).UTC(), CreatedAt: time.Now().UTC(),
		}
		if db != nil {
			if err := persistPriceRule(db, seed); err != nil {
				return nil, fmt.Errorf("seed default price rule: %w", err)
			}
		}
		store.rules = append(store.rules, seed)
	}
	store.sortLocked()
	return store, nil
}

func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	rate := value.Float64
	return &rate
}

func positiveFloat(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	return &value
}


func (s *PriceStore) List() []PriceRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]PriceRule(nil), s.rules...)
}

func (s *PriceStore) Resolve(model string, at time.Time) (PriceRule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	model = strings.TrimSpace(model)
	for _, candidateModel := range []string{model, "*"} {
		if candidateModel == "" {
			continue
		}
		for _, rule := range s.rules {
			if rule.Model == candidateModel && !rule.EffectiveAt.After(at) {
				return rule, true
			}
		}
	}
	return PriceRule{}, false
}

func (s *PriceStore) Create(rule PriceRule) (PriceRule, error) {
	rule.Model = strings.TrimSpace(rule.Model)
	if rule.Model == "" {
		return PriceRule{}, fmt.Errorf("model is required; use * for the default price")
	}
	if len(rule.Model) > 200 {
		return PriceRule{}, fmt.Errorf("model must not exceed 200 characters")
	}
	if rule.CacheHitCNYPerMillion < 0 || rule.CacheMissCNYPerMillion < 0 || rule.OutputCNYPerMillion < 0 {
		return PriceRule{}, fmt.Errorf("price values must not be negative")
	}
	for _, peak := range []*float64{rule.PeakCacheHitCNYPerMillion, rule.PeakCacheMissCNYPerMillion, rule.PeakOutputCNYPerMillion} {
		if peak != nil && *peak < 0 {
			return PriceRule{}, fmt.Errorf("peak price values must not be negative")
		}
	}
	if rule.EffectiveAt.IsZero() {
		rule.EffectiveAt = time.Now().UTC()
	} else {
		rule.EffectiveAt = rule.EffectiveAt.UTC()
	}
	rule.ID = "price-" + newID()[:16]
	rule.CreatedAt = time.Now().UTC()
	if s.db != nil {
		if err := persistPriceRule(s.db, rule); err != nil {
			return PriceRule{}, err
		}
	}
	s.mu.Lock()
	s.rules = append(s.rules, rule)
	s.sortLocked()
	s.mu.Unlock()
	return rule, nil
}

func (s *PriceStore) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, rule := range s.rules {
		if rule.ID != id {
			continue
		}
		if s.db != nil {
			if _, err := s.db.Exec(`DELETE FROM price_rules WHERE id = ?`, id); err != nil {
				return false, err
			}
		}
		s.rules = append(s.rules[:index], s.rules[index+1:]...)
		return true, nil
	}
	return false, nil
}

func (s *PriceStore) sortLocked() {
	sort.Slice(s.rules, func(i, j int) bool {
		if s.rules[i].EffectiveAt.Equal(s.rules[j].EffectiveAt) {
			return s.rules[i].CreatedAt.After(s.rules[j].CreatedAt)
		}
		return s.rules[i].EffectiveAt.After(s.rules[j].EffectiveAt)
	})
}

func persistPriceRule(db *sql.DB, rule PriceRule) error {
	_, err := db.Exec(`INSERT INTO price_rules
		(id, model, cache_hit_cny_per_million, cache_miss_cny_per_million, output_cny_per_million,
		 peak_cache_hit_cny_per_million, peak_cache_miss_cny_per_million, peak_output_cny_per_million,
		 effective_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rule.ID, rule.Model, rule.CacheHitCNYPerMillion,
		rule.CacheMissCNYPerMillion, rule.OutputCNYPerMillion,
		floatOrNull(rule.PeakCacheHitCNYPerMillion), floatOrNull(rule.PeakCacheMissCNYPerMillion),
		floatOrNull(rule.PeakOutputCNYPerMillion),
		rule.EffectiveAt.UTC().Format(time.RFC3339Nano), rule.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func floatOrNull(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func (s *Server) handlePrices(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/prices" {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, s.prices.List())
		case http.MethodPost:
			var input PriceRule
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid price payload"})
				return
			}
			rule, err := s.prices.Create(input)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			if s.audit != nil {
				s.audit.Record("price_rule_created", "price_rule", rule.ID, "创建模型价格规则", map[string]any{
					"model": rule.Model, "cache_hit_cny_per_million": rule.CacheHitCNYPerMillion,
					"cache_miss_cny_per_million": rule.CacheMissCNYPerMillion, "output_cny_per_million": rule.OutputCNYPerMillion,
					"peak_cache_hit_cny_per_million":  floatOrNull(rule.PeakCacheHitCNYPerMillion),
					"peak_cache_miss_cny_per_million": floatOrNull(rule.PeakCacheMissCNYPerMillion),
					"peak_output_cny_per_million":     floatOrNull(rule.PeakOutputCNYPerMillion),
					"effective_at":                    rule.EffectiveAt,
				}, time.Now())
			}
			writeJSON(w, http.StatusCreated, rule)
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/admin/prices/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	deleted, err := s.prices.Delete(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "delete price rule failed"})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "price rule not found"})
		return
	}
	if s.audit != nil {
		s.audit.Record("price_rule_deleted", "price_rule", id, "删除模型价格规则", nil, time.Now())
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}
