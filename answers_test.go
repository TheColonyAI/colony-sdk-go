package colony

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Accepted answers and pinned comments (platform release 2026-11-17b):
// AcceptAnswer sends PUT /posts/{post_id}/accepted-answer with the comment id
// and nothing else; ClearAcceptedAnswer, PinComment and UnpinComment send
// their routes with no body; each decodes its result and checks its ids
// before any request; Post.AcceptedCommentID and Comment.PinnedAt decode.

const (
	answerPostID    = "1f2e3d4c-5b6a-4978-8695-a4b3c2d1e0f9"
	answerCommentID = "3f2c1b9e-8d7a-4c6b-9e5f-1a2b3c4d5e6f"
	otherCommentID  = "0d4e5f6a-7b8c-4d9e-8f0a-1b2c3d4e5f60"
	pinnedAtJSON    = "2026-11-17T20:30:00Z"

	acceptedJSON = `{"post_id":"` + answerPostID + `","accepted_comment_id":"` + answerCommentID +
		`","status":"answered"}`
	clearedJSON = `{"post_id":"` + answerPostID + `","accepted_comment_id":null,"status":"open"}`
	pinnedJSON  = `{"comment_id":"` + answerCommentID + `","post_id":"` + answerPostID +
		`","pinned_at":"` + pinnedAtJSON + `","unpinned_comment_ids":["` + otherCommentID + `"]}`
	unpinnedJSON = `{"comment_id":"` + answerCommentID + `","post_id":"` + answerPostID +
		`","pinned_at":null,"unpinned_comment_ids":[]}`
)

func TestAcceptAnswer(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, acceptedJSON, nil})
	got, err := c.AcceptAnswer(context.Background(), answerPostID, answerCommentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPut || r.raw != "/posts/"+answerPostID+"/accepted-answer" || len(r.query) != 0 {
		t.Errorf("request = %s %s?%s, want PUT /posts/%s/accepted-answer", r.method, r.raw, r.query.Encode(), answerPostID)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Exactly the one field AcceptedAnswerSet declares.
	if b := bodyJSON(t, r.body); len(b) != 1 || b["comment_id"] != answerCommentID {
		t.Errorf("body = %s, want {\"comment_id\": %q} alone", r.body, answerCommentID)
	}
	if got.PostID != answerPostID || got.AcceptedCommentID == nil || *got.AcceptedCommentID != answerCommentID ||
		got.Status != "answered" {
		t.Errorf("decoded = %+v", got)
	}
}

func TestClearAcceptedAnswer(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, clearedJSON, nil})
	got, err := c.ClearAcceptedAnswer(context.Background(), answerPostID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodDelete || r.raw != "/posts/"+answerPostID+"/accepted-answer" || len(r.body) != 0 {
		t.Errorf("request = %s %s %q, want DELETE /posts/%s/accepted-answer with no body",
			r.method, r.raw, r.body, answerPostID)
	}
	if got.PostID != answerPostID || got.AcceptedCommentID != nil || got.Status != "open" {
		t.Errorf("decoded = %+v", got)
	}
}

func TestPinComment(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, pinnedJSON, nil})
	got, err := c.PinComment(context.Background(), answerCommentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodPut || r.raw != "/comments/"+answerCommentID+"/pin" || len(r.body) != 0 {
		t.Errorf("request = %s %s %q, want PUT /comments/%s/pin with no body", r.method, r.raw, r.body, answerCommentID)
	}
	want, _ := time.Parse(time.RFC3339, pinnedAtJSON)
	if got.CommentID != answerCommentID || got.PostID != answerPostID || got.PinnedAt == nil ||
		!got.PinnedAt.Equal(want) || len(got.UnpinnedCommentIDs) != 1 || got.UnpinnedCommentIDs[0] != otherCommentID {
		t.Errorf("decoded = %+v", got)
	}
}

func TestUnpinComment(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, unpinnedJSON, nil})
	got, err := c.UnpinComment(context.Background(), answerCommentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.method != http.MethodDelete || r.raw != "/comments/"+answerCommentID+"/pin" || len(r.body) != 0 {
		t.Errorf("request = %s %s %q, want DELETE /comments/%s/pin with no body",
			r.method, r.raw, r.body, answerCommentID)
	}
	if got.CommentID != answerCommentID || got.PinnedAt != nil || len(got.UnpinnedCommentIDs) != 0 {
		t.Errorf("decoded = %+v", got)
	}
}

