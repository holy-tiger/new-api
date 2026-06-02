package codexchat

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// CachedFunctionCall stores one function call from a previous response.
type CachedFunctionCall struct {
	CallID    string
	Name      string
	Arguments string
	Reasoning string // optional reasoning text attached to this call
}

// CachedResponse stores function_call items from a previous response.
type CachedResponse struct {
	ResponseID    string
	FunctionCalls []CachedFunctionCall
	InsertedAt    time.Time
}

// HistoryStore provides in-memory storage for response continuation recovery.
type HistoryStore struct {
	mu sync.RWMutex

	// Primary index: (ownerScope, channelID, responseID) -> CachedResponse
	byResponseID map[string]*CachedResponse

	// Fallback index: (ownerScope, channelID, sessionScope, callID) -> CachedResponse
	byCallID map[string]*CachedResponse

	maxTotal int
	ttl      time.Duration
}

// NewHistoryStore creates a new HistoryStore with the given capacity and TTL limits.
func NewHistoryStore(maxTotal int, ttl time.Duration) *HistoryStore {
	return &HistoryStore{
		byResponseID: make(map[string]*CachedResponse),
		byCallID:     make(map[string]*CachedResponse),
		maxTotal:     maxTotal,
		ttl:          ttl,
	}
}

// Store adds a response to the cache.
func (s *HistoryStore) Store(ownerScope string, channelID int, responseID string, sessionScope string, calls []CachedFunctionCall) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := &CachedResponse{
		ResponseID:    responseID,
		FunctionCalls: calls,
		InsertedAt:    time.Now(),
	}

	// Store in primary index
	respKey := fmt.Sprintf("%s:%d:%s", ownerScope, channelID, responseID)
	s.byResponseID[respKey] = entry

	// Store in fallback index for each call
	for i := range calls {
		callKey := fmt.Sprintf("%s:%d:%s:%s", ownerScope, channelID, sessionScope, calls[i].CallID)
		s.byCallID[callKey] = entry
	}

	s.enforceLimits()
}

// LookupByResponseID looks up a cached response by its response ID.
// Returns nil if not found or if the entry has expired.
func (s *HistoryStore) LookupByResponseID(ownerScope string, channelID int, responseID string) *CachedResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := fmt.Sprintf("%s:%d:%s", ownerScope, channelID, responseID)
	entry, ok := s.byResponseID[key]
	if !ok {
		return nil
	}
	if time.Since(entry.InsertedAt) > s.ttl {
		return nil
	}
	return entry
}

// LookupByCallID looks up a cached response by call ID within a session scope.
// Returns nil if sessionScope or callID is empty, or if not found, or if expired.
func (s *HistoryStore) LookupByCallID(ownerScope string, channelID int, sessionScope string, callID string) *CachedResponse {
	if sessionScope == "" || callID == "" {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	key := fmt.Sprintf("%s:%d:%s:%s", ownerScope, channelID, sessionScope, callID)
	entry, ok := s.byCallID[key]
	if !ok {
		return nil
	}
	if time.Since(entry.InsertedAt) > s.ttl {
		return nil
	}
	return entry
}

// enforceLimits evicts the oldest entries when the total count exceeds maxTotal.
// MUST be called while holding the write lock.
func (s *HistoryStore) enforceLimits() {
	if len(s.byResponseID) <= s.maxTotal {
		return
	}

	// Collect all entries with their keys and timestamps
	type keyedEntry struct {
		key      string
		inserted time.Time
	}
	entries := make([]keyedEntry, 0, len(s.byResponseID))
	for k, v := range s.byResponseID {
		entries = append(entries, keyedEntry{key: k, inserted: v.InsertedAt})
	}

	// Sort oldest first
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].inserted.Before(entries[j].inserted)
	})

	// Determine how many to evict
	toEvict := len(s.byResponseID) - s.maxTotal
	for i := 0; i < toEvict && i < len(entries); i++ {
		entry := s.byResponseID[entries[i].key]
		// Remove from byResponseID
		delete(s.byResponseID, entries[i].key)
		// Remove from byCallID — iterate and drop entries pointing to this CachedResponse
		for k, v := range s.byCallID {
			if v == entry {
				delete(s.byCallID, k)
			}
		}
	}
}

// Clear removes all cached entries. Useful for testing.
func (s *HistoryStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.byResponseID = make(map[string]*CachedResponse)
	s.byCallID = make(map[string]*CachedResponse)
}

// GlobalHistoryStore is the singleton history store used across the application.
var GlobalHistoryStore = NewHistoryStore(5000, 60*time.Minute)

// DetermineOwnerScope returns the owner scope string for cache isolation.
// tokenID and userID are int values.
func DetermineOwnerScope(tokenID int, userID int) string {
	if tokenID > 0 {
		return fmt.Sprintf("token:%d", tokenID)
	}
	return fmt.Sprintf("user:%d", userID)
}

// DetermineSessionScope returns the session scope string.
// priority: promptCacheKey > headerSessionID > metadataSessionID
func DetermineSessionScope(promptCacheKey string, headerSessionID string, metadataSessionID string) string {
	if promptCacheKey != "" {
		return promptCacheKey
	}
	if headerSessionID != "" {
		return headerSessionID
	}
	if metadataSessionID != "" {
		return metadataSessionID
	}
	return ""
}
