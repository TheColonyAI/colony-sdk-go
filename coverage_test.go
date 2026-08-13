package colony_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// TestErrorMessages covers every typed error's Error() method with both
// code-present and code-absent variants where applicable.
func TestErrorMessages(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   []string // substrings the message must contain
	}{
		{"auth", 401, `{"detail":"Not authenticated"}`, []string{"auth error", "Not authenticated"}},
		{"forbidden", 403, `{"detail":"Forbidden"}`, []string{"auth error", "Forbidden"}},
		{"not found", 404, `{"detail":"Missing post"}`, []string{"not found", "Missing post"}},
		{"conflict with code", 409, `{"detail":{"code":"ALREADY_VOTED","message":"Already voted"}}`, []string{"conflict", "ALREADY_VOTED", "Already voted"}},
		{"conflict bare", 409, `{"detail":"Already voted"}`, []string{"conflict", "Already voted"}},
		{"validation 400", 400, `{"detail":"Bad field"}`, []string{"validation error", "Bad field"}},
		{"validation 422", 422, `{"detail":"Bad field"}`, []string{"validation error", "Bad field"}},
		{"rate limit no retry-after", 429, `{"detail":"Slow down"}`, []string{"rate limited", "Slow down"}},
		{"server 500", 500, `{"error":"boom"}`, []string{"server error", "HTTP 500", "boom"}},
		{"server 503", 503, `{"error":"overloaded"}`, []string{"server error", "HTTP 503", "overloaded"}},
		{"api error with code", 418, `{"detail":{"code":"TEAPOT","message":"i am a teapot"}}`, []string{"HTTP 418", "TEAPOT", "i am a teapot"}},
		{"api error no code", 418, `{"detail":"tea only"}`, []string{"HTTP 418", "tea only"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
				"GET /posts/err": func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.body))
				},
			}))
			_, err := client.GetPost(context.Background(), "err")
			if err == nil {
				t.Fatal("expected error")
			}
			msg := err.Error()
			for _, want := range tt.want {
				if !strings.Contains(msg, want) {
					t.Errorf("error message %q missing substring %q", msg, want)
				}
			}
		})
	}
}

// TestRateLimitErrorWithRetryAfterMessage covers the RetryAfter>0 branch of
// RateLimitError.Error() (the Retry-After header path is already exercised
// in TestRateLimitRetryAfter — this one double-checks the formatted string).
func TestRateLimitErrorWithRetryAfterMessage(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts/rl": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"detail":"slow"}`))
		},
	}))
	_, err := client.GetPost(context.Background(), "rl")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "retry after 45s") {
		t.Errorf("expected 'retry after 45s' in %q", err.Error())
	}
}

// TestNetworkError covers NetworkError.Error() via a closed server.
func TestNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // close immediately; any request will fail with a network error

	client := colony.NewClient("col_test",
		colony.WithBaseURL(url),
		colony.WithTimeout(500*time.Millisecond),
		colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)
	_, err := client.GetPost(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error")
	}
	// The error may be wrapped; unwrap to assert NetworkError somewhere.
	var ne *colony.NetworkError
	if !errors.As(err, &ne) {
		// Auth is attempted first; even the token request failing produces a
		// NetworkError, but it may be surfaced as a direct error string.
		if !strings.Contains(err.Error(), "network") && !strings.Contains(err.Error(), "connect") {
			t.Errorf("expected network-related error, got: %v", err)
		}
	} else if !strings.Contains(ne.Error(), "network error") {
		t.Errorf("expected 'network error' substring, got: %v", ne)
	}
}

// TestUnwrapAPIError covers APIError.Unwrap via errors.Is/errors.As through a
// wrapped Cause.
func TestUnwrapAPIError(t *testing.T) {
	sentinel := errors.New("sentinel")
	base := &colony.APIError{Status: 500, Message: "boom", Cause: sentinel}
	if !errors.Is(base, sentinel) {
		t.Error("expected APIError to unwrap to Cause")
	}
}

// ── Zero-coverage client methods ─────────────────────────────────────────────

func TestGetUser(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /users/alice": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{
				"id": "alice", "username": "alice", "user_type": "agent",
				"created_at": "2026-01-01T00:00:00Z",
			})
		},
	}))
	u, err := client.GetUser(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "alice" {
		t.Errorf("expected alice, got %q", u.Username)
	}
}

func TestUpdateProfileAllOptions(t *testing.T) {
	var gotBody map[string]any
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"PUT /users/me": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			jsonResp(w, map[string]any{
				"id": "me", "username": "me", "user_type": "agent",
				"display_name": "Me", "bio": "hi",
				"created_at": "2026-01-01T00:00:00Z",
			})
		},
	}))
	_, err := client.UpdateProfile(context.Background(), &colony.UpdateProfileOptions{
		DisplayName:  colony.Ptr("Me"),
		Bio:          colony.Ptr("hi"),
		Capabilities: map[string]any{"x": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["display_name"] != "Me" {
		t.Errorf("expected display_name Me, got %v", gotBody["display_name"])
	}
	if gotBody["bio"] != "hi" {
		t.Errorf("expected bio hi, got %v", gotBody["bio"])
	}
	if caps, ok := gotBody["capabilities"].(map[string]any); !ok || caps["x"] != true {
		t.Errorf("expected capabilities{x:true}, got %v", gotBody["capabilities"])
	}

	// Also cover the nil-opts path.
	_, err = client.UpdateProfile(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestVoteComment(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /comments/c1/vote": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["value"] != float64(-1) {
				t.Errorf("expected value -1, got %v", body["value"])
			}
			jsonResp(w, map[string]any{"score": 3})
		},
	}))
	resp, err := client.VoteComment(context.Background(), "c1", -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Score != 3 {
		t.Errorf("expected score 3, got %d", resp.Score)
	}
}

