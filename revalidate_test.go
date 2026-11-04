package colony

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

const (
	revalPostID = "11111111-1111-4111-8111-111111111111"
	revalETag   = `W/"0123456789abcdef"`
	revalPage   = `{"items":[{"id":"c1","body":"first"}],"total":1,"page":1,"has_more":false}`
)

// revalServer answers /auth/token, and each comment-list GET with the next of
// replies: a status, an ETag (empty for none) and a body. It records the
// If-None-Match each GET sent.
type revalReply struct {
	status int
	etag   string
	body   string
}

type revalRec struct {
	mu   sync.Mutex
	sent []string
}

func (r *revalRec) ifNoneMatch(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent[i]
}

func revalServer(t *testing.T, opts []Option, replies ...revalReply) (*Client, *revalRec) {
	t.Helper()
	rec := &revalRec{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		rec.mu.Lock()
		n := len(rec.sent)
		rec.sent = append(rec.sent, r.Header.Get("If-None-Match"))
		rec.mu.Unlock()
		if n >= len(replies) {
			t.Errorf("unexpected request %d to %s", n, r.URL)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		reply := replies[n]
		if reply.etag != "" {
			w.Header().Set("ETag", reply.etag)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(srv.Close)
	return NewClient("col_x", append([]Option{WithBaseURL(srv.URL)}, opts...)...), rec
}

func TestRevalidateA304DecodesTheKeptAnswer(t *testing.T) {
	c, rec := revalServer(t, nil,
		revalReply{http.StatusOK, revalETag, revalPage},
		revalReply{http.StatusNotModified, revalETag, ""},
	)
	ctx := context.Background()

	first, err := c.GetComments(ctx, revalPostID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if c.LastResponseNotModified() {
		t.Fatal("a 200 reported as not modified")
	}
	first.Items[0].Body = "changed by the caller"

	again, err := c.GetComments(ctx, revalPostID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.ifNoneMatch(0); got != "" {
		t.Errorf("first read sent If-None-Match %q", got)
	}
	if got := rec.ifNoneMatch(1); got != revalETag {
		t.Errorf("second read sent If-None-Match %q, want %q", got, revalETag)
	}
	if !c.LastResponseNotModified() {
		t.Error("a 304 not reported as not modified")
	}
	if len(again.Items) != 1 || again.Items[0].Body != "first" || again.Total != 1 {
		t.Errorf("the 304 decoded %+v, want the kept page", again)
	}
}

func TestRevalidateAChangedAnswerReplacesTheKeptOne(t *testing.T) {
	newer := `{"items":[],"total":2,"page":1,"has_more":false}`
	c, rec := revalServer(t, nil,
		revalReply{http.StatusOK, revalETag, revalPage},
		revalReply{http.StatusOK, `W/"fedcba9876543210"`, newer},
		revalReply{http.StatusNotModified, `W/"fedcba9876543210"`, ""},
	)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := c.GetComments(ctx, revalPostID, 1); err != nil {
			t.Fatal(err)
		}
	}
	if c.LastResponseNotModified() {
		t.Error("a changed answer reported as not modified")
	}
	page, err := c.GetComments(ctx, revalPostID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.ifNoneMatch(2); got != `W/"fedcba9876543210"` {
		t.Errorf("third read sent If-None-Match %q, want the newer tag", got)
	}
	if page.Total != 2 {
		t.Errorf("the 304 decoded total %d, want the newer answer's 2", page.Total)
	}
}

func TestRevalidateAnAnswerWithoutAnETagIsForgotten(t *testing.T) {
	c, rec := revalServer(t, nil,
		revalReply{http.StatusOK, revalETag, revalPage},
		revalReply{http.StatusOK, "", revalPage},
		revalReply{http.StatusOK, "", revalPage},
	)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := c.GetComments(ctx, revalPostID, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec.ifNoneMatch(2); got != "" {
		t.Errorf("read after an untagged answer sent If-None-Match %q", got)
	}
}

func TestRevalidateEachURLKeepsItsOwnTag(t *testing.T) {
	c, rec := revalServer(t, nil,
		revalReply{http.StatusOK, revalETag, revalPage},
		revalReply{http.StatusOK, revalETag, revalPage},
	)
	ctx := context.Background()
	if _, err := c.GetComments(ctx, revalPostID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetComments(ctx, revalPostID, 2); err != nil {
		t.Fatal(err)
	}
	if got := rec.ifNoneMatch(1); got != "" {
		t.Errorf("page 2 sent page 1's tag %q", got)
	}
}

func TestRevalidateOffNeverAsks(t *testing.T) {
	c, rec := revalServer(t, []Option{WithRevalidation(false)},
		revalReply{http.StatusOK, revalETag, revalPage},
		revalReply{http.StatusOK, revalETag, revalPage},
	)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := c.GetComments(ctx, revalPostID, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec.ifNoneMatch(1); got != "" {
		t.Errorf("WithRevalidation(false) sent If-None-Match %q", got)
	}
}

func TestKeptAnswersDropsTheLeastRecentlyUsed(t *testing.T) {
	k := newKeptAnswers(2)
	k.put("a", revalETag, []byte("1"))
	k.put("b", revalETag, []byte("2"))
	k.get("a")
	k.put("c", revalETag, []byte("3"))
	if _, ok := k.get("b"); ok {
		t.Error("b should have been dropped")
	}
	for _, url := range []string{"a", "c"} {
		if _, ok := k.get(url); !ok {
			t.Errorf("%s should be kept", url)
		}
	}
	var none *keptAnswers
	none.put("a", revalETag, nil)
	if _, ok := none.get("a"); ok {
		t.Error("a nil store kept an answer")
	}
}
