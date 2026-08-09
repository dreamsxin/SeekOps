package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

const (
	routingPolicyAffinity  = "affinity"
	routingPolicyControl   = "control"
	routingPolicyNoSession = "no_session"
)

type affinityEntry struct {
	accountID string
	expiresAt time.Time
	lastUsed  time.Time
}

type AffinityStore struct {
	mu         sync.Mutex
	entries    map[string]affinityEntry
	ttl        time.Duration
	maxEntries int
}

func NewAffinityStore(ttl time.Duration, maxEntries int) *AffinityStore {
	return &AffinityStore{entries: make(map[string]affinityEntry), ttl: ttl, maxEntries: maxEntries}
}

func (s *AffinityStore) Get(key string, now time.Time) (string, bool) {
	if s == nil || key == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return "", false
	}
	if !entry.expiresAt.After(now) {
		delete(s.entries, key)
		return "", false
	}
	entry.lastUsed = now
	entry.expiresAt = now.Add(s.ttl)
	s.entries[key] = entry
	return entry.accountID, true
}

func (s *AffinityStore) Bind(key, accountID string, now time.Time) {
	if s == nil || key == "" || accountID == "" || s.ttl <= 0 || s.maxEntries <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[key]; !exists && len(s.entries) >= s.maxEntries {
		s.evictOne(now)
	}
	s.entries[key] = affinityEntry{accountID: accountID, expiresAt: now.Add(s.ttl), lastUsed: now}
}

func (s *AffinityStore) evictOne(now time.Time) {
	oldestKey := ""
	var oldest time.Time
	for key, entry := range s.entries {
		if !entry.expiresAt.After(now) {
			delete(s.entries, key)
			return
		}
		if oldestKey == "" || entry.lastUsed.Before(oldest) {
			oldestKey, oldest = key, entry.lastUsed
		}
	}
	if oldestKey != "" {
		delete(s.entries, oldestKey)
	}
}

func scopedSessionKey(virtualKeyID, seed string) string {
	if virtualKeyID == "" || seed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(virtualKeyID + "\x00" + seed))
	return hex.EncodeToString(sum[:])
}

func routingPolicy(sessionKey string, affinityPercent int) string {
	if sessionKey == "" {
		return routingPolicyNoSession
	}
	if affinityPercent <= 0 {
		return routingPolicyControl
	}
	if affinityPercent >= 100 {
		return routingPolicyAffinity
	}
	sum := sha256.Sum256([]byte("seekops-affinity-experiment\x00" + sessionKey))
	bucket := int(sum[0]) * 100 / 256
	if bucket < affinityPercent {
		return routingPolicyAffinity
	}
	return routingPolicyControl
}
