package codexchat

import (
	"testing"
	"time"
)

// --- Store and Lookup ---

func TestStoreAndLookupByResponseID(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)
	calls := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	}
	store.Store("token:1", 1, "resp-abc", "session-x", calls)

	entry := store.LookupByResponseID("token:1", 1, "resp-abc")
	if entry == nil {
		t.Fatal("expected cached response, got nil")
	}
	if len(entry.FunctionCalls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(entry.FunctionCalls))
	}
	if entry.FunctionCalls[0].CallID != "call-1" {
		t.Errorf("expected call_id 'call-1', got %q", entry.FunctionCalls[0].CallID)
	}
	if entry.ResponseID != "resp-abc" {
		t.Errorf("expected response_id 'resp-abc', got %q", entry.ResponseID)
	}
}

func TestLookupByCallID(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)
	calls := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
		{CallID: "call-2", Name: "get_time", Arguments: `{}`},
	}
	store.Store("token:1", 1, "resp-abc", "session-x", calls)

	entry := store.LookupByCallID("token:1", 1, "session-x", "call-1")
	if entry == nil {
		t.Fatal("expected cached response by call ID, got nil")
	}
	if len(entry.FunctionCalls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(entry.FunctionCalls))
	}

	// Also findable by the second call
	entry2 := store.LookupByCallID("token:1", 1, "session-x", "call-2")
	if entry2 == nil {
		t.Fatal("expected cached response by second call ID, got nil")
	}
}

func TestLookupByResponseIDNonExistent(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)

	entry := store.LookupByResponseID("token:1", 1, "resp-nonexistent")
	if entry != nil {
		t.Errorf("expected nil for non-existent response ID, got %v", entry)
	}
}

func TestLookupByCallIDNonExistent(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)

	entry := store.LookupByCallID("token:1", 1, "session-x", "call-nonexistent")
	if entry != nil {
		t.Errorf("expected nil for non-existent call ID, got %v", entry)
	}
}

func TestLookupByCallIDEmptySessionScope(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)
	calls := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	}
	store.Store("token:1", 1, "resp-abc", "session-x", calls)

	entry := store.LookupByCallID("token:1", 1, "", "call-1")
	if entry != nil {
		t.Errorf("expected nil when sessionScope is empty, got %v", entry)
	}
}

func TestLookupByCallIDEmptyCallID(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)
	calls := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	}
	store.Store("token:1", 1, "resp-abc", "session-x", calls)

	entry := store.LookupByCallID("token:1", 1, "session-x", "")
	if entry != nil {
		t.Errorf("expected nil when callID is empty, got %v", entry)
	}
}

// --- Isolation ---

func TestDifferentOwnerScopesIsolated(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)
	calls := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	}
	store.Store("token:1", 1, "resp-abc", "session-x", calls)

	entry := store.LookupByResponseID("token:2", 1, "resp-abc")
	if entry != nil {
		t.Errorf("expected nil for different owner scope, got %v", entry)
	}
}

func TestDifferentChannelsIsolated(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)
	calls := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	}
	store.Store("token:1", 1, "resp-abc", "session-x", calls)

	entry := store.LookupByResponseID("token:1", 2, "resp-abc")
	if entry != nil {
		t.Errorf("expected nil for different channel, got %v", entry)
	}
}

func TestSameOwnerSameCallIDDifferentSession(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)
	calls1 := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	}
	calls2 := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_time", Arguments: `{}`},
	}
	store.Store("token:1", 1, "resp-aaa", "session-x", calls1)
	store.Store("token:1", 1, "resp-bbb", "session-y", calls2)

	// Look up by call ID with session-x should return the first response
	entryX := store.LookupByCallID("token:1", 1, "session-x", "call-1")
	if entryX == nil {
		t.Fatal("expected entry for session-x, got nil")
	}
	if entryX.ResponseID != "resp-aaa" {
		t.Errorf("expected resp-aaa, got %q", entryX.ResponseID)
	}

	// Look up by call ID with session-y should return the second response
	entryY := store.LookupByCallID("token:1", 1, "session-y", "call-1")
	if entryY == nil {
		t.Fatal("expected entry for session-y, got nil")
	}
	if entryY.ResponseID != "resp-bbb" {
		t.Errorf("expected resp-bbb, got %q", entryY.ResponseID)
	}
}

// --- TTL ---