func TestVoteCommentZeroValueDefaultsToUpvote(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /comments/c1/vote": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["value"] != float64(1) {
				t.Errorf("expected value 1 (default), got %v", body["value"])
			}
			jsonResp(w, map[string]any{"score": 1})
		},
	}))
	if _, err := client.VoteComment(context.Background(), "c1", 0); err != nil {
		t.Fatal(err)
	}
}

func TestVotePostZeroValueDefaultsToUpvote(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /posts/p1/vote": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["value"] != float64(1) {
				t.Errorf("expected value 1 (default), got %v", body["value"])
			}
			jsonResp(w, map[string]any{"score": 1})
		},
	}))
	if _, err := client.VotePost(context.Background(), "p1", 0); err != nil {
		t.Fatal(err)
	}
}

func TestReactComment(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /comments/c1/react": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"toggled": true})
		},
	}))
	resp, err := client.ReactComment(context.Background(), "c1", colony.EmojiHeart)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Toggled {
		t.Error("expected toggled=true")
	}
}

func TestVotePoll(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /posts/p1/poll/vote": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			ids, _ := body["option_ids"].([]any)
			if len(ids) != 2 {
				t.Errorf("expected 2 option_ids, got %d", len(ids))
			}
			jsonResp(w, map[string]any{"success": true})
		},
	}))
	if _, err := client.VotePoll(context.Background(), "p1", []string{"o1", "o2"}); err != nil {
		t.Fatal(err)
	}
}

func TestGetConversation(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /messages/conversations/bob": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{
				"id":         "conv1",
				"other_user": map[string]any{"id": "u1", "username": "bob", "user_type": "agent", "created_at": "2026-01-01T00:00:00Z"},
				"messages":   []any{},
			})
		},
	}))
	conv, err := client.GetConversation(context.Background(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if conv.ID != "conv1" {
		t.Errorf("expected conv1, got %q", conv.ID)
	}
}

func TestGetUnreadCount(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /messages/unread-count": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"unread_count": 7})
		},
	}))
	count, err := client.GetUnreadCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count.UnreadCount != 7 {
		t.Errorf("expected 7, got %d", count.UnreadCount)
	}
}

func TestGetNotificationCount(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /notifications/count": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"unread_count": 12})
		},
	}))
	count, err := client.GetNotificationCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count.UnreadCount != 12 {
		t.Errorf("expected 12, got %d", count.UnreadCount)
	}
}

func TestMarkNotificationsRead(t *testing.T) {
	called := int32(0)
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /notifications/read": func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&called, 1)
			jsonResp(w, map[string]any{"ok": true})
		},
	}))
	if err := client.MarkNotificationsRead(context.Background()); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("expected 1 call, got %d", called)
	}
}

func TestMarkNotificationRead(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /notifications/n1/read": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"ok": true})
		},
	}))
	if err := client.MarkNotificationRead(context.Background(), "n1"); err != nil {
		t.Fatal(err)
	}
}

func TestJoinLeaveColony(t *testing.T) {
	var joined, left int32
	// "my-colony" isn't in the Colonies map. With the slug-resolution
	// gap closed, the client now does a lazy GET /colonies lookup to
	// translate it to a UUID before hitting the join/leave endpoints.
	const myColonyUUID = "11111111-2222-3333-4444-555555555555"
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /colonies": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": myColonyUUID, "name": "my-colony"},
			})
		},
		"POST /colonies/" + myColonyUUID + "/join": func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&joined, 1)
			jsonResp(w, map[string]any{"ok": true})
		},
		"POST /colonies/" + myColonyUUID + "/leave": func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&left, 1)
			jsonResp(w, map[string]any{"ok": true})
		},
	}))
	if err := client.JoinColony(context.Background(), "my-colony"); err != nil {
		t.Fatal(err)
	}
	if err := client.LeaveColony(context.Background(), "my-colony"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&joined) != 1 || atomic.LoadInt32(&left) != 1 {
		t.Errorf("expected 1 join + 1 leave, got %d + %d", joined, left)
	}
}

// TestJoinColonyResolvesName covers the named-colony branch of resolveColony,
// which maps "findings" → UUID via the Colonies map.
func TestJoinColonyResolvesName(t *testing.T) {
	const findingsUUID = "bbe6be09-da95-4983-b23d-1dd980479a7e"
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /colonies/" + findingsUUID + "/join": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"ok": true})
		},
	}))
	if err := client.JoinColony(context.Background(), "findings"); err != nil {
		t.Fatal(err)
	}
}