func TestAcceptedAnswerAndPinRefusals(t *testing.T) {
	ctx := context.Background()
	refusal := func(status int, code string) projectReply {
		return projectReply{status, `{"detail":{"message":"refused","code":"` + code + `"}}`, nil}
	}
	calls := map[string]func(*Client) error{
		"AcceptAnswer": func(c *Client) error {
			_, err := c.AcceptAnswer(ctx, answerPostID, answerCommentID)
			return err
		},
		"ClearAcceptedAnswer": func(c *Client) error { _, err := c.ClearAcceptedAnswer(ctx, answerPostID); return err },
		"PinComment":          func(c *Client) error { _, err := c.PinComment(ctx, answerCommentID); return err },
		"UnpinComment":        func(c *Client) error { _, err := c.UnpinComment(ctx, answerCommentID); return err },
	}
	cases := []struct {
		status int
		code   string
		as     func(error) (int, string, bool)
	}{
		{403, "FORBIDDEN", func(err error) (int, string, bool) {
			var e *AuthError
			ok := errors.As(err, &e)
			if !ok {
				return 0, "", false
			}
			return e.Status, e.Code, true
		}},
		{400, "INVALID_INPUT", func(err error) (int, string, bool) {
			var e *ValidationError
			ok := errors.As(err, &e)
			if !ok {
				return 0, "", false
			}
			return e.Status, e.Code, true
		}},
		{404, "POST_NOT_FOUND", func(err error) (int, string, bool) {
			var e *NotFoundError
			ok := errors.As(err, &e)
			if !ok {
				return 0, "", false
			}
			return e.Status, e.Code, true
		}},
		{404, "NOT_FOUND", func(err error) (int, string, bool) {
			var e *NotFoundError
			ok := errors.As(err, &e)
			if !ok {
				return 0, "", false
			}
			return e.Status, e.Code, true
		}},
	}
	for name, call := range calls {
		for _, tc := range cases {
			t.Run(name+" "+tc.code, func(t *testing.T) {
				c, reqs := projectServer(t, refusal(tc.status, tc.code))
				err := call(c)
				status, code, ok := tc.as(err)
				if !ok || status != tc.status || code != tc.code {
					t.Errorf("error = %#v, want %d %s", err, tc.status, tc.code)
				}
				// A 403 is sent again once after a token refresh, in case the
				// token was the cause, so only the 400s and 404s are counted.
				if tc.status != 403 && len(*reqs) != 1 {
					t.Errorf("%d requests, want 1: a %d is not retried", len(*reqs), tc.status)
				}
			})
		}
	}

	t.Run("the hourly limit is a RateLimitError", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{429, `{"detail":{"message":"slow down","code":"RATE_LIMITED"}}`,
			map[string]string{"Retry-After": "0"}})
		c.retry.MaxRetries = 0
		_, err := c.PinComment(ctx, answerCommentID)
		var rl *RateLimitError
		if !errors.As(err, &rl) {
			t.Errorf("error = %#v, want a *RateLimitError", err)
		}
	})
}

// The ids go into the path (and, for AcceptAnswer's commentID, the body), and
// the routes take UUIDs only, so a value that is not one is refused before
// anything is sent, a truncated id included.
func TestAcceptedAnswerAndPinCheckIDsBeforeTheRequest(t *testing.T) {
	ctx := context.Background()
	bad := []string{"", ".", "..", "a/b", "../users/me", "1f2e3d4c", "p1", " " + answerPostID, answerPostID + "/x"}
	for _, v := range bad {
		c, reqs := projectServer(t, projectReply{200, acceptedJSON, nil})
		if _, err := c.AcceptAnswer(ctx, v, answerCommentID); err == nil || !strings.Contains(err.Error(), "postID") {
			t.Errorf("AcceptAnswer(postID %q): error = %v", v, err)
		}
		if _, err := c.AcceptAnswer(ctx, answerPostID, v); err == nil || !strings.Contains(err.Error(), "commentID") {
			t.Errorf("AcceptAnswer(commentID %q): error = %v", v, err)
		}
		if _, err := c.ClearAcceptedAnswer(ctx, v); err == nil || !strings.Contains(err.Error(), "postID") {
			t.Errorf("ClearAcceptedAnswer(%q): error = %v", v, err)
		}
		if _, err := c.PinComment(ctx, v); err == nil || !strings.Contains(err.Error(), "commentID") {
			t.Errorf("PinComment(%q): error = %v", v, err)
		}
		if _, err := c.UnpinComment(ctx, v); err == nil || !strings.Contains(err.Error(), "commentID") {
			t.Errorf("UnpinComment(%q): error = %v", v, err)
		}
		if len(*reqs) != 0 {
			t.Errorf("%q: %d request(s) reached the server", v, len(*reqs))
		}
	}
}

func TestAcceptedCommentIDAndPinnedAtDecode(t *testing.T) {
	for name, tc := range map[string]struct {
		json string
		want *string
	}{
		"absent, as from a platform before release 2026-11-17b": {`{"id":"p"}`, nil},
		"null":  {`{"id":"p","accepted_comment_id":null}`, nil},
		"an id": {`{"id":"p","accepted_comment_id":"` + answerCommentID + `"}`, Ptr(answerCommentID)},
	} {
		t.Run("Post "+name, func(t *testing.T) {
			var p Post
			if err := json.Unmarshal([]byte(tc.json), &p); err != nil {
				t.Fatal(err)
			}
			if _, ok := p.Extra["accepted_comment_id"]; ok {
				t.Errorf("accepted_comment_id leaked into Extra: %v", p.Extra)
			}
			if !equalStrPtr(p.AcceptedCommentID, tc.want) {
				t.Errorf("AcceptedCommentID = %v, want %v", p.AcceptedCommentID, tc.want)
			}
		})
	}

	want, _ := time.Parse(time.RFC3339, pinnedAtJSON)
	for name, tc := range map[string]struct {
		json   string
		pinned bool
	}{
		"absent, as from a platform before release 2026-11-17b": {`{"id":"c"}`, false},
		"null":   {`{"id":"c","pinned_at":null}`, false},
		"a time": {`{"id":"c","pinned_at":"` + pinnedAtJSON + `"}`, true},
	} {
		t.Run("Comment "+name, func(t *testing.T) {
			var c Comment
			if err := json.Unmarshal([]byte(tc.json), &c); err != nil {
				t.Fatal(err)
			}
			if _, ok := c.Extra["pinned_at"]; ok {
				t.Errorf("pinned_at leaked into Extra: %v", c.Extra)
			}
			if tc.pinned != (c.PinnedAt != nil) || (tc.pinned && !c.PinnedAt.Equal(want)) {
				t.Errorf("PinnedAt = %v, want pinned %v at %v", c.PinnedAt, tc.pinned, want)
			}
		})
	}
}
