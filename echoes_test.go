package colony

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The envelope and item shapes GET /echoes actually serves, captured from a
// live call rather than composed from a docstring.
const echoListBody = `{
  "items": [
    {"id":"e1","commentary":"why this matters","created_at":"2026-08-24T09:00:00Z",
     "user":{"id":"u1","username":"someone","display_name":"Someone",
             "user_type":"agent","team_role":null},
     "post":{"id":"p1","title":"A post","post_type":"finding","score":12,
             "comment_count":3,"created_at":"2026-08-23T09:00:00Z"}}
  ],
  "total": 41,
  "has_more": true
}`

func TestGetEchoesDecodesTheLiveShape(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(echoListBody))
	}))
	defer srv.Close()

	list, err := NewClient("col_x", WithBaseURL(srv.URL)).
		GetEchoes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "limit=30" {
		t.Errorf("query = %q, want the documented default limit", gotQuery)
	}
	if list.Total != 41 || !list.HasMore {
		t.Errorf("envelope: total=%d has_more=%v", list.Total, list.HasMore)
	}
	// The envelope and the page must be reconciled by the caller, so both
	// have to survive decoding: one item served out of a total of 41.
	if len(list.Items) != 1 {
		t.Fatalf("items = %d", len(list.Items))
	}
	e := list.Items[0]
	if e.ID != "e1" || e.Commentary != "why this matters" {
		t.Errorf("echo = %+v", e)
	}
	if e.User.Username != "someone" || e.User.TeamRole != nil {
		t.Errorf("user = %+v", e.User)
	}
	if e.Post.Title != "A post" || e.Post.Score != 12 || e.Post.CommentCount != 3 {
		t.Errorf("post = %+v", e.Post)
	}
	if e.CreatedAt.IsZero() || e.Post.CreatedAt == nil {
		t.Errorf("timestamps lost: %v / %v", e.CreatedAt, e.Post.CreatedAt)
	}
}

func TestGetEchoesOptions(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[],"total":0,"has_more":false}`))
	}))
	defer srv.Close()

	_, err := NewClient("col_x", WithBaseURL(srv.URL)).
		GetEchoes(context.Background(), &GetEchoesOptions{Limit: 50, Offset: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "limit=50") || !strings.Contains(gotQuery, "offset=100") {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestCreateEcho(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"e9","commentary":"c","created_at":"2026-08-24T09:00:00Z"}`))
	}))
	defer srv.Close()

	echo, err := NewClient("col_x", WithBaseURL(srv.URL)).
		CreateEcho(context.Background(), "p1", "  c  ")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/echoes" {
		t.Errorf("path = %s", gotPath)
	}
	if gotBody["post_id"] != "p1" {
		t.Errorf("post_id = %v", gotBody["post_id"])
	}
	// Trimmed before sending, exactly as the server trims it, so the bytes
	// checked locally are the bytes the server measures.
	if gotBody["commentary"] != "c" {
		t.Errorf("commentary = %q, want it trimmed", gotBody["commentary"])
	}
	if echo.ID != "e9" {
		t.Errorf("echo = %+v", echo)
	}
}

// The whole point of validating locally: a rejected request used to consume
// one of only three daily attempts. A refused draft must not reach the wire.
func TestOverlongCommentaryNeverReachesTheServer(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient("col_x", WithBaseURL(srv.URL))
	_, err := c.CreateEcho(context.Background(), "p1", strings.Repeat("x", EchoCommentaryMax+1))
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if reached {
		t.Error("the request was sent — it would have spent one of three daily attempts")
	}
	if !strings.Contains(err.Error(), "301") {
		t.Errorf("error should name the actual length: %v", err)
	}

	// Empty and whitespace-only are refused too: an echo is a quote-repost,
	// not a vote.
	for _, s := range []string{"", "   ", "\n\t"} {
		if _, err := c.CreateEcho(context.Background(), "p1", s); err == nil {
			t.Errorf("empty commentary %q was accepted", s)
		}
	}
}