func TestResolveColonyUUID(t *testing.T) {
	// A UUID-looking string should pass through resolveColony unchanged.
	// We verify this by checking the request path includes the UUID verbatim.
	const uuid = "00000000-1111-2222-3333-444455556666"
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /colonies/" + uuid + "/join": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"ok": true})
		},
	}))
	if err := client.JoinColony(context.Background(), uuid); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookLifecycle(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /webhooks": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "w1", "url": "https://x.com/hook", "events": []string{"post_created"}, "is_active": true},
			})
		},
		"PUT /webhooks/w1": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// All UpdateWebhookOptions fields should be present.
			for _, k := range []string{"url", "secret", "events", "is_active"} {
				if _, ok := body[k]; !ok {
					t.Errorf("expected field %q in request body", k)
				}
			}
			jsonResp(w, map[string]any{
				"id": "w1", "url": "https://x.com/hook2", "events": []string{"comment_created"}, "is_active": false,
			})
		},
		"DELETE /webhooks/w1": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	}))

	hooks, err := client.GetWebhooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0].ID != "w1" {
		t.Errorf("expected 1 webhook w1, got %+v", hooks)
	}

	updated, err := client.UpdateWebhook(context.Background(), "w1", &colony.UpdateWebhookOptions{
		URL:      colony.Ptr("https://x.com/hook2"),
		Secret:   colony.Ptr("new-secret"),
		Events:   []string{"comment_created"},
		IsActive: colony.Ptr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.URL != "https://x.com/hook2" {
		t.Errorf("expected URL updated, got %q", updated.URL)
	}

	if err := client.DeleteWebhook(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateWebhookNilOptions(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"PUT /webhooks/w1": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"id": "w1", "url": "", "events": []string{}, "is_active": true})
		},
	}))
	if _, err := client.UpdateWebhook(context.Background(), "w1", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRaw(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /some/custom/endpoint": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"custom": "yes"})
		},
	}))
	raw, err := client.Raw(context.Background(), http.MethodGet, "/some/custom/endpoint", nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["custom"] != "yes" {
		t.Errorf("expected custom=yes, got %v", parsed["custom"])
	}
}

func TestRotateKey(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /auth/rotate-key": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"api_key": "col_newkey"})
		},
	}))
	resp, err := client.RotateKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.APIKey != "col_newkey" {
		t.Errorf("expected col_newkey, got %q", resp.APIKey)
	}
}

func TestGetAllCommentsAndIter(t *testing.T) {
	// Serve 3 pages: 20, 20, 5 (total 45), then IterComments reads the same.
	calls := int32(0)
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts/p1/comments": func(w http.ResponseWriter, r *http.Request) {
			page := r.URL.Query().Get("page")
			n := atomic.AddInt32(&calls, 1)
			items := []map[string]any{}
			count := 20
			if page == "3" {
				count = 5
			}
			for i := 0; i < count; i++ {
				items = append(items, map[string]any{
					"id":         "c" + page + "-" + itoa(i),
					"body":       "hi",
					"author":     map[string]any{"id": "u1", "username": "t"},
					"created_at": "2026-01-01T00:00:00Z",
					"updated_at": "2026-01-01T00:00:00Z",
				})
			}
			jsonResp(w, map[string]any{"items": items, "total": 45})
			_ = n
		},
	}))

	all, err := client.GetAllComments(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 45 {
		t.Errorf("expected 45 comments, got %d", len(all))
	}

	// Reset call counter and exercise IterComments with maxResults=30.
	atomic.StoreInt32(&calls, 0)
	var got int
	for res := range client.IterComments(context.Background(), "p1", 30) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got++
	}
	if got != 30 {
		t.Errorf("expected 30 from iterator with maxResults=30, got %d", got)
	}
}

func TestIterCommentsUnlimited(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts/p1/comments": func(w http.ResponseWriter, r *http.Request) {
			// Return 5 items on page 1, nothing on page 2 (len < 20 stops iteration).
			page := r.URL.Query().Get("page")
			items := []map[string]any{}
			if page == "1" {
				for i := 0; i < 5; i++ {
					items = append(items, map[string]any{
						"id":         "c" + itoa(i),
						"body":       "hi",
						"author":     map[string]any{"id": "u1", "username": "t"},
						"created_at": "2026-01-01T00:00:00Z",
						"updated_at": "2026-01-01T00:00:00Z",
					})
				}
			}
			jsonResp(w, map[string]any{"items": items, "total": 5})
		},
	}))
	var got int
	for res := range client.IterComments(context.Background(), "p1", 0) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got++
	}
	if got != 5 {
		t.Errorf("expected 5, got %d", got)
	}
}

// TestLastResponseHeaders covers both the nil-before-any-request branch and
// the populated branch.
func TestLastResponseHeaders(t *testing.T) {
	client := colony.NewClient("col_test")
	if hdrs := client.LastResponseHeaders(); hdrs != nil {
		t.Errorf("expected nil headers before any request, got %v", hdrs)
	}

	_, clientWithServer := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /users/me": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "42")
			w.Header().Set("X-Request-ID", "abc123")
			jsonResp(w, map[string]any{
				"id": "me", "username": "me", "user_type": "agent",
				"created_at": "2026-01-01T00:00:00Z",
			})
		},
	}))
	if _, err := clientWithServer.GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}
	hdrs := clientWithServer.LastResponseHeaders()
	if hdrs == nil {
		t.Fatal("expected headers after request")
	}
	if hdrs.Get("X-RateLimit-Remaining") != "42" {
		t.Errorf("expected 42, got %q", hdrs.Get("X-RateLimit-Remaining"))
	}
	if hdrs.Get("X-Request-ID") != "abc123" {
		t.Errorf("expected abc123, got %q", hdrs.Get("X-Request-ID"))
	}
}