func TestTTLExpiry(t *testing.T) {
	store := NewHistoryStore(100, 10*time.Millisecond)
	calls := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	}
	store.Store("token:1", 1, "resp-abc", "session-x", calls)

	// Wait for TTL to expire
	time.Sleep(20 * time.Millisecond)

	entry := store.LookupByResponseID("token:1", 1, "resp-abc")
	if entry != nil {
		t.Errorf("expected nil after TTL expiry, got %v", entry)
	}
}

func TestTTLNotExpired(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)
	calls := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	}
	store.Store("token:1", 1, "resp-abc", "session-x", calls)

	entry := store.LookupByResponseID("token:1", 1, "resp-abc")
	if entry == nil {
		t.Fatal("expected cached entry within TTL, got nil")
	}
	if entry.ResponseID != "resp-abc" {
		t.Errorf("expected resp-abc, got %q", entry.ResponseID)
	}
}

// --- Limits ---

func TestCapacityEnforcement(t *testing.T) {
	store := NewHistoryStore(3, time.Hour)

	// Store 5 entries (maxTotal=3 means oldest 2 should be evicted)
	store.Store("token:1", 1, "resp-1", "s", []CachedFunctionCall{{CallID: "c1"}})
	// Small sleep to ensure different InsertedAt timestamps
	time.Sleep(time.Millisecond)
	store.Store("token:1", 1, "resp-2", "s", []CachedFunctionCall{{CallID: "c2"}})
	time.Sleep(time.Millisecond)
	store.Store("token:1", 1, "resp-3", "s", []CachedFunctionCall{{CallID: "c3"}})
	time.Sleep(time.Millisecond)
	store.Store("token:1", 1, "resp-4", "s", []CachedFunctionCall{{CallID: "c4"}})
	time.Sleep(time.Millisecond)
	store.Store("token:1", 1, "resp-5", "s", []CachedFunctionCall{{CallID: "c5"}})

	// Oldest (resp-1, resp-2) should be evicted
	if entry := store.LookupByResponseID("token:1", 1, "resp-1"); entry != nil {
		t.Errorf("expected resp-1 to be evicted, got %v", entry)
	}
	if entry := store.LookupByResponseID("token:1", 1, "resp-2"); entry != nil {
		t.Errorf("expected resp-2 to be evicted, got %v", entry)
	}
	// Newest should remain
	if entry := store.LookupByResponseID("token:1", 1, "resp-3"); entry == nil {
		t.Error("expected resp-3 to remain, got nil")
	}
	if entry := store.LookupByResponseID("token:1", 1, "resp-4"); entry == nil {
		t.Error("expected resp-4 to remain, got nil")
	}
	if entry := store.LookupByResponseID("token:1", 1, "resp-5"); entry == nil {
		t.Error("expected resp-5 to remain, got nil")
	}
}

func TestClearResets(t *testing.T) {
	store := NewHistoryStore(100, time.Hour)
	calls := []CachedFunctionCall{
		{CallID: "call-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	}
	store.Store("token:1", 1, "resp-abc", "session-x", calls)

	store.Clear()

	entry := store.LookupByResponseID("token:1", 1, "resp-abc")
	if entry != nil {
		t.Errorf("expected nil after Clear, got %v", entry)
	}
	entry2 := store.LookupByCallID("token:1", 1, "session-x", "call-1")
	if entry2 != nil {
		t.Errorf("expected nil after Clear (call ID), got %v", entry2)
	}
}

// --- Scope Helpers ---

func TestDetermineOwnerScopeWithTokenID(t *testing.T) {
	result := DetermineOwnerScope(5, 10)
	if result != "token:5" {
		t.Errorf("expected 'token:5', got %q", result)
	}
}

func TestDetermineOwnerScopeWithUserIDOnly(t *testing.T) {
	result := DetermineOwnerScope(0, 42)
	if result != "user:42" {
		t.Errorf("expected 'user:42', got %q", result)
	}
}

func TestDetermineSessionScopeWithPromptCacheKey(t *testing.T) {
	result := DetermineSessionScope("pc-key-123", "header-sess", "meta-sess")
	if result != "pc-key-123" {
		t.Errorf("expected 'pc-key-123', got %q", result)
	}
}

func TestDetermineSessionScopeFallback(t *testing.T) {
	result := DetermineSessionScope("", "header-sess", "meta-sess")
	if result != "header-sess" {
		t.Errorf("expected 'header-sess', got %q", result)
	}
}

func TestDetermineSessionScopeTripleFallback(t *testing.T) {
	result := DetermineSessionScope("", "", "meta-sess")
	if result != "meta-sess" {
		t.Errorf("expected 'meta-sess', got %q", result)
	}
}

func TestDetermineSessionScopeAllEmpty(t *testing.T) {
	result := DetermineSessionScope("", "", "")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}
