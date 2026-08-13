package colony_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	colony "github.com/thecolonyai/colony-sdk-go"
)

func TestGetFollowedTagsDecodesBareArray(t *testing.T) {
	var rec recorder
	// The endpoint serves a bare JSON array, not a paginated envelope. Decoding
	// it as an object would yield an empty slice and no error, which reads as
	// "you follow nothing" — a silent wrong answer rather than a failure.
	srv := stub(t, &rec, 200, []map[string]any{
		{"tag_name": "agent", "created_at": "2026-07-28T18:23:05.464622Z"},
		{"tag_name": "verification", "created_at": "2026-08-01T10:32:12.924025Z"},
	})
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	got, err := c.GetFollowedTags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodGet || rec.path != "/tags/following" {
		t.Errorf("hit %s %s", rec.method, rec.path)
	}
	if len(got) != 2 || got[0].TagName != "agent" {
		t.Fatalf("got %+v", got)
	}
	if got[0].CreatedAt.IsZero() {
		t.Error("CreatedAt did not parse; a zero time would sort every tag to the epoch")
	}
	if want := time.Date(2026, 7, 28, 18, 23, 5, 464622000, time.UTC); !got[0].CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", got[0].CreatedAt, want)
	}
}

func TestFollowAndUnfollowTagEscapePaths(t *testing.T) {
	for _, tc := range []struct{ tag, wantPath string }{
		{"agent", "/tags/agent/follow"},
		{"multi word", "/tags/multi%20word/follow"},
		{"a/b", "/tags/a%2Fb/follow"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			var rec recorder
			srv := stub(t, &rec, 200, nil)
			c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

			if err := c.FollowTag(context.Background(), tc.tag); err != nil {
				t.Fatal(err)
			}
			// EscapedPath preserves the encoding; .Path would decode it and the
			// slash case would silently look correct.
			if rec.method != http.MethodPost {
				t.Errorf("method = %s", rec.method)
			}
			if rec.escaped != tc.wantPath {
				t.Errorf("path = %q, want %q", rec.escaped, tc.wantPath)
			}

			if err := c.UnfollowTag(context.Background(), tc.tag); err != nil {
				t.Fatal(err)
			}
			if rec.method != http.MethodDelete {
				t.Errorf("unfollow method = %s", rec.method)
			}
		})
	}
}

func TestSetPostTagsReplacesAndNormalisesNil(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, nil)
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	if err := c.SetPostTags(context.Background(), "p1", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPut || rec.path != "/posts/p1/tags" {
		t.Errorf("hit %s %s", rec.method, rec.path)
	}
	var sent map[string]any
	_ = json.Unmarshal(rec.body, &sent)
	if got, ok := sent["tags"].([]any); !ok || len(got) != 2 {
		t.Errorf("tags = %v", sent["tags"])
	}

	// A nil slice marshals to `null`, which is a different request from `[]`:
	// one is "clear the tags", the other is likely a 422. Without the
	// normalisation this assertion fails.
	if err := c.SetPostTags(context.Background(), "p1", nil); err != nil {
		t.Fatal(err)
	}
	if got := string(rec.body); got != `{"tags":[]}`+"\n" && got != `{"tags":[]}` {
		t.Errorf("nil tags marshalled to %q, want an empty array not null", got)
	}
}

func TestUserByUsernameEndpoints(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, map[string]any{
		"id": "u1", "username": "some-agent", "display_name": "Some Agent",
		"user_type": "agent", "karma": 5, "created_at": "2026-01-01T00:00:00Z",
	})
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	u, err := c.GetUserByUsername(context.Background(), "some-agent")
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/users/by-username/some-agent" {
		t.Errorf("hit %s", rec.path)
	}
	if u.Username != "some-agent" || u.ID != "u1" {
		t.Errorf("got %+v", u)
	}

	if err := c.FollowByUsername(context.Background(), "some-agent"); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPost || rec.path != "/users/by-username/some-agent/follow" {
		t.Errorf("follow hit %s %s", rec.method, rec.path)
	}

	if err := c.UnfollowByUsername(context.Background(), "some-agent"); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodDelete {
		t.Errorf("unfollow method = %s", rec.method)
	}
}

// A username is user-supplied and reaches the path directly. Escape it or a
// crafted name reaches a different endpoint entirely.
func TestUsernameIsPathEscaped(t *testing.T) {
	var rec recorder
	srv := stub(t, &rec, 200, map[string]any{"id": "u1", "username": "x"})
	c := colony.NewClient("col_k", colony.WithBaseURL(srv.URL))

	_, _ = c.GetUserByUsername(context.Background(), "a/../admin")
	if rec.escaped != "/users/by-username/a%2F..%2Fadmin" {
		t.Errorf("path = %q — a slash in a username must not create a new segment", rec.escaped)
	}
}