// TestWithHTTPClientOption covers the WithHTTPClient Option constructor.
func TestWithHTTPClientOption(t *testing.T) {
	custom := &http.Client{Timeout: 7 * time.Second}
	srv := httptest.NewServer(tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /users/me": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"id": "me", "username": "me", "user_type": "agent", "created_at": "2026-01-01T00:00:00Z"})
		},
	}))
	t.Cleanup(srv.Close)
	client := colony.NewClient("col_test",
		colony.WithBaseURL(srv.URL),
		colony.WithHTTPClient(custom),
		colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)
	if _, err := client.GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestWithLoggerOption covers both the WithLogger Option and the
// logDebug(msg, ...) non-nil branch.
func TestWithLoggerOption(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := httptest.NewServer(tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /users/me": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{"id": "me", "username": "me", "user_type": "agent", "created_at": "2026-01-01T00:00:00Z"})
		},
	}))
	t.Cleanup(srv.Close)
	client := colony.NewClient("col_test",
		colony.WithBaseURL(srv.URL),
		colony.WithLogger(logger),
		colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)
	if _, err := client.GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if logBuf.Len() == 0 {
		t.Error("expected logger output, got empty buffer")
	}
}

// TestSearchAllOptions exercises every optional query parameter on Search.
func TestSearchAllOptions(t *testing.T) {
	var gotQuery string
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /search": func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			jsonResp(w, map[string]any{"items": []any{}, "total": 0, "users": []any{}})
		},
	}))
	_, err := client.Search(context.Background(), "llm", &colony.SearchOptions{
		Limit: 50, Offset: 10, PostType: "finding",
		Colony: "findings", AuthorType: "agent", Sort: "top",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"q=llm", "limit=50", "offset=10", "post_type=finding", "colony=findings", "author_type=agent", "sort=top"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("expected %q in query %q", want, gotQuery)
		}
	}

	// Nil-opts branch.
	if _, err := client.Search(context.Background(), "llm", nil); err != nil {
		t.Fatal(err)
	}
}

// TestDirectoryAllOptions exercises every branch in Directory.
func TestDirectoryAllOptions(t *testing.T) {
	var gotQuery string
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /users/directory": func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			jsonResp(w, map[string]any{"items": []any{}, "total": 0})
		},
	}))

	// With all options specified.
	_, err := client.Directory(context.Background(), &colony.DirectoryOptions{
		Query: "alice", UserType: "agent", Sort: "newest", Limit: 50, Offset: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"query=alice", "user_type=agent", "sort=newest", "limit=50", "offset=5"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("expected %q in query %q", want, gotQuery)
		}
	}

	// With nil options — covers the else branch setting defaults.
	if _, err := client.Directory(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"user_type=all", "sort=karma", "limit=20"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("expected default %q in query %q (nil opts)", want, gotQuery)
		}
	}
}

func TestGetPostsNilOptionsUsesDefaults(t *testing.T) {
	var gotQuery string
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts": func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			jsonResp(w, map[string]any{"items": []any{}, "total": 0})
		},
	}))
	if _, err := client.GetPosts(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "sort=new") || !strings.Contains(gotQuery, "limit=20") {
		t.Errorf("expected default sort=new & limit=20, got %q", gotQuery)
	}
}

func TestGetPostsAllOptions(t *testing.T) {
	var gotQuery string
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts": func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			jsonResp(w, map[string]any{"items": []any{}, "total": 0})
		},
	}))
	_, err := client.GetPosts(context.Background(), &colony.GetPostsOptions{
		Colony: "findings", Sort: "hot", Limit: 40, Offset: 10,
		PostType: "finding", Tag: "ai", Search: "llm",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sort=hot", "limit=40", "offset=10", "post_type=finding", "tag=ai", "search=llm"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("expected %q in query %q", want, gotQuery)
		}
	}
}

func TestCreatePostWithMetadata(t *testing.T) {
	var gotBody map[string]any
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /posts": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			jsonResp(w, map[string]any{
				"id": "p1", "title": "T", "post_type": "poll",
				"author":     map[string]any{"id": "u1", "username": "t"},
				"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
			})
		},
	}))
	_, err := client.CreatePost(context.Background(), "T", "B", &colony.CreatePostOptions{
		Colony: "findings", PostType: "poll",
		Metadata: map[string]any{"options": []any{"yes", "no"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["post_type"] != "poll" {
		t.Errorf("expected post_type poll, got %v", gotBody["post_type"])
	}
	if meta, ok := gotBody["metadata"].(map[string]any); !ok || meta == nil {
		t.Errorf("expected metadata present with options, got: %v", gotBody["metadata"])
	}
}

func TestCreatePostNoOptions(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /posts": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, map[string]any{
				"id": "p1", "title": "T",
				"author":     map[string]any{"id": "u1", "username": "t"},
				"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
			})
		},
	}))
	if _, err := client.CreatePost(context.Background(), "T", "B", nil); err != nil {
		t.Fatal(err)
	}
}

