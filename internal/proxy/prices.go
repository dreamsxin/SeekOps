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

type PriceRule struct {
	ID                     string    `json:"id"`
	Model                  string    `json:"model"`
	CacheHitCNYPerMillion  float64   `json:"cache_hit_cny_per_million"`
	CacheMissCNYPerMillion float64   `json:"cache_miss_cny_per_million"`
	OutputCNYPerMillion    float64   `json:"output_cny_per_million"`
	EffectiveAt            time.Time `json:"effective_at"`
	CreatedAt              time.Time `json:"created_at"`
}

type PriceStore struct {
	mu    sync.RWMutex
	rules []PriceRule
	db    *sql.DB
}

func NewPriceStore(db *sql.DB, hit, miss, output float64) (*PriceStore, error) {
	store := &PriceStore{db: db, rules: []PriceRule{}}
	if db != nil {
		rows, err := db.Query(`SELECT id, model, cache_hit_cny_per_million, cache_miss_cny_per_million,
			output_cny_per_million, effective_at, created_at FROM price_rules ORDER BY effective_at DESC, created_at DESC`)
		if err != nil {
			return nil, fmt.Errorf("load price rules: %w", err)
		}
		for rows.Next() {
			var rule PriceRule
			var effectiveAt, createdAt string
			if err := rows.Scan(&rule.ID, &rule.Model, &rule.CacheHitCNYPerMillion, &rule.CacheMissCNYPerMillion,
				&rule.OutputCNYPerMillion, &effectiveAt, &createdAt); err != nil {
				return nil, fmt.Errorf("scan price rule: %w", err)
			}
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
			ID: "price-default", Model: "*", CacheHitCNYPerMillion: hit,
			CacheMissCNYPerMillion: miss, OutputCNYPerMillion: output,
			EffectiveAt: time.Unix(0, 0).UTC(), CreatedAt: time.Now().UTC(),
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
		(id, model, cache_hit_cny_per_million, cache_miss_cny_per_million, output_cny_per_million, effective_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, rule.ID, rule.Model, rule.CacheHitCNYPerMillion,
		rule.CacheMissCNYPerMillion, rule.OutputCNYPerMillion,
		rule.EffectiveAt.UTC().Format(time.RFC3339Nano), rule.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
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
					"effective_at": rule.EffectiveAt,
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
