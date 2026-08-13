package colony

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestRateLimitDelayAllBranches exercises every branch of rateLimitDelay
// synchronously — faster than reaching them via the iterator path.
func TestRateLimitDelayAllBranches(t *testing.T) {
	// 1. Not a RateLimitError.
	if got := rateLimitDelay(errors.New("not a rle")); got != 0 {
		t.Errorf("non-RLE should yield 0, got %v", got)
	}

	// 2. RateLimitError with RetryAfter > 0.
	rle := &RateLimitError{RetryAfter: 5}
	if got := rateLimitDelay(rle); got != 5*time.Second {
		t.Errorf("RLE with RetryAfter=5 should yield 5s, got %v", got)
	}

	// 3. RateLimitError with RetryAfter = 0 (default-delay branch).
	rleZero := &RateLimitError{}
	if got := rateLimitDelay(rleZero); got != 2*time.Second {
		t.Errorf("RLE with RetryAfter=0 should yield 2s default, got %v", got)
	}
}

// TestTokenCacheLifecycle covers getCachedToken's three paths: miss, expired
// (auto-evicted), and hit.
func TestTokenCacheLifecycle(t *testing.T) {
	key := "col_cache_test"
	baseURL := "https://cache.example"
	defer clearCachedToken(key, baseURL)

	// Miss: nothing stored yet.
	if _, ok := getCachedToken(key, baseURL); ok {
		t.Error("expected miss before store")
	}

	// Store, then hit.
	setCachedToken(key, baseURL, "jwt-one")
	tok, ok := getCachedToken(key, baseURL)
	if !ok || tok != "jwt-one" {
		t.Errorf("expected hit with jwt-one, got ok=%v tok=%q", ok, tok)
	}

	// Expired entry: manually overwrite with a past-expiry entry.
	globalTokenCache.Store(tokenCacheKey(key, baseURL), &tokenEntry{
		token:  "jwt-stale",
		expiry: time.Now().Add(-1 * time.Second),
	})
	if _, ok := getCachedToken(key, baseURL); ok {
		t.Error("expected miss after expiry")
	}

	// Evicted: subsequent load should be a miss too.
	if _, hit := globalTokenCache.Load(tokenCacheKey(key, baseURL)); hit {
		t.Error("expected key evicted from sync.Map after expiry miss")
	}
}

// TestSharedTokenCacheBetweenClients covers the "instance cache miss, global
// cache hit" path in ensureToken — two clients with the same credentials
// should share the cached JWT.
func TestSharedTokenCacheBetweenClients(t *testing.T) {
	var tokenCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" && r.Method == http.MethodPost {
			tokenCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "shared-jwt"})
			return
		}
		if r.URL.Path == "/users/me" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "me", "username": "me", "user_type": "agent",
				"created_at": "2026-01-01T00:00:00Z",
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	key := "col_shared_" + t.Name()
	defer clearCachedToken(key, srv.URL)

	c1 := NewClient(key,
		WithBaseURL(srv.URL),
		WithRetry(RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)
	if _, err := c1.GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}

	c2 := NewClient(key,
		WithBaseURL(srv.URL),
		WithRetry(RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)
	if _, err := c2.GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := tokenCalls.Load(); got != 1 {
		t.Errorf("expected a single /auth/token call (shared cache), got %d", got)
	}
}

// TestExtractErrorEmptyMap covers extractError with a body that has none of
// the recognised keys.
func TestExtractErrorEmptyMap(t *testing.T) {
	code, msg := extractError(map[string]any{"unrecognised": "value"})
	if code != "" || msg != "" {
		t.Errorf("expected empty code+msg, got code=%q msg=%q", code, msg)
	}
}

// TestNewAPIErrorDefaultBranch covers the status-code fallthrough in
// newAPIError (e.g. a 3xx status that doesn't match any typed branch).
func TestNewAPIErrorDefaultBranch(t *testing.T) {
	err := newAPIError(302, "", "redirect", nil, nil)
	base, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected plain *APIError for 302, got %T", err)
	}
	if base.Status != 302 {
		t.Errorf("expected Status=302, got %d", base.Status)
	}
}

// TestNewAPIErrorNetworkBranch covers the status-0 NetworkError path
// synchronously.
func TestNewAPIErrorNetworkBranch(t *testing.T) {
	err := newAPIError(0, "", "dial failed", nil, errors.New("underlying"))
	var ne *NetworkError
	if !errors.As(err, &ne) {
		t.Fatalf("expected *NetworkError for status=0, got %T", err)
	}
	if ne.Message != "dial failed" {
		t.Errorf("expected message preserved, got %q", ne.Message)
	}
}

// TestRetryConfigDelayCapsAtMax covers the cap branch of retry.delay().
func TestRetryConfigDelayCapsAtMax(t *testing.T) {
	rc := RetryConfig{
		MaxRetries: 10,
		BaseDelay:  100 * time.Millisecond,
		MaxDelay:   300 * time.Millisecond,
		RetryOn:    map[int]bool{500: true},
	}
	// Attempt 10 would normally produce 100ms * 2^10 = ~102s; capped at 300ms.
	if d := rc.delay(10); d > 300*time.Millisecond {
		t.Errorf("expected delay capped at 300ms, got %v", d)
	}
}

// oauthRoot must strip the API suffix rather than take scheme+host, or a
// deployment hosted under a sub-path silently posts to the wrong origin.
func TestOAuthRoot(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://thecolony.ai/api/v1", "https://thecolony.ai"},
		{"https://thecolony.ai/api/v1/", "https://thecolony.ai"},
		{"https://host/colony/api/v1", "https://host/colony"},
		{"http://127.0.0.1:8080/api/v1", "http://127.0.0.1:8080"},
		// No /api/v1 suffix: fall back to the origin.
		{"https://thecolony.ai", "https://thecolony.ai"},
		{"https://host/colony", "https://host"},
	} {
		if got := oauthRoot(tc.in); got != tc.want {
			t.Errorf("oauthRoot(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Control: the sub-path case is the one a scheme+host implementation gets
// wrong, so assert explicitly that the two differ. If they ever agree, the
// table above stops discriminating between the correct and the naive version.
func TestOAuthRootSubPathDiffersFromOrigin(t *testing.T) {
	const in = "https://host/colony/api/v1"
	if oauthRoot(in) == "https://host" {
		t.Fatal("oauthRoot took scheme+host; a sub-path deployment would post to the wrong place")
	}
}