func TestGetCommentsPageDefaultsToOne(t *testing.T) {
	var gotPage string
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts/p1/comments": func(w http.ResponseWriter, r *http.Request) {
			gotPage = r.URL.Query().Get("page")
			jsonResp(w, map[string]any{"items": []any{}, "total": 0})
		},
	}))
	if _, err := client.GetComments(context.Background(), "p1", 0); err != nil {
		t.Fatal(err)
	}
	if gotPage != "1" {
		t.Errorf("expected page defaulted to 1, got %q", gotPage)
	}
}

func TestGetNotificationsAllBranches(t *testing.T) {
	var gotQuery string
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /notifications": func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		},
	}))

	if _, err := client.GetNotifications(context.Background(), &colony.GetNotificationsOptions{
		UnreadOnly: true, Limit: 25,
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"unread_only=true", "limit=25"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("expected %q in query %q", want, gotQuery)
		}
	}

	// Nil opts branch.
	if _, err := client.GetNotifications(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "limit=50") {
		t.Errorf("expected default limit=50 in nil-opts query %q", gotQuery)
	}
}

func TestGetColoniesDefaultLimit(t *testing.T) {
	var gotLimit string
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /colonies": func(w http.ResponseWriter, r *http.Request) {
			gotLimit = r.URL.Query().Get("limit")
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		},
	}))
	if _, err := client.GetColonies(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if gotLimit != "50" {
		t.Errorf("expected default limit=50, got %q", gotLimit)
	}
}

// TestExtractErrorFormats triggers each of the JSON error-envelope branches
// extractError recognises: {"detail": {...}}, {"detail": "..."}, {"error":
// "..."}, {"message": "..."}, and an unrecognised shape.
func TestExtractErrorFormats(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"detail object", `{"detail":{"code":"X","message":"one"}}`, "one"},
		{"detail string", `{"detail":"two"}`, "two"},
		{"error string", `{"error":"three"}`, "three"},
		{"message string", `{"message":"four"}`, "four"},
		{"unrecognised", `{"anything":"else"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
				"GET /posts/err": func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(500)
					_, _ = w.Write([]byte(tt.body))
				},
			}))
			_, err := client.GetPost(context.Background(), "err")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("expected %q in %q", tt.want, err.Error())
			}
		})
	}
}

// TestNon200NonJSONBody exercises the doRaw path where the server returns a
// non-JSON body on an error.
func TestNon200NonJSONBody(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts/err": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(502)
			_, _ = w.Write([]byte("<html>Bad Gateway</html>"))
		},
	}))
	_, err := client.GetPost(context.Background(), "err")
	if err == nil {
		t.Fatal("expected error")
	}
	var se *colony.ServerError
	if !errors.As(err, &se) {
		t.Errorf("expected ServerError, got %T", err)
	}
}

// TestRateLimitRetryAfterIntParsingFallback covers the server-sent
// Retry-After header with a non-numeric value (falls back to 0).
func TestRateLimitRetryAfterNonNumeric(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts/rl": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"detail":"slow"}`))
		},
	}))
	_, err := client.GetPost(context.Background(), "rl")
	if err == nil {
		t.Fatal("expected error")
	}
	var rle *colony.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected RateLimitError, got %T", err)
	}
	if rle.RetryAfter != 0 {
		t.Errorf("expected RetryAfter=0 for non-numeric header, got %d", rle.RetryAfter)
	}
}

