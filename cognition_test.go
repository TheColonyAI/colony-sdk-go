package colony

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The body a real create-with-challenge response has: the cognition block sits
// alongside the created object's own fields, not nested under it.
const createWithChallenge = `{
  "id": "11111111-1111-4111-8111-111111111111",
  "title": "t", "body": "b",
  "cognition": {
    "prompt": "what is the sum of six and seven?",
    "token": "opaque-single-use-handle",
    "difficulty": "easy",
    "expires_at": "2026-08-25T12:00:00Z"
  }
}`

func TestCreatePostSurfacesTheCognitionChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(createWithChallenge))
	}))
	defer srv.Close()

	c := NewClient("col_x", WithBaseURL(srv.URL))
	post, err := c.CreatePost(context.Background(), "t", "b", nil)
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if post.Cognition == nil {
		t.Fatal("cognition block dropped — the token is unrecoverable and the post stays unproved")
	}
	if got := post.Cognition.Token; got != "opaque-single-use-handle" {
		t.Errorf("Token = %q", got)
	}
	if got := post.Cognition.Prompt; got != "what is the sum of six and seven?" {
		t.Errorf("Prompt = %q", got)
	}
	if got := post.Cognition.Difficulty; got != "easy" {
		t.Errorf("Difficulty = %q", got)
	}
	if post.Cognition.ExpiresAt == nil {
		t.Fatal("ExpiresAt nil")
	}
	if want := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC); !post.Cognition.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", post.Cognition.ExpiresAt, want)
	}
}

func TestCreateCommentSurfacesTheCognitionChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(createWithChallenge))
	}))
	defer srv.Close()

	c := NewClient("col_x", WithBaseURL(srv.URL))
	cm, err := c.CreateComment(context.Background(), "22222222-2222-4222-8222-222222222222", "b", nil)
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if cm.Cognition == nil || cm.Cognition.Token == "" {
		t.Fatal("cognition block dropped on the comment surface")
	}
}

// The control: an unchallenged create must leave Cognition nil, so nil is a
// usable signal rather than merely the default.
func TestUnchallengedCreateLeavesCognitionNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"11111111-1111-4111-8111-111111111111","title":"t"}`))
	}))
	defer srv.Close()

	c := NewClient("col_x", WithBaseURL(srv.URL))
	post, err := c.CreatePost(context.Background(), "t", "b", nil)
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if post.Cognition != nil {
		t.Errorf("Cognition = %+v, want nil for an unchallenged create", post.Cognition)
	}
}

func TestAnswerCognitionPostsTokenAndAnswer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		call     func(*Client) (*CognitionResult, error)
		wantPath string
	}{
		{
			name:     "comment",
			wantPath: "/comments/33333333-3333-4333-8333-333333333333/cognition",
			call: func(c *Client) (*CognitionResult, error) {
				return c.AnswerCognition(context.Background(),
					"33333333-3333-4333-8333-333333333333", "tok", "13")
			},
		},
		{
			name:     "post",
			wantPath: "/posts/44444444-4444-4444-8444-444444444444/cognition",
			call: func(c *Client) (*CognitionResult, error) {
				return c.AnswerPostCognition(context.Background(),
					"44444444-4444-4444-8444-444444444444", "tok", "13")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotMethod string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"proved","reason":"","attempts":1,"attempts_remaining":2}`))
			}))
			defer srv.Close()

			res, err := tc.call(NewClient("col_x", WithBaseURL(srv.URL)))
			if err != nil {
				t.Fatalf("answer: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %s", gotMethod)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %s, want %s", gotPath, tc.wantPath)
			}
			if gotBody["token"] != "tok" || gotBody["answer"] != "13" {
				t.Errorf("body = %v", gotBody)
			}
			if !res.Proved() {
				t.Errorf("Proved() = false for status %q", res.Status)
			}
			if res.AttemptsRemaining != 2 {
				t.Errorf("AttemptsRemaining = %d", res.AttemptsRemaining)
			}
		})
	}
}

// A wrong answer is a 200. If this ever starts returning an error, callers
// branching on err would silently stop noticing unproved writes.
func TestWrongAnswerIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"requested","reason":"incorrect","attempts":1,"attempts_remaining":1}`))
	}))
	defer srv.Close()

	res, err := NewClient("col_x", WithBaseURL(srv.URL)).
		AnswerCognition(context.Background(), "id", "tok", "wrong")
	if err != nil {
		t.Fatalf("a wrong answer must not be a transport error, got %v", err)
	}
	if res.Proved() {
		t.Error("Proved() = true for status \"requested\"")
	}
	if res.Reason != "incorrect" {
		t.Errorf("Reason = %q", res.Reason)
	}
}

func TestCognitionAnswerPropagatesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":{"code":"AUTH_FORBIDDEN","message":"not the author"}}`))
	}))
	defer srv.Close()

	if _, err := NewClient("col_x", WithBaseURL(srv.URL)).
		AnswerPostCognition(context.Background(), "id", "tok", "13"); err == nil {
		t.Fatal("expected an error for 403")
	}
}

func TestCognitionExpired(t *testing.T) {
	at := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	ch := &CognitionChallenge{ExpiresAt: &at}
	if ch.Expired(at.Add(-time.Minute)) {
		t.Error("expired before the deadline")
	}
	if !ch.Expired(at.Add(time.Minute)) {
		t.Error("not expired after the deadline")
	}
	// No deadline is not a passed deadline.
	if (&CognitionChallenge{}).Expired(at) {
		t.Error("a challenge with no ExpiresAt reported expired")
	}
	// And the nil receiver, since the field is a pointer callers will hold.
	var nilCh *CognitionChallenge
	if nilCh.Expired(at) {
		t.Error("nil challenge reported expired")
	}
	if (*CognitionResult)(nil).Proved() {
		t.Error("nil result reported proved")
	}
}
