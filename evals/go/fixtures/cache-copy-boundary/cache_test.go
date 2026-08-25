package cache

import "testing"

func TestCacheOwnsStoredBytes(t *testing.T) {
	cache := New()
	input := []byte("ready")
	cache.Set("state", input)
	input[0] = 'X'
	first, ok := cache.Get("state")
	if !ok || string(first) != "ready" {
		t.Fatalf("Get() = %q, %t", first, ok)
	}
	first[0] = 'Y'
	second, _ := cache.Get("state")
	if string(second) != "ready" {
		t.Fatalf("Get() leaked cache storage: %q", second)
	}
}

func TestCacheMissingKey(t *testing.T) {
	value, ok := New().Get("missing")
	if ok || value != nil {
		t.Fatalf("Get(missing) = %q, %t", value, ok)
	}
}