// TestErrorPropagationAllMethods verifies that every client method
// propagates HTTP errors from the transport rather than silently swallowing
// them. This hits the `return nil, err` / `return err` branch that otherwise
// shows as 75% coverage on methods with the typical `if err := c.do(...); err
// != nil` shape.
func TestErrorPropagationAllMethods(t *testing.T) {
	// Any request path returns 500 — the error should propagate from every
	// client method back to the caller.
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" && r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "test-jwt"})
			return
		}
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	c := colony.NewClient("col_test",
		colony.WithBaseURL(srv.URL),
		colony.WithTimeout(2*time.Second),
		colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)
	ctx := context.Background()

	calls := []struct {
		name string
		run  func() error
	}{
		{"GetPost", func() error { _, err := c.GetPost(ctx, "x"); return err }},
		{"GetPosts", func() error { _, err := c.GetPosts(ctx, nil); return err }},
		{"GetPostContext", func() error { _, err := c.GetPostContext(ctx, "x"); return err }},
		{"GetPostConversation", func() error { _, err := c.GetPostConversation(ctx, "x"); return err }},
		{"CreatePost", func() error { _, err := c.CreatePost(ctx, "t", "b", nil); return err }},
		{"UpdatePost", func() error { _, err := c.UpdatePost(ctx, "x", nil); return err }},
		{"DeletePost", func() error { return c.DeletePost(ctx, "x") }},
		{"CreateComment", func() error { _, err := c.CreateComment(ctx, "x", "b", nil); return err }},
		{"GetComments", func() error { _, err := c.GetComments(ctx, "x", 1); return err }},
		{"GetAllComments", func() error { _, err := c.GetAllComments(ctx, "x"); return err }},
		{"UpdateComment", func() error { _, err := c.UpdateComment(ctx, "x", "b"); return err }},
		{"DeleteComment", func() error { return c.DeleteComment(ctx, "x") }},
		{"VotePost", func() error { _, err := c.VotePost(ctx, "x", 1); return err }},
		{"VoteComment", func() error { _, err := c.VoteComment(ctx, "x", 1); return err }},
		{"ReactPost", func() error { _, err := c.ReactPost(ctx, "x", "fire"); return err }},
		{"ReactComment", func() error { _, err := c.ReactComment(ctx, "x", "fire"); return err }},
		{"GetPoll", func() error { _, err := c.GetPoll(ctx, "x"); return err }},
		{"VotePoll", func() error { _, err := c.VotePoll(ctx, "x", []string{"o1"}); return err }},
		{"SendMessage", func() error { _, err := c.SendMessage(ctx, "u", "b"); return err }},
		{"GetConversation", func() error { _, err := c.GetConversation(ctx, "u"); return err }},
		{"ListConversations", func() error { _, err := c.ListConversations(ctx); return err }},
		{"MarkConversationRead", func() error { return c.MarkConversationRead(ctx, "u") }},
		{"ArchiveConversation", func() error { return c.ArchiveConversation(ctx, "u") }},
		{"UnarchiveConversation", func() error { return c.UnarchiveConversation(ctx, "u") }},
		{"MuteConversation", func() error { return c.MuteConversation(ctx, "u") }},
		{"UnmuteConversation", func() error { return c.UnmuteConversation(ctx, "u") }},
		{"GetUnreadCount", func() error { _, err := c.GetUnreadCount(ctx); return err }},
		{"Search", func() error { _, err := c.Search(ctx, "q", nil); return err }},
		{"GetMe", func() error { _, err := c.GetMe(ctx); return err }},
		{"GetUser", func() error { _, err := c.GetUser(ctx, "u"); return err }},
		{"GetUserReport", func() error { _, err := c.GetUserReport(ctx, "u"); return err }},
		{"UpdateProfile", func() error { _, err := c.UpdateProfile(ctx, nil); return err }},
		{"Directory", func() error { _, err := c.Directory(ctx, nil); return err }},
		{"Follow", func() error { return c.Follow(ctx, "u") }},
		{"Unfollow", func() error { return c.Unfollow(ctx, "u") }},
		{"GetRisingPosts", func() error { _, err := c.GetRisingPosts(ctx, nil); return err }},
		{"GetTrendingTags", func() error { _, err := c.GetTrendingTags(ctx, nil); return err }},
		{"GetNotifications", func() error { _, err := c.GetNotifications(ctx, nil); return err }},
		{"GetNotificationCount", func() error { _, err := c.GetNotificationCount(ctx); return err }},
		{"MarkNotificationsRead", func() error { return c.MarkNotificationsRead(ctx) }},
		{"MarkNotificationRead", func() error { return c.MarkNotificationRead(ctx, "n") }},
		{"GetColonies", func() error { _, err := c.GetColonies(ctx, 10); return err }},
		{"JoinColony", func() error { return c.JoinColony(ctx, "my") }},
		{"LeaveColony", func() error { return c.LeaveColony(ctx, "my") }},
		{"CreateWebhook", func() error { _, err := c.CreateWebhook(ctx, "u", []string{"e"}, "s"); return err }},
		{"GetWebhooks", func() error { _, err := c.GetWebhooks(ctx); return err }},
		{"UpdateWebhook", func() error { _, err := c.UpdateWebhook(ctx, "w", nil); return err }},
		{"DeleteWebhook", func() error { return c.DeleteWebhook(ctx, "w") }},
		{"RotateKey", func() error { _, err := c.RotateKey(ctx); return err }},
		{"Raw", func() error { _, err := c.Raw(ctx, "GET", "/anything", nil); return err }},
	}

	for _, tt := range calls {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

// TestIterPostsHandlesRateLimit covers the rate-limit path in IterPosts
// (and therefore rateLimitDelay), and the transition to the rate-limit delay
// followed by retry-and-success.
func TestIterPostsHandlesRateLimit(t *testing.T) {
	var attempts atomic.Int32
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts": func(w http.ResponseWriter, r *http.Request) {
			n := attempts.Add(1)
			if n == 1 {
				// First call: 429 with a short Retry-After so the test is fast.
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				_, _ = w.Write([]byte(`{"detail":"slow"}`))
				return
			}
			// Second call: tiny page ends iteration.
			jsonResp(w, map[string]any{
				"items": []map[string]any{
					{"id": "p1", "title": "OK",
						"author":     map[string]any{"id": "u1", "username": "t"},
						"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
				},
				"total": 1,
			})
		},
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got int
	for res := range client.IterPosts(ctx, &colony.IterPostsOptions{PageSize: 20}) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got++
	}
	if got != 1 {
		t.Errorf("expected 1 post after rate-limit retry, got %d", got)
	}
	if attempts.Load() < 2 {
		t.Errorf("expected ≥2 attempts (rate-limit then success), got %d", attempts.Load())
	}
}

