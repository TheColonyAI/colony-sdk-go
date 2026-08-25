package colony

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func boolp(b bool) *bool { return &b }

// The tri-state is the whole point: nil (field absent) must NOT behave like
// false, or a server that stops sending has_more silently truncates every
// listing in this package after one page.
func TestMoreAfterTriState(t *testing.T) {
	full := make([]Post, 20)
	short := make([]Post, 3)

	cases := []struct {
		name     string
		items    []Post
		hasMore  *bool
		pageSize int
		want     bool
	}{
		{"absent + full page -> legacy heuristic says continue", full, nil, 20, true},
		{"absent + short page -> legacy heuristic says stop", short, nil, 20, false},
		{"true + short page -> continue (the truncation case)", short, boolp(true), 20, true},
		{"false + full page -> stop (saves a wasted request)", full, boolp(false), 20, false},
		{"true + full page -> continue", full, boolp(true), 20, true},
		{"false + short page -> stop", short, boolp(false), 20, false},
		// An empty page ends the walk whatever the server claims, so a
		// server contradicting itself cannot spin the iterator forever.
		{"empty + true -> stop anyway", nil, boolp(true), 20, false},
		{"empty + absent -> stop", nil, nil, 20, false},
		// A zero page size cannot support the heuristic; without has_more
		// there is nothing to go on, so stop rather than loop.
		{"absent + zero page size -> stop", full, nil, 0, false},
		{"true + zero page size -> the server still knows", full, boolp(true), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &PaginatedList[Post]{Items: tc.items, HasMore: tc.hasMore}
			if got := p.MoreAfter(tc.pageSize); got != tc.want {
				t.Errorf("MoreAfter(%d) = %v, want %v", tc.pageSize, got, tc.want)
			}
		})
	}
	if (*PaginatedList[Post])(nil).MoreAfter(20) {
		t.Error("nil receiver said there was more")
	}
}

func TestPaginatedListDecodesHasMoreAndCursor(t *testing.T) {
	var l PaginatedList[Post]
	if err := json.Unmarshal([]byte(
		`{"items":[{"id":"p1"}],"total":9,"has_more":true,"next_cursor":"opaque"}`), &l); err != nil {
		t.Fatal(err)
	}
	if l.HasMore == nil || !*l.HasMore {
		t.Errorf("HasMore = %v", l.HasMore)
	}
	if l.NextCursor == nil || *l.NextCursor != "opaque" {
		t.Errorf("NextCursor = %v", l.NextCursor)
	}
	// And the control: absent must decode to nil, not false.
	var bare PaginatedList[Post]
	if err := json.Unmarshal([]byte(`{"items":[],"total":0}`), &bare); err != nil {
		t.Fatal(err)
	}
	if bare.HasMore != nil {
		t.Errorf("absent has_more decoded to %v, want nil", *bare.HasMore)
	}
	if bare.NextCursor != nil {
		t.Errorf("absent next_cursor decoded to %q, want nil", *bare.NextCursor)
	}
}

// pagerServer replays a fixed script of pages, so the same sequence can be fed
// to both iterator styles and their outputs compared.
type pagerServer struct {
	pages    []string
	requests int32
}

func (p *pagerServer) start(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth/token" {
			// NOT counted. The token cache is shared between clients in this
			// package, so whether this request happens at all depends on
			// what ran before — counting it would make both the page index
			// and the request assertion order-dependent.
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		n := int(atomic.AddInt32(&p.requests, 1)) - 1
		if n >= len(p.pages) {
			n = len(p.pages) - 1
		}
		_, _ = w.Write([]byte(p.pages[n]))
	}))
	t.Cleanup(srv.Close)
	return NewClient("col_x", WithBaseURL(srv.URL))
}

// The bug this change exists to fix: a SHORT page that says has_more.
// Terminating on page length truncates the listing right there and reports a
// clean finish.
func TestIterPostsFollowsHasMoreOverPageLength(t *testing.T) {
	srv := &pagerServer{pages: []string{
		`{"items":[{"id":"p1"},{"id":"p2"}],"total":3,"has_more":true}`,
		`{"items":[{"id":"p3"}],"total":3,"has_more":false}`,
	}}
	c := srv.start(t)

	var got []string
	for res := range c.IterPosts(context.Background(), &IterPostsOptions{PageSize: 20}) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got = append(got, res.Value.ID)
	}
	if strings.Join(got, ",") != "p1,p2,p3" {
		t.Errorf("got %v — the listing was truncated at the short page", got)
	}
}

// The other direction, worth having on its own: a FULL page that says
// has_more:false must stop, rather than spending a request to discover it.
func TestIterPostsStopsOnHasMoreFalseDespiteFullPage(t *testing.T) {
	page := `{"items":[` + strings.TrimSuffix(strings.Repeat(`{"id":"p"},`, 20), ",") +
		`],"total":20,"has_more":false}`
	srv := &pagerServer{pages: []string{page, page}}
	c := srv.start(t)

	n := 0
	for res := range c.IterPosts(context.Background(), &IterPostsOptions{PageSize: 20}) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		n++
	}
	if n != 20 {
		t.Errorf("yielded %d, want 20", n)
	}
	if got := atomic.LoadInt32(&srv.requests); got != 1 {
		t.Errorf("made %d page requests, want 1 — a full page with has_more:false was re-fetched", got)
	}
}

// The regression guard. Against a server that does not send has_more at all,
// behaviour must be exactly what it was before this change.
func TestIterPostsFallsBackWhenServerOmitsHasMore(t *testing.T) {
	full := `{"items":[` + strings.TrimSuffix(strings.Repeat(`{"id":"p"},`, 20), ",") + `],"total":23}`
	srv := &pagerServer{pages: []string{full, `{"items":[{"id":"last"}],"total":23}`}}
	c := srv.start(t)

	n := 0
	for res := range c.IterPosts(context.Background(), &IterPostsOptions{PageSize: 20}) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		n++
	}
	if n != 21 {
		t.Errorf("yielded %d, want 21 — the legacy length heuristic was not preserved", n)
	}
}

func TestIterCommentsFollowsHasMore(t *testing.T) {
	srv := &pagerServer{pages: []string{
		`{"items":[{"id":"c1"}],"total":2,"page":1,"has_more":true}`,
		`{"items":[{"id":"c2"}],"total":2,"page":2,"has_more":false}`,
	}}
	c := srv.start(t)

	var got []string
	for res := range c.IterComments(context.Background(), "p1", 0) {
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got = append(got, res.Value.ID)
	}
	if strings.Join(got, ",") != "c1,c2" {
		t.Errorf("got %v — a one-item page with has_more:true ended the walk", got)
	}
}

func TestGetAllCommentsFollowsHasMore(t *testing.T) {
	srv := &pagerServer{pages: []string{
		`{"items":[{"id":"c1"}],"total":2,"page":1,"has_more":true}`,
		`{"items":[{"id":"c2"}],"total":2,"page":2,"has_more":false}`,
	}}
	all, err := srv.start(t).GetAllComments(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("got %d comments, want 2 — GetAllComments stopped on page length", len(all))
	}
}

// errorServer returns a client whose every non-auth request fails with status.
func errorServer(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClient("col_x", WithBaseURL(srv.URL), WithRetry(RetryConfig{}))
}