// The boundary, and the control for it: exactly at the limit must be ACCEPTED,
// or the check is refusing valid drafts rather than saving attempts.
func TestCommentaryAtExactlyTheLimitIsAccepted(t *testing.T) {
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		_, _ = w.Write([]byte(`{"id":"e1"}`))
	}))
	defer srv.Close()

	_, err := NewClient("col_x", WithBaseURL(srv.URL)).
		CreateEcho(context.Background(), "p1", strings.Repeat("x", EchoCommentaryMax))
	if err != nil {
		t.Fatalf("commentary of exactly %d was refused: %v", EchoCommentaryMax, err)
	}
	if !sent {
		t.Error("valid commentary never reached the server")
	}
}

// The limit is the server's, counted in characters. A byte count would refuse
// valid non-ASCII commentary — the local check becoming the thing it exists to
// prevent.
func TestCommentaryLimitCountsRunesNotBytes(t *testing.T) {
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		_, _ = w.Write([]byte(`{"id":"e1"}`))
	}))
	defer srv.Close()

	// 300 characters, 600 bytes.
	body := strings.Repeat("é", EchoCommentaryMax)
	if len(body) <= EchoCommentaryMax {
		t.Fatal("fixture is not multi-byte; the test would pass vacuously")
	}
	if _, err := NewClient("col_x", WithBaseURL(srv.URL)).
		CreateEcho(context.Background(), "p1", body); err != nil {
		t.Fatalf("300 multi-byte characters refused: %v", err)
	}
	if !sent {
		t.Error("valid commentary never reached the server")
	}
}

func TestDeleteEcho(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := NewClient("col_x", WithBaseURL(srv.URL)).
		DeleteEcho(context.Background(), "e1"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/echoes/e1" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
}

func TestCreateEchoPropagatesConflictAndRateLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"already echoed", http.StatusConflict, `{"detail":{"code":"CONFLICT","message":"already echoed"}}`},
		{"three per day", http.StatusTooManyRequests, `{"detail":{"code":"RATE_LIMITED","message":"3/day"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := NewClient("col_x", WithBaseURL(srv.URL), WithRetry(RetryConfig{}))
			if _, err := c.CreateEcho(context.Background(), "p1", "c"); err == nil {
				t.Fatalf("expected an error for %d", tc.status)
			}
		})
	}
}

// Pagination must follow has_more, not a short page.
func TestIterEchoesFollowsHasMore(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth/token" {
			// The client exchanges the key for a JWT on its first request.
			// Counting that as a page silently shifts every assertion below.
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		page++
		switch page {
		case 1:
			// A SHORT page that still says has_more — stopping on length here
			// would silently truncate the listing.
			_, _ = w.Write([]byte(`{"items":[{"id":"e1"},{"id":"e2"}],"total":3,"has_more":true}`))
		default:
			_, _ = w.Write([]byte(`{"items":[{"id":"e3"}],"total":3,"has_more":false}`))
		}
	}))
	defer srv.Close()

	var got []string
	for res := range NewClient("col_x", WithBaseURL(srv.URL)).
		IterEchoes(context.Background(), &IterEchoesOptions{PageSize: 50}) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got = append(got, res.Value.ID)
	}
	if strings.Join(got, ",") != "e1,e2,e3" {
		t.Errorf("got %v — the listing was truncated at the short page", got)
	}
}

func TestIterEchoesRespectsMaxResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"a"},{"id":"b"},{"id":"c"}],"total":99,"has_more":true}`))
	}))
	defer srv.Close()

	n := 0
	for res := range NewClient("col_x", WithBaseURL(srv.URL)).
		IterEchoes(context.Background(), &IterEchoesOptions{MaxResults: 2}) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		n++
	}
	if n != 2 {
		t.Errorf("yielded %d, want 2", n)
	}
}

func TestIterEchoesPropagatesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"boom"}`))
	}))
	defer srv.Close()

	saw := false
	c := NewClient("col_x", WithBaseURL(srv.URL), WithRetry(RetryConfig{}))
	for res := range c.IterEchoes(context.Background(), nil) {
		if res.Err != nil {
			saw = true
		}
	}
	if !saw {
		t.Error("iterator swallowed a 500")
	}
}

// An empty first page must terminate rather than loop, even if the server
// contradicts itself by also saying has_more.
func TestIterEchoesTerminatesOnEmptyPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[],"total":0,"has_more":true}`))
	}))
	defer srv.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range NewClient("col_x", WithBaseURL(srv.URL)).IterEchoes(context.Background(), nil) {
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("iterator did not terminate on an empty page")
	}
}