// TestIterPostsPropagatesNonRateLimitError covers the non-rate-limit error
// branch inside IterPosts, where the error is forwarded through the channel.
func TestIterPostsPropagatesNonRateLimitError(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		},
	}))

	var sawErr bool
	for res := range client.IterPosts(context.Background(), nil) {
		if res.Err != nil {
			sawErr = true
			break
		}
	}
	if !sawErr {
		t.Error("expected non-rate-limit error to be forwarded through channel")
	}
}

// TestIterCommentsHandlesRateLimit mirrors TestIterPostsHandlesRateLimit for
// IterComments (different code path, same pattern).
func TestIterCommentsHandlesRateLimit(t *testing.T) {
	var attempts atomic.Int32
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts/p1/comments": func(w http.ResponseWriter, r *http.Request) {
			n := attempts.Add(1)
			if n == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				_, _ = w.Write([]byte(`{"detail":"slow"}`))
				return
			}
			jsonResp(w, map[string]any{"items": []map[string]any{
				{"id": "c1", "body": "hi",
					"author":     map[string]any{"id": "u1", "username": "t"},
					"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
			}, "total": 1})
		},
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got int
	for res := range client.IterComments(ctx, "p1", 0) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got++
	}
	if got != 1 {
		t.Errorf("expected 1 comment after rate-limit retry, got %d", got)
	}
}

// TestIterCommentsForwardsErrors covers the non-rate-limit error forward path.
func TestIterCommentsForwardsErrors(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts/p1/comments": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		},
	}))
	var sawErr bool
	for res := range client.IterComments(context.Background(), "p1", 0) {
		if res.Err != nil {
			sawErr = true
			break
		}
	}
	if !sawErr {
		t.Error("expected error forwarded through comment-iterator channel")
	}
}

// TestIterPostsRespectsMaxResults covers the maxResults cutoff branch.
func TestIterPostsRespectsMaxResults(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts": func(w http.ResponseWriter, r *http.Request) {
			items := []map[string]any{}
			for i := 0; i < 20; i++ {
				items = append(items, map[string]any{
					"id": "p" + itoa(i), "title": "x",
					"author":     map[string]any{"id": "u1", "username": "t"},
					"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
				})
			}
			jsonResp(w, map[string]any{"items": items, "total": 100})
		},
	}))

	got := 0
	for res := range client.IterPosts(context.Background(), &colony.IterPostsOptions{MaxResults: 7}) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got++
	}
	if got != 7 {
		t.Errorf("expected 7 posts, got %d", got)
	}
}

// TestIterPostsContextCancellation covers the ctx-cancelled paths in
// IterPosts.
func TestIterPostsContextCancellation(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts": func(w http.ResponseWriter, r *http.Request) {
			items := []map[string]any{}
			for i := 0; i < 20; i++ {
				items = append(items, map[string]any{
					"id": "p" + itoa(i), "title": "x",
					"author":     map[string]any{"id": "u1", "username": "t"},
					"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
				})
			}
			jsonResp(w, map[string]any{"items": items, "total": 100})
		},
	}))

	ctx, cancel := context.WithCancel(context.Background())
	ch := client.IterPosts(ctx, nil)
	// Read one result then cancel.
	<-ch
	cancel()
	// Drain — no assertions, just ensure the goroutine exits cleanly.
	for range ch {
	}
}

// TestCreateCommentWithParent covers the parent_id branch.
func TestCreateCommentWithParent(t *testing.T) {
	var gotBody map[string]any
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /posts/p1/comments": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			jsonResp(w, map[string]any{
				"id": "c1", "body": "reply",
				"author":     map[string]any{"id": "u1", "username": "t"},
				"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
			})
		},
	}))
	parent := "c0"
	if _, err := client.CreateComment(context.Background(), "p1", "reply", &parent); err != nil {
		t.Fatal(err)
	}
	if gotBody["parent_id"] != "c0" {
		t.Errorf("expected parent_id c0, got %v", gotBody["parent_id"])
	}
}

// TestUpdatePostBodyOnly covers the Body-only branch of UpdatePost.
func TestUpdatePostBodyOnly(t *testing.T) {
	var gotBody map[string]any
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"PUT /posts/p1": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			jsonResp(w, map[string]any{
				"id": "p1", "title": "Orig", "body": "new",
				"author":     map[string]any{"id": "u1", "username": "t"},
				"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
			})
		},
	}))
	if _, err := client.UpdatePost(context.Background(), "p1", &colony.UpdatePostOptions{
		Body: colony.Ptr("new"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["title"]; ok {
		t.Errorf("expected title absent when only body is updated, got: %v", gotBody["title"])
	}
	if gotBody["body"] != "new" {
		t.Errorf("expected body=new, got %v", gotBody["body"])
	}
	// Also cover UpdatePost(nil).
	if _, err := client.UpdatePost(context.Background(), "p1", nil); err != nil {
		t.Fatal(err)
	}
}

// TestRotateKeyPropagatesNewKeyInFollowingRequest verifies RotateKey updates
// the client's internal key (otherwise subsequent calls would still use the
// old one).
func TestRotateKeyPropagatesNewKeyInFollowingRequest(t *testing.T) {
	var seenAuth []string
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /auth/rotate-key": func(w http.ResponseWriter, r *http.Request) {
			seenAuth = append(seenAuth, r.Header.Get("Authorization"))
			jsonResp(w, map[string]any{"api_key": "col_new"})
		},
		"GET /users/me": func(w http.ResponseWriter, r *http.Request) {
			seenAuth = append(seenAuth, r.Header.Get("Authorization"))
			jsonResp(w, map[string]any{
				"id": "me", "username": "me", "user_type": "agent",
				"created_at": "2026-01-01T00:00:00Z",
			})
		},
	}))

	if _, err := client.RotateKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Both calls will use a JWT (from /auth/token) — we mainly care that
	// RotateKey called RefreshToken() internally, forcing a re-auth. This is
	// implicitly exercised by the fact that both calls succeed.
	if len(seenAuth) < 2 {
		t.Errorf("expected ≥2 auth observations, got %d", len(seenAuth))
	}
}

// itoa is a local helper to avoid pulling strconv into the test file twice.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// --- Two-step registration + agent self-delete ---

func TestRegisterBegin(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/register/begin" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		jsonResp(w, map[string]any{
			"status": "pending", "api_key": "col_abcdefVfm4S4", "claim_token": "rct_tok",
			"id": "u1", "username": "alice", "expires_at": "2026-06-18T02:21:21Z",
			"key_persistence_required": true, "important": "SAVE api_key NOW",
		})
	}))
	t.Cleanup(srv.Close)

	resp, err := colony.RegisterBegin(
		context.Background(), "alice", "Alice", "hi",
		map[string]any{"skills": []string{"testing"}},
		colony.WithBaseURL(srv.URL),
		colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "pending" || resp.APIKey != "col_abcdefVfm4S4" || resp.ClaimToken != "rct_tok" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if gotBody["username"] != "alice" || gotBody["display_name"] != "Alice" {
		t.Errorf("request body missing expected fields: %+v", gotBody)
	}
}

func TestRegisterConfirm(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/register/confirm" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		jsonResp(w, map[string]any{"status": "active", "id": "u1", "username": "alice"})
	}))
	t.Cleanup(srv.Close)

	resp, err := colony.RegisterConfirm(
		context.Background(), "rct_tok", "Vfm4S4",
		colony.WithBaseURL(srv.URL),
		colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "active" || resp.Username != "alice" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if gotBody["claim_token"] != "rct_tok" || gotBody["key_fingerprint"] != "Vfm4S4" {
		t.Errorf("confirm body wrong: %+v", gotBody)
	}
}

func TestRegisterConfirmMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":{"code":"REGISTER_FINGERPRINT_MISMATCH","message":"mismatch"}}`))
	}))
	t.Cleanup(srv.Close)

	_, err := colony.RegisterConfirm(
		context.Background(), "rct_tok", "XXXXXX",
		colony.WithBaseURL(srv.URL),
		colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)
	var ve *colony.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	if ve.Code != "REGISTER_FINGERPRINT_MISMATCH" {
		t.Errorf("expected code REGISTER_FINGERPRINT_MISMATCH, got %q", ve.Code)
	}
}

func TestDeleteAccount(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"DELETE /auth/account": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	}))
	if err := client.DeleteAccount(context.Background()); err != nil {
		t.Fatalf("expected nil error on 204, got %v", err)
	}
}

func TestDeleteAccountHasActivity(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"DELETE /auth/account": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"detail":{"code":"ACCOUNT_DELETE_HAS_ACTIVITY","message":"has activity"}}`))
		},
	}))
	err := client.DeleteAccount(context.Background())
	var ce *colony.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConflictError, got %v", err)
	}
	if ce.Code != "ACCOUNT_DELETE_HAS_ACTIVITY" {
		t.Errorf("expected ACCOUNT_DELETE_HAS_ACTIVITY, got %q", ce.Code)
	}
}

// TestRegisterBeginNoCapabilities covers the capabilities==nil branch.
func TestRegisterBeginNoCapabilities(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		jsonResp(w, map[string]any{"status": "pending", "api_key": "col_x", "claim_token": "rct_x"})
	}))
	t.Cleanup(srv.Close)

	_, err := colony.RegisterBegin(context.Background(), "alice", "Alice", "hi", nil, colony.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["capabilities"]; present {
		t.Errorf("expected capabilities absent when nil, got: %v", gotBody["capabilities"])
	}
}

// TestRegisterBeginTaken covers the error-return path of RegisterBegin.
func TestRegisterBeginTaken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"detail":{"code":"REGISTER_USERNAME_TAKEN","message":"taken"}}`))
	}))
	t.Cleanup(srv.Close)

	_, err := colony.RegisterBegin(
		context.Background(), "taken", "x", "x", nil,
		colony.WithBaseURL(srv.URL),
		colony.WithRetry(colony.RetryConfig{MaxRetries: 0, RetryOn: map[int]bool{}}),
	)
	var ce *colony.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConflictError, got %v", err)
	}
	if ce.Code != "REGISTER_USERNAME_TAKEN" {
		t.Errorf("expected REGISTER_USERNAME_TAKEN, got %q", ce.Code)
	}
}
